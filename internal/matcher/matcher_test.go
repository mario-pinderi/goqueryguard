package matcher

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/mario-pinderi/goqueryguard/internal/config"
)

func TestIsLikelyQueryMethodName(t *testing.T) {
	m := New(config.Default().QueryMatch)

	tests := []struct {
		name   string
		method string
		want   bool
	}{
		{name: "query", method: "Query", want: true},
		{name: "query context", method: "QueryContext", want: true},
		{name: "exec", method: "Exec", want: true},
		{name: "named exec context", method: "NamedExecContext", want: true},
		{name: "scan should not match", method: "Scan", want: false},
		{name: "get should not match", method: "Get", want: false},
		{name: "find should not match", method: "Find", want: false},
		{name: "execute activity should not match", method: "ExecuteActivity", want: false},
		{name: "execute should not match", method: "Execute", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.IsLikelyQueryMethodName(tt.method)
			if got != tt.want {
				t.Fatalf("IsLikelyQueryMethodName(%q)=%v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestMatchDynamicCustomMethod(t *testing.T) {
	cfg := config.Default().QueryMatch
	cfg.CustomMethods = []config.CustomMethod{
		{Package: "svc", Methods: []string{"SaveSale"}, Terminal: true},
		{Package: "svc", Methods: []string{"UpdateThing"}, Terminal: false},
	}
	m := New(cfg)

	tests := []struct {
		name      string
		callerPkg string
		methodPkg string
		method    string
		wantMatch bool
		wantDef   bool
	}{
		{
			name:      "terminal rule",
			callerPkg: "svc",
			methodPkg: "svc",
			method:    "SaveSale",
			wantMatch: true,
			wantDef:   true,
		},
		{
			name:      "non terminal rule",
			callerPkg: "svc",
			methodPkg: "svc",
			method:    "UpdateThing",
			wantMatch: true,
			wantDef:   false,
		},
		{
			name:      "method not configured",
			callerPkg: "svc",
			methodPkg: "svc",
			method:    "Other",
			wantMatch: false,
			wantDef:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newMethod(tt.methodPkg, tt.method)
			gotMatch, gotDef := m.MatchDynamicCustomMethod(tt.callerPkg, f)
			if gotMatch != tt.wantMatch || gotDef != tt.wantDef {
				t.Fatalf(
					"MatchDynamicCustomMethod(%q, %q) = (%v, %v), want (%v, %v)",
					tt.callerPkg, tt.method,
					gotMatch, gotDef,
					tt.wantMatch, tt.wantDef,
				)
			}
		})
	}
}

func newMethod(pkgPath, name string) *types.Func {
	pkg := types.NewPackage(pkgPath, "p")
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false)
	return types.NewFunc(token.NoPos, pkg, name, sig)
}
