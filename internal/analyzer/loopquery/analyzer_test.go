package loopquery

import (
	"testing"

	"github.com/mario-pinderi/goqueryguard/internal/config"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	cfg := config.Default()
	analyzer := NewWithRuntime(cfg, RuntimeOptions{DisableFactExport: true})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p")
}

func TestAnalyzerExplain(t *testing.T) {
	cfg := config.Default()
	analyzer := NewWithRuntime(cfg, RuntimeOptions{
		DisableFactExport: true,
		Explain:           true,
	})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p_explain")
}

func TestAnalyzerDynamicCustomInterfaceMethod(t *testing.T) {
	cfg := config.Default()
	cfg.QueryMatch.CustomMethods = []config.CustomMethod{
		{
			Package:  "p_dynamic_custom",
			Methods:  []string{"SaveSale"},
			Terminal: true,
		},
	}
	analyzer := NewWithRuntime(cfg, RuntimeOptions{DisableFactExport: true})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p_dynamic_custom")
}

func TestAnalyzerDynamicCustomInterfaceMethodPossible(t *testing.T) {
	cfg := config.Default()
	cfg.QueryMatch.CustomMethods = []config.CustomMethod{
		{
			Package:  "p_dynamic_custom_possible",
			Methods:  []string{"UpdateThing"},
			Terminal: false,
		},
	}
	analyzer := NewWithRuntime(cfg, RuntimeOptions{DisableFactExport: true})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p_dynamic_custom_possible")
}

func TestAnalyzerDynamicInterfaceAuto(t *testing.T) {
	cfg := config.Default()
	analyzer := NewWithRuntime(cfg, RuntimeOptions{DisableFactExport: true})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p_dynamic_auto")
}

func TestAnalyzerRowsIterationNotQuery(t *testing.T) {
	cfg := config.Default()
	analyzer := NewWithRuntime(cfg, RuntimeOptions{DisableFactExport: true})
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, analyzer, "p_rows")
}
