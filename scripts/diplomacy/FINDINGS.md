# AI Diplomacy Experiment Findings

## The Experiment

7 Claude Sonnet 4.6 agents play the board game Diplomacy for 7 years. Each year, gemot analyzes every bilateral negotiation, the global diplomatic table, and any detected alliances. Agents receive cooperation-first intelligence briefings with shared ground, cruxes, and power balance warnings. Control runs use identical prompts and seeds but skip gemot analysis.

## Results

| Run | Control Spread | Gemot Spread | Notes |
|-----|---------------|-------------|-------|
| v5 | 8 (old prompts) | 8 | Sonnet without cooperation framing |
| v6 | 8 (old prompts) | 5 | Cooperation framing, but zero shared ground (no votes) |
| v7 | 8 (old prompts) | 4 | LLM agreement detection + power balance warnings |
| v8 | 8 (old prompts) | 2 | + global assembly, broader engagement |
| v9 | 10 | 5 | Alliance-forming prompts — control gets worse, gemot compensates |
| v10 | 5 | 6 | Alliance-aware prompts — control improves dramatically, gemot eliminates Turkey |

"Spread" = max SCs - min SCs across all 7 powers. Lower is more balanced.

## The v10 Finding

v10 used prompts that teach agents to form alliances, honor support orders, and judge trust by follow-through. These prompts raised the control baseline from spread 8-10 to spread 5 — agents cooperate well without gemot.

With gemot briefings, the top of the distribution flattened beautifully (four powers tied at 6 SCs vs control's leader at 8). But Turkey was eliminated (0 SCs). Spread 6 — worse than the control.

### What happened

Gemot analyzed the Austria-Russia bilateral negotiation and found extensive shared ground around a joint anti-Turkey campaign. The briefing produced:

- An "AVAILABLE COMPROMISE" proposing a formal anti-Turkish alliance with specific territorial divisions (Austria gets Serbia/Greece/Bulgaria, Russia gets Rumania/Black Sea/Constantinople)
- 9 "SHARED GROUND" items reinforcing the coordination, including phased execution plans, mutual red lines, and — notably — "deception of Turkey is sanctioned alliance policy"

Austria-Russia anti-Turkey sentiment existed in the control too (98% of their messages mentioned Turkey). But in the control, they couldn't coordinate effectively — Turkey survived at 4 SCs. Gemot made the coordination actionable enough to execute a complete elimination.

### The mechanism

Gemot's LLM agreement detection identified what Austria and Russia genuinely agreed on. The compromise generator synthesized their positions into a specific, executable plan. The briefing presented this plan as "cooperation-first" — because from Austria and Russia's perspective, it was cooperation. From Turkey's perspective, it was coordinated aggression.

The briefing was doing exactly what it was designed to do: surface agreement and make it actionable. The problem is that "find agreement between A and B" is value-neutral — the agreement can be pro-social or adversarial toward third parties.

## Interpretation

### This is majority tyranny

The oldest problem in democratic governance. A majority (or coalition) reaching consensus on an action that harms the minority. Madison wrote about it in Federalist No. 10. The deliberation theory literature has extensive treatment — Habermas's ideal speech situation assumes all affected parties participate, which Turkey didn't in the Austria-Russia bilateral.

### Gemot amplifies coordination quality, not direction

In v8 and v9 (spread 2 and 5), gemot's briefings helped agents coordinate toward balanced outcomes — the power balance warnings flagged runaway leaders and the shared ground surfaced cooperative opportunities. In v10, the same mechanism surfaced adversarial coordination. The tool is direction-neutral; the game's incentives determine the direction.

### The metric matters

"Spread" conflates two different outcomes:

- **Flattening the top** (reducing dominance): v10 gemot wins decisively — max is 6 vs control's 8
- **Protecting the bottom** (preventing elimination): v10 gemot loses — min is 0 vs control's 3

A Gini coefficient or survival count tells a different story than raw spread.

### The prompts did most of the work in v10

The alliance-aware prompt overhaul (system prompts emphasizing "you need allies," support order tracking through the diary, trust based on order execution) raised the control baseline from spread 8-10 to spread 5. When agents already cooperate well, gemot's marginal contribution becomes smaller and potentially counter-productive — it's optimizing a system that was already near-optimal.

## Possible mitigations

1. **Coalition risk warnings**: When gemot detects that two powers' shared ground is primarily about attacking a third, include a warning in the target power's briefing. "COALITION RISK: Austria and Russia share significant agreement on territorial division that affects your interests."

2. **Affected party inclusion**: Before generating a compromise that involves a third party's territory, check whether that party has been consulted. Flag "this agreement was reached without Turkey's participation" as an integrity warning.

3. **Structural protections**: Gemot already has templates with quorum requirements, cooling periods, and constitutional rules. A "minimum viability" rule could prevent analysis from producing agreements that would eliminate any participant.

4. **Balanced briefing framing**: When the power balance shows one power declining rapidly, weight the briefing toward that power's survival — surface opportunities for the declining power to form counter-alliances.

These are design decisions, not immediate fixes. The current product is correct in its behavior — the question is whether the product should have opinions about what kinds of agreement are desirable.

## Recommendation for demo

Use v9 as the primary demo (control spread 10 → gemot spread 5). It shows the intended use case: cooperation-first framing producing balanced outcomes against a strong adversarial baseline. Everyone survives, the spread halves, and the paired comparison is clean.

v10 is a research finding, not a product demo. It belongs in a blog post or paper about "what happens when structured deliberation meets adversarial incentives" — which is genuinely interesting and publishable.

## Raw data

All experiment results are in `~/Documents/AI_Diplomacy/results/`:
- `control_v9/`, `gemot_v9_sonnet/` — v9 paired comparison (demo)
- `control_v10/`, `gemot_v10_sonnet/` — v10 paired comparison (research)
- `gemot_v8_sonnet/` — best absolute spread (2)
- Earlier runs: `gemot_v{5,6,7}_sonnet/`

Deliberation data is on gemot.dev, tagged by group:
- `gemot-v8-sonnet` (22 deliberations)
- `gemot-v9-sonnet` (22 deliberations)
- `gemot-v10-sonnet` (22 deliberations)
