package moduleplugin

import (
	publicanalyzer "github.com/mario-pinderi/goqueryguard/golangci/analyzer"
	"golang.org/x/tools/go/analysis"
)

// NewAnalyzerFromConfigPath is a compatibility wrapper for direct analyzer
// construction. Prefer importing github.com/mario-pinderi/goqueryguard/golangci/analyzer.
func NewAnalyzerFromConfigPath(path string) (*analysis.Analyzer, error) {
	return publicanalyzer.NewAnalyzerFromConfigPath(path)
}
