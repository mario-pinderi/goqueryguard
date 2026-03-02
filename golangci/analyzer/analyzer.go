package analyzer

import (
	"fmt"

	"github.com/mario-pinderi/goqueryguard/internal/analyzer/loopquery"
	"github.com/mario-pinderi/goqueryguard/internal/config"
	"golang.org/x/tools/go/analysis"
)

// NewAnalyzerFromConfigPath loads a goqueryguard config file and returns a
// configured go/analysis analyzer. Empty path enables default config discovery.
func NewAnalyzerFromConfigPath(path string) (*analysis.Analyzer, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return loopquery.New(cfg), nil
}
