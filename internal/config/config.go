package config

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigFile = ".goqueryguard.yaml"
)

// Config is the root configuration for goqueryguard.
type Config struct {
	Version      int               `yaml:"version"`
	Rules        RulesConfig       `yaml:"rules"`
	QueryMatch   QueryMatchConfig  `yaml:"query_match"`
	Analysis     AnalysisConfig    `yaml:"analysis"`
	Scope        ScopeConfig       `yaml:"scope"`
	Suppressions SuppressionConfig `yaml:"suppressions"`
	Baseline     BaselineConfig    `yaml:"baseline"`
	Output       OutputConfig      `yaml:"output"`
}

type RulesConfig struct {
	QueryInLoop RuleConfig `yaml:"query-in-loop"`
}

type RuleConfig struct {
	Enabled bool     `yaml:"enabled"`
	FailOn  []string `yaml:"fail_on"`
	Report  []string `yaml:"report"`
}

type QueryMatchConfig struct {
	Builtins      BuiltinConfig  `yaml:"builtins"`
	CustomMethods []CustomMethod `yaml:"custom_methods"`
}

type BuiltinConfig struct {
	DatabaseSQL bool `yaml:"database_sql"`
	GORM        bool `yaml:"gorm"`
	SQLX        bool `yaml:"sqlx"`
	PGX         bool `yaml:"pgx"`
	Bun         bool `yaml:"bun"`
	Ent         bool `yaml:"ent"`
}

type CustomMethod struct {
	Package  string   `yaml:"package"`
	Receiver string   `yaml:"receiver"`
	Methods  []string `yaml:"methods"`
	Terminal bool     `yaml:"terminal"`
}

type AnalysisConfig struct {
	LoopKinds     []string `yaml:"loop_kinds"`
	MaxChainDepth int      `yaml:"max_chain_depth"`
	CallgraphMode string   `yaml:"callgraph_mode"`
	IncludeTests  bool     `yaml:"include_tests"`
}

type ScopeConfig struct {
	ExcludePaths []string `yaml:"exclude_paths"`
}

type SuppressionConfig struct {
	Directive     string `yaml:"directive"`
	RequireReason bool   `yaml:"require_reason"`
	ReportUnused  bool   `yaml:"report_unused"`
}

type BaselineConfig struct {
	Mode string `yaml:"mode"`
	File string `yaml:"file"`
}

type OutputConfig struct {
	Format         string `yaml:"format"`
	ShowSuppressed bool   `yaml:"show_suppressed"`
}

func Default() *Config {
	return &Config{
		Version: 1,
		Rules: RulesConfig{
			QueryInLoop: RuleConfig{
				Enabled: true,
				FailOn:  []string{"definite"},
				Report:  []string{"definite", "possible"},
			},
		},
		QueryMatch: QueryMatchConfig{
			Builtins: BuiltinConfig{
				DatabaseSQL: true,
				GORM:        true,
				SQLX:        true,
				PGX:         true,
				Bun:         true,
				Ent:         true,
			},
		},
		Analysis: AnalysisConfig{
			LoopKinds:     []string{"for", "range", "goroutine_in_loop"},
			MaxChainDepth: 0,
			CallgraphMode: "cha_plus_vta",
			IncludeTests:  true,
		},
		Suppressions: SuppressionConfig{
			Directive:     "goqueryguard:ignore",
			RequireReason: true,
			ReportUnused:  true,
		},
		Baseline: BaselineConfig{
			Mode: "off",
			File: ".goqueryguard-baseline.json",
		},
		Output: OutputConfig{
			Format: "text",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()

	if path == "" {
		if _, err := os.Stat(DefaultConfigFile); err == nil {
			path = DefaultConfigFile
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat config %q: %w", DefaultConfigFile, err)
		}
	}
	if path == "" {
		return cfg, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Version <= 0 {
		return errors.New("version must be > 0")
	}
	validConfidence := []string{"definite", "possible"}
	for _, v := range c.Rules.QueryInLoop.FailOn {
		if !slices.Contains(validConfidence, v) {
			return fmt.Errorf("rules.query-in-loop.fail_on contains invalid value %q", v)
		}
	}
	for _, v := range c.Rules.QueryInLoop.Report {
		if !slices.Contains(validConfidence, v) {
			return fmt.Errorf("rules.query-in-loop.report contains invalid value %q", v)
		}
	}
	validBaseline := []string{"off", "new_only", "enforce_all"}
	if !slices.Contains(validBaseline, c.Baseline.Mode) {
		return fmt.Errorf("baseline.mode contains invalid value %q", c.Baseline.Mode)
	}
	if c.Suppressions.Directive == "" {
		return errors.New("suppressions.directive cannot be empty")
	}
	return nil
}

func (c *Config) ReportsConfidence(level string) bool {
	return slices.Contains(c.Rules.QueryInLoop.Report, level)
}

func (c *Config) FailsOnConfidence(level string) bool {
	if c.Baseline.Mode == "new_only" {
		return slices.Contains(c.Rules.QueryInLoop.FailOn, level)
	}
	if c.Baseline.Mode == "off" || c.Baseline.Mode == "enforce_all" {
		return slices.Contains(c.Rules.QueryInLoop.FailOn, level)
	}
	return false
}
