# Changelog

All notable changes to gemot are documented here.

## Unreleased — DARPA Track 1 M8 work

### New Features

- **EigenTrust reputation + cold-start cap**: new `internal/analysis/eigentrust.go` implements the Kamvar/Schlosser/Garcia-Molina (SIGIR 2003) power-iteration eigenvector over a sparse directed trust graph with leaked-rank correction and a teleport-weighted pre-trust vector. New `internal/reputation/Weigher` composes this with persistent per-agent state (`agent_reputation`, `agent_trust_edges` tables) to produce multipliers for the `text.go` effective-weight chain. The cold-start cap clamps agents with fewer than `GEMOT_EIGENTRUST_COLD_THRESHOLD` (default 5) survived-round validations to at most `GEMOT_EIGENTRUST_COLD_CAP` (default 0.1); graduation is non-decreasing within a single cohort call (not globally monotone — cohort composition shifts the normalization denominator). Edges accumulate after each round from qualified-vote agreements filtered by crux survival; `AgreeAgents` are deduped per crux and self-endorsement is dropped. `survived_count` requires ≥2 distinct non-self agreers per round, which blocks 2-Sybil pair graduation but not larger rings — the graduation-cliff limitation and the five companion Planned-Defense rows (reputation decay, pubkey-bound identity, edge-table pruning, fail-closed DB toggle, per-delib privacy) are disclosed honestly in THREAT_MODEL.md. The global eigenvector is recomputed + persisted synchronously at round close via batched `unnest`-backed UPSERTs, eliminating the per-row transaction loop that was a lock-contention amplifier on large cohorts. Primary Sybil defense is the cold-start cap — canonical EigenTrust under a uniform teleport does not defeat closed trust cycles without OOB-trusted seeds, and hosted-mode Gemot has no such bootstrap set. Opt-in via `GEMOT_EIGENTRUST_ENABLED=true`. Schema bumped to version 2.
- **TLA+ specification of the deliberation protocol**: new `specs/Deliberation.tla` models the protocol state machine — round progression, position submission, qualified voting, commitment lifecycle, termination — with the LLM analysis step treated as an external oracle per the abstract's §3 design. Safety properties (append-only positions/votes, commitment lifecycle, Closed absorbing, no honest equivocation, no double-vote) and liveness (eventual termination under `WF(StartAnalysis) + SF(AnalysisClose)`) are mechanically checked by TLC 2.19 on OpenJDK. Matching `Deliberation.cfg` checks the minimal model (4 agents, 1 Byzantine, 12,675 distinct states) in ~3s with no errors. The initial liveness check found and fixed a real bug in the fairness annotation — `WF(AnalysisClose)` admitted an `Open → Analyzing → AnalysisFail → Open → …` lasso that `SF(AnalysisClose)` closes. `specs/README.md` documents the n-agent generalization argument (projection + cutoff) and bound rationale. Matches abstract §3 cross-cutting + §5 M16 TLA+ line.
- **Durable nonce cache for multi-instance envelope verification**: new `auth.PostgresNonceCache` (internal/auth/pg_nonce_cache.go) backs the envelope replay defense on the `envelope_nonces` Postgres table. Observation uses `INSERT ... ON CONFLICT DO NOTHING` so concurrent replicas converge on a single winner without distributed locking; a background janitor sweeps expired rows on a `ReplayWindow` cadence. Selected at startup by `GEMOT_NONCE_STORE=postgres`; defaults to the existing `MemoryNonceCache` for single-instance deployments. Closes the open THREAT_MODEL row that blocked horizontal scaling with `GEMOT_ENVELOPE_MODE=required`.
- **THREAT_MODEL.md updated**: "Durable nonce cache for multi-instance envelope verification" moved from planned → implemented.

### Tests

- New `tests/eigentrust_test.go` (6 tests): empty graph, uniform-no-edges, star-graph convergence (hub dominates), pre-trust-seeded Sybil starvation (closed ring loses under seeded teleport), pre-trust-seed boosts the seeded vertex, pure-sink no-mass-leak.
- New `tests/reputation_weigher_test.go` (7 tests): cold-start cap clamps newcomers to exactly `ColdCap`; graduation is monotonic within a single cohort call; `UpdateFromRound` accumulates agreement edges and increments `survived_count` without self-endorsement when ≥2 distinct non-self agreers sign off; duplicate `AgreeAgents` within a crux are deduped so repeated listings cannot double-count; a 2-Sybil pair ring cannot graduate either participant because neither meets the distinct-agreer threshold; Sybil ring with pumped scores but zero survived validations is starved by the cold-start cap; disabled-config yields nil weigher.
- New `tests/reputation_integration_test.go` (2 tests, Postgres): store-layer batched UPSERT roundtrips (`IncrementSurvivedCounts`, `AccumulateTrustEdges`, `PersistEigenTrustScores`, `LoadReputation`, `LoadTrustEdges`) against real Postgres schema — insert + ON CONFLICT DO UPDATE paths both covered, `survived_count` preservation across score upserts verified; full end-to-end round→reputation→next-round flow verifies `UpdateFromRound` from round 1 cold-caps all agents, and round 2 (threshold=2) graduates alice's weight above `ColdCap` while bob/carol remain cold.
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
