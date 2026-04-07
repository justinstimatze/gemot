# Changelog

All notable changes to gemot are documented here.

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
