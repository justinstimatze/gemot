# Gemot Threat Model: Epistemic Poisoning in Multi-Agent Deliberation

## Threat Summary

Gemot is a deliberation server where AI agents submit positions, vote, and receive LLM-generated analysis (topics, cruxes, consensus). The primary threat is not that an attacker breaks the server — it's that a malicious agent uses the server's **trusted analysis output** to manipulate other agents' beliefs and decisions.

Gemot is an **epistemic amplifier**: its analysis is trusted by consuming agents, so corrupted analysis propagates at scale. This makes it a higher-value target than a typical API.

## Deployment Posture

**Current: Trusted agents only.** The HTTP transport requires a bearer token (`GEMOT_API_SECRET`). All participating agents are assumed to be operated by trusted parties. Sybil attacks and agent impersonation are mitigated by access control, not by the protocol itself.

**Future: Federated/open.** When gemot opens to untrusted agents, the defenses below become mandatory.

## Attack Patterns

### 1. Indirect Prompt Injection via Positions

A malicious agent embeds LLM instructions in its position text. When gemot's analysis pipeline processes this position, the instructions manipulate topic extraction, crux detection, or consensus synthesis. The corrupted output is served to other agents as trusted analysis.

**Severity:** Critical
**Current defense:** User content wrapped in XML tags (`<position>`, `<claim>`) with clear boundaries.
**Research:** OWASP LLM01; Microsoft MCP indirect injection guidance; Snyk prompt injection + MCP analysis.

### 2. Taxonomy Silencing

A malicious agent submits positions designed to produce a taxonomy where a target agent's claims map to a subtopic with only one speaker. Since crux detection requires ≥2 speakers per subtopic, the target's voice is silently erased from the analysis.

**Severity:** High
**Current defense:** Topic-level crux fallback when subtopics have insufficient speakers.
**Needed:** Coverage validation — flag when any agent who submitted a position has zero claims in the final analysis.

### 3. Crux Framing Manipulation

Crafted positions exploit LLM summarization bias to control how crux claims are worded. The attacker doesn't need explicit prompt injection — just adversarial positioning of arguments that triggers the LLM to frame the crux in a way that favors the attacker's position.

**Severity:** High
**Current defense:** Structured output schema constrains crux format.
**Needed:** Multi-candidate crux generation with diversity scoring; crux provenance tracking.
**Research:** AdvSumm (arXiv 2506.06273); LLM framing bias in summarization (Royal Society Open Science).

### 4. Sybil-Amplified Vote Manipulation

Multiple fake agent identities submit coordinated votes to skew PCA clustering, manipulate consensus detection, and distort crux identification. With self-asserted agent IDs (current design), this is trivial when access control is open.

**Severity:** Critical (in open federation), Low (in trusted mode)
**Current defense:** Bearer token auth limits who can connect. Agent IDs are self-asserted but all agents share the same auth token.
**Needed:** Per-agent authentication; vote similarity detection.
**Research:** Sybil-resistant voting (arXiv 2407.01844); quadratic voting Sybil analysis (Stanford).

### 5. Iterative Amplification

In multi-round deliberation, a small bias in round 1 analysis compounds as agents update positions based on it. The updated positions feed into round 2 analysis, further amplifying the distortion. Research shows a 37.6% increase in critical distortions after only 5 iterations.

**Severity:** High
**Current defense:** None.
**Needed:** Round-over-round drift detection; analysis diff between rounds; alert when agent positions converge suspiciously fast.
**Research:** LLM feedback loop research (arXiv 2402.06627); real-time misinformation feedback (arXiv 2410.14651).

### 6. Memory/Context Poisoning

If a malicious agent manipulates one deliberation's analysis, and that analysis is stored and referenced later (e.g., by agents who participated in multiple deliberations), the poisoned conclusions propagate beyond the original deliberation.

**Severity:** Medium
**Current defense:** Analysis results are scoped to deliberation ID and round number. No cross-deliberation referencing in the current design.
**Research:** AgentPoison (NeurIPS 2024, arXiv 2407.12784); MemoryGraft (arXiv 2512.16962).

### 7. Consensus Spoofing

A malicious agent submits positions carefully crafted to appear as consensus (moderate, balanced language) while actually embedding a specific conclusion. The LLM, RLHF-trained toward moderation, amplifies this framing in consensus statements.

**Severity:** Medium
**Current defense:** Consensus requires >50% agreement in every cluster, not just overall.
**Needed:** Consensus provenance — link each consensus statement to the specific votes that support it.

