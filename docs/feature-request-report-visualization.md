# Feature Request: Richer Report Data for gemotvis

## Context

gemotvis has a report mode (`?view=report`) that renders deliberation analysis as a static document. It currently consumes data from the standard `gemot/analyze action:get_result` A2A response (`AnalysisResult` struct).

The t3c-import pipeline's `report.go` generates richer reports with sections that depend on data not currently available through the standard API: null control comparisons, replication stability, stance verification (1-5 confidence scoring), coverage gap analysis, resolution proposals, and multi-round evolution tracking.

This doc describes what gemot could expose to let gemotvis (and any other consumer) render these richer reports without reimplementing pipeline-specific logic.

## Already Available But Not Consumed

### 1. `discarded_cruxes` field

**Status**: Already on `AnalysisResult` in gemot (`DiscardedCruxes []Crux`), already serialized as `discarded_cruxes` in JSON. gemotvis just needs to add the field to its mirrored type and consume it.

**gemotvis action**: Add `discarded_cruxes` to `internal/gemot/types.go` and `frontend/src/types.ts`. Replace the current hack of parsing `DEGENERATE:` integrity warnings.

### 2. HALLUCINATION warnings in `integrity_warnings`

**Status**: Already emitted by the analysis engine. gemotvis could parse `HALLUCINATION:` prefixed warnings to show a hallucination corrections row in the reliability table (tiered severity: none/minor 1-3/moderate 4-9/high 10+).

**gemotvis action**: Frontend-only change, no API work needed.

### 3. ANALYSIS_REFUSED warnings

**Status**: Already emitted. gemotvis should suppress the compromise proposal when this warning is present (report.go already does this).

**gemotvis action**: Frontend-only change.

### 4. `topic_id` on TopicSummary

**Status**: Already on `TopicSummary` in gemot (`TopicID string`). Assigned as T1-T6 in Go after taxonomy extraction, persists across rounds via `ContextKeyPriorTopicIDs`. gemotvis can use this for cross-round topic tracking.

**gemotvis action**: Add `topic_id` to the mirrored type. Use for round selector and evolution display.

## Proposed New API Fields

### 5. Multi-round analysis access

**Problem**: `get_result` returns only the latest round's analysis. report.go compares R1 vs R2 vs R3 to show evolution (new cruxes, topic taxonomy changes, position revisions).

**Proposal**: Add `round` parameter to `get_result`:

```
gemot/analyze action:get_result deliberation_id:X           → latest round (current behavior)
gemot/analyze action:get_result deliberation_id:X round:1   → specific round
gemot/analyze action:get_result deliberation_id:X round:all → all rounds as array
```

**Impact**: Enables evolution tracking, cross-round crux comparison, position revision display.

### 6. Validation results as structured data

The t3c-import pipeline produces five validation types. These are currently pipeline-internal structs (`verifyResult`, `nullControlResult`, `replicationResult`, `spotCheckResult`, `coverageResult` in `scripts/t3c-import/`). Making them available through the API enables any consumer to render the full reliability table.

**Proposal**: Add optional fields to `AnalysisResult`:

