package loopquery

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mario-pinderi/goqueryguard/internal/baseline"
	"github.com/mario-pinderi/goqueryguard/internal/config"
	"github.com/mario-pinderi/goqueryguard/internal/matcher"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const (
	RuleID = "query-in-loop"
)

// loopRange marks the token range of a loop in a file and whether that loop
// is suppressed for this rule.
type loopRange struct {
	Start      token.Pos
	End        token.Pos
	Suppressed bool
}

// loopIndex groups loop ranges by file for O(number-of-loops-in-file) lookup.
type loopIndex struct {
	ByFile map[*token.File][]loopRange
}

// callEdge is a single callgraph edge used for certainty propagation.
type callEdge struct {
	To *ssa.Function
}

// loopCallsite captures all information needed to classify one call expression
// that appears inside loop context.
type loopCallsite struct {
	Pos               token.Pos
	Callee            *ssa.Function
	ResolvedCallees   []*ssa.Function
	ResolvedHints     []targetHint
	ResolvedPrecise   bool
	MethodName        string
	DynamicDefiniteDB bool
	DynamicCustomDef  bool
	DynamicCustomPoss bool
	DynamicQueryLike  bool
	LoopSuppressed    bool
	CallSuppressed    bool
}

// funcSummary is the per-function summary built from SSA before fixpoint
// propagation. It contains direct query capability and outgoing edges.
type funcSummary struct {
	Fn             *ssa.Function
	DirectDefinite bool
	DirectPossible bool
	Edges          []callEdge
	LoopCallsites  []loopCallsite
}

// funcState is the propagated certainty state for a function.
type funcState struct {
	Definite bool
	Possible bool
}

// dispatchTargets stores candidate dynamic callees for one callsite position.
// Precise indicates VTA/refined resolution; false means CHA/fallback.
type dispatchTargets struct {
	Callees []*ssa.Function
	Hints   []targetHint
	Precise bool
}

// targetHint is a compact classification hint for dynamic targets when full
// function objects may not be available/needed.
type targetHint struct {
	Name      string
	QueryFunc bool
	Definite  bool
	Possible  bool
}

// globalDispatchCacheEntry memoizes cross-package dynamic dispatch maps for a
// package-pattern/config combination.
type globalDispatchCacheEntry struct {
	once  sync.Once
	byPkg map[string]map[token.Pos]dispatchTargets
	err   error
}

var globalDispatchCache sync.Map

// RuntimeOptions carries run-time behavior that should not come from global
// process state (env vars), making analyzer execution deterministic and testable.
type RuntimeOptions struct {
	// Explain appends structured reasoning to diagnostics.
	Explain bool
	// PackagePatterns is used to build/lookup cross-package dynamic dispatch.
	PackagePatterns []string
	// BaselineModeOverride temporarily overrides config.Baseline.Mode.
	BaselineModeOverride string
	// DisableFactExport disables cross-package fact export.
	DisableFactExport bool
	// CaptureOnly records findings but suppresses pass.Report diagnostics.
	CaptureOnly bool
	// CaptureSink receives emitted findings for stats/baseline workflows.
	CaptureSink FindingSink
	// CapturePackageAllowset optionally limits which package findings are captured.
	CapturePackageAllowset map[string]struct{}
}

// New creates the analyzer with default runtime behavior.
func New(cfg *config.Config) *analysis.Analyzer {
	return NewWithRuntime(cfg, RuntimeOptions{})
}

// NewWithRuntime creates the analyzer with explicit runtime options.
func NewWithRuntime(cfg *config.Config, opts RuntimeOptions) *analysis.Analyzer {
	if cfg == nil {
		cfg = config.Default()
	}
	opts = normalizeRuntimeOptions(opts)

	return &analysis.Analyzer{
		Name:     "goqueryguard",
		Doc:      "reports database queries executed inside loops, including indirect call chains",
		Requires: []*analysis.Analyzer{inspect.Analyzer, buildssa.Analyzer},
		Run: func(pass *analysis.Pass) (any, error) {
			return run(pass, cfg, opts)
		},
		FactTypes: []analysis.Fact{new(QueryFact)},
	}
}

