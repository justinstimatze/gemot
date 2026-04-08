# Report Visualization: gemotvis Integration Guide

All API features are shipped and deployed. A sample deliberation with validation data is live on prod.

## CRITICAL: Speaker Identity Anonymization

**Legal requirement**: Reports MUST NOT attribute AI-synthesized stances to named real individuals without consent. See *Angwin v. Superhuman Platform* (S.D.N.Y. 2026) — right of publicity under CA Civil Code §3344, NY Civil Rights Law §§50-51.

**What this means for gemotvis**:
1. The gemot pipeline now anonymizes speaker names by default (Speaker A, Speaker B, etc.)
2. gemotvis MUST NOT reverse-map pseudonyms to real names in its display
3. If gemotvis stores or caches reports with real names from before this change, **scrub that data and its history**
4. The `--named` flag exists for internal/authorized use only — gemotvis should never request or display named reports to end users without explicit consent from named individuals
5. Agent IDs are now anonymous (e.g., `t3c-speaker-a`, `t3c-steelman-b`). No anonymization layer needed in gemotvis — the API data is safe to display directly.

**Git history**: The `integrations/t3c/ai-manifestos-report.md` file history has been scrubbed. If gemotvis imported or cached any previous version, delete those cached copies.

## Live Test Data

**Deliberation**: `3b820d7b-a70d-4e4a-a48d-c259c6bd28d6`
- 3 rounds (R1: initial analysis, R2: bridge + empty chair, R3: revised positions + 4 resolution proposals)
- 7 R1 agents with `metadata.kind` on all positions
- Verification data stored on R1 analysis result (44 stances checked, 20 downgraded, threshold ≤2)
- Agent kinds: steelman, speaker, probe, bridge, empty-chair, resolution

**Reference report**: `integrations/t3c/ai-manifestos-report.md` (anonymized markdown)
**Reference export**: `integrations/t3c/ai-manifestos-report.json` (anonymized structured JSON — all 3 rounds)

**Data is fully anonymized in the database.** Agent IDs, position content, and analysis results all use pseudonyms. gemotvis can export directly from the API — no client-side anonymization needed. A static JSON export is also available at `integrations/t3c/ai-manifestos-report.json`.

## Shared Types Package

Replace gemotvis's mirrored `internal/gemot/types.go`:

```go
import "github.com/justinstimatze/gemot/types"
// types.AnalysisResult, types.Crux, types.Position, etc.
```

Delete `internal/gemot/types.go`. The `types/` package (`types/types.go` + `types/analysis.go`) is the single source of truth. Imports only `"time"`.

## API Endpoints

### Get all rounds
```
gemot/analyze action:get_result deliberation_id:5e0c53f0-... round:-1
→ [AnalysisResult, AnalysisResult, AnalysisResult]  (R1, R2, R3)
```

### Get specific round
```
gemot/analyze action:get_result deliberation_id:5e0c53f0-... round:1
→ AnalysisResult  (with verification field populated)
```

### Get positions with kind metadata
```
gemot/participate action:get_positions deliberation_id:5e0c53f0-...
→ [{position_id, agent_id, content, metadata: {kind: "speaker"}, ...}, ...]
```

## Data Available on AnalysisResult

### Always present
- `cruxes` — with `agree_agents`, `disagree_agents`, `controversy_score`, `crux_type`, `degenerate`
- `discarded_cruxes` — degenerate cruxes with empty sides (use instead of parsing DEGENERATE warnings)
- `consensus_statements`, `bridging_statements`
- `topic_summaries` — each has `topic_id` (T1-T6) for cross-round tracking
- `integrity_warnings` — parse for `ANALYSIS_REFUSED:`, `HALLUCINATION:`, `SYBIL_SIGNAL:`
- `compromise_proposal` — suppress when `ANALYSIS_REFUSED` is present
- `audit_log` — pipeline stage details