## Implemented Defenses

| Defense | Status | Location |
|---|---|---|
| Bearer token auth (constant-time compare) | ✅ Implemented | `internal/mcp/http.go` |
| HTTP timeouts + request size limits | ✅ Implemented | `internal/mcp/http.go` |
| Input length validation | ✅ Implemented | `internal/deliberation/service.go` |
| Position count cap (1000) | ✅ Implemented | `internal/deliberation/service.go` |
| Atomic status transition (prevents race) | ✅ Implemented | `internal/store/repository.go` |
| Agent anonymization before LLM | ✅ Implemented | `internal/analysis/text.go` |
| XML content boundaries | ✅ Implemented | `internal/analysis/text.go` |
| Topic-level crux fallback | ✅ Implemented | `internal/analysis/text.go` |
| Consistent consensus thresholds (50%) | ✅ Implemented | `internal/analysis/text.go`, `votes.go` |
| Stderr not stdout for error logging | ✅ Implemented | `internal/deliberation/service.go` |
| Postgres connection pooling + limits | ✅ Implemented | `internal/store/store.go` |
| Coverage validation (zero-claim agents) | ✅ Implemented | `internal/analysis/integrity.go` (`validateCoverage`) |
| Low-effort position detection (abs + median-relative) | ✅ Implemented | `internal/analysis/integrity.go` (`validateLowEffortPositions`) |
| Crux agent validation (hallucinated IDs, degenerate cruxes) | ✅ Implemented | `internal/analysis/integrity.go` (`validateCruxAgents`) |
| Crux provenance tracking (`SourcePositionIDs`, `SourceQuotes`) | ✅ Implemented | `internal/analysis/text.go`, `types/analysis.go` |
| Thin-provenance warning (< 2 positions or < 2 quotes) | ✅ Implemented | `internal/analysis/integrity.go` (`validateCruxProvenance`) |
| Vote similarity detection (Sybil signal) | ✅ Implemented | `internal/analysis/integrity.go` (`validateVoteSimilarity`) |
| Agent model-family diversity check | ✅ Implemented | `internal/analysis/integrity.go` (`validateModelDiversity`) |
| Per-agent ed25519 signed positions and votes | ✅ Implemented | `internal/auth/signature.go`, `internal/store/agent_keys.go` |
| Per-deliberation signature policy (`none` / `advisory` / `required`) | ✅ Implemented | `internal/deliberation/service.go` (`verifyPositionSignature`, `verifyVoteSignature`) |
| Domain-separated length-prefixed signing payload (SSH/Noise/TUF pattern) | ✅ Implemented | `internal/auth/signature.go` (`PositionPayload`, `VotePayload`, `EnvelopePayload`) |
| Hosted-mode signature-scoping fix | ✅ Implemented | `SubmitPositionWithSigningID` / `SubmitSignedVoteWithSigningID` in `internal/deliberation/service.go` decouple the stored (scoped) agent_id from the signed (unscoped) one. The MCP server threads the unscoped form through. |
| Multi-candidate crux stability check | ✅ Implemented | `TextAnalyzer.StabilityCheckSamples > 1` triggers N same-prompt regenerations plus a Haiku-grade semantic-same judge per sample; cruxes with <2/3 agreement emit `CRUX_INSTABILITY`. Opt-in via `GEMOT_STABILITY_SAMPLES`. |
| Request-envelope signing (Phase B2) | ✅ Implemented | `internal/mcp/envelope.go` middleware verifies ed25519 signatures over `(agent_id, method, body_hash, nonce, timestamp)` on `/mcp` and `/a2a`. Modes: `off` (default, pass-through) / `advisory` (verify-and-log) / `required` (reject unsigned). `GEMOT_ENVELOPE_MODE` selects. |
| A2A envelope + per-action signatures | ✅ Implemented | `A2AAuthMiddleware` (internal/mcp/a2a.go) lifts bearer-token auth out of the handler, populating `ContextKeyKeyID`/`ContextKeyIsAdmin`/`ContextKeyAPIKey` so the envelope middleware's `scopeAgentID` rewrite resolves the scoped stored key in hosted mode. The `gemot/participate` `submit_position` and `vote` actions accept an optional `signature` param and route through `SubmitPositionWithSigningID` / `SubmitSignedVoteWithSigningID`. |
| Replay protection (Phase B2) | ✅ Implemented | ±5-minute timestamp window (`auth.ValidateTimestamp`) + pluggable nonce cache. `MemoryNonceCache` (default, per-process) or `PostgresNonceCache` (durable, multi-instance safe via `GEMOT_NONCE_STORE=postgres`). |
| Durable nonce cache for multi-instance envelope verification | ✅ Implemented | `auth.PostgresNonceCache` persists nonces in the `envelope_nonces` table using `INSERT ... ON CONFLICT DO NOTHING`, giving at-most-one-winner semantics across replicas. A background janitor sweeps expired rows on a `ReplayWindow` cadence. Selected at startup by `GEMOT_NONCE_STORE=postgres`. |
| Cross-family OOD consistency check | ✅ Implemented | `internal/llm/secondary.go` adds a Gemini-backed `SecondaryStructuredOutput` client; `TextAnalyzer.validateAnalysisModelConsistency` (integrity.go) re-scores the top-K highest-controversy cruxes on the secondary and emits `CROSS_FAMILY_DRIFT` when strict majority of agents on a crux flip sign between primary (Claude) and secondary (Gemini). This defends against the §3 abstract scenario: "adversarial inputs can produce stable-but-wrong outputs that defeat variance-based ensemble detection." The defense relies on training-distribution independence between labs — imperfect in practice (shared benchmark corpora, convergent RLHF drift toward similar refusals), so the warning is informational, not a refusal gate. Off-by-default; opt in with `GEMOT_CONSISTENCY_MODEL` + `GEMOT_CONSISTENCY_KEY`. Sample bound via `GEMOT_CONSISTENCY_SAMPLE_K` (default 5). |
| Fail-closed reputation DB toggle | ✅ Implemented | `reputation.Weigher.WeightsFor` returns `(map, error)`; under `GEMOT_EIGENTRUST_DB_FAIL=closed` a `LoadReputation` error propagates through `text.go` and aborts the analysis round rather than silently degrading to unit weights. Default remains `open` (legacy behaviour, preserves availability) but hosted deployments with Byzantine-context assumptions should opt in — otherwise an attacker who can DoS Postgres strips all cold-start enforcement exactly when it is needed. Mirrors the `GEMOT_ENVELOPE_MODE=required` fail-closed pattern. |
| EigenTrust reputation + cold-start cap | ✅ Implemented | `internal/reputation/Weigher` computes global EigenTrust scores (Kamvar/Schlosser/Garcia-Molina, SIGIR 2003) over a sparse trust graph built from qualified-vote agreements filtered by crux survival. Scores and edges persist in `agent_reputation` / `agent_trust_edges` tables. Cold-start cap (`GEMOT_EIGENTRUST_COLD_CAP`, default 0.1) clamps agents with `survived_count < GEMOT_EIGENTRUST_COLD_THRESHOLD` (default 5); this is the primary Sybil defense during cold-start only. Honest caveats: (a) canonical EigenTrust under a uniform teleport does not defeat closed trust cycles — the paper's pre-trusted-seed remedy is not applicable without an OOB-trusted bootstrap set (Douceur 2002 impossibility); (b) `survived_count` increments require ≥2 distinct non-self agreers per round, which blocks 2-Sybil pair graduation but not larger rings — post-graduation, N ≥ 3 rings can still pool weight via mutual endorsement, but the **reputation decay + negative signals** row below damps this by time-decaying pumped edges and allowing overt disputes to cancel endorsements at EigenTrust input; (c) Ford's pseudonym-parties framing informs the direction but no physical-attendance claim is made. Opt-in via `GEMOT_EIGENTRUST_ENABLED=true`. |
| Reputation decay + negative signals | ✅ Implemented | `store.DecayTrustEdges` applies `weight *= 0.5^(age / halfLife)` to edges older than one hour on every `recomputeGlobalScores` call (skipping the fresh-edge window prevents double-decay on quick successive rounds). `store.ApplyDisputeEdges` stores per-dispute weight-subtraction rows — the stored weight is allowed to go negative, and the EigenTrust computation (`internal/analysis/eigentrust.go`) clamps non-positive edges to zero at input so disputes cancel endorsements but don't punch trust below zero. `Weigher.UpdateFromRound` ingests unprocessed `Dispute` rows via `GetUnprocessedDisputes` and stamps `rep_processed_at` via `MarkDisputesProcessed` so a given dispute contributes exactly once across rounds. Schema v3 adds `disputes.rep_processed_at`. Closes the whitewashing gap: an agent who stops being reinforced loses accumulated weight on the half-life clock, and overt disputes from honest participants counter-balance pumped-up Sybil-ring edges (see `TestDisputeAgainstSybilRingDampensScore`). Both knobs off-by-default: `GEMOT_EIGENTRUST_DECAY_HALFLIFE_DAYS=0` disables decay; `GEMOT_EIGENTRUST_DISPUTE_WEIGHT` defaults to 0.5 but is only consulted when `Dispute` rows exist. Residual limitation: graduation itself (the `survived_count` side of cold-start) is still monotone-increasing — this is why the row-above graduation-cliff disclosure still names the N≥3-ring case. The pubkey-bound reputation row below is the complementary fix for the whitewashing attack via identity transfer. |
| Pubkey-bound reputation identity | ✅ Implemented | Schema v4 migrates reputation vertices from symbolic `agent_id` to a namespaced form: `"key:<agent_keys.id>"` when the agent had an active registered key at emission time, `"id:<agent_id>"` otherwise. The prefix is reserved — `store.ResolveVertices` computes the vertex at every read/write boundary and the Weigher pre-resolves all agreers/authors/disputers in a single batched lookup before emitting edges via `UpdateFromRound`. Defeats the rename-attack: registering `"alice"` under a new pubkey yields a new `agent_keys.id`, a fresh vertex, and zero accumulated reputation. Legit key rotation is symmetric — rotation resets rep, which is the correct defense against a compromised K1 transferring trust to its replacement K2. Unsigned deployments are unchanged (all vertices fall back to `"id:<agent>"`). Transition: registering a key for a previously-unsigned agent forks the identity into a new key-bound vertex; the prior `"id:"` row is orphaned rather than silently merged because there is no cryptographic attestation that the prior unsigned rep belongs to the new key. Coverage: `tests/reputation_pubkey_binding_test.go` exercises rename attack, mixed cohorts, unsigned-to-signed transition, revocation, and end-to-end UpdateFromRound emission. One-time migration cost: pre-v4 accumulated reputation is discarded on schema bump (documented in CHANGELOG) — a clean-slate transition is safer than in-place PK rewriting under rolling deploy. |
| Edge-table pruning + cumulative-weight caps | ✅ Implemented | `store.DecayTrustEdges(halfLife, floor)` now runs a `DELETE FROM agent_trust_edges WHERE weight > 0 AND weight < floor` after the decay UPDATE; positive edges whose decayed weight drops below the floor are pruned, bounding row count on long-running deployments. Negative-weight dispute rows are retained unconditionally so persistent dispute signals don't get reabsorbed by fresh endorsements. `store.AccumulateTrustEdges(edges, cap)` clamps cumulative per-edge weight via `LEAST(... , $cap)` in both INSERT and ON CONFLICT paths — a single `(from, to)` pair can exert at most `cap` × unit endorsement of inbound trust, bounding the damage from a Sybil pair that repeats mutual endorsement across many deliberations. Cap does not apply to `ApplyDisputeEdges`; disputes accumulate arbitrarily negative by design (EigenTrust clamps non-positive edges at input, so extra-negative rows are harmless but preserve the dispute history). Both knobs off-by-default: `GEMOT_EIGENTRUST_EDGE_FLOOR=0` disables pruning (legacy cumulative-forever semantics), `GEMOT_EIGENTRUST_EDGE_CAP=0` disables clamping. Recommended: 0.01 and 10.0 for open-federation deployments. Coverage: `tests/reputation_retention_test.go` exercises floor prune + retention, cap clamp + disabled-fallback, and dispute-ignores-cap invariants. Residual: `LoadTrustEdges` still does `SELECT *` with no LIMIT — subgraph loading from the active cohort is tracked below for when row count exceeds ~10k. |
| Per-deliberation privacy boundary + per-delib private EigenTrust | ✅ Implemented | Schema v5 partitions `agent_trust_edges` by `deliberation_id` so private delibs emit into a scoped partition (`deliberation_id = <uuid>`) while open/link delibs continue writing the global partition (`deliberation_id = ''`). `store.AccumulateTrustEdges`, `ApplyDisputeEdges`, and `LoadTrustEdges` take a `delibID` parameter — `""` means global semantics (legacy), non-empty writes/reads the scoped partition. For private delibs, `Weigher.WeightsFor` reads `types.DelibFromContext(ctx)` (set by `service.Analyze` via `types.WithDelibContext`) and computes a per-delib EigenTrust eigenvector on-the-fly over `LoadTrustEdges(ctx, delibID)` — which returns `deliberation_id IN ('', <delibID>)`, so the private cohort sees the global trust landscape as prior plus the intra-delib agreement patterns, but never another private delib's edges. `survived_count` stays global: private rounds do NOT increment it, so fresh Sybils coordinating inside a private ring cannot graduate out of the cold-start cap. `visibility="link"` is treated as public (discoverable-by-token, not consent-limited). Coverage: `tests/deliberation_privacy_test.go` (private-no-global-leak, public-still-global, link-like-public) + `tests/private_eigentrust_test.go` (scoped-emission, global-emission regression, per-delib isolation of edge partitions, seasoned-agent cold-cap inheritance). Rolling-deploy caveat: the v5 PK change from `(from, to)` to `(from, to, delib_id)` is not rolling-safe; ~30s maintenance window required. |