// normalizeRuntimeOptions trims and deduplicates runtime inputs so downstream
// code can assume canonicalized values.
func normalizeRuntimeOptions(opts RuntimeOptions) RuntimeOptions {
	if len(opts.PackagePatterns) > 0 {
		seen := make(map[string]struct{}, len(opts.PackagePatterns))
		out := make([]string, 0, len(opts.PackagePatterns))
		for _, p := range opts.PackagePatterns {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
		opts.PackagePatterns = out
	}

	if len(opts.CapturePackageAllowset) > 0 {
		cp := make(map[string]struct{}, len(opts.CapturePackageAllowset))
		for k := range opts.CapturePackageAllowset {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			cp[k] = struct{}{}
		}
		opts.CapturePackageAllowset = cp
	}

	opts.BaselineModeOverride = strings.TrimSpace(opts.BaselineModeOverride)
	return opts
}

// run executes one analyzer pass for one package.
//
// High-level stages:
// 1) Build per-function summaries from SSA.
// 2) Propagate query certainty through call edges.
// 3) Classify loop callsites as definite/possible findings.
// 4) Capture/report findings and suppression diagnostics.
func run(pass *analysis.Pass, cfg *config.Config, opts RuntimeOptions) (any, error) {
	// Work on a config copy so per-run overrides don't mutate shared config.
	cfgCopy := *cfg
	if mode := opts.BaselineModeOverride; mode != "" {
		cfgCopy.Baseline.Mode = mode
	}
	cfg = &cfgCopy
	explainMode := opts.Explain

	if !cfg.Rules.QueryInLoop.Enabled {
		return nil, nil
	}

	// Build static context for this package.
	suppressions := parseSuppressions(pass, cfg)
	loops := collectLoops(pass.Files, pass.Fset, suppressions, cfg)
	match := matcher.New(cfg.QueryMatch)
	queryFuncCache := make(map[*ssa.Function]uint8)

	// Build dynamic-dispatch maps (local + optional cross-package) and summarize
	// every source function once before propagation.
	ssaResult := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	localDispatch := buildDynamicDispatchIndex(cfg, ssaResult)
	globalDispatch := globalDispatchForPackage(pass, cfg, opts.PackagePatterns)
	dynamicDispatch := mergeDispatchTargets(localDispatch, globalDispatch)
	summaries := make(map[*ssa.Function]*funcSummary, len(ssaResult.SrcFuncs))
	for _, fn := range ssaResult.SrcFuncs {
		if fn == nil || fn.Blocks == nil || fn.Pkg == nil {
			continue
		}
		summaries[fn] = summarizeFunction(pass, cfg, fn, loops, suppressions, match, queryFuncCache, dynamicDispatch)
	}

	// Propagate certainty over function edges (fixpoint).
	states := propagate(pass, summaries)
	if !opts.DisableFactExport {
		// Export package facts so other packages can consume certainty without
		// re-analyzing this package body.
		exportFacts(pass, states)
	}

	// Baseline lookup is only used for "new_only" mode.
	baselineSet, err := baseline.LoadSet(cfg.Baseline.File)
	if err != nil && cfg.Baseline.Mode == "new_only" {
		return nil, fmt.Errorf("load baseline: %w", err)
	}

	for _, sum := range summaries {
		callerName := displayFn(sum.Fn)
		for _, site := range sum.LoopCallsites {
			if site.LoopSuppressed || site.CallSuppressed {
				continue
			}

			// Confidence classification order:
			// 1) direct static query call, 2) propagated callee state,
			// 3) dynamic dispatch resolved via call graph, 4) dynamic DB signatures,
			// 5) dynamic custom method rules, 6) dynamic query-like names.
			level := ""
			msg := ""
			chain := []string{callerName}
			explainReason := ""

			// Case A: direct static call to known query function.
			if site.Callee != nil && match.IsQueryFunc(site.Callee) {
				level = "definite"
				chain = append(chain, displayFn(site.Callee))
				msg = fmt.Sprintf("db query executed inside loop via call chain: %s", strings.Join(chain, " -> "))
				explainReason = "direct static call to matched query function"
				// Case B: static call where certainty comes from propagated state.
			} else if site.Callee != nil {
				if isDatabaseSQLCursorMethod(site.Callee) {
					continue
				}
				state, ok := states[site.Callee]
				stateSource := "local-state"
				if !ok {
					fact := importFact(pass, site.Callee)
					state = funcState{Definite: fact.HasDefiniteQuery, Possible: fact.HasPossibleQuery || fact.HasDefiniteQuery}
					stateSource = "imported-fact"
				}
				switch {
				case state.Definite:
					level = "definite"
					explainReason = "callee has propagated definite query capability (" + stateSource + ")"
				case state.Possible:
					level = "possible"
					explainReason = "callee has propagated possible query capability (" + stateSource + ")"
				default:
					continue
				}

				path := queryPath(sum.Fn, site.Callee, level, summaries, states, pass, cfg.Analysis.MaxChainDepth)
				if len(path) > 0 {
					chain = path
				} else {
					chain = append(chain, displayFn(site.Callee))
				}
				msg = fmt.Sprintf("db query reachable from loop via call chain: %s", strings.Join(chain, " -> "))
				// Case C: dynamic call resolved to concrete targets/hints.
			} else if len(site.ResolvedCallees) > 0 || len(site.ResolvedHints) > 0 {
				selected := (*ssa.Function)(nil)
				selectedName := ""
				selectedSource := ""
				if site.ResolvedPrecise {
					// VTA/refined callgraph: can produce definite.
					for _, hint := range site.ResolvedHints {
						if hint.QueryFunc || hint.Definite {
							level = "definite"
							selectedName = hint.Name
							explainReason = "dynamic dispatch resolved to query-capable target (VTA/precise)"
							break
						}
						if hint.Possible && level == "" {
							level = "possible"
							selectedName = hint.Name
							selectedSource = "global-hint"
						}
					}
					if level != "definite" {
						for _, candidate := range site.ResolvedCallees {
							if candidate == nil {
								continue
							}
							if isDatabaseSQLCursorMethod(candidate) {
								continue
							}
							if match.IsQueryFunc(candidate) {
								level = "definite"
								selected = candidate
								selectedName = displayFn(candidate)
								explainReason = "dynamic dispatch resolved to matched query function (VTA/precise)"
								break
							}
							state, ok := states[candidate]
							stateSource := "local-state"
							if !ok {
								fact := importFact(pass, candidate)
								state = funcState{Definite: fact.HasDefiniteQuery, Possible: fact.HasPossibleQuery || fact.HasDefiniteQuery}
								stateSource = "imported-fact"
							}
							switch {
							case state.Definite:
								if level != "definite" {
									level = "definite"
									selected = candidate
									selectedName = displayFn(candidate)
									selectedSource = stateSource
								}
							case state.Possible:
								if level == "" {
									level = "possible"
									selected = candidate
									selectedName = displayFn(candidate)
									selectedSource = stateSource
								}
							}
						}
					}
				} else {
					// CHA fallback: intentionally cap at possible due imprecision.
					for _, hint := range site.ResolvedHints {
						if hint.QueryFunc || hint.Definite || hint.Possible {
							level = "possible"
							selectedName = hint.Name
							explainReason = "dynamic dispatch resolved via CHA fallback; confidence capped at possible"
							break
						}
					}
					if level == "" {
						for _, candidate := range site.ResolvedCallees {
							if candidate == nil {
								continue
							}
							if isDatabaseSQLCursorMethod(candidate) {
								continue
							}
							if match.IsQueryFunc(candidate) {
								level = "possible"
								selected = candidate
								selectedName = displayFn(candidate)
								explainReason = "dynamic dispatch resolved via CHA fallback; confidence capped at possible"
								break
							}
							state, ok := states[candidate]
							stateSource := "local-state"
							if !ok {
								fact := importFact(pass, candidate)
								state = funcState{Definite: fact.HasDefiniteQuery, Possible: fact.HasPossibleQuery || fact.HasDefiniteQuery}
								stateSource = "imported-fact"
							}
							if state.Definite || state.Possible {
								level = "possible"
								selected = candidate
								selectedName = displayFn(candidate)
								selectedSource = stateSource
								break
							}
						}
					}
				}
				if level == "" || (selected == nil && selectedName == "") {
					continue
				}
				if explainReason == "" {
					explainReason = "dynamic dispatch resolved to callee with propagated " + level + " query capability (" + selectedSource + ")"
				}
				if selected != nil {
					path := queryPath(sum.Fn, selected, level, summaries, states, pass, cfg.Analysis.MaxChainDepth)
					if len(path) > 0 {
						chain = path
					} else {
						chain = append(chain, displayFn(selected))
					}
				} else {
					chain = append(chain, selectedName)
				}
				if site.ResolvedPrecise {
					msg = fmt.Sprintf("db query reachable from loop via dynamic dispatch method %q and call chain: %s", site.MethodName, strings.Join(chain, " -> "))
				} else {
					msg = fmt.Sprintf("possible db query reachable from loop via dynamic dispatch method %q (CHA fallback) and call chain: %s", site.MethodName, strings.Join(chain, " -> "))
				}
				// Case D: unresolved dynamic call but DB-like signature.
			} else if site.DynamicDefiniteDB {
				level = "definite"
				chain = append(chain, "<dynamic:"+site.MethodName+">")
				msg = fmt.Sprintf("db query executed inside loop via dynamic db interface method %q", site.MethodName)
				explainReason = "dynamic interface call matched DB signature (ExecContext/QueryContext/QueryRowContext)"
				// Case E: unresolved dynamic call matched custom method config.
			} else if site.DynamicCustomDef {
				level = "definite"
				chain = append(chain, "<dynamic:"+site.MethodName+">")
				msg = fmt.Sprintf("db query executed inside loop via configured dynamic interface method %q", site.MethodName)
				explainReason = "dynamic interface call matched configured custom method (terminal=true)"
			} else if site.DynamicCustomPoss {
				level = "possible"
				chain = append(chain, "<dynamic:"+site.MethodName+">")
				msg = fmt.Sprintf("possible db query executed inside loop via configured dynamic interface method %q", site.MethodName)
				explainReason = "dynamic interface call matched configured custom method (terminal=false)"
				// Case F: unresolved dynamic call with query-like method name.
			} else if site.DynamicQueryLike {
				level = "possible"
				msg = fmt.Sprintf("possible db query executed inside loop (dynamic call %q)", site.MethodName)
				explainReason = "dynamic unresolved call with DB-like method name allowlist match"
			} else {
				continue
			}

			if !cfg.ReportsConfidence(level) {
				continue
			}

			entry := baseline.Entry{
				Rule:       RuleID,
				Confidence: level,
				Package:    pass.Pkg.Path(),
				File:       pass.Fset.PositionFor(site.Pos, false).Filename,
				Line:       pass.Fset.PositionFor(site.Pos, false).Line,
				Function:   callerName,
				Terminal:   chain[len(chain)-1],
				Chain:      chain,
			}
			if cfg.Baseline.Mode == "new_only" && baselineSet.Has(entry) {
				continue
			}
			// Capture is used by CLI stats/baseline workflows and may be restricted
			// to root packages only.
			if opts.CaptureSink != nil && capturePackageAllowed(pass.Pkg.Path(), opts.CapturePackageAllowset) {
				if err := opts.CaptureSink.Add(entry); err != nil {
					return nil, err
				}
			}
			if opts.CaptureOnly {
				continue
			}

			// Human-facing diagnostic payload.
			diagMsg := fmt.Sprintf(
				"%s [%s]: %s. Stats: chain_depth=%d. Hint: batch queries or move DB access outside the loop.",
				RuleID,
				level,
				msg,
				max(0, len(chain)-1),
			)
			if explainMode {
				// Keep explain payload structured and deterministic so users can diff/debug classifications.
				callee := "<dynamic>"
				if site.Callee != nil {
					callee = displayFn(site.Callee)
				} else if len(site.ResolvedCallees) > 0 {
					callee = displayFn(site.ResolvedCallees[0])
				} else if len(site.ResolvedHints) > 0 && site.ResolvedHints[0].Name != "" {
					callee = site.ResolvedHints[0].Name
				}
				diagMsg = fmt.Sprintf(
					"%s Explain: reason=%s; package=%s; function=%s; callee=%s; method=%s; chain=%s; report_levels=%s; fail_levels=%s.",
					diagMsg,
					explainReason,
					pass.Pkg.Path(),
					callerName,
					callee,
					site.MethodName,
					strings.Join(chain, " -> "),
					strings.Join(cfg.Rules.QueryInLoop.Report, ","),
					strings.Join(cfg.Rules.QueryInLoop.FailOn, ","),
				)
			}

			pass.Report(analysis.Diagnostic{
				Pos:      site.Pos,
				Category: RuleID,
				Message:  diagMsg,
			})
		}
	}

	suppressions.reportUnused(pass, cfg)
	return nil, nil
}

// summarizeFunction inspects SSA instructions once and builds:
// - direct certainty flags
// - outgoing call edges for propagation
// - loop callsites with enough metadata for final classification/reporting
func summarizeFunction(
	pass *analysis.Pass,
	cfg *config.Config,
	fn *ssa.Function,
	loops *loopIndex,
	suppressions *suppressionIndex,
	match *matcher.Matcher,
	queryFuncCache map[*ssa.Function]uint8,
	dynamicDispatch map[token.Pos]dispatchTargets,
) *funcSummary {
	sum := &funcSummary{Fn: fn}

	// Walk all call-like instructions and derive callsite metadata.
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			common, ok := callCommon(instr)
			if !ok || common == nil {
				continue
			}
			pos := instr.Pos()
			if pos == token.NoPos {
				continue
			}

			callee := common.StaticCallee()
			methodName := ""
			resolvedCallees := make([]*ssa.Function, 0)
			resolvedHints := make([]targetHint, 0)
			resolvedPrecise := false
			dynamicDefiniteDB := false
			dynamicCustomDef := false
			dynamicCustomPoss := false
			dynamicQueryLike := false

			if callee == nil {
				if fnv, ok := common.Value.(*ssa.Function); ok {
					callee = fnv
				}
			}
			if callee != nil {
				methodName = callee.Name()
			} else {
				// Dynamic callsite: try precomputed dispatch targets first.
				if site, ok := instr.(ssa.CallInstruction); ok && dynamicDispatch != nil {
					if targets, ok := dynamicDispatch[site.Pos()]; ok {
						if len(targets.Callees) > 0 {
							resolvedCallees = append(resolvedCallees, targets.Callees...)
						}
						if len(targets.Hints) > 0 {
							resolvedHints = append(resolvedHints, targets.Hints...)
						}
						if len(targets.Callees) > 0 || len(targets.Hints) > 0 {
							resolvedPrecise = targets.Precise
						}
					}
				}
				if common.Method != nil {
					// Unresolved interface method: classify by signature/custom rules.
					methodName = common.Method.Name()
					dynamicDefiniteDB = isDBLikeInterfaceMethod(common.Method)
					if !dynamicDefiniteDB {
						matched, definite := match.MatchDynamicCustomMethod(pass.Pkg.Path(), common.Method)
						if matched {
							dynamicCustomDef = definite
							dynamicCustomPoss = !definite
						}
					}
				}
				if !dynamicDefiniteDB && !dynamicCustomDef && !dynamicCustomPoss {
					dynamicQueryLike = match.IsLikelyQueryMethodName(methodName)
				}
			}

			if callee != nil {
				// Static calls contribute propagation edges and direct query certainty.
				sum.Edges = append(sum.Edges, callEdge{To: callee})
				if isDatabaseSQLCursorMethod(callee) {
					// Cursor iteration/scan APIs are not query execution points.
				} else if isQueryFuncCached(match, queryFuncCache, callee) {
					sum.DirectDefinite = true
				}
			} else if len(resolvedCallees) > 0 {
				if resolvedPrecise {
					// Refined targets are treated like static edges.
					for _, resolved := range resolvedCallees {
						if resolved == nil {
							continue
						}
						if isDatabaseSQLCursorMethod(resolved) {
							continue
						}
						sum.Edges = append(sum.Edges, callEdge{To: resolved})
						if isQueryFuncCached(match, queryFuncCache, resolved) {
							sum.DirectDefinite = true
						}
					}
				} else {
					// CHA fallback is sound but imprecise; keep confidence at possible.
					for _, resolved := range resolvedCallees {
						if isDatabaseSQLCursorMethod(resolved) {
							continue
						}
						if isQueryFuncCached(match, queryFuncCache, resolved) {
							sum.DirectPossible = true
							break
						}
					}
				}
			} else if dynamicDefiniteDB || dynamicCustomDef {
				sum.DirectDefinite = true
			} else if dynamicCustomPoss || dynamicQueryLike {
				sum.DirectPossible = true
			}

			// We only track loop callsites for reporting, even though edges were
			// already recorded for propagation.
			inLoop, loopSuppressed := inLoopContext(pass.Fset, pos, loops)
			if !inLoop {
				continue
			}
			callSuppressed := suppressions.consume(pass, pos, RuleID, cfg)
			sum.LoopCallsites = append(sum.LoopCallsites, loopCallsite{
				Pos:               pos,
				Callee:            callee,
				ResolvedCallees:   resolvedCallees,
				ResolvedHints:     resolvedHints,
				ResolvedPrecise:   resolvedPrecise,
				MethodName:        methodName,
				DynamicDefiniteDB: dynamicDefiniteDB,
				DynamicCustomDef:  dynamicCustomDef,
				DynamicCustomPoss: dynamicCustomPoss,
				DynamicQueryLike:  dynamicQueryLike,
				LoopSuppressed:    loopSuppressed,
				CallSuppressed:    callSuppressed,
			})
		}
	}

	return sum
}