### Present when validation was run (check for nil/missing)
```json
{
  "verification": {
    "total": 44,
    "checked": 44,
    "downgraded": 20,
    "threshold": 2,
    "score_dist": [0, 4, 14, 8, 14, 4],
    "details": [
      {
        "speaker": "speaker-d",
        "crux": "The design of AGI systems inherently encourages power-seeking...",
        "orig_stance": "disagree",
        "score": 2,
        "reason": "The quotes show this speaker acknowledges both AI risks and benefits..."
      }
    ]
  },
  "null_control": {
    "null_delib_id": "044de1c7-...",
    "real_metrics": {"crux_count": 13, "avg_controversy": 0.72, "consensus_count": 1, "bridging_count": 0, "cluster_count": 8, "confidence": "high"},
    "null_metrics": {"crux_count": 10, "avg_controversy": 0.51, "consensus_count": 0, "bridging_count": 0, "cluster_count": 8, "confidence": "high"},
    "failed_metrics": [],
    "pass": true
  },
  "replication": {
    "num_runs": 3,
    "delib_ids": ["...", "...", "..."],
    "runs": [{"crux_count": 12, "avg_controversy": 0.68, ...}, ...],
    "stability": {"tier": 2, "crux_cv": 0.08, "controv_cv": 0.12, "consensus_cv": 0.15, "all_stable": true}
  },
  "coverage_gaps": [
    {"position": "Current AI alignment techniques are insufficient...", "missing_perspective": "AI safety researchers who believe current techniques are adequate...", "suggested_source": "Center for AI Safety..."}
  ]
}
```

Note: The live test deliberation currently has `verification` on R1. `null_control`, `replication`, and `coverage_gaps` are not on this deliberation (those flags weren't used in this run to save cost). The fields will be null/absent — handle gracefully.

## Agent Kind Values

Read from `position.metadata.kind`:

| Kind | Category | Description |
|---|---|---|
| `speaker` | speaker-derived | Singleton from T3C source |
| `steelman` | speaker-derived | Multi-member cluster |
| `probe` | structural | Topic investigator (was "adversary") |
| `bridge` | structural | Cross-cluster common ground (R2) |
| `dissent` | structural | Challenges consensus (R2) |
| `empty-chair` | structural | Amplifies minority side (R2) |
| `resolution` | structural | Actionable proposal (R3) |

Use for:
- Two-track crux display (speaker-derived vs structural agree/disagree)
- Participant categorization
- Graph view node differentiation

## Rendering Guide

### Reliability Table
Check each field's presence and render conditionally:

| Dimension | How to compute status |
|---|---|
| Internal coherence | `discarded_cruxes.length / (cruxes.length + discarded_cruxes.length)` — pass if <20% |
| Agent hallucinations | Count `HALLUCINATION:` in `integrity_warnings` — none/minor(1-3)/moderate(4-9)/high(10+) |
| Stance grounding | `verification.downgraded` / `verification.checked` — show "cleaned" with counts, or "untested" if null |
| Null control | `null_control.pass` — show pass/fail with failed metrics, or "untested" if null |
| Replication | `replication.stability.all_stable` — show tier + CV, or "untested" if null |
| Grounding fidelity | Spot-check pass rate (not stored in API — only in markdown report) |

### Suppressing Compromise on ANALYSIS_REFUSED
```typescript
const refused = result.integrity_warnings?.some(w => w.startsWith("ANALYSIS_REFUSED:"));
if (refused) {
  // Show warning banner instead of compromise proposal
}
```

### Cross-Round Evolution
Fetch all rounds via `round:-1`. Compare:
- **New cruxes**: claims in R2 not in R1 (exact string match)
- **Topic renames**: same `topic_id`, different `topic` name
- **Position evolution**: match R3 agents by stripping `-r3` suffix to find R1 original

### Report Sections (in order)
1. **Key Findings** — top 3 cruxes by controversy + resolution proposals (R3 agents with kind=resolution)
2. **Stance Verification** — `verification.score_dist` table + `verification.details`
3. **Participants** — grouped by `metadata.kind`
4. **Round 1-3 Analysis** — cruxes (two-track), unchallenged, compromise, topics
5. **Evolution** — cross-round comparison using topic_id
6. **Discarded Cruxes** — `discarded_cruxes` with degenerate flag
7. **Integrity** — `integrity_warnings` (excluding DEGENERATE)
8. **Null Control** — `null_control` comparison table
9. **Missing Perspectives** — `coverage_gaps`
10. **Replication** — `replication.runs` table + stability
11. **Cluster Stability** — not in API (report-only, would need separate storage)
12. **Reliability** — summary table from above
13. **Methodology** — static text
