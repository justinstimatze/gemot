# Changelog

## 2026-04-06 (afternoon)

### New Features

- **Expert panel MCP tool**: `analyze action:expert_panel` creates a deliberation, submits expert critiques, and triggers async analysis in a single call. Returns deliberation_id immediately — poll for completion.
- **Source-type specialization**: `source_type` parameter selects expert panels tuned to the review context: `code_review` (security, API, reliability, maintainability), `architecture` (scalability, security, operations, simplicity), `experiment` (methodology, statistics, validity), `proposal` (feasibility, user value, tech debt, business case). Custom experts via JSON override source_type.
- **Position metadata**: Positions now support an extensible `metadata` JSON field. Used by the diplomacy script to pass lat/lon coordinates for gemotvis world map rendering.

### Improvements

- **Parallel crux detection**: Subtopic crux detection (dedup + getCrux) now runs 5 concurrent goroutines per topic. Reduces crux detection from ~20 minutes to ~4 minutes for panels with 18+ subtopics. Context cancellation and API rate limits respected.
- **Graceful deploys**: Switched from bluegreen to rolling deploy strategy with 10-minute drain timeout. In-flight analyses survive deploys instead of being killed mid-analysis. Go HTTP shutdown timeout increased from 5s to 10 minutes.
- **Position content limit**: Increased from 10K to 50K characters to support larger documents in expert panels and general use.
- **Diplomacy lat/lon**: Agent positions now include capital city coordinates (Vienna, London, Paris, Berlin, Rome, Moscow, Istanbul) as metadata, enabling automatic world map rendering in gemotvis without manual config.
- **Expert panel script**: Simplified from manual 5-step orchestration to single `expert_panel` MCP tool call + poll + fetch.

### Bug Fixes

- **Deploy killing in-flight analysis**: Old bluegreen strategy destroyed machines while analysis was running. Rolling deploy with drain timeout preserves in-flight work.
- **A2A audit trail**: `fulfill` and `break` commitment actions now logged in audit trail.

### Tests

- 4 new expert panel tests: core flow, custom experts, source_type specialization, validation.

## 2026-04-06

### Breaking Changes

- **`SetTemplate` now replaces all rules** instead of merging with existing rules. Previously, switching templates (e.g., jury to assembly) preserved old template rules like `min_participants: 6` even when the new template defined `min_participants: 3`. Now the new template's defaults fully replace the old rules. If you relied on the merge behavior to preserve custom rules across template switches, set them again after calling `set_template`.

### New Features

- **Incremental crux detection**: Multi-round analysis now skips re-extracting claims from prior rounds. `ExtractedClaims` persisted in `AnalysisResult` so subsequent rounds carry forward prior claims and only process new positions. Reduces analysis time proportionally for long-running deliberations.
- **Quorum pre-check**: `analyze run` now checks quorum synchronously before launching async analysis. Returns a clear error instead of failing silently in the background.
- **Quorum hints**: `submit_position` response includes a note when more participants are needed before analysis can run.
- **Expert panel tool**: New script at `scripts/expert-panel/` for one-command adversarial review of any document. Default 5-expert panel with customizable experts via JSON.
- **Thin results for < 2 agents**: Single-agent deliberations return a valid `AnalysisResult` with `INSUFFICIENT_AGENTS` warning instead of erroring. Round still advances.

### Improvements

- **Homepage**: Demo cards now all start collapsed with chevron indicators. New Adversarial Expert Panel demo. Updated Diplomacy demo with v14 per-season experiment results.
- **Diplomacy script**: Batched V12 LLM calls (14 per-power calls reduced to 2 batched calls). Per-season mode via `--through-phase`. Position dedup prevents re-submission on reruns. Resilient SSE sessions with auto-reconnect. Reduced Stage B inter-position delay from 200ms to 50ms.
- **Poll loop fix**: Analysis polling now checks deliberation status instead of `get_context`, preventing false reconnects during long analyses.

### Bug Fixes

- Fixed `SetTemplate` rule merge bug (see Breaking Changes above).
- Fixed poll loop burning reconnect attempts on normal server error responses.
- Fixed per-season mode collecting both seasons' messages instead of just the target season.
- Fixed `--end_at_phase` stopping before the target phase instead of after.

### Tests

- 8 new service-level tests: multi-round claims/norms threading, thin results, status transitions, cancellation.
- 1 new test for `SetTemplate` rule replacement behavior.
- 1 new test for incremental text analysis.