// propagate computes a fixpoint over summaries so each function knows whether
// any reachable path can definitely/possibly execute a query.
func propagate(pass *analysis.Pass, summaries map[*ssa.Function]*funcSummary) map[*ssa.Function]funcState {
	states := make(map[*ssa.Function]funcState, len(summaries))
	for fn, sum := range summaries {
		states[fn] = funcState{Definite: sum.DirectDefinite, Possible: sum.DirectPossible || sum.DirectDefinite}
	}

	for i := 0; i < len(summaries)+4; i++ {
		changed := false
		for fn, sum := range summaries {
			st := states[fn]
			definite := st.Definite
			possible := st.Possible
			for _, edge := range sum.Edges {
				if edge.To == nil {
					continue
				}
				if dst, ok := states[edge.To]; ok {
					definite = definite || dst.Definite
					possible = possible || dst.Possible || dst.Definite
					continue
				}
				fact := importFact(pass, edge.To)
				definite = definite || fact.HasDefiniteQuery
				possible = possible || fact.HasPossibleQuery || fact.HasDefiniteQuery
			}
			if definite != st.Definite || possible != st.Possible {
				states[fn] = funcState{Definite: definite, Possible: possible}
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return states
}

// exportFacts publishes per-function certainty so dependent packages can import
// query capability via go/analysis facts.
func exportFacts(pass *analysis.Pass, states map[*ssa.Function]funcState) {
	for fn, state := range states {
		obj := fn.Object()
		if obj == nil {
			continue
		}
		fact := &QueryFact{HasDefiniteQuery: state.Definite, HasPossibleQuery: state.Possible}
		pass.ExportObjectFact(obj, fact)
	}
}

// queryPath returns one representative call chain from caller to a terminal
// query-capable node, capped by maxDepth for bounded diagnostics.
func queryPath(
	caller *ssa.Function,
	start *ssa.Function,
	level string,
	summaries map[*ssa.Function]*funcSummary,
	states map[*ssa.Function]funcState,
	pass *analysis.Pass,
	maxDepth int,
) []string {
	if start == nil {
		return nil
	}
	if maxDepth <= 0 {
		maxDepth = 32
	}
	seen := map[*ssa.Function]bool{caller: true}
	path := dfsPath(start, level, summaries, states, pass, seen, 0, maxDepth)
	if len(path) == 0 {
		return nil
	}
	chain := []string{displayFn(caller)}
	for _, fn := range path {
		chain = append(chain, displayFn(fn))
	}
	return chain
}

// dfsPath searches one path to a terminal query node while avoiding cycles.
func dfsPath(
	fn *ssa.Function,
	level string,
	summaries map[*ssa.Function]*funcSummary,
	states map[*ssa.Function]funcState,
	pass *analysis.Pass,
	seen map[*ssa.Function]bool,
	depth int,
	maxDepth int,
) []*ssa.Function {
	if fn == nil || depth > maxDepth {
		return nil
	}

	if isTerminalQueryNode(fn, level, summaries, states, pass) {
		return []*ssa.Function{fn}
	}

	if seen[fn] {
		return nil
	}
	seen[fn] = true
	defer delete(seen, fn)

	sum := summaries[fn]
	if sum == nil {
		return nil
	}
	for _, edge := range sum.Edges {
		if edge.To == nil || seen[edge.To] {
			continue
		}
		if p := dfsPath(edge.To, level, summaries, states, pass, seen, depth+1, maxDepth); len(p) > 0 {
			return append([]*ssa.Function{fn}, p...)
		}
	}
	return nil
}

// isTerminalQueryNode decides whether a function can terminate a reported
// chain under the requested confidence level.
func isTerminalQueryNode(
	fn *ssa.Function,
	level string,
	summaries map[*ssa.Function]*funcSummary,
	states map[*ssa.Function]funcState,
	pass *analysis.Pass,
) bool {
	if st, ok := states[fn]; ok {
		if level == "definite" {
			return summaries[fn] != nil && summaries[fn].DirectDefinite
		}
		return st.Possible || st.Definite
	}
	fact := importFact(pass, fn)
	if level == "definite" {
		return fact.HasDefiniteQuery
	}
	return fact.HasPossibleQuery || fact.HasDefiniteQuery
}

// importFact reads propagated certainty for functions outside current package.
func importFact(pass *analysis.Pass, fn *ssa.Function) QueryFact {
	obj := fn.Object()
	if obj == nil {
		return QueryFact{}
	}
	var fact QueryFact
	if pass.ImportObjectFact(obj, &fact) {
		return fact
	}
	return QueryFact{}
}

// inLoopContext checks whether a token position lies inside any indexed loop.
func inLoopContext(fset *token.FileSet, pos token.Pos, loops *loopIndex) (inLoop bool, suppressed bool) {
	if loops == nil || fset == nil {
		return false, false
	}
	tf := fset.File(pos)
	if tf == nil {
		return false, false
	}
	for _, lr := range loops.ByFile[tf] {
		if pos >= lr.Start && pos <= lr.End {
			if lr.Suppressed {
				return true, true
			}
			inLoop = true
		}
	}
	return inLoop, false
}

// isQueryFuncCached memoizes matcher results to avoid repeated string/type work.
func isQueryFuncCached(match *matcher.Matcher, cache map[*ssa.Function]uint8, fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	if v, ok := cache[fn]; ok {
		return v == 1
	}
	if match.IsQueryFunc(fn) {
		cache[fn] = 1
		return true
	}
	cache[fn] = 2
	return false
}

// callCommon normalizes *ssa.Call/*ssa.Defer/*ssa.Go handling.
func callCommon(instr ssa.Instruction) (*ssa.CallCommon, bool) {
	switch c := instr.(type) {
	case *ssa.Call:
		return c.Common(), true
	case *ssa.Defer:
		return c.Common(), true
	case *ssa.Go:
		return c.Common(), true
	default:
		return nil, false
	}
}

// globalDispatchForPackage resolves cross-package dynamic targets once per
// cache key and returns only entries for the current package.
func globalDispatchForPackage(pass *analysis.Pass, cfg *config.Config, patterns []string) map[token.Pos]dispatchTargets {
	if pass == nil || pass.Pkg == nil {
		return nil
	}
	if len(patterns) == 0 {
		return nil
	}
	key := globalDispatchCacheKey(cfg, patterns)
	v, _ := globalDispatchCache.LoadOrStore(key, &globalDispatchCacheEntry{})
	entry := v.(*globalDispatchCacheEntry)
	entry.once.Do(func() {
		entry.byPkg, entry.err = buildGlobalDispatchIndex(cfg, patterns)
	})
	if entry.err != nil || entry.byPkg == nil {
		return nil
	}
	return entry.byPkg[pass.Pkg.Path()]
}

// globalDispatchCacheKey ensures cache reuse for equivalent pattern/mode input.
func globalDispatchCacheKey(cfg *config.Config, patterns []string) string {
	mode := strings.TrimSpace(cfg.Analysis.CallgraphMode)
	if mode == "" {
		mode = "cha_plus_vta"
	}
	cp := append([]string(nil), patterns...)
	sort.Strings(cp)
	return mode + "|" + strconv.FormatBool(cfg.Analysis.IncludeTests) + "|" + strings.Join(cp, ",")
}

// buildGlobalDispatchIndex builds a whole-program dynamic-dispatch map keyed by
// package path and callsite token position.
func buildGlobalDispatchIndex(cfg *config.Config, patterns []string) (map[string]map[token.Pos]dispatchTargets, error) {
	mode := strings.TrimSpace(cfg.Analysis.CallgraphMode)
	if mode == "" {
		mode = "cha_plus_vta"
	}
	if mode == "off" || mode == "none" {
		return nil, nil
	}

	loadMode := packages.NeedName |
		packages.NeedFiles |
		packages.NeedCompiledGoFiles |
		packages.NeedImports |
		packages.NeedDeps |
		packages.NeedTypes |
		packages.NeedSyntax |
		packages.NeedTypesInfo

	pkgs, err := packages.Load(&packages.Config{
		Mode:  loadMode,
		Tests: cfg.Analysis.IncludeTests,
	}, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages for global dispatch: %w", err)
	}
	if err := firstPackagesError(pkgs); err != nil {
		return nil, fmt.Errorf("load packages for global dispatch: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	prog, _ := ssautil.Packages(pkgs, ssa.BuilderMode(0))
	prog.Build()

	match := matcher.New(cfg.QueryMatch)
	stateByFn := computeGlobalFunctionStates(ssautil.AllFunctions(prog), match)

	switch mode {
	case "cha":
		return indexDynamicDispatchEdgesByPackage(cha.CallGraph(prog), match, stateByFn, false), nil
	case "cha_plus_vta", "vta":
		base := cha.CallGraph(prog)
		refined := vta.CallGraph(ssautil.AllFunctions(prog), base)
		precise := indexDynamicDispatchEdgesByPackage(refined, match, stateByFn, true)
		fallback := indexDynamicDispatchEdgesByPackage(base, match, stateByFn, false)
		return mergePackageDispatch(precise, fallback), nil
	default:
		base := cha.CallGraph(prog)
		refined := vta.CallGraph(ssautil.AllFunctions(prog), base)
		precise := indexDynamicDispatchEdgesByPackage(refined, match, stateByFn, true)
		if len(precise) > 0 {
			return precise, nil
		}
		return indexDynamicDispatchEdgesByPackage(base, match, stateByFn, false), nil
	}
}

// firstPackagesError returns the first load/type error from package graph.
func firstPackagesError(pkgs []*packages.Package) error {
	var first error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if first != nil {
			return
		}
		for _, e := range p.Errors {
			first = fmt.Errorf("%s: %s", p.PkgPath, e.Msg)
			return
		}
	})
	return first
}

// computeGlobalFunctionStates is a lightweight global certainty pass used only
// for dynamic-dispatch hints in cross-package mode.
func computeGlobalFunctionStates(funcs map[*ssa.Function]bool, match *matcher.Matcher) map[*ssa.Function]funcState {
	type globalSummary struct {
		DirectDefinite bool
		DirectPossible bool
		Edges          []*ssa.Function
	}

	summaries := make(map[*ssa.Function]*globalSummary, len(funcs))
	for fn := range funcs {
		if fn == nil || fn.Blocks == nil || fn.Pkg == nil {
			continue
		}
		sum := &globalSummary{}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				common, ok := callCommon(instr)
				if !ok || common == nil {
					continue
				}
				callee := common.StaticCallee()
				if callee == nil {
					if fnv, ok := common.Value.(*ssa.Function); ok {
						callee = fnv
					}
				}
				if callee != nil {
					if isDatabaseSQLCursorMethod(callee) {
						continue
					}
					sum.Edges = append(sum.Edges, callee)
					if match.IsQueryFunc(callee) {
						sum.DirectDefinite = true
					}
					continue
				}

				if common.Method == nil {
					continue
				}
				if isDBLikeInterfaceMethod(common.Method) {
					sum.DirectDefinite = true
					continue
				}
				if match.IsLikelyQueryMethodName(common.Method.Name()) {
					sum.DirectPossible = true
				}
			}
		}
		summaries[fn] = sum
	}

	states := make(map[*ssa.Function]funcState, len(summaries))
	for fn, sum := range summaries {
		states[fn] = funcState{Definite: sum.DirectDefinite, Possible: sum.DirectPossible || sum.DirectDefinite}
	}
	for i := 0; i < len(summaries)+4; i++ {
		changed := false
		for fn, sum := range summaries {
			st := states[fn]
			definite := st.Definite
			possible := st.Possible
			for _, edge := range sum.Edges {
				if edge == nil || isDatabaseSQLCursorMethod(edge) {
					continue
				}
				if dst, ok := states[edge]; ok {
					definite = definite || dst.Definite
					possible = possible || dst.Possible || dst.Definite
					continue
				}
				if match.IsQueryFunc(edge) {
					definite = true
					possible = true
				}
			}
			if definite != st.Definite || possible != st.Possible {
				states[fn] = funcState{Definite: definite, Possible: possible}
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return states
}

// indexDynamicDispatchEdgesByPackage converts callgraph edges into per-package
// callsite maps and attaches precomputed certainty hints.
func indexDynamicDispatchEdgesByPackage(
	cg *callgraph.Graph,
	match *matcher.Matcher,
	stateByFn map[*ssa.Function]funcState,
	precise bool,
) map[string]map[token.Pos]dispatchTargets {
	out := make(map[string]map[token.Pos]dispatchTargets)
	if cg == nil {
		return out
	}

	for _, node := range cg.Nodes {
		if node == nil || node.Func == nil || node.Func.Pkg == nil || node.Func.Pkg.Pkg == nil {
			continue
		}
		pkgPath := node.Func.Pkg.Pkg.Path()
		if pkgPath == "" {
			continue
		}
		pkgOut, ok := out[pkgPath]
		if !ok {
			pkgOut = make(map[token.Pos]dispatchTargets)
			out[pkgPath] = pkgOut
		}
		for _, edge := range node.Out {
			if edge == nil || edge.Site == nil || edge.Callee == nil || edge.Callee.Func == nil {
				continue
			}
			callee := edge.Callee.Func
			if isDatabaseSQLCursorMethod(callee) {
				continue
			}
			hint := buildTargetHint(callee, match, stateByFn)
			if !hint.QueryFunc && !hint.Definite && !hint.Possible {
				continue
			}
			pos := edge.Site.Pos()
			if pos == token.NoPos {
				continue
			}
			cur := pkgOut[pos]
			cur.Precise = precise
			cur.Callees = appendUniqueFn(cur.Callees, callee)
			cur.Hints = appendUniqueHint(cur.Hints, hint)
			pkgOut[pos] = normalizeDispatchTargets(cur)
		}
	}

	return out
}

// buildTargetHint snapshots the certainty we know about a dynamic target.
func buildTargetHint(fn *ssa.Function, match *matcher.Matcher, stateByFn map[*ssa.Function]funcState) targetHint {
	h := targetHint{Name: displayFn(fn)}
	if fn == nil {
		return h
	}
	h.QueryFunc = match.IsQueryFunc(fn)
	if st, ok := stateByFn[fn]; ok {
		h.Definite = st.Definite
		h.Possible = st.Possible
	}
	if h.QueryFunc {
		h.Definite = true
		h.Possible = true
	}
	return h
}

// mergePackageDispatch merges cross-package dispatch maps (usually precise +
// fallback), preferring precise callsites where available.
func mergePackageDispatch(
	primary map[string]map[token.Pos]dispatchTargets,
	secondary map[string]map[token.Pos]dispatchTargets,
) map[string]map[token.Pos]dispatchTargets {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}
	out := make(map[string]map[token.Pos]dispatchTargets, len(primary)+len(secondary))
	for pkg, byPos := range primary {
		out[pkg] = mergeDispatchTargets(nil, byPos)
	}
	for pkg, byPos := range secondary {
		out[pkg] = mergeDispatchTargets(out[pkg], byPos)
	}
	return out
}

// mergeDispatchTargets merges two per-position dispatch maps while preserving
// "precise beats fallback" semantics.
func mergeDispatchTargets(
	primary map[token.Pos]dispatchTargets,
	secondary map[token.Pos]dispatchTargets,
) map[token.Pos]dispatchTargets {
	if len(primary) == 0 && len(secondary) == 0 {
		return nil
	}
	out := make(map[token.Pos]dispatchTargets, len(primary)+len(secondary))
	for pos, t := range primary {
		out[pos] = normalizeDispatchTargets(dispatchTargets{
			Callees: append([]*ssa.Function(nil), t.Callees...),
			Hints:   append([]targetHint(nil), t.Hints...),
			Precise: t.Precise,
		})
	}
	for pos, t := range secondary {
		cur, ok := out[pos]
		if !ok {
			out[pos] = normalizeDispatchTargets(dispatchTargets{
				Callees: append([]*ssa.Function(nil), t.Callees...),
				Hints:   append([]targetHint(nil), t.Hints...),
				Precise: t.Precise,
			})
			continue
		}
		if !cur.Precise && t.Precise {
			cur = dispatchTargets{
				Callees: append([]*ssa.Function(nil), t.Callees...),
				Hints:   append([]targetHint(nil), t.Hints...),
				Precise: true,
			}
			out[pos] = normalizeDispatchTargets(cur)
			continue
		}
		if cur.Precise && !t.Precise {
			continue
		}
		for _, fn := range t.Callees {
			cur.Callees = appendUniqueFn(cur.Callees, fn)
		}
		for _, h := range t.Hints {
			cur.Hints = appendUniqueHint(cur.Hints, h)
		}
		cur.Precise = cur.Precise || t.Precise
		out[pos] = normalizeDispatchTargets(cur)
	}
	return out
}

// normalizeDispatchTargets sorts targets/hints for stable deterministic output.
func normalizeDispatchTargets(t dispatchTargets) dispatchTargets {
	sort.Slice(t.Callees, func(i, j int) bool {
		return displayFn(t.Callees[i]) < displayFn(t.Callees[j])
	})
	sort.Slice(t.Hints, func(i, j int) bool {
		if t.Hints[i].Name != t.Hints[j].Name {
			return t.Hints[i].Name < t.Hints[j].Name
		}
		if t.Hints[i].Definite != t.Hints[j].Definite {
			return t.Hints[i].Definite
		}
		if t.Hints[i].Possible != t.Hints[j].Possible {
			return t.Hints[i].Possible
		}
		return t.Hints[i].QueryFunc
	})
	return t
}

// appendUniqueHint keeps hint lists deduplicated by all semantic fields.
func appendUniqueHint(in []targetHint, h targetHint) []targetHint {
	for _, existing := range in {
		if existing.Name == h.Name &&
			existing.QueryFunc == h.QueryFunc &&
			existing.Definite == h.Definite &&
			existing.Possible == h.Possible {
			return in
		}
	}
	return append(in, h)
}

// buildDynamicDispatchIndex builds local package dynamic-dispatch resolution.
// Depending on mode, it uses CHA, CHA+VTA, or no dynamic resolution.
func buildDynamicDispatchIndex(cfg *config.Config, ssaResult *buildssa.SSA) map[token.Pos]dispatchTargets {
	if ssaResult == nil || ssaResult.Pkg == nil || ssaResult.Pkg.Prog == nil {
		return nil
	}
	if !hasUnresolvedCalls(ssaResult.SrcFuncs) {
		return nil
	}

	mode := strings.TrimSpace(cfg.Analysis.CallgraphMode)
	if mode == "" {
		mode = "cha_plus_vta"
	}

	targetPkg := ""
	if ssaResult.Pkg != nil && ssaResult.Pkg.Pkg != nil {
		targetPkg = ssaResult.Pkg.Pkg.Path()
	}

	switch mode {
	case "off", "none":
		return nil
	case "cha":
		baseIndex := indexDynamicDispatchEdges(cha.CallGraph(ssaResult.Pkg.Prog), targetPkg)
		out := make(map[token.Pos]dispatchTargets, len(baseIndex))
		for pos, callees := range baseIndex {
			out[pos] = dispatchTargets{Callees: callees, Precise: false}
		}
		return out
	case "cha_plus_vta", "vta":
		base := cha.CallGraph(ssaResult.Pkg.Prog)
		refined := vta.CallGraph(ssautil.AllFunctions(ssaResult.Pkg.Prog), base)
		refinedIndex := indexDynamicDispatchEdges(refined, targetPkg)
		baseIndex := indexDynamicDispatchEdges(base, targetPkg)
		out := make(map[token.Pos]dispatchTargets, len(refinedIndex)+len(baseIndex))
		for pos, callees := range refinedIndex {
			out[pos] = dispatchTargets{Callees: callees, Precise: true}
		}
		// Prefer VTA precision, but fall back to CHA for sites VTA cannot resolve.
		for pos, callees := range baseIndex {
			if _, ok := out[pos]; ok {
				continue
			}
			out[pos] = dispatchTargets{Callees: callees, Precise: false}
		}
		return out
	default:
		// Be permissive for unknown values and use the default precision mode.
		base := cha.CallGraph(ssaResult.Pkg.Prog)
		refined := vta.CallGraph(ssautil.AllFunctions(ssaResult.Pkg.Prog), base)
		refinedIndex := indexDynamicDispatchEdges(refined, targetPkg)
		if len(refinedIndex) > 0 {
			out := make(map[token.Pos]dispatchTargets, len(refinedIndex))
			for pos, callees := range refinedIndex {
				out[pos] = dispatchTargets{Callees: callees, Precise: true}
			}
			return out
		}
		baseIndex := indexDynamicDispatchEdges(base, targetPkg)
		out := make(map[token.Pos]dispatchTargets, len(baseIndex))
		for pos, callees := range baseIndex {
			out[pos] = dispatchTargets{Callees: callees, Precise: false}
		}
		return out
	}
}

// indexDynamicDispatchEdges extracts dynamic call edges keyed by callsite pos.
func indexDynamicDispatchEdges(cg *callgraph.Graph, targetPkg string) map[token.Pos][]*ssa.Function {
	out := make(map[token.Pos][]*ssa.Function)
	if cg == nil {
		return out
	}

	for _, node := range cg.Nodes {
		if node == nil || node.Func == nil || node.Func.Pkg == nil || node.Func.Pkg.Pkg == nil {
			continue
		}
		if targetPkg != "" && node.Func.Pkg.Pkg.Path() != targetPkg {
			continue
		}
		for _, edge := range node.Out {
			if edge == nil || edge.Site == nil || edge.Callee == nil || edge.Callee.Func == nil {
				continue
			}
			pos := edge.Site.Pos()
			if pos == token.NoPos {
				continue
			}
			out[pos] = appendUniqueFn(out[pos], edge.Callee.Func)
		}
	}
	for pos, callees := range out {
		sort.Slice(callees, func(i, j int) bool {
			return displayFn(callees[i]) < displayFn(callees[j])
		})
		out[pos] = callees
	}
	return out
}

// hasUnresolvedCalls is a fast gate to avoid callgraph work when everything is
// statically resolved.
func hasUnresolvedCalls(funcs []*ssa.Function) bool {
	for _, fn := range funcs {
		if fn == nil || fn.Blocks == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				common, ok := callCommon(instr)
				if !ok || common == nil {
					continue
				}
				if common.StaticCallee() == nil {
					return true
				}
			}
		}
	}
	return false
}

// appendUniqueFn appends fn only if it is non-nil and not already present.
func appendUniqueFn(in []*ssa.Function, fn *ssa.Function) []*ssa.Function {
	if fn == nil {
		return in
	}
	for _, existing := range in {
		if existing == fn {
			return in
		}
	}
	return append(in, fn)
}

// isDBLikeInterfaceMethod matches dynamic interface methods that are strongly
// equivalent to database/sql query/exec signatures.
func isDBLikeInterfaceMethod(m *types.Func) bool {
	if m == nil {
		return false
	}
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return false
	}

	switch m.Name() {
	case "ExecContext":
		return sig.Variadic() &&
			sig.Params().Len() == 3 &&
			isContextType(sig.Params().At(0).Type()) &&
			isStringType(sig.Params().At(1).Type()) &&
			sig.Results().Len() == 2 &&
			isDatabaseSQLNamed(sig.Results().At(0).Type(), "Result") &&
			isErrorType(sig.Results().At(1).Type())
	case "QueryContext":
		return sig.Variadic() &&
			sig.Params().Len() == 3 &&
			isContextType(sig.Params().At(0).Type()) &&
			isStringType(sig.Params().At(1).Type()) &&
			sig.Results().Len() == 2 &&
			isDatabaseSQLNamed(sig.Results().At(0).Type(), "Rows") &&
			isErrorType(sig.Results().At(1).Type())
	case "QueryRowContext":
		return sig.Variadic() &&
			sig.Params().Len() == 3 &&
			isContextType(sig.Params().At(0).Type()) &&
			isStringType(sig.Params().At(1).Type()) &&
			sig.Results().Len() == 1 &&
			isDatabaseSQLNamed(sig.Results().At(0).Type(), "Row")
	default:
		return false
	}
}

