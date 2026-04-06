# Changelog

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
