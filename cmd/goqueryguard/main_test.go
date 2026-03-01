package main

import (
	"strings"
	"testing"

	"github.com/mario-pinderi/goqueryguard/internal/baseline"
)

func TestStripExplainFlag(t *testing.T) {
	t.Setenv(envExplain, "")
	tests := []struct {
		name    string
		args    []string
		want    []string
		explain bool
		wantErr bool
	}{
		{name: "no explain", args: []string{"./..."}, want: []string{"./..."}, explain: false},
		{name: "explain bool", args: []string{"--explain", "./..."}, want: []string{"./..."}, explain: true},
		{name: "explain equals true", args: []string{"--explain=true", "./..."}, want: []string{"./..."}, explain: true},
		{name: "explain equals false", args: []string{"--explain=false", "./..."}, want: []string{"./..."}, explain: false},
		{name: "invalid explain", args: []string{"--explain=wat"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotExplain, err := stripExplainFlag(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotExplain != tt.explain {
				t.Fatalf("explain=%v, want %v", gotExplain, tt.explain)
			}
			if len(gotArgs) != len(tt.want) {
				t.Fatalf("args len=%d, want %d", len(gotArgs), len(tt.want))
			}
			for i := range gotArgs {
				if gotArgs[i] != tt.want[i] {
					t.Fatalf("args[%d]=%q, want %q", i, gotArgs[i], tt.want[i])
				}
			}
		})
	}
}

func TestFormatBaselineFinding(t *testing.T) {
	e := baseline.Entry{
		Rule:       "query-in-loop",
		Confidence: "possible",
		Package:    "example/internal/store",
		File:       "store.go",
		Line:       12,
		Function:   "example/internal/store.(*Store).Save",
		Terminal:   "(*database/sql.DB).ExecContext",
		Chain: []string{
			"example/internal/store.(*Store).Save",
			"(*database/sql.DB).ExecContext",
		},
	}

	got := formatBaselineFinding(e, false)
	want := "store.go:12 query-in-loop [possible] example/internal/store.(*Store).Save -> (*database/sql.DB).ExecContext"
	if got != want {
		t.Fatalf("formatBaselineFinding(no explain)=%q, want %q", got, want)
	}

	gotExplain := formatBaselineFinding(e, true)
	if gotExplain == want {
		t.Fatalf("formatBaselineFinding(explain)=same as concise output")
	}
	if !containsAll(gotExplain,
		"Explain:",
		"package=example/internal/store",
		"chain_depth=1",
		"chain=example/internal/store.(*Store).Save -> (*database/sql.DB).ExecContext",
	) {
		t.Fatalf("formatBaselineFinding(explain) missing expected fields: %q", gotExplain)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
