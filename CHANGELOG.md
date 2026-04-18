# Changelog

All notable changes to gemot are documented here.

## Unreleased — DARPA Track 1 M8 work

### New Features

- **EigenTrust reputation + cold-start cap**: new `internal/analysis/eigentrust.go` implements the Kamvar/Schlosser/Garcia-Molina (SIGIR 2003) power-iteration eigenvector over a sparse directed trust graph with leaked-rank correction and a teleport-weighted pre-trust vector. New `internal/reputation/Weigher` composes this with persistent per-agent state (`agent_reputation`, `agent_trust_edges` tables) to produce multipliers for the `text.go` effective-weight chain. The cold-start cap clamps agents with fewer than `GEMOT_EIGENTRUST_COLD_THRESHOLD` (default 5) survived-round validations to at most `GEMOT_EIGENTRUST_COLD_CAP` (default 0.1); graduation is non-decreasing within a single cohort call (not globally monotone — cohort composition shifts the normalization denominator). Edges accumulate after each round from qualified-vote agreements filtered by crux survival; `AgreeAgents` are deduped per crux and self-endorsement is dropped. `survived_count` requires ≥2 distinct non-self agreers per round, which blocks 2-Sybil pair graduation but not larger rings — the graduation-cliff limitation and the five companion Planned-Defense rows (reputation decay, pubkey-bound identity, edge-table pruning, fail-closed DB toggle, per-delib privacy) are disclosed honestly in THREAT_MODEL.md. The global eigenvector is recomputed + persisted synchronously at round close via batched `unnest`-backed UPSERTs, eliminating the per-row transaction loop that was a lock-contention amplifier on large cohorts. Primary Sybil defense is the cold-start cap — canonical EigenTrust under a uniform teleport does not defeat closed trust cycles without OOB-trusted seeds, and hosted-mode Gemot has no such bootstrap set. Opt-in via `GEMOT_EIGENTRUST_ENABLED=true`. Schema bumped to version 2.
- **TLA+ specification of the deliberation protocol**: new `specs/Deliberation.tla` models the protocol state machine — round progression, position submission, qualified voting, commitment lifecycle, termination — with the LLM analysis step treated as an external oracle per the abstract's §3 design. Safety properties (append-only positions/votes, commitment lifecycle, Closed absorbing, no honest equivocation, no double-vote) and liveness (eventual termination under `WF(StartAnalysis) + SF(AnalysisClose)`) are mechanically checked by TLC 2.19 on OpenJDK. Matching `Deliberation.cfg` checks the minimal model (4 agents, 1 Byzantine, 12,675 distinct states) in ~3s with no errors. The initial liveness check found and fixed a real bug in the fairness annotation — `WF(AnalysisClose)` admitted an `Open → Analyzing → AnalysisFail → Open → …` lasso that `SF(AnalysisClose)` closes. `specs/README.md` documents the n-agent generalization argument (projection + cutoff) and bound rationale. Matches abstract §3 cross-cutting + §5 M16 TLA+ line.
- **Durable nonce cache for multi-instance envelope verification**: new `auth.PostgresNonceCache` (internal/auth/pg_nonce_cache.go) backs the envelope replay defense on the `envelope_nonces` Postgres table. Observation uses `INSERT ... ON CONFLICT DO NOTHING` so concurrent replicas converge on a single winner without distributed locking; a background janitor sweeps expired rows on a `ReplayWindow` cadence. Selected at startup by `GEMOT_NONCE_STORE=postgres`; defaults to the existing `MemoryNonceCache` for single-instance deployments. Closes the open THREAT_MODEL row that blocked horizontal scaling with `GEMOT_ENVELOPE_MODE=required`.
- **Cross-family OOD consistency check**: new `internal/llm/secondary.go` adds a Gemini-backed `SecondaryStructuredOutput` client (REST against `generativelanguage.googleapis.com` with `responseSchema` structured output) and a `SecondaryFunc` adapter used by tests. `TextAnalyzer` gains `SecondaryLLM` + `SecondarySampleK` fields plus a `SetSecondary` setter; the previously stubbed `validateAnalysisModelConsistency` is replaced with a real implementation that samples the top-K highest-controversy cruxes by `ControversyScore`, re-scores each agent's stance on the secondary against their latest-round position, and emits `CROSS_FAMILY_DRIFT` warnings when strict majority of agents on a given crux flip sign between primary and secondary. This is the §3 abstract defense against stable-but-wrong single-family outputs — "adversarial inputs can produce stable-but-wrong outputs that defeat variance-based ensemble detection." The warning is informational, not a refusal gate; training-distribution independence between frontier labs is imperfect (shared benchmark corpora, convergent RLHF) and that caveat is recorded in THREAT_MODEL. Off-by-default; opt in with `GEMOT_CONSISTENCY_MODEL` + `GEMOT_CONSISTENCY_KEY` (or `GEMINI_API_KEY`). Cost scales linearly with `GEMOT_CONSISTENCY_SAMPLE_K` (default 5) — ~5 Gemini-Flash-tier calls per analysis, single-digit cents on current pricing.
- **Fail-closed reputation DB toggle**: `reputation.Weigher.WeightsFor` now returns `(map[string]float64, error)`. Under the new `GEMOT_EIGENTRUST_DB_FAIL=closed` mode, a `LoadReputation` failure propagates through `TextAnalyzer.Analyze` and aborts the analysis round rather than silently degrading to unit weights. The default remains `open` (fail-open, preserves availability and legacy behaviour) but hosted / Byzantine-context deployments should opt in — otherwise an attacker who can DoS Postgres strips the cold-start enforcement exactly when it is needed. This is the concrete mitigation the Byzantine adversarial suite flagged as missing: the cold-start cap is the load-bearing Sybil defense, so a silent neutralization path is a single-point-of-failure. Mirrors the `GEMOT_ENVELOPE_MODE=required` fail-closed pattern. Config field `EigenTrustDBFail` plumbs through `main.go`; THREAT_MODEL.md row moved from planned → implemented.
- **Reputation decay + negative signals**: new `store.DecayTrustEdges` applies `weight *= 0.5^(age / halfLife)` to `agent_trust_edges` rows older than one hour inside `Weigher.recomputeGlobalScores`; one-hour skip prevents double-decay across back-to-back rounds. New `store.ApplyDisputeEdges` ingests per-dispute negative-weight rows via the same `unnest`-backed UPSERT pattern, allowing stored weight to go negative — EigenTrust clamps non-positive edges to zero at input (see `internal/analysis/eigentrust.go:142`), so an overt dispute cancels an endorsement of equal magnitude but does not punch trust below zero. `Weigher.UpdateFromRound` gains a `disputes []types.Dispute` parameter (breaking for in-tree callers only — three test files updated); it maps each dispute's `CruxClaim` to the surviving crux's source-position authors and emits `{From: disputer, To: author, Weight: DisputeWeight}` negative edges, skipping self-disputes and disputes that don't match any crux in the round. Schema bumped to version 3 (new `disputes.rep_processed_at` column); `store.GetUnprocessedDisputes` + `store.MarkDisputesProcessed` provide once-only ingestion semantics so a dispute contributes exactly once across rounds. Closes the whitewashing gap documented in the previous EigenTrust graduation-cliff disclosure: pumped Sybil-ring edges now fade on the half-life clock, and honest participants can counter-balance mutual-endorsement attacks by filing disputes. Both knobs off-by-default: `GEMOT_EIGENTRUST_DECAY_HALFLIFE_DAYS=0` disables decay; `GEMOT_EIGENTRUST_DISPUTE_WEIGHT` defaults to 0.5 but is only consulted when `Dispute` rows exist. Residual limitation: `survived_count` (the graduation side of cold-start) is still monotone-increasing — pubkey-bound reputation identity (companion Planned-Defense row) is the complementary fix for identity-transfer whitewashing.
- **THREAT_MODEL.md updated**: "Durable nonce cache for multi-instance envelope verification" moved from planned → implemented.

