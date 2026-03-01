package moduleplugin

import (
	"fmt"

	"github.com/mario-pinderi/goqueryguard/internal/analyzer/loopquery"
	"github.com/mario-pinderi/goqueryguard/internal/config"
	"golang.org/x/tools/go/analysis"
)

// NewAnalyzer creates a go/analysis analyzer suitable for golangci-lint
// module plugin integration.
func NewAnalyzer(cfg *config.Config) *analysis.Analyzer {
	return loopquery.New(cfg)
}

// NewAnalyzerFromConfigPath loads a config file and returns a configured analyzer.
func NewAnalyzerFromConfigPath(path string) (*analysis.Analyzer, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return loopquery.New(cfg), nil
}