## Planned Defenses (Pre-Open-Federation)

| Defense | Priority | Description |
|---|---|---|
| Registration-boundary key ownership (open federation) | High | In hosted mode, `scopeAgentID` namespaces key registration per API key. In open federation, first-come-first-served for arbitrary agent IDs must be gated behind a registration authority or proof-of-agent-identity. (DARPA-PS-26-09 Track 1) |
| Signed-content post-hoc verification | Medium | Server-side PII sanitization may mutate content after the signature was verified at submit time. The audit log records the submit-time verdict; later reverifiers against stored content will correctly fail. Storing both raw and sanitized content would let readers reverify without audit, but doubles storage. |
| Timing leak on agent-key existence | Low | `GetActiveAgentKey` hits Postgres; lookup hit vs. miss differs in latency. An attacker can probe which agent IDs have a registered key via envelope/per-action rejection timing. Pre-existing for Phase B1 signatures; no regression. |
| Subgraph-scoped `LoadTrustEdges` | Medium | `LoadTrustEdges` still returns the full graph. With prune + cap in place, cardinality is bounded but not small — at >10k edges, per-recompute scan cost becomes significant. Subgraph expansion from the active cohort (BFS out to depth 2–3) bounds the work per recompute without changing the EigenTrust algorithm. |

| Consensus vote provenance | Medium | Link each consensus statement to the specific votes supporting it (pattern #7). |
| Round drift detection | Medium | Alert when positions converge >50% between rounds (amplification signal, pattern #5). |
| Robust aggregation | Medium | Byzantine-tolerant vote aggregation (trimmed-mean, Krum). |
| Rate limiting per agent | Medium | Prevent analyze-spam that burns API credits. |
| Byzantine-tolerant sequence agreement | High | 🟡 Partially Implemented (session 5c of N, single-node routed + cross-boot). `internal/bft/` implements chained-HotStuff core + view change + real BLS12-381 multi-signatures + durable commit log: proposal/vote/QC/two-chain commit; `Timeout()`/`HandleNewView()` drive view change under Byzantine leader failure; `BLSSigner` (session 3) ships real sign/verify/aggregate/verify-aggregate on top of gnark-crypto's BLS12-381 primitives (pure-Go, passes `CGO_ENABLED=0 go build`). Sign = s·H(msg) on G1 with RFC 9380 hash-to-curve; VerifyAggregate sums signer public keys on G2 and does a pairing check. `specs/HotStuff.tla` + `HotStuff.cfg` model-check safety in ~2s at N=4, f=1; stress cfg with view change exercises ~540k states in ~10s. 25 bft tests pass (13 protocol + 7 BLS unit + 5 log/replay), plus 2 Postgres integration tests for the session-4 durable log. Session 4 adds `LogStore` interface (Append/Load/HighestHeight) with `InMemoryLogStore` and `PostgresLogStore` impls; schema v6 `bft_log` table (additive migration, no prod impact); `Replica.SetLog` attaches a log; `commitBlock` writes log-first-then-memory so the in-memory state never advances past the persisted tail; `Replay` reconstructs knownBlocks/committed/view/highQC/lockedQC on a fresh replica. Fork detection: duplicate-height append with different block hash surfaces as `ErrLogForkDetected` at both in-memory and Postgres layers. Session 5c adds BLS keypair persistence: a new `ReplicaKeyStore` interface with in-memory + Postgres implementations backs a schema-v8 `bft_replica_keys` table (replica_id PK, private_key + public_key BYTEA). `BootstrapSingleNode` now takes a key store and calls `LoadOrGenerate(replicaID)` — first boot generates + persists; subsequent boots read the same keypair. `Marshal`/`UnmarshalBLSKeypair` round-trip (priv, pub) bytes with a `priv*g2 == pub` equality check so a tampered stored pair fails loudly at load. Restored `TestBFTEngineResumesAcrossBoot` drives 3 submits, restarts with a fresh engine against the same stores, and extends the chain — which was impossible in session 5b because fresh-per-boot keys broke QC verification across the restart boundary. This is also the prerequisite for client-side QC verification (session 5d+): clients can now rely on a stable replica public key to verify received QCs. Session 5b wires the BFT package into the service layer: `main.go` constructs a single-node `bft.Engine` at startup (N=1, F=0, Postgres-backed log + vote history, fresh BLS keypair per boot), and `SubmitPosition` routes each submission through `engine.Submit` — propose → self-vote → QC formation → two-chain commit on the next submission. The returned `Position` carries the prepared QC as `BFTProof`. Degenerate BFT at N=1 (quorum=1), but exercises the full state machine end to end. New files: `internal/bft/engine.go` (serialized Submit), `internal/bft/bootstrap.go` (Replay + RestoreVoteHistory + view-advance past prior `proposedInView`). Known session-5b limitation: cross-boot Submit fails because BLS keys regenerate per boot — QCs from the prior boot cannot be verified under the new roster. The committed log survives; only extending the chain post-restart is blocked pending key persistence (session 5c). Tests: engine unit tests (2), service-layer integration (`SubmitPosition` returns non-empty `BFTProof`; Bootstrap is idempotent against an existing log). Session 5a added the durable vote-history side-table (`VoteHistoryStore` interface + `InMemoryVoteHistoryStore` + `PostgresVoteHistoryStore`, schema v7 `bft_vote_history` table): `HandleProposal` persists `lastVotedView` and `Propose` persists `proposedInView` BEFORE emitting messages; `RestoreVoteHistory` propagates those counters into a fresh replica on restart. Monotonic UPSERT (`GREATEST`) prevents stale writes from regressing stored values. This closes the session-4 anti-equivocation gap: a crash-restart can no longer resurrect a voting right the replica already used, even under a Byzantine peer racing the restart. 30 bft tests pass (13 protocol + 7 BLS + 5 log/replay + 5 vote-history unit), plus 4 Postgres integration tests (2 log, 2 vote-history). Still deferred: service-layer integration (routing `submit_position`/`vote`/`analyze` through the BFT state machine, session 5b), multi-node Fly deployment with `HTTPTransport` + real wall-clock timeout timer, client-side QC proof verification, replica-key distribution via trusted setup or DKG (current `GenerateBLSKeyset` is test-only), formal `<>[]progress` liveness property in TLA+. The current package is NOT wired into `internal/deliberation/service.go` — production requests still land on the single-server path. See `specs/hotstuff-design.md` for the full design + deferred list. (DARPA-PS-26-09 Track 1) |
| Verifiable tally with vote privacy | High | Helios/ElectionGuard-lineage. Threshold crypto vs. additively homomorphic Pedersen commitments TBD per threat model. (DARPA-PS-26-09 Track 1) |
| TLA+ specification of protocol state machine | — | Target Byzantine-tolerant protocol, symmetric reduction n≤5. LLM treated as external oracle. (DARPA-PS-26-09 Cross-cutting) |

## Key References

- Marchal et al., "Architecting Trust in Artificial Epistemic Agents" (Speaker L, 2026). arXiv:2603.02960
- Multi-author, "Multi-Agent Risks from Advanced AI" (Cooperative AI Foundation, 2025). arXiv:2502.14143
- Schroeder de Witt, "Open Challenges in Multi-Agent Security" (2025). arXiv:2505.02077
- "Cracking the Collective Mind: Adversarial Manipulation in Multi-Agent Systems" (OpenReview, 2024). openreview.net/forum?id=kgZFaAtzYi
- "AgentPoison: Red-teaming LLM Agents via Poisoning Memory" (NeurIPS 2024). arXiv:2407.12784
- "MemoryGraft: Persistent Compromise of LLM Agents" (2024). arXiv:2512.16962
- OWASP Top 10 for Agentic Applications (2026). genai.owasp.org
- "AI can help humans find common ground" (Science, 2024) — Habermas Machine. doi:10.1126/science.adq2852
- "Opportunities and Risks of LLMs for Scalable Deliberation with Polis". arXiv:2306.11932
- Robust aggregation survey. arXiv:2312.14461