### Tests

- New `tests/eigentrust_test.go` (6 tests): empty graph, uniform-no-edges, star-graph convergence (hub dominates), pre-trust-seeded Sybil starvation (closed ring loses under seeded teleport), pre-trust-seed boosts the seeded vertex, pure-sink no-mass-leak.
- New `tests/reputation_weigher_test.go` (7 tests): cold-start cap clamps newcomers to exactly `ColdCap`; graduation is monotonic within a single cohort call; `UpdateFromRound` accumulates agreement edges and increments `survived_count` without self-endorsement when ≥2 distinct non-self agreers sign off; duplicate `AgreeAgents` within a crux are deduped so repeated listings cannot double-count; a 2-Sybil pair ring cannot graduate either participant because neither meets the distinct-agreer threshold; Sybil ring with pumped scores but zero survived validations is starved by the cold-start cap; disabled-config yields nil weigher.
- New `tests/reputation_integration_test.go` (2 tests, Postgres): store-layer batched UPSERT roundtrips (`IncrementSurvivedCounts`, `AccumulateTrustEdges`, `PersistEigenTrustScores`, `LoadReputation`, `LoadTrustEdges`) against real Postgres schema — insert + ON CONFLICT DO UPDATE paths both covered, `survived_count` preservation across score upserts verified; full end-to-end round→reputation→next-round flow verifies `UpdateFromRound` from round 1 cold-caps all agents, and round 2 (threshold=2) graduates alice's weight above `ColdCap` while bob/carol remain cold.
- New `tests/cross_family_consistency_test.go` (5 tests): disabled-path leaves `SecondaryLLM` nil and incurs zero calls; full-agreement between primary and secondary emits no warning and exactly one secondary call per sampled crux; majority sign-flip (3/4 agents) emits `CROSS_FAMILY_DRIFT` with model + provider + truncated claim in the message; single-agent noise (25% flip) stays under the strict-majority threshold and does not false-positive; `sampleK=2` caps secondary calls at 2 even with 4 candidate cruxes (budgets per-analysis secondary cost). Uses `llm.SecondaryFunc` closure-based mock so the full drift-sample → re-score → compare pipeline runs without real Gemini HTTP.
- Two new cases in `tests/reputation_weigher_test.go` cover the fail-mode toggle: `TestWeightsForFailsClosedOnDBError` asserts that `DBFail="closed"` propagates a wrapped store error and returns nil weights; `TestWeightsForFailsOpenOnDBError` confirms the default mode still swallows errors into unit weights.
- New `tests/reputation_decay_test.go` (7 tests, unit): dispute creates a negative edge on a previously-empty pair; a dispute from an agent who previously endorsed nets out to `1 - DisputeWeight` on the same edge; self-disputes drop (no self-edge); disputes referencing unknown crux claims are silently dropped; `DecayTrustEdges` is invoked exactly once per `UpdateFromRound` when `DecayHalfLifeDays > 0`; `DecayTrustEdges` is skipped when halflife=0 (default); and the key §3 defense demonstration — a 3-Sybil mutual-endorsement ring plus one cross-group `s1 → honest` edge flips ordering under six matching disputes, with `honest` now scoring higher than the ring's best member because the one surviving positive channel is the only open mass conduit. Three new Postgres integration cases in `tests/reputation_integration_test.go` cover the real SQL: `TestDecayTrustEdgesPostgres` seeds one fresh and one 14-day-old edge, runs `DecayTrustEdges(7d)`, and asserts the fresh edge is preserved while the stale one decays to ~0.25× (two half-lives); `TestApplyDisputeEdgesPostgres` covers both INSERT (new negative row) and ON CONFLICT (subtract from existing positive) branches; `TestUnprocessedDisputesGatingPostgres` verifies the `rep_processed_at` round-to-round idempotency.
- New `tests/adversarial_byzantine_test.go` (8 tests, DARPA Track 1 Task 5): full-pipeline Byzantine adversarial suite against real Postgres with reputation enabled. `f<1/4` (4 agents, 1 Sybil) passes with honest/Sybil effective-weight ratio = 3.00; `f=1/3` (6 agents, 2 Sybils) passes at the theoretical limit with ratio = 2.00; `f>1/2` (4 agents, 3 Sybils) documents the failure mode — attacker majority dominates, but cold-cap still bounds Sybil aggregate weight; cold-start flooding (10 fresh Sybils vs 2 seasoned legit) verifies Sybil reputation-weight sum = n × ColdCap exactly; reputation-laundering documents the no-decay gap (`survived_count` is sticky); crux-poisoning confirms `LOW_EFFORT_ABS`/`COVERAGE` warnings fire and notes the cold cap only protects synthesis, not extraction; graduation-cliff proves a 3-Sybil mutual-endorsement ring defeats `minDistinctAgreers=2` in 2 rounds, motivating future decay work; reframing-attack logs that reputation is orthogonal to framing defenses (cross-family consistency / Task 4 is the intended countermeasure). New `METRICS.md` captures the measured ratios for DARPA bid citation.
- New `tests/pg_nonce_cache_test.go` (4 tests): first-observation accepts, duplicate rejects with `ErrReplay`, janitor-sweeps-expired-rows, concurrent-safe across two independent cache instances backed by the same DB.

