# goqueryguard

`goqueryguard` is a Go static-analysis tool that reports database queries executed inside loops.

It detects:
1. Direct query calls inside `for`/`range` loops.
2. Indirect query execution through call chains originating in loop bodies.
3. Possible query execution in dynamic/uncertain dispatch paths.
4. Interface-dispatched calls by resolving concrete targets from code (CHA+VTA), without requiring custom method config.

## Install

```bash
go install github.com/mario-pinderi/goqueryguard/cmd/goqueryguard@latest
```

## Quick Start

Run against packages:

```bash
goqueryguard ./...
```

Add full reasoning per finding:

```bash
goqueryguard --explain ./...
```

Normal runs also print summary stats at the end:
1. total findings
2. definite vs possible counts
3. package count
4. average/max call-chain depth
5. execution time
6. per-rule counts

To suppress stats output:

```bash
GOQUERYGUARD_DISABLE_STATS=1 goqueryguard ./...
```

Run with an explicit config file:

```bash
GOQUERYGUARD_CONFIG=.goqueryguard.yaml goqueryguard ./...
```

Exit codes:
1. `0`: no blocking findings
2. `1`: blocking findings found
3. `2`: config/runtime error

## Baseline Workflow

Create a baseline from current findings:

```bash
goqueryguard baseline write ./...
```

Check only for new findings compared to baseline:

```bash
goqueryguard baseline check ./...
```

Print full baseline finding context:

```bash
goqueryguard --explain baseline check ./...
```

Both `baseline write` and `baseline check` print summary stats:
1. total findings
2. definite vs possible counts
3. package count
4. average/max call-chain depth
5. execution time
6. per-rule counts

Override baseline path:

```bash
goqueryguard baseline write -file .goqueryguard-baseline.json ./...
goqueryguard baseline check -file .goqueryguard-baseline.json ./...
```

## Suppressions

Suppress the next loop or call with a required reason:

```go
//goqueryguard:ignore query-in-loop -- legacy path pending refactor
for _, id := range ids {
    _ = repo.Load(id)
}
```

If reason enforcement is enabled and the reason is missing, `goqueryguard` reports an error.

## Configuration

Create `.goqueryguard.yaml`:

```yaml
version: 1

rules:
  query-in-loop:
    enabled: true
    fail_on: ["definite"]
    report: ["definite", "possible"]

query_match:
  builtins:
    database_sql: true
    gorm: true
    sqlx: true
    pgx: true
    bun: true
    ent: true
  custom_methods:
    - package: "myapp/internal/store"
      receiver: "*Repo"
      methods: ["LoadUser", "SaveUser"]
      terminal: true
    # Interface-dispatched methods can omit receiver and match by package+method.
    # Use terminal=true when calling this method implies a real DB query.
    - package: "myapp/internal/service"
      methods: ["SaveSale", "UpsertBeneficiaries"]
      terminal: true

analysis:
  loop_kinds: ["for", "range", "goroutine_in_loop"]
  max_chain_depth: 0
  callgraph_mode: "cha_plus_vta"
  include_tests: true

scope:
  exclude_paths:
    - "**/vendor/**"
    - "**/*_generated.go"

suppressions:
  directive: "goqueryguard:ignore"
  require_reason: true
  report_unused: true

baseline:
  mode: "new_only"
  file: ".goqueryguard-baseline.json"

output:
  format: "text"
  show_suppressed: false
```

## Development

Run tests:

```bash
go test ./...
```

## Direct go/analysis API

For non-plugin integrations, import:
`github.com/mario-pinderi/goqueryguard/golangci/analyzer`

Use `NewAnalyzerFromConfigPath` to construct a configured analyzer directly:

```go
package main

import (
	"log"

	goqueryanalyzer "github.com/mario-pinderi/goqueryguard/golangci/analyzer"
)

func main() {
	a, err := goqueryanalyzer.NewAnalyzerFromConfigPath(".goqueryguard.yaml")
	if err != nil {
		log.Fatal(err)
	}
	_ = a
}
```

## golangci-lint Module Plugin

This repository now exposes a public module-plugin package at:
`github.com/mario-pinderi/goqueryguard/golangci/moduleplugin`

The module plugin uses the direct constructor above under the hood.

Registered linter name: `goqueryguard`

Create `.custom-gcl.yml`:

```yaml
version: v2.8.0
plugins:
  - module: 'github.com/mario-pinderi/goqueryguard'
    import: 'github.com/mario-pinderi/goqueryguard/golangci/moduleplugin'
    version: 'v0.0.0'
```

Create/update `.golangci.yml`:

```yaml
linters:
  settings:
    custom:
      goqueryguard:
        type: module
        description: Detects DB queries executed in loops
        settings:
          # Optional path to goqueryguard config.
          # If omitted, default config discovery is used.
          config: .goqueryguard.yaml
  enable:
    - goqueryguard
```

Build and run custom golangci-lint binary:

```bash
golangci-lint custom
./custom-gcl run ./...
```
