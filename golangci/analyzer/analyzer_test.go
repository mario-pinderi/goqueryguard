package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAnalyzerFromConfigPath(t *testing.T) {
	t.Run("loads explicit config", func(t *testing.T) {
		tempDir := t.TempDir()
		cfgPath := filepath.Join(tempDir, "goqueryguard.yaml")
		if err := os.WriteFile(cfgPath, []byte("version: 1\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}

		a, err := NewAnalyzerFromConfigPath(cfgPath)
		if err != nil {
			t.Fatalf("NewAnalyzerFromConfigPath() error = %v", err)
		}
		if a == nil {
			t.Fatal("NewAnalyzerFromConfigPath() returned nil analyzer")
		}
	})

	t.Run("returns wrapped load error", func(t *testing.T) {
		_, err := NewAnalyzerFromConfigPath(filepath.Join(t.TempDir(), "missing.yaml"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "load config") {
			t.Fatalf("expected wrapped load config error, got %q", err.Error())
		}
	})
}