## v0.10.2 (2026-04-17) — A2A envelope

### New Features

- **A2A envelope signing + per-action signatures**: New `A2AAuthMiddleware` in `internal/mcp/a2a.go` lifts bearer-token auth out of the inline handler path into a proper middleware that sets `ContextKeyKeyID`/`ContextKeyIsAdmin`/`ContextKeyAPIKey` on the request context. This lets the envelope middleware's `scopeAgentID` rewrite resolve the scoped stored key during hosted-mode verification. `gemot/participate`'s `submit_position` and `vote` now accept an optional `signature` param and thread through `SubmitPositionWithSigningID` / `SubmitSignedVoteWithSigningID`, mirroring MCP's B1.5 path. Envelope middleware ordering is `A2AAuthMiddleware(envelope(handler))`, same as `/mcp`.
- **THREAT_MODEL.md updated**: "Signatures over the A2A transport" moved from open → implemented.

### Fixes

- **A2A `expert_panel`/`follow_up` credit refund**: Refund path called `creditStore.AddCredits(keyID, ...)` with the 8-char key prefix, but `AddCredits` looks up by `WHERE key = $2` and expects the full `gmt_` token. The error was swallowed with `_, _ =` so customers silently lost their credits on any upstream failure. Now uses `token` with a `gmt_` guard. Pre-existing bug surfaced during the A2A review.

