package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mario-pinderi/goqueryguard/internal/analyzer/loopquery"
	"github.com/mario-pinderi/goqueryguard/internal/baseline"
	"github.com/mario-pinderi/goqueryguard/internal/config"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

const (
	envConfig       = "GOQUERYGUARD_CONFIG"
	envExplain      = "GOQUERYGUARD_EXPLAIN"
	envDisableStats = "GOQUERYGUARD_DISABLE_STATS"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func main() {
	args, explain, err := stripExplainFlag(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if len(args) > 0 && args[0] == "baseline" {
		if err := runBaseline(args[1:], explain); err != nil {
			var ee *exitError
			if errors.As(err, &ee) {
				if ee.err != nil {
					fmt.Fprintln(os.Stderr, ee.err)
				}
				os.Exit(ee.code)
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}

	code, err := runStandard(args, explain)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(code)
}

type analyzerRunOptions struct {
	ConfigPath      string
	Explain         bool
	CaptureOnly     bool
	BaselineMode    string
	CollectFindings bool
	NoFactExport    bool
}

func runStandard(args []string, explain bool) (int, error) {
	start := time.Now()
	opts := analyzerRunOptions{
		ConfigPath:      strings.TrimSpace(os.Getenv(envConfig)),
		Explain:         explain,
		CollectFindings: strings.TrimSpace(os.Getenv(envDisableStats)) != "1",
	}

	exitCode, entries, err := runAnalyzer(args, opts)
	if err != nil {
		return 0, err
	}
	if opts.CollectFindings {
		printStats(os.Stderr, "findings", baseline.ComputeStats(dedupeEntries(entries)), time.Since(start))
	}
	return exitCode, nil
}

func runAnalyzer(args []string, opts analyzerRunOptions) (int, []baseline.Entry, error) {
	patterns := packagePatternsFromArgs(args)
	if len(patterns) == 0 {
		return 0, nil, fmt.Errorf("usage: goqueryguard [packages]")
	}

	cfg, err := config.Load(strings.TrimSpace(opts.ConfigPath))
	if err != nil {
		return 0, nil, err
	}

	initial, err := packages.Load(&packages.Config{
		Mode:  packages.LoadAllSyntax,
		Tests: cfg.Analysis.IncludeTests,
	}, patterns...)
	if err != nil {
		return 0, nil, fmt.Errorf("load packages: %w", err)
	}
	if len(initial) == 0 {
		return 0, nil, fmt.Errorf("%s matched no packages", strings.Join(patterns, " "))
	}

	pkgsExitCode := 0
	if n := packages.PrintErrors(initial); n > 0 {
		pkgsExitCode = 1
	}

	var collector *loopquery.FindingCollector
	if opts.CollectFindings {
		collector = loopquery.NewFindingCollector()
	}
	runtime := loopquery.RuntimeOptions{
		Explain:                opts.Explain,
		PackagePatterns:        patterns,
		BaselineModeOverride:   strings.TrimSpace(opts.BaselineMode),
		DisableFactExport:      opts.NoFactExport,
		CaptureOnly:            opts.CaptureOnly,
		CaptureSink:            collector,
		CapturePackageAllowset: rootPackageSet(initial),
	}

	graph, err := checker.Analyze([]*analysis.Analyzer{loopquery.NewWithRuntime(cfg, runtime)}, initial, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("run analyzer: %w", err)
	}
	if err := printDiagnostics(graph, cfg.Output.Format); err != nil {
		return 0, nil, err
	}

	if collector == nil {
		return analysisExitCode(graph, pkgsExitCode), nil, nil
	}
	return analysisExitCode(graph, pkgsExitCode), collector.Entries(), nil
}

func printDiagnostics(graph *checker.Graph, format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return graph.PrintText(os.Stderr, -1)
	case "json":
		return graph.PrintJSON(os.Stdout)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func analysisExitCode(graph *checker.Graph, pkgsExitCode int) int {
	exitCode := pkgsExitCode
	graph.All()(func(act *checker.Action) bool {
		if act.Err != nil {
			exitCode = 1
			return true
		}
		if act.IsRoot && len(act.Diagnostics) > 0 {
			exitCode = 1
		}
		return true
	})
	return exitCode
}

func packagePatternsFromArgs(args []string) []string {
	patterns := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		patterns = append(patterns, arg)
	}
	return patterns
}

func runBaseline(args []string, explain bool) error {
	start := time.Now()
	if len(args) == 0 {
		return &exitError{code: 2, err: fmt.Errorf("usage: goqueryguard baseline <write|check> [flags] [packages]")}
	}

	subcmd := args[0]
	fs := flag.NewFlagSet("goqueryguard baseline "+subcmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("config", strings.TrimSpace(os.Getenv(envConfig)), "path to config file")
	outFile := fs.String("file", "", "baseline file path")
	if err := fs.Parse(args[1:]); err != nil {
		return &exitError{code: 2, err: err}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	baselineFile := *outFile
	if baselineFile == "" {
		baselineFile = cfg.Baseline.File
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	entries, err := captureFindings(*cfgPath, patterns, explain)
	if err != nil {
		return &exitError{code: 2, err: err}
	}

	switch subcmd {
	case "write":
		if err := baseline.Write(baselineFile, entries); err != nil {
			return &exitError{code: 2, err: err}
		}
		fmt.Fprintf(os.Stderr, "wrote %d baseline findings to %s\n", len(entries), baselineFile)
		printStats(os.Stderr, "baseline findings", baseline.ComputeStats(entries), time.Since(start))
		return nil
	case "check":
		set, err := baseline.LoadSet(baselineFile)
		if err != nil {
			return &exitError{code: 2, err: err}
		}
		newFindings := make([]baseline.Entry, 0)
		for _, e := range entries {
			if !set.Has(e) {
				newFindings = append(newFindings, e)
			}
		}
		if len(newFindings) == 0 {
			fmt.Fprintf(os.Stderr, "no new findings compared to %s\n", baselineFile)
			printStats(os.Stderr, "current findings", baseline.ComputeStats(entries), time.Since(start))
			return nil
		}
		for _, e := range newFindings {
			fmt.Fprintln(os.Stderr, formatBaselineFinding(e, explain))
		}
		printStats(os.Stderr, "new findings", baseline.ComputeStats(newFindings), time.Since(start))
		printStats(os.Stderr, "current findings", baseline.ComputeStats(entries), time.Since(start))
		return &exitError{code: 1, err: fmt.Errorf("%d new findings", len(newFindings))}
	default:
		return &exitError{code: 2, err: fmt.Errorf("unknown baseline subcommand %q", subcmd)}
	}
}

func captureFindings(configPath string, patterns []string, explain bool) ([]baseline.Entry, error) {
	exitCode, entries, err := runAnalyzer(patterns, analyzerRunOptions{
		ConfigPath:      configPath,
		Explain:         explain,
		CaptureOnly:     true,
		BaselineMode:    "enforce_all",
		CollectFindings: true,
	})
	if err != nil {
		return nil, fmt.Errorf("collect findings from analyzer: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("collect findings from analyzer: analyzer exited with code %d", exitCode)
	}
	return dedupeEntries(entries), nil
}

func stripExplainFlag(args []string) ([]string, bool, error) {
	explain := strings.TrimSpace(os.Getenv(envExplain)) == "1"
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg == "--explain":
			explain = true
		case strings.HasPrefix(arg, "--explain="):
			raw := strings.TrimPrefix(arg, "--explain=")
			v, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, false, fmt.Errorf("invalid --explain value %q", raw)
			}
			explain = v
		default:
			out = append(out, arg)
		}
	}
	return out, explain, nil
}

func rootPackageSet(initial []*packages.Package) map[string]struct{} {
	out := make(map[string]struct{}, len(initial))
	for _, pkg := range initial {
		if pkg == nil {
			continue
		}
		path := strings.TrimSpace(pkg.PkgPath)
		if path == "" {
			continue
		}
		out[path] = struct{}{}
	}
	return out
}

func dedupeEntries(in []baseline.Entry) []baseline.Entry {
	seen := make(map[string]struct{}, len(in))
	out := make([]baseline.Entry, 0, len(in))
	for _, e := range in {
		k := e.Key()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	return out
}

func formatBaselineFinding(e baseline.Entry, explain bool) string {
	base := fmt.Sprintf("%s:%d %s [%s] %s -> %s", e.File, e.Line, e.Rule, e.Confidence, e.Function, e.Terminal)
	if !explain {
		return base
	}

	// Mirror analyzer explain mode so baseline diffs include enough context to triage quickly.
	chain := strings.Join(e.Chain, " -> ")
	if chain == "" {
		chain = e.Function
	}

	return fmt.Sprintf(
		"%s Explain: package=%s; function=%s; terminal=%s; chain_depth=%d; chain=%s.",
		base,
		e.Package,
		e.Function,
		e.Terminal,
		max(0, len(e.Chain)-1),
		chain,
	)
}

func printStats(w *os.File, label string, stats baseline.Stats, elapsed time.Duration) {
	fmt.Fprintf(w, "%s: total=%d", label, stats.Total)
	if v, ok := stats.ByConfidence["definite"]; ok {
		fmt.Fprintf(w, ", definite=%d", v)
	} else {
		fmt.Fprint(w, ", definite=0")
	}
	if v, ok := stats.ByConfidence["possible"]; ok {
		fmt.Fprintf(w, ", possible=%d", v)
	} else {
		fmt.Fprint(w, ", possible=0")
	}
	fmt.Fprintf(
		w,
		", packages=%d, chain_depth_avg=%.2f, chain_depth_max=%d, elapsed=%s\n",
		len(stats.ByPackage),
		stats.AvgChainDepth,
		stats.MaxChainDepth,
		elapsed.Round(time.Millisecond),
	)

	if len(stats.ByRule) == 0 {
		return
	}
	keys := make([]string, 0, len(stats.ByRule))
	for k := range stats.ByRule {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, stats.ByRule[k]))
	}
	fmt.Fprintf(w, "%s by rule: %s\n", label, strings.Join(parts, ", "))
}
