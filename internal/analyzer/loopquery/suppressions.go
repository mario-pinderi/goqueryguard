package loopquery

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/mario-pinderi/goqueryguard/internal/config"
	"golang.org/x/tools/go/analysis"
)

type suppression struct {
	Rule      string
	Reason    string
	Used      bool
	ReasonErr bool
	Pos       token.Pos
	TargetKey string
}

type suppressionIndex struct {
	ByTarget map[string][]*suppression
	All      []*suppression
}

func parseSuppressions(pass *analysis.Pass, cfg *config.Config) *suppressionIndex {
	idx := &suppressionIndex{ByTarget: make(map[string][]*suppression)}
	for _, f := range pass.Files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				raw := strings.TrimSpace(c.Text)
				raw = strings.TrimPrefix(raw, "//")
				raw = strings.TrimSuffix(strings.TrimPrefix(raw, "/*"), "*/")
				raw = strings.TrimSpace(raw)
				marker := cfg.Suppressions.Directive
				i := strings.Index(raw, marker)
				if i == -1 {
					continue
				}
				payload := strings.TrimSpace(raw[i+len(marker):])
				rule := "query-in-loop"
				reason := ""
				if payload != "" {
					parts := strings.SplitN(payload, "--", 2)
					head := strings.TrimSpace(parts[0])
					if head != "" {
						fields := strings.Fields(head)
						if len(fields) > 0 {
							rule = fields[0]
						}
					}
					if len(parts) == 2 {
						reason = strings.TrimSpace(parts[1])
					}
				}
				pos := pass.Fset.PositionFor(c.Slash, false)
				targetKey := fileLineKey(pos.Filename, pos.Line+1)
				s := &suppression{Rule: rule, Reason: reason, Pos: c.Slash, TargetKey: targetKey}
				idx.ByTarget[targetKey] = append(idx.ByTarget[targetKey], s)
				idx.All = append(idx.All, s)
			}
		}
	}
	return idx
}

func (s *suppressionIndex) consume(pass *analysis.Pass, pos token.Pos, rule string, cfg *config.Config) bool {
	p := pass.Fset.PositionFor(pos, false)
	cands := s.ByTarget[fileLineKey(p.Filename, p.Line)]
	for _, cand := range cands {
		if cand.Rule != rule {
			continue
		}
		cand.Used = true
		if cfg.Suppressions.RequireReason && cand.Reason == "" {
			cand.ReasonErr = true
			pass.Reportf(cand.Pos, "suppression for %s requires a reason", rule)
			return false
		}
		return true
	}
	return false
}

func (s *suppressionIndex) reportUnused(pass *analysis.Pass, cfg *config.Config) {
	if !cfg.Suppressions.ReportUnused {
		return
	}
	for _, sup := range s.All {
		if sup.Rule != "query-in-loop" {
			pass.Reportf(sup.Pos, "unsupported suppression rule %q", sup.Rule)
			continue
		}
		if cfg.Suppressions.RequireReason && sup.Reason == "" {
			if sup.ReasonErr {
				continue
			}
			pass.Reportf(sup.Pos, "suppression for %s requires a reason", sup.Rule)
			continue
		}
		if !sup.Used {
			pass.Reportf(sup.Pos, "unused suppression for %s", sup.Rule)
		}
	}
}

func collectLoops(files []*ast.File, fset *token.FileSet, suppressionIdx *suppressionIndex, cfg *config.Config) *loopIndex {
	idx := &loopIndex{ByFile: make(map[*token.File][]loopRange)}
	for _, f := range files {
		ast.Inspect(f, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ForStmt:
				tf := fset.File(n.Pos())
				if tf == nil {
					return true
				}
				p := fset.PositionFor(n.For, false)
				key := fileLineKey(p.Filename, p.Line)
				idx.ByFile[tf] = append(idx.ByFile[tf], loopRange{
					Start:      n.Pos(),
					End:        n.End(),
					Suppressed: hasSuppression(suppressionIdx.ByTarget[key], "query-in-loop", cfg),
				})
			case *ast.RangeStmt:
				tf := fset.File(n.Pos())
				if tf == nil {
					return true
				}
				p := fset.PositionFor(n.For, false)
				key := fileLineKey(p.Filename, p.Line)
				idx.ByFile[tf] = append(idx.ByFile[tf], loopRange{
					Start:      n.Pos(),
					End:        n.End(),
					Suppressed: hasSuppression(suppressionIdx.ByTarget[key], "query-in-loop", cfg),
				})
			}
			return true
		})
	}
	return idx
}

func hasSuppression(sups []*suppression, rule string, cfg *config.Config) bool {
	for _, s := range sups {
		if s.Rule == rule {
			if cfg.Suppressions.RequireReason && s.Reason == "" {
				return false
			}
			s.Used = true
			return true
		}
	}
	return false
}

func fileLineKey(file string, line int) string {
	return file + ":" + strconvItoa(line)
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + (v % 10))
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