```go
type AnalysisResult struct {
    // ... existing fields ...

    // Validation results (optional, populated by pipelines that run these checks)
    NullControl   *NullControlResult   `json:"null_control,omitempty"`
    Replication   *ReplicationResult   `json:"replication,omitempty"`
    Verification  *VerificationResult  `json:"verification,omitempty"`
    CoverageGaps  []CoverageGap        `json:"coverage_gaps,omitempty"`
}

type NullControlResult struct {
    Pass          bool              `json:"pass"`
    RealMetrics   ValidationMetrics `json:"real_metrics"`
    NullMetrics   ValidationMetrics `json:"null_metrics"`
    FailedMetrics []string          `json:"failed_metrics,omitempty"`
    NullDelibID   string            `json:"null_delib_id"`
}

type ValidationMetrics struct {
    CruxCount      int     `json:"crux_count"`
    AvgControversy float64 `json:"avg_controversy"`
    ConsensusCount int     `json:"consensus_count"`
    BridgingCount  int     `json:"bridging_count"`
    ClusterCount   int     `json:"cluster_count"`
    Confidence     string  `json:"confidence"`
}

type ReplicationResult struct {
    Runs      []ValidationMetrics `json:"runs"`
    Stability struct {
        CruxCV      float64 `json:"crux_cv"`
        ControvCV   float64 `json:"controv_cv"`
        ConsensusCV float64 `json:"consensus_cv"`
        AllStable   bool    `json:"all_stable"`
        Tier        int     `json:"tier"` // 0=unreplicated, 1=replicated, 2=stable
    } `json:"stability"`
}

type VerificationResult struct {
    Checked    int            `json:"checked"`
    Downgraded int            `json:"downgraded"`
    Threshold  int            `json:"threshold"` // stances at or below this score downgraded
    ScoreDist  map[int]int    `json:"score_dist"` // score (1-5) → count
    Details    []VerifyDetail `json:"details,omitempty"`
}

type VerifyDetail struct {
    Speaker    string `json:"speaker"`
    Crux       string `json:"crux"`
    OrigStance string `json:"orig_stance"`
    Score      int    `json:"score"` // 1-5 grounding confidence
    Reason     string `json:"reason"`
}

type CoverageGap struct {
    Position           string `json:"position"`
    MissingPerspective string `json:"missing_perspective"`
    SuggestedSource    string `json:"suggested_source"`
}
```

These fields are `omitempty` so they add zero overhead for deliberations that haven't run validation. The t3c-import pipeline would store them alongside the analysis result after each validation pass.

### 7. Agent metadata: type classification

**Problem**: report.go splits crux agree/disagree lists by agent type (speaker-derived vs structural). gemotvis can't do this because `AgentInfo` only has `id`, `model_family`, `conviction`, and optional coordinates.

**Proposal**: Add `kind` field to agent info:

```go
type AgentInfo struct {
    ID          string  `json:"id"`
    ModelFamily string  `json:"model_family"`
    Conviction  float64 `json:"conviction"`
    Kind        string  `json:"kind,omitempty"` // "speaker", "steelman", "probe", "bridge", "dissent", "empty-chair", "resolution"
    // ... existing optional fields ...
}
```

This is already tracked internally (t3c-import `agentPlan.Kind`). Just needs to be stored on the agent record and returned via `get_positions` or agent listing.

**Impact**: Enables speaker/structural split in crux display, participant categorization in report, resolution proposal highlighting, and visual differentiation in graph view.

## What gemotvis Would Do With This Data

| Data | Report section | Graph view enhancement |
|---|---|---|
| `discarded_cruxes` | Dedicated section (currently hacked via warning parsing) | — |
| `topic_id` | Cross-round topic tracking in evolution section | — |
| Multi-round analysis | Evolution section (new cruxes, topic changes, position revision) | Round selector in scrubber |
| Null control | Comparison table + reliability row | Confidence badge color |
| Replication | Stability table + reliability row | — |
| Verification | Score distribution (1-5) + reliability row | — |
| Coverage gaps | "Missing Perspectives" section | — |
| Agent kind | Speaker/structural split in cruxes, resolution highlighting | Node shape/color by type |

## Priority

1. **Consume `discarded_cruxes` + `topic_id`** — zero API work, just gemotvis types (do now)
2. **Parse ANALYSIS_REFUSED / HALLUCINATION from integrity warnings** — frontend-only (do now)
3. **Agent `kind` field** — small API change, high impact on both report and graph
4. **Multi-round access** — enables the most valuable report sections (evolution)
5. **Validation results on AnalysisResult** — enables full reliability table parity with report.go
6. **Coverage gaps** — nice-to-have, lower priority

## Alternatives Considered

- **gemotvis calls t3c-import directly**: Wrong — t3c-import is a batch pipeline, not a service. gemotvis should only talk to gemot's A2A API.
- **Embed report.go output as markdown**: Could render pre-generated markdown, but loses interactivity (collapsible sections, theme support, graph integration).
- **Parse all data from integrity_warnings**: Current approach for discarded cruxes. Works but fragile — coupled to warning string format, loses structured data.