### Tests

- New `tests/a2a_envelope_test.go` (14 tests): off-mode pass-through, required rejects unsigned, valid signature, bad signature, replay, advisory unsigned passes, advisory invalid rejects, auth middleware missing/bad bearer with JSON-RPC error, auth rate-limit rejection, hosted-mode scoped key with ContextKeyKeyID injection, signature param round-trip through real A2AHandler, partial headers, stale timestamp.

## v0.10.1 (2026-04-17) — Track 1 follow-ups

### New Features

- **Phase B1.5 — hosted-mode signature fix**: `scopeAgentID` in the MCP transport used to rewrite `"alice"` → `"<keyID>:alice"` before the service layer, which broke signature verification in hosted mode (client signs `"alice"`; server reconstructed the payload with the scoped form). New `SubmitPositionWithSigningID` / `SubmitSignedVoteWithSigningID` service methods decouple the stored agent_id from the signing agent_id; MCP threads the unscoped form through.
- **Task A extension — `CRUX_INSTABILITY` wired end-to-end**: `TextAnalyzer.StabilityCheckSamples > 1` now issues N extra same-prompt crux calls per subtopic and judges them against the chosen crux via a Haiku-grade semantic-same prompt. Cruxes with <2/3 semantic agreement emit `CRUX_INSTABILITY`. Opt-in via `GEMOT_STABILITY_SAMPLES` env var.
- **Phase B2 — request-envelope signing + replay protection**: new `internal/mcp/envelope.go` HTTP middleware verifies optional ed25519 signatures over `(agent_id, method, body_hash, nonce, timestamp)` on `/mcp` and `/a2a`. Three modes (`off` default / `advisory` / `required`) via `GEMOT_ENVELOPE_MODE`. `±5 min` timestamp window; in-memory `MemoryNonceCache` per-process (single-instance). New domain tag `gemot/v1/envelope` in `internal/auth`.
- **THREAT_MODEL.md updated**: all three items marked implemented; three new open items tracked (durable nonce cache for multi-instance, per-action signing over A2A, cross-family model consistency).

