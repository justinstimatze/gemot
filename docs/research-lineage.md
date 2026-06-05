# Research Lineage

MCP gives agents hands. A2A gives agents a network. Gemot gives agents a forum.

## The Semantic Web dream (2001)

Berners-Lee, Hendler, and Lassila's 2001 *Scientific American* article described agents negotiating doctor's appointments, cross-referencing insurance, and scheduling logistics — all on behalf of their humans. The vision assumed shared ontologies (RDF, OWL) would make mutual understanding automatic, and negotiation would somehow emerge from shared data formats.

What they didn't specify: any concrete protocol for what happens when agents disagree, have different values, or need to reach collective decisions. The deliberation primitive was missing.

## FIPA: the first agent protocols (1997–2005)

The Foundation for Intelligent Physical Agents standardized agent communication with FIPA-ACL (based on speech act theory) and interaction protocols like the Contract Net.

**Contract Net flow:**
1. Initiator sends CFP (call for proposals) → `deliberation action:create`
2. Participants respond with propose → `participate action:submit_position`
3. Initiator evaluates proposals → `analyze action:run` (but with LLM crux detection, not just bid comparison)
4. Accept/reject → `participate action:get_context` + `decide action:commit`
5. Iterated Contract Net (re-propose) → multi-round `participate action:get_context` → revised `participate action:submit_position`

The critical difference: FIPA Contract Net was task-allocation (one winner takes the job). Gemot is deliberation (finding shared understanding, surfacing *why* agents disagree). FIPA had no mechanism for crux detection.

## Argumentation theory: formalizing disagreement

### Dung's abstract argumentation (1995)

Arguments as abstract nodes, attacks as directed edges. An *extension* is a conflict-free set that defends itself. This formalized the structure of disagreement — which arguments defeat which.

Gemot's crux detection does something analogous via LLM understanding rather than formal attack relations. A potential enhancement: represent crux output as argumentation graphs with extension semantics.

### Bench-Capon's value-based argumentation (2003)

Extends Dung: each argument promotes a *value*, and attack success depends on the audience's value ordering. Different agents with different value orderings accept different conclusions from the same arguments.

This is deeply relevant. When gemot's analysis finds disagreement, it often stems from different *value priorities*, not different facts. The Polis-inspired vote clustering already captures value alignment implicitly — agents who vote similarly likely share value orderings.

### Walton & Krabbe's dialogue types (1995)

Six types: persuasion, negotiation, deliberation, information-seeking, inquiry, eristic. Gemot primarily supports *deliberation* (agents collectively deciding on action) with elements of *inquiry* (collaborative investigation).

The framework suggests gemot could support explicit dialogue type transitions — a deliberation that surfaces a factual dispute could spawn an inquiry sub-dialogue.

## What went wrong (2001–2010)

The Semantic Web agent vision stalled for reinforcing reasons:

- **Ontology bottleneck**: Creating and maintaining shared ontologies was prohibitively expensive. No individual site had incentive to add semantic markup for external agents' benefit.
- **Complexity barrier**: OWL required formal logic training. Most developers couldn't or wouldn't learn it.
- **No natural language understanding**: The architecture assumed meaning must be *explicitly encoded*. Without NLU, agents were brittle — they could only interoperate over pre-agreed ontologies.
- **Closed agent ecosystems**: Platforms like JADE were powerful but heavyweight. FIPA compliance was expensive.
- **The intelligence was in the wrong place**: Encoded in external data (ontologies) or the platform (FIPA/JADE), rather than in the agent itself.

## The resurrection (2024–2026)

LLMs dramatically reduce the ontology bottleneck. Agents can now understand natural language positions, detect implicit disagreements, and bridge vocabularies without pre-agreed schemas.

| Then (2001–2006) | Now (2024–2026) |
|---|---|
| Ontologies + RDF for shared meaning | Natural language + LLM understanding |
| Formal logic for reasoning | LLM inference (probabilistic) |
| Hand-coded agent behaviors (BDI) | LLM agents with tool use |
| FIPA-ACL message passing | MCP (tool access) + A2A (agent-to-agent) |
| OWL-S for service description | MCP tool schemas (JSON Schema + NL descriptions) |
| JADE platform (heavyweight, Java) | Lightweight protocol servers (MCP over stdio/HTTP) |
| Ontology consensus required | LLMs bridge vocabularies automatically |
| Closed ecosystems | Open protocols (MCP, A2A) |

## Where gemot fits

The FIPA stack had communication (ACL) and interaction protocols (Contract Net) but no deliberation protocol. The modern stack has communication (A2A) and tool access (MCP) but no deliberation protocol. Gemot fills the same gap in both eras.

