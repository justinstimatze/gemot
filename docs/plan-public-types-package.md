# Public Types Package — SHIPPED

## What

All pure data types from `internal/deliberation/` extracted to `github.com/justinstimatze/gemot/types` — a public package that external consumers can import directly.

```
types/
  types.go        # Deliberation, Position, Vote, Criterion, JoinCode, etc.
  analysis.go     # AnalysisResult, Crux, ConsensusStatement, BridgingStatement,
                  # TopicSummary, Coalition, AuditEntry, OpinionCluster,
                  # NullControlResult, VerificationResult, ReplicationResult,
                  # CoverageGap, PipelineMetrics, StabilityReport, etc.
```

## How it works

`internal/deliberation/models.go` and `analysis.go` now contain only type aliases:

```go
package deliberation

import "github.com/justinstimatze/gemot/types"

type Deliberation = types.Deliberation
type Position = types.Position
type AnalysisResult = types.AnalysisResult
// ... ~30 types total
```

Go type aliases (`=`) are fully transparent — all existing internal code works with zero changes. Field access, struct literals, JSON marshaling, interface satisfaction — all identical.

## Consumer usage

```go
import "github.com/justinstimatze/gemot/types"

var result types.AnalysisResult
var crux types.Crux
var pos types.Position
```

The package imports only `"time"`. Zero transitive dependencies.

## What stayed in `internal/deliberation`

- `Service` and all methods (business logic)
- `EventBus`, `Event` (internal pub/sub)
- `Templates` (deliberation configuration)
- Store interfaces (`AnalysisStore`, `AccessStore`, etc.)
- Context keys (`ContextKeyPriorTaxonomy`, etc.)

## Why `types/` not `api/`

- `api/` stays free for a future REST API or client package
- `types` reads naturally: `types.Deliberation`, `types.AnalysisResult`
- Pure data types are a separate concern from API handlers/clients
