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