### Tests

- 287 tests (up from 286 after reconciling `validateCruxStability` with the wired path). New coverage:
  - Hosted-mode per-action signature path (`TestHostedModeSigningID`)
  - `cruxStabilityWarning` wired path (`TestCruxStabilityWarning_Wired`)
  - Envelope canonical payload + domain separation + sign/verify round-trip
  - Replay window validation (zero-skew, past skew, future skew)
  - In-memory nonce cache (first observation, replay rejection, TTL expiry, capacity eviction)
  - Envelope middleware: off / advisory / required modes, advisory-valid-passes, advisory-invalid-rejects, bad sig, stale timestamp, replay, missing key, partial headers, non-POST bypass, body-size limit, hosted-mode scoped-key lookup, hosted-mode missing-context rejection

## v0.10.0 (2026-04-17)

### New Features — DARPA Track 1 foundation

- **LLM-specific integrity checks** (`internal/analysis/integrity.go`): `LOW_EFFORT_ABS` / `LOW_EFFORT_REL` (absolute floor <2 claims, median-relative <25% when median ≥4), `THIN_PROVENANCE` (crux backed by <2 source positions or <2 quotes), `CRUX_INSTABILITY` hook (pure-Go; candidate generator + semantic judge unwired pending LLM-defense runs), cross-family consistency stub.
- **Per-agent ed25519 signed positions and votes**: new `internal/auth` package with SSH/Noise/TUF-style length-prefixed canonical payloads and domain separation (`gemot/v1/position`, `gemot/v1/vote`). Signatures verified against registered public keys at submit time.
- **Per-deliberation signature policy**: `none` / `advisory` / `required`, DMARC-style staged migration. DB-enforced via CHECK constraint.
- **Agent key registration and rotation**: new `agent_keys` table, `participate action:register_key` and `revoke_key` MCP tool actions, in-transaction rotation with UNIQUE partial index preventing concurrent-registration races.
- **`participate` tool gained**: optional `signature` field on `submit_position` and `vote`, `register_key` and `revoke_key` actions.
- **Threat model updated**: 7 new implemented-defense rows; planned-defense table reorganized around DARPA-PS-26-09 Track 1 items.

### Tests

- 286 tests (up from 187). New coverage:
  - Signature canonicalization, domain separation, cross-domain replay
  - Signed position/vote with policy matrix (none/advisory/required)
  - Key rotation, revocation, signature verification after rotation
  - Signed content with PII sanitization (verify against raw content)
  - Low-effort position detection (absolute + median-relative)
  - Crux provenance validation

## v0.9.0 (2026-04-06)

### Breaking Changes

- **`SetTemplate` now replaces all rules** instead of merging with existing rules. Previously, switching templates preserved old template rules even when the new template defined different defaults. Now the new template's defaults fully replace the old rules.

### Security

