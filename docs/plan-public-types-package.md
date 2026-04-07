# Plan: Extract Public Types Package

## Problem

gemotvis mirrors ~30 type definitions from `internal/deliberation` in its own `internal/gemot/types.go`. Every time a field is added to `AnalysisResult` or a new validation type ships, both repos need manual updates. The types already drifted this week — gemotvis was missing `DiscardedCruxes`, `TopicID`, and all validation result types until a manual sync.

Other consumers (plancheck, future CLI tools, third-party integrations) would hit the same problem. The types are in `internal/`, so Go's access rules prevent any external module from importing them.

## Proposal

Move the pure data types to a new public package: `github.com/justinstimatze/gemot/types`.

```
types/
  types.go        # Deliberation, Position, Vote, Criterion, JoinCode, etc.
  analysis.go          # AnalysisResult, Crux, ConsensusStatement, BridgingStatement,
                  # TopicSummary, Coalition, AuditEntry, OpinionCluster,
                  # NullControlResult, VerificationResult, ReplicationResult,
                  # CoverageGap, PipelineMetrics, StabilityReport, etc.
```

### What moves

All exported struct types from `models.go` and `analysis.go` that are pure data (JSON-serializable, no methods with business logic, no store/DB dependencies). Currently ~30 types, all importing only `"time"`.

### What stays in `internal/deliberation`

- `Service` and all methods (business logic)
- `EventBus`, `Event` (internal pub/sub)
- `Templates` (deliberation configuration)
- Any type that has methods beyond simple getters

### Migration steps

1. Create `api/types.go` and `api/analysis.go` — copy the struct definitions
2. In `internal/deliberation/models.go` and `analysis.go`, replace struct definitions with type aliases:
   ```go
   package deliberation
   
   import "github.com/justinstimatze/gemot/types"
   
   type Deliberation = types.Deliberation
   type Position = types.Position
   type AnalysisResult = types.AnalysisResult
   // ... etc
   ```
   Go type aliases (`=`) are fully transparent — all existing internal code continues to work with zero changes. Field access, struct literals, JSON marshaling, interface satisfaction — all identical.
3. Run tests — everything should pass with no other changes
4. Tag a release so consumers can pin a version

### Consumer side (gemotvis)

After the extraction, gemotvis replaces its mirrored types:

```go
// Before (gemotvis/internal/gemot/types.go — 180 lines of mirrored structs)
type AnalysisResult struct { ... }

// After
import "github.com/justinstimatze/gemot/types"
// Use types.AnalysisResult, api.Crux, etc. directly
```

Delete `internal/gemot/types.go` entirely. The poller and SSE types reference `types.*` instead.

The TypeScript types still need to be maintained separately (different language), but having a single Go source of truth means the TS types only need to track one canonical definition, and we can potentially auto-generate them.

### Dependency cost

The `api` package imports only `"time"`. Adding `gemot` as a dependency to gemotvis pulls in gemot's `go.sum` entries, but since `api/` has no transitive dependencies beyond stdlib, the actual binary size impact is zero. `go mod tidy` will only fetch what's actually imported.

### Why `types/` not `api/`

- `api/` should stay free for a future REST API or client package
- `types` reads naturally: `types.Deliberation`, `types.AnalysisResult`
- Pure data types are a separate concern from API handlers/clients
- Follows Docker's pattern (`api/types/`) but at the top level since we don't need the nesting yet

### Risk

Low. Type aliases are invisible to callers. The only risk is if a type in `internal/deliberation` has methods that reference other internal types — those methods can't move to `api/`. Quick grep shows the data types have no methods, only the `Service` does. Clean separation.

## Not in scope

- Moving the A2A client (`internal/gemot/client.go` in gemotvis) — that stays in gemotvis since it's specific to gemotvis's polling architecture
- Auto-generating TypeScript types from Go — worth exploring later but not a prerequisite
- Moving event types — `Event` and `EventBus` are internal pub/sub machinery, not API types
