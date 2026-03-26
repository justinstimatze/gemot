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
| SQLite single-conn + busy timeout | ✅ Implemented | `internal/store/store.go` |

## Planned Defenses (Pre-Open-Federation)

| Defense | Priority | Description |
|---|---|---|
| Coverage validation | High | After claim extraction, verify every agent has ≥1 claim. Flag silent erasure. |
| Crux agent validation | High | After crux detection, verify every listed agent actually submitted a position. |
| Analysis provenance | High | Link cruxes to source positions; link consensus to source votes. |
| Per-agent authentication | Critical | Required before opening to untrusted agents. Agent registration step. |
| Vote similarity detection | Medium | Flag suspiciously correlated voting patterns (Sybil signal). |
| Round drift detection | Medium | Alert when positions converge >50% between rounds (amplification signal). |
| Robust aggregation | Medium | Byzantine-tolerant vote aggregation (trimmed-mean, Krum). |
| LLM output validation | Medium | Structured checks on analysis output (e.g., no hallucinated agent IDs). |
| Rate limiting per agent | Medium | Prevent analyze-spam that burns API credits. |

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