- **14 audit findings fixed**: Metrics endpoint method restriction, CSV export quoting, SSE shutdown ordering, webhook idempotency (partial unique index), schema version tracking, A2A job tracking, resolution lock cleanup on delete.
- **Dev mode hardening**: Rate limiting (30/min per IP) when running without `GEMOT_API_SECRET`. Auth bypass in A2A, SSE, and metrics endpoints for dev mode.
- **Atomic credit operations**: Replaced mutex + separate SELECT with `UPDATE...RETURNING` for AddCredits, Deduct, AddCreditsByEmail. Eliminates TOCTOU race conditions.
- **Webhook idempotency**: `AddCreditsByEmail` now rejects duplicate session IDs, preventing double-crediting from webhook retries.
- **Docker Postgres localhost-only**: `docker-compose.yml` binds Postgres to `127.0.0.1` instead of all interfaces.

### New Features

- **Expert panel tool**: `analyze action:expert_panel` creates a deliberation with domain-specialized expert critiques in a single call. Source types: `code_review`, `architecture`, `experiment`, `proposal`. Custom experts via JSON.
- **Service-level audit logging**: All write operations (create, submit, vote, commit, delete) logged via `SetAuditLogger` callback. Full audit log included in deliberation exports.
- **Priority API semaphore**: 7 background + 3 interactive-reserved slots for LLM calls. Expert panels and follow-up analyses get priority access, preventing starvation behind long-running batch jobs.
- **Incremental crux detection**: Multi-round analysis skips re-extracting claims from prior rounds. Persisted `ExtractedClaims` carry forward, reducing analysis time proportionally.
- **Commitment accountability**: Conditional commitments, fulfillment tracking, and agent reputation scores.
- **Position metadata**: Extensible JSON metadata field on positions. Used by diplomacy integration for lat/lon coordinates.
- **Schema version table**: Warns on startup if database schema is ahead of binary version (downgrade detection).

### Performance

- **Parallel topic analysis**: Topics run in parallel (summaries + crux detection share bounded semaphore of 5). Previously sequential — 3x speedup for multi-topic deliberations.
- **CheckParticipantCap**: Single `COUNT(DISTINCT agent_id)` query replaces loading all positions into memory for max_participants enforcement.
- **Resolution deferred to analysis**: Resolution recalculated after analysis completes, not on every vote. Reduces per-vote overhead.

### Improvements

- **Context propagation**: All 42 Service methods accept `context.Context`. 90 `context.TODO()` calls eliminated. Request cancellation and deadlines propagate from HTTP handlers through to database queries.
- **SSE reliability**: Robust reconnect (max 10 retries, 3s backoff), server IdleTimeout increased to 10 minutes.
- **Graceful deploys**: Rolling strategy with 10-minute drain timeout. SSE clients notified before HTTP server shutdown.
- **Parallel claim extraction**: 6 concurrent goroutines for claim extraction.
- **LLM response caching**: 24h TTL, SHA256 keys.
- **CSV export**: Position IDs and timestamps now quoted to prevent injection.

### Documentation

- **Getting Started tutorial**: End-to-end walkthrough of first deliberation.
- **Environment variables reference**: All config vars documented in README.
- **Stale references fixed**: SQLite references updated to Postgres throughout docs.

### Tests

- 187 tests (up from 161). New coverage:
  - Audit logger callback verification
  - Participant cap enforcement and existing-agent bypass
  - Concurrent credit deductions (race condition tests)
  - Webhook idempotency (double-credit prevention)
  - Resolution update after analysis
  - Soft-delete and resolution lock cleanup

## v0.8.0 (2026-04-04)

- Grouped MCP tools (6 tools with `action` parameter, down from 30+)
- Postgres migration (from SQLite via pgx)
- Resolution layer with template-aware consensus thresholds
- Governance templates: assembly, sortition, parliament, jury, consensus, negotiation, review
- Delegation (liquid democracy), withdrawal, deadlines
- A2A JSON-RPC endpoint with MCP/A2A method parity
- Structured logging (slog)

## v0.7.0

- Enriched agent context (cluster, allies, disagreements, diversity nudge)
- Multi-scope deliberations
- Security hardening

## v0.5.0

- Streamable HTTP transport
- Multi-use join codes
- XSS fix
- Sandbox mode