What gemot does that the Semantic Web era couldn't:
- **Natural language crux detection**: The T3C pipeline uses LLM understanding to find where positions actually conflict. FIPA agents could exchange formal propositions but couldn't detect implicit disagreements.
- **Zero-ontology interoperability**: Agents don't need shared vocabularies. The LLM bridges semantic gaps automatically.
- **Statistical consensus via Polis vote math**: PCA + K-means identifies opinion clusters without requiring agents to self-organize.
- **Personalized context**: `participate action:get_context` tells each agent where it sits in the opinion landscape, what its cluster thinks, and what the key cruxes are.

What they had that gemot could learn from:
- **Formal argumentation structure**: Attack graphs with extension semantics could formalize crux output.
- **Dialogue game protocols**: Explicit rules for what moves are legal when — commitment stores, challenge protocols, burden of proof.
- **Value-based reasoning**: Distinguishing factual cruxes from value cruxes.
- **Dialogue type transitions**: Deliberation → inquiry → back to deliberation.

## Bibliography

### Foundational

1. Berners-Lee, T., Hendler, J., & Lassila, O. (2001). "The Semantic Web." *Scientific American*, 284(5), 34-43.
2. Wooldridge, M. & Jennings, N.R. (1995). "Intelligent Agents: Theory and Practice." *Knowledge Engineering Review*, 10(2), 115-152.
3. Jennings, N.R. (1999). "Agent-Based Computing: Promise and Perils." *Proc. IJCAI-99*.
4. Dung, P.M. (1995). "On the Acceptability of Arguments." *Artificial Intelligence*, 77(2), 321-357.
5. Bench-Capon, T.J.M. (2003). "Persuasion in Practical Argument Using Value-Based Argumentation Frameworks." *Journal of Logic and Computation*, 13(3).
6. Walton, D.N. & Krabbe, E.C.W. (1995). *Commitment in Dialogue.* SUNY Press.
7. Atkinson, K. & Bench-Capon, T. (2007). "Argumentation in the Framework of Deliberation Dialogue."
8. FIPA specifications (1997-2002): FIPA-ACL (SC00061), Contract Net (SC00029), Iterated Contract Net (SC00030).
9. OWL-S 1.0 (2004). W3C Member Submission.

### Deliberation platforms

10. Small, C. et al. (2021). "Polis: Scaling deliberation by mapping high dimensional opinion spaces." *RECERCA: Revista de Pensament i Analisi*, 26(2).
11. Tessler, M.H. et al. (2024). "AI Can Help Humans Find Common Ground in Democratic Deliberation." *Science*.
12. Fish, S. et al. (2023). "Generative Social Choice." *arXiv:2309.01291*.
13. Weyl, E.G. & Tang, A. (2024). *Plurality: The Future of Collaborative Technology and Democracy.*
14. Li, M., Li, X., & Zhou, T. (2026). "Does Socialization Emerge in AI Agent Society? A Case Study of Moltbook." *arXiv:2602.14299*.

### Modern agent coordination

15. Botti, V. (2025). "Agentic AI and Multiagentic: Are We Reinventing the Wheel?" *arXiv:2506.01463*.
16. "From Semantic Web and MAS to Agentic AI: A Unified Narrative of the Web of Agents." (2025). *arXiv:2507.10644*.
17. "A Survey of Agent Interoperability Protocols: MCP, ACP, A2A, and ANP." (2025). *arXiv:2505.02279*.

## Multi-agent debate 2025/26

The 2024–2026 literature on multi-agent LLM debate moved from "does it help" to "when does it actively hurt." The findings map onto gemot's existing primitives unusually cleanly, which is the point of this section: future contributors thinking about adding debate mechanisms to the calibration runner or the deliberation service should read this first to avoid rebuilding what `internal/deliberation/service.go` already has.

### The dominant failure modes (verified in 2025 literature)