// isContextType reports whether t is exactly context.Context.
func isContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// isStringType reports whether t is built-in string.
func isStringType(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Kind() == types.String
}

// isErrorType reports whether t is the predeclared error interface.
func isErrorType(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

// isDatabaseSQLNamed matches database/sql named or pointer-to-named types.
func isDatabaseSQLNamed(t types.Type, name string) bool {
	switch tt := t.(type) {
	case *types.Named:
		obj := tt.Obj()
		if obj == nil || obj.Pkg() == nil {
			return false
		}
		return obj.Pkg().Path() == "database/sql" && obj.Name() == name
	case *types.Pointer:
		elem, ok := tt.Elem().(*types.Named)
		if !ok {
			return false
		}
		obj := elem.Obj()
		if obj == nil || obj.Pkg() == nil {
			return false
		}
		return obj.Pkg().Path() == "database/sql" && obj.Name() == name
	default:
		return false
	}
}

// displayFn returns a stable readable function name for diagnostics/sorting.
func displayFn(fn *ssa.Function) string {
	if fn == nil {
		return "<nil>"
	}
	if fn.String() != "" {
		return fn.String()
	}
	return fn.Name()
}

// isDatabaseSQLCursorMethod filters cursor iteration APIs that should not be
// treated as query execution points.
func isDatabaseSQLCursorMethod(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	if fn.Pkg.Pkg.Path() != "database/sql" {
		return false
	}
	switch fn.Name() {
	case "Next", "NextResultSet", "Scan", "Err", "Close", "Columns", "ColumnTypes":
		return true
	default:
		return false
	}
}
