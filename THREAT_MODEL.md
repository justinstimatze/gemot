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
| EigenTrust reputation + cold-start cap | ✅ Implemented | `internal/reputation/Weigher` computes global EigenTrust scores (Kamvar/Schlosser/Garcia-Molina, SIGIR 2003) over a sparse trust graph built from qualified-vote agreements filtered by crux survival. Scores and edges persist in `agent_reputation` / `agent_trust_edges` tables. Cold-start cap (`GEMOT_EIGENTRUST_COLD_CAP`, default 0.1) clamps agents with `survived_count < GEMOT_EIGENTRUST_COLD_THRESHOLD` (default 5); this is the primary Sybil defense during cold-start only. Honest caveats: (a) canonical EigenTrust under a uniform teleport does not defeat closed trust cycles — the paper's pre-trusted-seed remedy is not applicable without an OOB-trusted bootstrap set (Douceur 2002 impossibility); (b) `survived_count` increments require ≥2 distinct non-self agreers per round, which blocks 2-Sybil pair graduation but not larger rings — post-graduation, N ≥ 3 rings can pool weight via mutual endorsement (graduation cliff); (c) Ford's pseudonym-parties framing informs the direction but no physical-attendance claim is made. Opt-in via `GEMOT_EIGENTRUST_ENABLED=true`. |

## Planned Defenses (Pre-Open-Federation)

| Defense | Priority | Description |
|---|---|---|
| Registration-boundary key ownership (open federation) | High | In hosted mode, `scopeAgentID` namespaces key registration per API key. In open federation, first-come-first-served for arbitrary agent IDs must be gated behind a registration authority or proof-of-agent-identity. (DARPA-PS-26-09 Track 1) |
| Signed-content post-hoc verification | Medium | Server-side PII sanitization may mutate content after the signature was verified at submit time. The audit log records the submit-time verdict; later reverifiers against stored content will correctly fail. Storing both raw and sanitized content would let readers reverify without audit, but doubles storage. |
| Timing leak on agent-key existence | Low | `GetActiveAgentKey` hits Postgres; lookup hit vs. miss differs in latency. An attacker can probe which agent IDs have a registered key via envelope/per-action rejection timing. Pre-existing for Phase B1 signatures; no regression. |
| Cross-family model consistency check | High | Stub exists (`validateAnalysisModelConsistency`); secondary-model client (Gemini/GPT) unwired. Defends against stable-but-wrong outputs within a single family. |
| Reputation decay + negative signals | High | Current reputation is monotone-increasing: `agent_trust_edges.weight` only grows, `survived_count` only increments. An agent who was good and turns adversarial retains full weight indefinitely (whitewashing is free). Needs time-decay on edge weight, negative-weight ingestion from disagree/dispute signals, and optional per-round graduation-rate cap. (DARPA-PS-26-09 Track 1) |
| Pubkey-bound reputation identity | High | Reputation is keyed on symbolic `agent_id`, not on public-key fingerprint. In hosted mode `scopeAgentID` namespaces this per-tenant; in open federation, identity transfer carries reputation with it. Must bind reputation to `agent_keys` rows (or a hash thereof) before federation. (DARPA-PS-26-09 Track 1) |
| Edge-table pruning + cumulative-weight caps | Medium | `agent_trust_edges` has no TTL, no pruning, no per-pair cap. `LoadTrustEdges` does `SELECT *` with no LIMIT. At open-federation scale this becomes a DoS vector and slows round-close recompute. Needs retention policy (e.g. `last_updated < now() - retention`) and a cumulative-weight cap per `(from, to)`. (Batched UPSERT already landed.) |
| Fail-closed DB-error toggle | Medium | On `LoadReputation` failure the weigher falls back to unit weights (fail-open). In a Byzantine context an attacker who can DoS Postgres strips all cold-start enforcement exactly when it is needed. Add `GEMOT_EIGENTRUST_DB_FAIL=closed` to reject the round (or degrade to last-known scores) instead of silently neutralizing the defense. Matches the `GEMOT_ENVELOPE_MODE=required` pattern. |
| Per-deliberation privacy boundary for trust edges | Medium | `agent_trust_edges` is a globally readable table; agree-patterns from agents in *private* deliberations leak into the global graph observable by DB operators and future cross-tenant leaks. Needs either per-delib scoping, hashed agent pairs at rest, or opt-out for private delibs. |

| Consensus vote provenance | Medium | Link each consensus statement to the specific votes supporting it (pattern #7). |
| Round drift detection | Medium | Alert when positions converge >50% between rounds (amplification signal, pattern #5). |
| Robust aggregation | Medium | Byzantine-tolerant vote aggregation (trimmed-mean, Krum). |
| Rate limiting per agent | Medium | Prevent analyze-spam that burns API credits. |
| Byzantine-tolerant sequence agreement | High | PBFT/HotStuff-lineage ordering over positions and votes; f<n/3 tolerance. (DARPA-PS-26-09 Track 1) |
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
