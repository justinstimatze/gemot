# Feature Request: Richer Report Data for gemotvis

## Context

gemotvis has a report mode (`?view=report`) that renders deliberation analysis as a static document. It currently consumes data from the standard `gemot/analyze action:get_result` A2A response (`AnalysisResult` struct).

The t3c-import pipeline now generates rich validation data and multi-round analysis (null control, stance verification with 1-5 confidence scoring, replication stability, coverage gap analysis, resolution proposals, position revision). Most of this data is now available through gemot's API — gemotvis just needs to consume it.

**Reference output**: `integrations/t3c/ai-manifestos-report.md` shows the full report structure that gemotvis should be able to render interactively.

## What's Shipped (gemot side) — Ready for gemotvis to Consume

### 1. `discarded_cruxes` field — SHIPPED
On `AnalysisResult` as `DiscardedCruxes []Crux`, serialized as `discarded_cruxes`. Includes `Degenerate bool` flag.

**gemotvis TODO**: Add `discarded_cruxes` to `internal/gemot/types.go` and `frontend/src/types.ts`. Render as a "Discarded Cruxes" section. Replace the current hack of parsing `DEGENERATE:` integrity warnings.

### 2. `topic_id` on TopicSummary — SHIPPED
On `TopicSummary` as `TopicID string`. Assigned as T1-T6 after taxonomy extraction, persists across rounds.

**gemotvis TODO**: Add `topic_id` to mirrored type. Use for cross-round topic tracking in evolution display.

### 3. Multi-round analysis via `round:-1` — SHIPPED
`get_result` now accepts `round: -1` to return all rounds as a JSON array:

```
analyze action:get_result deliberation_id:X round:-1
→ [AnalysisResult, AnalysisResult, ...]  (ordered by round number)
```

Existing behavior unchanged: omit `round` for latest, or `round:N` for specific round.

**gemotvis TODO**: Add a round selector/scrubber. Fetch all rounds on load. Show evolution section: new cruxes between rounds, topic taxonomy changes (renamed/added/dropped using `topic_id`), position revision comparison.

### 4. Validation results on AnalysisResult — SHIPPED
Four optional fields added to `AnalysisResult` (all `omitempty`, zero overhead for non-validated deliberations):

```json
{
  "null_control": {
    "null_delib_id": "...",
    "real_metrics": { "crux_count": 13, "avg_controversy": 0.72, ... },
    "null_metrics": { "crux_count": 10, "avg_controversy": 0.51, ... },
    "failed_metrics": [],
    "pass": true
  },
  "verification": {
    "total": 44, "checked": 44, "downgraded": 19, "threshold": 2,
    "score_dist": [0, 4, 15, 9, 13, 3],
    "details": [{ "speaker": "...", "crux": "...", "orig_stance": "disagree", "score": 2, "reason": "..." }]
  },
  "replication": {
    "num_runs": 3, "delib_ids": ["...", "...", "..."],
    "runs": [{ "crux_count": 12, ... }, ...],
    "stability": { "tier": 2, "crux_cv": 0.08, "controv_cv": 0.12, "consensus_cv": 0.15, "all_stable": true }
  },
  "coverage_gaps": [
    { "position": "...", "missing_perspective": "...", "suggested_source": "..." }
  ]
}
```

The t3c-import pipeline stores these on the R1 analysis result via `update_result` after validation steps complete.

**gemotvis TODO**: 
- **Reliability table**: Render a table matching report.go's format with rows for internal coherence, agent hallucinations, stance grounding, null control, replication stability, grounding fidelity. Each row has status (pass/fail/partial/untested/cleaned) and detail text. Check for field presence — show "untested" when a field is null.
- **Stance verification section**: If `verification` is present, show score distribution table (1-5 scale) and list of downgraded stances with reasons. Use blockquote for reasons.
- **Null control section**: If `null_control` is present, show comparison table (real vs null metrics with delta percentages) and pass/fail verdict.
- **Replication section**: If `replication` is present, show per-run metrics table, CV scores, and stability tier.
- **Missing Perspectives**: If `coverage_gaps` is present, list each gap with the missing perspective and suggested source.

### 5. Agent kind via position metadata — SHIPPED
t3c-import now sends `{"kind": "speaker"}` (or steelman, probe, bridge, dissent, empty-chair, resolution) in position metadata on all submissions. Available on `Position.Metadata` in `get_positions` responses.

**gemotvis TODO**: Read `metadata.kind` from positions. Use for:
- **Crux display**: Split agree/disagree lists into "Speakers" and "Structural" tracks (matching report.go's two-track display)
- **Participant list**: Categorize agents by kind (Clusters, Speakers, Probes, R2 Agents, R3 Agents, Resolutions)
- **Graph view**: Different node shapes or colors by kind (speakers = circles, probes = diamonds, resolutions = squares, etc.)

### 6. `update_result` action — SHIPPED
Pipelines can write validation data back to analysis results:

```
analyze action:update_result deliberation_id:X round:1 result_json:"{...}"
```

Access-checked. gemotvis doesn't call this directly — it's for pipeline use. But it means validation data will be present in `get_result` responses for deliberations that ran validation.

### 7. ANALYSIS_REFUSED / HALLUCINATION warnings — ALREADY AVAILABLE
Already in `integrity_warnings` array. No API changes needed.

**gemotvis TODO**:
- Parse `ANALYSIS_REFUSED:` prefix → suppress compromise proposal, show warning banner
- Parse `HALLUCINATION:` prefix → count occurrences, show tiered severity in reliability table (1-3: minor, 4-9: moderate, 10+: high)

## Implementation Guide for gemotvis

### Phase 1: Quick wins (frontend-only, no API changes)
1. Add `discarded_cruxes` and `topic_id` to mirrored types
2. Parse `ANALYSIS_REFUSED` / `HALLUCINATION` from integrity warnings
3. Read `metadata.kind` from positions for agent categorization
4. Suppress compromise when ANALYSIS_REFUSED

### Phase 2: Validation display
5. Check for `null_control`, `verification`, `replication`, `coverage_gaps` on analysis results
6. Render reliability table (conditional on field presence)
7. Render stance verification score distribution
8. Render missing perspectives section

### Phase 3: Multi-round evolution
9. Fetch all rounds via `round:-1`
10. Build evolution section: cross-round crux comparison, topic rename tracking, position revision display
11. Add round selector/scrubber to report view

### Report structure reference
The sections in order (matching `integrations/t3c/ai-manifestos-report.md`):

1. **Key Findings** — top 3 cruxes + proposed resolutions (if R3)
2. **Stance Verification** — score distribution table + downgraded stances
3. **Participants** — categorized by kind (clusters, speakers, probes, R2, R3)
4. **Round 1: Initial Analysis** — cruxes (two-track display), unchallenged positions, compromise, topics
5. **Round 2: Emergent Findings** — same structure, plus evolution section
6. **Round 3: Revised Positions** — cruxes with resolution agents, position evolution (R1→R3)
7. **Discarded Cruxes** — with failure mode classification
8. **Integrity** — warnings (excluding DEGENERATE which is shown above)
9. **Null Control** — comparison table + verdict
10. **Missing Perspectives** — coverage gaps
11. **Replication** — per-run metrics + stability CV
12. **Spot-Check Failures** — failed stance verifications with reasons
13. **Cluster Stability** — multi-threshold table (70/80/90%)
14. **Reliability** — summary table (coherence, hallucinations, stance grounding, null control, replication, grounding fidelity)
15. **Methodology Notes** — agent construction, score interpretation, multiple comparisons, circularity, replicability
16. **Next Steps** — join code, continue deliberating, replicate, validate
