package matcher

import (
	"strings"

	"github.com/mario-pinderi/goqueryguard/internal/config"
	"go/types"
	"golang.org/x/tools/go/ssa"
)

type Matcher struct {
	cfg          config.QueryMatchConfig
	customLookup map[string]struct{}
	dynamicRules map[string][]dynamicRule
}

type dynamicRule struct {
	pkg      string
	receiver string
	terminal bool
}

func New(cfg config.QueryMatchConfig) *Matcher {
	m := &Matcher{
		cfg:          cfg,
		customLookup: make(map[string]struct{}),
		dynamicRules: make(map[string][]dynamicRule),
	}
	for _, cm := range cfg.CustomMethods {
		for _, method := range cm.Methods {
			k := customKey(cm.Package, cm.Receiver, method)
			m.customLookup[k] = struct{}{}
			m.dynamicRules[method] = append(m.dynamicRules[method], dynamicRule{
				pkg:      cm.Package,
				receiver: cm.Receiver,
				terminal: cm.Terminal,
			})
		}
	}
	return m
}

func (m *Matcher) IsQueryFunc(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	pkgPath := ""
	if fn.Pkg != nil && fn.Pkg.Pkg != nil {
		pkgPath = fn.Pkg.Pkg.Path()
	}
	methodName := fn.Name()
	recvShort, recvFull := receiverNames(fn)

	if pkgPath != "" {
		if _, ok := m.customLookup[customKey(pkgPath, recvShort, methodName)]; ok {
			return true
		}
		if _, ok := m.customLookup[customKey(pkgPath, recvFull, methodName)]; ok {
			return true
		}
	}

	if m.cfg.Builtins.DatabaseSQL && isDatabaseSQL(pkgPath, methodName) {
		return true
	}
	if m.cfg.Builtins.SQLX && isSQLX(pkgPath, methodName) {
		return true
	}
	if m.cfg.Builtins.PGX && isPGX(pkgPath, methodName) {
		return true
	}
	if m.cfg.Builtins.GORM && isGORM(pkgPath, methodName) {
		return true
	}
	if m.cfg.Builtins.Bun && isBun(pkgPath, methodName) {
		return true
	}
	if m.cfg.Builtins.Ent && isEnt(pkgPath, methodName) {
		return true
	}
	return false
}

func (m *Matcher) IsLikelyQueryMethodName(method string) bool {
	if method == "" {
		return false
	}
	// Keep dynamic-dispatch heuristics strict to reduce false positives.
	// Use a narrow allowlist instead of prefixes so methods like ExecuteActivity
	// are not treated as query-like.
	switch method {
	case "Query", "QueryContext", "QueryRow", "QueryRowContext",
		"Queryx", "QueryxContext",
		"Exec", "ExecContext",
		"NamedExec", "NamedExecContext",
		"RawQuery", "SendBatch":
		return true
	default:
		return false
	}
}

// MatchDynamicCustomMethod reports whether a dynamic interface dispatch matches
// a configured custom query method. If one or more rules match and any is
// terminal=true, the call can be classified as definite.
func (m *Matcher) MatchDynamicCustomMethod(callerPkg string, method *types.Func) (matched bool, definite bool) {
	if method == nil {
		return false, false
	}

	rules := m.dynamicRules[method.Name()]
	if len(rules) == 0 {
		return false, false
	}

	methodPkg := ""
	if p := method.Pkg(); p != nil {
		methodPkg = p.Path()
	}
	recvShort, recvFull := receiverNamesFromTypesFunc(method)

	for _, rule := range rules {
		if !matchesDynamicPackage(rule.pkg, callerPkg, methodPkg) {
			continue
		}
		if rule.receiver != "" && rule.receiver != recvShort && rule.receiver != recvFull {
			continue
		}
		matched = true
		if rule.terminal {
			return true, true
		}
	}

	return matched, false
}

func receiverNames(fn *ssa.Function) (string, string) {
	sig := fn.Signature
	if sig == nil || sig.Recv() == nil {
		return "", ""
	}
	raw := sig.Recv().Type().String()
	short := raw
	if i := strings.LastIndex(short, "."); i != -1 {
		short = short[i+1:]
	}
	return short, raw
}

func receiverNamesFromTypesFunc(fn *types.Func) (string, string) {
	if fn == nil {
		return "", ""
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", ""
	}
	raw := sig.Recv().Type().String()
	short := raw
	if i := strings.LastIndex(short, "."); i != -1 {
		short = short[i+1:]
	}
	return short, raw
}

func customKey(pkg, receiver, method string) string {
	return pkg + "|" + receiver + "|" + method
}

func matchesDynamicPackage(rulePkg, callerPkg, methodPkg string) bool {
	if rulePkg == "" || rulePkg == "*" {
		return true
	}
	return rulePkg == callerPkg || rulePkg == methodPkg
}

func isDatabaseSQL(pkg, method string) bool {
	if pkg != "database/sql" {
		return false
	}
	switch method {
	case "Query", "QueryContext", "QueryRow", "QueryRowContext", "Exec", "ExecContext":
		return true
	default:
		return false
	}
}

func isSQLX(pkg, method string) bool {
	if pkg != "github.com/jmoiron/sqlx" {
		return false
	}
	switch method {
	case "Get", "Select", "Queryx", "QueryxContext", "Exec", "NamedExec", "NamedExecContext":
		return true
	default:
		return false
	}
}

func isPGX(pkg, method string) bool {
	if !strings.Contains(pkg, "github.com/jackc/pgx") {
		return false
	}
	switch method {
	case "Query", "QueryRow", "Exec", "SendBatch":
		return true
	default:
		return false
	}
}

func isGORM(pkg, method string) bool {
	if pkg != "gorm.io/gorm" {
		return false
	}
	switch method {
	case "First", "Find", "Take", "Raw", "Scan", "Create", "Save", "Delete", "Update", "Updates", "Exec":
		return true
	default:
		return false
	}
}

func isBun(pkg, method string) bool {
	if pkg != "github.com/uptrace/bun" {
		return false
	}
	switch method {
	case "Scan", "Exec":
		return true
	default:
		return false
	}
}

func isEnt(pkg, method string) bool {
	if !strings.Contains(pkg, "/ent") && !strings.Contains(pkg, "entgo.io") {
		return false
	}
	switch method {
	case "All", "First", "Only", "Count", "Exec":
		return true
	default:
		return false
	}
}