- **Sycophantic / conformist drift.** Agents updating on peers tend to adopt majority positions even when the minority is correct. Disagreement rate decreases over rounds and correlates with performance degradation. ([Peacemaker or Troublemaker](https://arxiv.org/abs/2509.23055), Sept 2025; [Talk Isn't Always Cheap](https://arxiv.org/abs/2509.05396), Sept 2025; [Can LLM Agents Really Debate?](https://arxiv.org/abs/2511.07784), Nov 2025.)
- **Identity bias.** Agents are prone to self-bias (stubbornly adhering to own prior output) and peer-identity sycophancy (adopting a peer's view because of *who* said it, not the argument). Sycophancy is far more prevalent than self-bias. Anonymization mitigates. ([When Identity Skews Debate](https://arxiv.org/abs/2510.07517), Oct 2025.)
- **Confidence-weighted persuasion as a measurement of being misled.** The CW-POR (Confidence-Weighted Persuasion Override Rate) metric captures both how often a judge is deceived by peer reasoning AND how strongly it believes the incorrect choice. Smaller LLMs can advocate confidently for false claims, eliciting high-confidence errors from a judge. The paper presents CW-POR as a calibration/severity *metric*, not a design recommendation against confidence weighting per se. ([When Persuasion Overrides Truth](https://arxiv.org/abs/2504.00374), Apr 2025.)
- **Structural parameters dominate less than expected.** Controlled study finds that *intrinsic reasoning strength* and *group diversity* are the dominant drivers of debate success, while structural parameters like order or confidence visibility offer limited gains. Majority pressure suppresses independent correction. ([Can LLM Agents Really Debate?](https://arxiv.org/abs/2511.07784), Nov 2025.)
- **Confidence communication can help — when calibrated.** Separately, vanilla MAD lacks *explicit, calibrated confidence communication* and *diversity of initial viewpoints*; adding both can systematically improve outcomes. This is in tension with the "limited gains from confidence visibility" finding above — the resolution appears to be that *calibrated* confidence helps, raw or uncalibrated confidence visibility doesn't. ([Demystifying Multi-Agent Debate](https://arxiv.org/abs/2601.19921), Jan 2026.)
- **Cost-side waste.** Unnecessary debate cascades error and burns tokens. Adaptive stopping (debate-only-when-necessary) saves ~6× compute while preserving accuracy. ([Debate Only When Necessary](https://arxiv.org/abs/2504.05047), Apr 2025.)
- **Empirical benchmark of debate strategies.** Hyperparameter-tuned MAD can outperform alternatives; vanilla MAD often underperforms, suggesting the approach is sensitive to optimization. ([Should we be going MAD?](https://arxiv.org/abs/2311.17371), 2023.)

### How gemot's primitives map onto the SOTA prescription

| Literature finding (2025/26) | Gemot primitive | Status |
|---|---|---|
| Anti-sycophancy: spotlight minority cruxes | `buildDiversityNudge` (service.go:1639–1677), FREE-MAD pattern, included in `AgentContext` via `GetContext` | Built, used by `participate action:get_context`, **bypassed by calibration runner** |
| Bridging / minority amplification | `BridgingStatements` extracted in `text.go:1063–1091`, surfaced by `buildStrategicNudge` (service.go:1771–1810) | Built, used by `GetContext`, **bypassed by calibration runner** |
| Anonymization for identity bias | `agent_id` is first-class throughout positions/nudges; not stripped | **Not built**; needs anonymized rendering path for revision context |
| Adaptive stopping | `AdvanceRound` exists (storage layer); no convergence-based stop verdict | **Not built**; integrity warnings (`CRUX_INSTABILITY`, `ARTIFICIAL_CONSENSUS`) flag but don't stop |
| Group-diversity dominance | `ConsistencyModel` cross-family OOD check (`llm/secondary.go`); single-family primary fleet | Partial — diversity is a verification check, not a primary fleet composition |
| Aggregation that isn't trivially gameable by confident-but-wrong agents | Vote tally is +2 (self/match) / -1 (mismatch); no raw confidence weighting at the aggregation layer (CW-POR shows persuasive-but-wrong agents can exploit raw-confidence weighting). Calibrated confidence *communication* (per Demystifying MAD) is a separately interesting gap — agents don't currently express explicit confidence in their rationales. | Aggregation: correctly absent. Confidence communication: open question. |
| Reputation / agent weighting | EigenTrust with cold-start cap (`internal/analysis/eigentrust.go`); enabled by default in deliberation service | Built, **not wired into calibration runner** |
| Integrity warnings for artificial consensus | `ARTIFICIAL_CONSENSUS`, `CROSS_FAMILY_DRIFT`, `AGGREGATION_DRIFT`, `CRUX_INSTABILITY` (`internal/analysis/integrity.go`) | Built; surfaced via analysis pipeline, not used as a stopping condition |

The pattern: gemot's original design anticipated most of the 2025/26 failure modes and built primitives to defend against them. The calibration runner has been a stripped-down simulation that orchestrates the deliberation service's outer surface (`CreateDeliberation → SubmitPosition → Vote → Analyze → ProposeCompromise`) but bypasses the inner anti-conformity / bridging / reputation primitives. Restoration of those primitives in the calibration runner — not invention of new ones — is the recommended next step.

### What's genuinely new (post-restoration, if measurements still flatline)

After GetContext-grounded revision is wired in, the remaining gaps relative to 2025/26 SOTA are:

1. **Agent anonymization** in revision context (literature-grounded; modest code add).
2. **Convergence-based adaptive stopping** (literature-grounded; modest code add).
3. **Heterogeneous-model fleet as primary mode**, not just verification (literature finding: diversity dominates; bigger architectural change to the calibration runner).

Role-based agents (proponent/skeptic/judge) and more rounds-by-default are NOT in the post-restoration plan — literature finds the former encodes the conclusion in the role assignment, and the latter shows diminishing or negative returns. Raw confidence-weighted aggregation is not recommended (CW-POR shows it as gameable), but *calibrated confidence communication* in rationales (per Demystifying MAD) is an open question worth investigating separately.
