package moduleplugin

import (
	"fmt"
	"strings"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
)

const pluginName = "goqueryguard"

func init() {
	register.Plugin(pluginName, New)
}

type settings struct {
	Config string `json:"config"`
}

type plugin struct {
	configPath string
}

// New constructs the golangci-lint module plugin instance.
//
// Supported settings:
// - config: optional path to .goqueryguard.yaml
func New(conf any) (register.LinterPlugin, error) {
	s, err := register.DecodeSettings[settings](conf)
	if err != nil {
		return nil, fmt.Errorf("decode plugin settings: %w", err)
	}

	return &plugin{
		configPath: strings.TrimSpace(s.Config),
	}, nil
}

// BuildAnalyzers returns the analyzers exposed by this plugin.
func (p *plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	a, err := NewAnalyzerFromConfigPath(p.configPath)
	if err != nil {
		return nil, err
	}
	return []*analysis.Analyzer{a}, nil
}

// GetLoadMode requests full type info, required by the SSA-based analyzer.
func (p *plugin) GetLoadMode() string {
	return register.LoadModeTypesInfo
}
