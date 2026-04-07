# Gemot x Talk to the City: Deliberation on T3C Reports

**Research prototype.** This is an experiment in turning static discourse analysis into live deliberation. Structural mode (topology-derived agents) is bleeding-edge — grounded in recent research but not validated at scale.

## What This Is

[Talk to the City](https://github.com/AIObjectives/talktothe.city) takes raw text (survey responses, interviews, forum posts) and produces structured reports: claims, cruxes, speaker-crux matrices, bridging scores. The report tells you *what* people think and *where* they disagree.

Gemot takes T3C reports and turns them into live deliberations. The cruxes become deliberation topics. The speakers (or the discourse topology itself) become agents. Voting, compromise proposals, commitment tracking, and multi-round convergence happen on top of what T3C extracted.

T3C tells you what the discourse looks like. Gemot lets you do something about it.

**Important:** All agents created by the importer are AI-synthesized from T3C's extracted claims — they are not real participants. Every deliberation created is tagged with provenance in its topic. Structural agents (steelmen, adversaries, dissent, bridge) self-identify by role in their position text.

## Try It

Download a T3C report JSON (the download button on any report page), then:

```bash
# Speaker mode: one deliberation per controversial crux
go run scripts/t3c-import/ report.json

# Structural mode: topology-derived agents in one arena
go run scripts/t3c-import/ report.json --mode structural

# Dry run (see what would be created without connecting)
go run scripts/t3c-import/ report.json --mode structural --dry-run
```

Requires a running gemot instance (local or hosted). Set `GEMOT_URL` and `GEMOT_API_SECRET` or use `--url`.

## Two Modes

### Speaker mode (default)

For each crux above the controversy threshold:

1. Creates a separate gemot deliberation
2. Each speaker with a stance becomes an agent
3. Their relevant claims become their position
4. Votes seeded from the speaker-crux matrix
5. Template auto-selects by controversy score

For a Diplomacy report with 7 speakers and 7 cruxes, this produces 2 deliberations (the 2 cruxes above 30% controversy), 4-6 speakers each. Useful, but basically replaying T3C's analysis through gemot's framework.

### Structural mode

The novel part. Instead of mapping speakers to agents, maps the *topology* of the discourse to agents.

**Step 1: Derive clusters.** T3C doesn't output clusters. We compute them by grouping speakers with >= 70% voting pattern similarity across all cruxes in the speaker-crux matrix.

**Step 2: Create structural agents.** Five types, each with a different deliberative role:

| Agent Type | Count | Role |
|---|---|---|
| Cluster steelman | 1 per cluster | Synthesize the strongest version of the cluster's collective position, grounded in actual claims and vote data |
| Crux adversary | 1 per controversial crux | Probe deeper than the stated crux — find the underlying disagreement that produces the surface split |
| Dissent | Up to 2 | Challenge consensus claims where everyone agrees and no one disagrees |
| Bridge | 1 (if >= 2 clusters) | Build outward from cross-cluster shared claims toward positions both sides can endorse |

**Step 3: One deliberation.** All structural agents participate in a single arena. Template chosen by max controversy.

**Step 4: Seed votes.** Cluster agents vote based on pattern similarity — high similarity clusters agree, low similarity disagree.

**Step 5: Run analysis.** Gemot finds new cruxes that weren't in the T3C report — disagreements that emerged from structural agents interacting. Then you run round 2, agents update positions, the discourse evolves.

## What a Structural Import Looks Like

From a Diplomacy test game T3C report (7 speakers, 7 cruxes, 30 claims):

```
Structural agents:

  cluster-0-austria              Steelman for Austria/England/Germany
  cluster-1-france               Steelman for France/Italy
  cluster-2-russia               Steelman for Russia
  cluster-3-turkey               Steelman for Turkey
  crux-0                         Adversary: Mediterranean cooperation
  crux-1                         Adversary: Non-aggression pacts as expansion tools
  dissent-0                      Dissent: Balkans cooperation consensus
  dissent-1                      Dissent: Black Sea spheres of influence
  bridge                         Cross-cluster common ground

  1 deliberation, 9 agents, jury template
```

Clusters derived automatically from the vote matrix: Austria/England/Germany split from France/Italy on whether non-aggression pacts are tools for expansion. Russia stands alone on Mediterranean cooperation. Turkey agrees with everyone — natural bridge.

## Why Not Just Read the T3C Report

A T3C report is a snapshot. It tells you the discourse topology at one point in time. What it doesn't have:

- **Explicit voting** — the matrix is extracted from text, not from deliberate choices
- **Compromise proposals** — optimized for cross-cluster endorsement
- **Commitment tracking** — "we agreed to X, did we do it?"
- **Resolution detection** — "this crux is resolved now"
- **Multi-round convergence** — structured iteration toward decisions
- **Structural challenge** — dissent agents testing whether consensus is genuine

The importer makes the T3C report the *initial conditions* for a living process, not the final output.

## Template Selection

Template auto-selects based on max controversy across imported cruxes:

| Max Controversy | Template | Why |
|---|---|---|
| >= 70% | `negotiation` | Deep disagreement, need ZOPA detection |
| >= 40% | `jury` | Moderate disagreement, near-unanimous resolution |
| >= 20% | `assembly` | Mild disagreement, democratic |
| < 20% | `consensus` | Near-agreement, find unanimity |

Override with `--template <name>`.

## The T3C Report Format

The importer reads T3C's `PipelineOutput` JSON (the file you get from the download button). Key structures used:

| T3C Field | What We Use It For |
|---|---|
| `data[1].topics[].subtopics[].claims[]` | Claim text for agent positions |
| `data[1].sources[]` | Speaker identity, linking claims to speakers |
| `data[1].addOns.subtopicCruxes[]` | Crux claims, controversy scores, agree/disagree lists |
| `data[1].addOns.speakerCruxMatrix` | Cluster derivation, vote seeding |
| `claims[].quotes[].reference.sourceId` | Attributing claims to speakers |

Note: `data` and `metadata` are tuples (`["v0.2", {...}]`), not plain objects.

## Research Basis

The structural mode draws on recent work in computational argumentation and deliberative AI:

- **Steelman agents** avoid persona drift (Batzner et al. AIES 2025 found LLM personas systematically drift toward training biases). Grounding in actual vote data and extracted claims keeps agents honest.
- **Crux adversaries** are validated by CQs-Gen (ArgMining @ ACL 2025): multi-agent critical question generation through debate produces better crux identification than single-pass extraction.
- **Dissent agents** follow Prakash 2025 (DCI): uncontested consensus is less trustworthy than consensus that survived structured challenge.
- **Bridge agents** implement the bridging approach from Tessler et al. (Science 2024): AI mediators generating common-ground statements left groups less divided than human mediators across 5,734 participants.
- **Cluster derivation from vote matrices** is standard Polis methodology, adapted for T3C's speaker-crux matrix format.

## Flags

| Flag | Default | Description |
|---|---|---|
| `--mode` | `speaker` | `speaker` (one deliberation per crux) or `structural` (topology-derived agents) |
| `--template` | `auto` | Governance template: auto, negotiation, jury, assembly, consensus, parliament |
| `--threshold` | `0.3` | Minimum controversy score to import a crux |
| `--all-cruxes` | `false` | Import all cruxes regardless of controversy |
| `--dry-run` | `false` | Print plan without connecting to gemot |
| `--url` | `$GEMOT_URL` | Gemot MCP URL (default: http://localhost:8080/mcp) |
| `--group` | `t3c-import` | Group ID for organizing deliberations |

## Known Limitations

- **Cluster derivation is a simple heuristic** — 70% pattern similarity on the speaker-crux matrix. Works for small datasets, untested on large ones.
- **Coupled to T3C's undocumented schema** — tuple-indexed `data[1]`, matrix labels as `"Topic -> Subtopic"`, claim attribution via `sourceId`. Could break if T3C changes their output format.
- **Only tested on small reports** — 7 speakers, 30 claims, 7 cruxes. Behavior on 500+ speaker reports is unknown.
- **No empty chair agents yet** — T3C's audit log could distinguish "absent perspective" from "excluded for toxicity" to spawn agents for missing viewpoints. Not implemented.

## Expert Panel Results

We ran a [gemot expert panel](https://gemot.dev) on this proposal. Five experts (feasibility, customer advocate, staff engineer, business analyst, devil's advocate) pressure-tested it. Key findings:

- **The research grounding is credible** — nobody disputed the theoretical basis for structural agents.
- **Ethics matter** — AI-synthesized agents must self-identify. Provenance on every deliberation is non-negotiable. Done: topic tags, agent ID prefixes, role labels in position text.
- **Schema fragility is the main technical risk** — T3C's output format is undocumented and could change.
- **Demand is not the point** — this is a research prototype exploring what happens when you animate discourse topology. It's cool. We'll find out if it's useful by trying it.
