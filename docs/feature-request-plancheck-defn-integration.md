# Feature Request: Plancheck ↔ Defn Integration

**Status: Mostly complete as of 2026-04-06.**

## What's done

Plancheck already integrates deeply with defn:

- **Callers/callees/constructors** — spike agent's impact() and constructors() tools query `graph.CallerDefs()`, `graph.CalleeDefs()`, `graph.Tests()`
- **code() tool** — definition-level navigation via defn graph + go/ast, navigates directly to functions in 6K+ line files
- **Blast radius** — `refgraph.CheckBlastRadius()` finds callers outside the plan's file set
- **Build-check** — `go build -overlay` with probed symbols for compiler-verified obligations
- **Comod analysis** — git-based co-modification in `internal/comod/`
- **Test coverage** — `graph.Tests(defID)` returns transitive test functions

## What's new in defn (not yet adopted by plancheck)

### `validate-plan` op
Defn now has a first-class `validate-plan` op that takes plan files + mutations and returns gaps. Plancheck currently reimplements this client-side via `buildMutationsFromPlan()` + `simulate.Run()` with raw Dolt queries. Should be replaced with a single defn MCP call.

### `simulate` op
Defn now has server-side mutation simulation on throwaway Dolt branches. Plancheck does this client-side in `internal/simulate/simulate.go`. Could be simplified.

## What's still missing

### Interface implementation detection
Neither tool tracks which types implement which interfaces. Would enable: "you're adding a method to interface X but didn't update implementors Y, Z."

### Fuzzy definition lookup
Diff hunk headers don't always match defn's stored receivers. Need fuzzy matching.

## Original workflow vision

The original workflow (plan → defn blast radius → verify coverage → implement → verify) is now functional via `plancheck check` + `plancheck review` + `suggest` MCP tool.
