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
| v13 | 9 | 7 | First clean Sonnet 4.6 paired comparison; 7/7 vs 6/7 survival |
| v14 | (v13 control) | 7 | Per-season analysis; Gini plateau at 0.09 for 8 cycles |

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

## Retroactive Gini Analysis

Following the expert panel's recommendation (consensus: "spread metric is insufficient"), we retroactively computed Gini coefficient and survival count for all runs.

### Paired comparisons (the experiments that matter)

| Metric | v9 Control | v9 Gemot | v10 Control | v10 Gemot | v13 Control | v13 Gemot | v14 Gemot |
|--------|-----------|---------|------------|----------|------------|----------|----------|
| Spread | 10 | 5 | 5 | 6 | 9 | 7 | 7 |
| Gini | 0.420 | 0.227 | 0.185 | 0.176 | 0.345 | 0.252 | 0.244 |
| Survival | 6/7 | 7/7 | 7/7 | 6/7 | 6/7 | 7/7 | 7/7 |

**v9**: Gemot wins decisively on all three metrics. Gini halved (0.42 → 0.23), all powers survive.

**v10**: Mixed. Gini is essentially equal (0.185 vs 0.176 — within noise). But gemot loses a power (Turkey eliminated). The spread metric made this look like a gemot loss; Gini shows the top of the distribution is actually comparable.

**v13**: Gemot improves on all three metrics, but the gap is smaller than v9. The v13 control was already worse than v10 control (prompt regression? different game seed?), making the comparison less clean.

### Best absolute results

| Run | Spread | Gini | Survival | Notes |
|-----|--------|------|----------|-------|
| gemot_v13_sonnet | 1 | 0.048 | 7/7 | Best ever (non-live) |
| gemot_v8_sonnet | 2 | 0.067 | 7/7 | Best w/ old prompts |
| t3c_v2 | 2 | 0.083 | 7/7 | T3C baseline |
| gemot_v4b | 3 | 0.098 | 7/7 | Early gemot |
| gemot_v14_seasonal | 7 | 0.244 | 7/7 | Per-season, live game |
| gemot_v13_live | 7 | 0.252 | 7/7 | Per-year, live game |

### Interpretation

Gini tells a more nuanced story than spread:
- v10 gemot is not actually worse than control — the Gini values are nearly identical. The spread penalty came entirely from Turkey's elimination (0 SCs), which inflates spread but barely affects Gini since the remaining 6 powers were well-balanced.
- The survival metric captures what Gini misses: whether any power is completely destroyed. The combination of Gini + survival is more informative than spread alone.
- Across all paired comparisons, gemot consistently improves Gini. The survival impact is +1, +1, -1 — net positive but not conclusive at N=1.

## The v13 Finding

v13 was the first clean paired comparison: same model (Claude Sonnet 4.6), same game runner, same negotiation rounds. The only variable was gemot briefings injected between years.

| Metric | Treatment | Control |
|--------|-----------|---------|
| Survival | 7/7 | 6/7 (Germany eliminated Y6) |
| Gini | 0.25 | 0.34 |
| Spread | 7 | 9 |
| Leader | England: 9 | England: 9 |

### What happened

Treatment starts *more* unequal than control in years 1-2 (Gini 0.11 vs 0.06), then rebalances sharply at year 3-4. The briefings appear to encourage aggressive early positioning, but deception detection and coalition intelligence then promote rebalancing — weaker powers gain the information they need to coordinate responses against emerging leaders.

In the control, France surged to 8 SCs at year 4 and Germany was eliminated by year 6. Without intelligence briefings, weaker powers couldn't organize a counter-coalition in time.

### The mechanism

Unlike v10 (where gemot amplified adversarial coordination), v13's cooperation-first framing worked as intended. The briefings flagged power imbalances and surfaced opportunities for defensive alliances. Agents explicitly cited briefing intelligence in their negotiations — 45+ references to briefing content across all negotiations.

## The v14 Finding

v14 tested per-season analysis (14 briefing cycles) against v13's per-year analysis (7 cycles) and the v13 control.

| Metric | Control | V13/year | V14/season |
|--------|---------|----------|------------|
| Survival | 6/7 | 7/7 | 7/7 |
| Gini | 0.34 | 0.25 | 0.24 |
| Spread | 9 | 7 | 7 |
| Leader | England: 9 | England: 9 | England: 9 |
| Weakest | Germany: 0 | Russia: 2 | Germany: 2 |
| Year 4 Gini | 0.29 | 0.13 | 0.09 |

### The plateau-then-break pattern

Per-season analysis held Gini at 0.09 for 8 consecutive analysis cycles (steps 4-11, covering F1902 through S1906). Then England broke away in a single step — Gini jumped from 0.09 to 0.18 at F1906, reaching 0.24 by game end.

This suggests the briefings create a coordination equilibrium: powers balance against each other based on shared intelligence, but the equilibrium is fragile. When one power finds a move the others can't counter, the system breaks.

### Per-season delays divergence but doesn't prevent it

Control diverges at year 3 (Gini > 0.15). Per-year diverges at year 5. Per-season holds until year 6. More frequent analysis buys time but doesn't change the endgame — all three conditions end with England dominant at 9 SCs.

### England always wins

England finished with exactly 9 SCs in all three conditions (control, v13, v14). This is either a structural advantage (island position, naval dominance, defensible home centers) or a Claude Sonnet behavioral bias (model-specific England strategy). N=1 per condition cannot distinguish between these hypotheses.

## Expert Panel Critique (v14)

An adversarial expert panel (5 experts, gemot deliberation afd13913) reviewed the v14 results and identified fatal experimental design flaws:

1. **N=1 per condition.** No statistical significance is possible. England's dominance could be entirely random.
2. **Confounded variables.** V14 bundles per-season frequency AND incremental analysis. Cannot attribute effects to either individually.
3. **Same random seed.** All three runs may share initialization biases that favor particular outcomes.
4. **England structural bias.** The consistent 9-SC result across all conditions suggests a confound that the experimental design cannot control for.

### Recommended next steps (from expert panel consensus)

- 5-10 replications per condition with varied random seeds
- 2×2 factorial design: frequency (per-year vs per-season) × analysis type (full vs incremental)
- Power randomization or positional controls to isolate structural advantages
- Alternative metrics: Gini trajectory slope, time-to-divergence, coalition stability index

These replications are parked for cost reasons but remain the highest priority for publishable results.

## v15 Experimental Design

The shell script now supports `--seed` and `--no-incremental` flags, enabling a 2×2 factorial design that isolates the two confounded variables from v14.

### 2×2 factorial

| Condition | Frequency | Incremental | Seed | Command |
|-----------|-----------|-------------|------|---------|
| A | per-season | yes | 2027 | `--per-season --seed 2027` |
| B | per-season | no | 2027 | `--per-season --seed 2027 --no-incremental` |
| C | per-year | yes | 2027 | `--seed 2027` |
| D | per-year | no | 2027 | `--seed 2027 --no-incremental` |
| Control | — | — | 2027 | `--no-gemot --seed 2027` |

Comparing A vs B isolates incremental analysis. Comparing A vs C isolates frequency. All use seed 2027 to break the seed=2026 confound from v9-v14.

### Additional features being tested

v15 is also the first run with commitment accountability, trust tracking, elimination warnings, coalition risk warnings, and affected party warnings. These are bundled (not factored) — isolating each would require too many conditions.

## Shipped but untested: commitment accountability

As of v14, the diplomacy pipeline includes:
- **Trust tracking**: Cross-references support promises in negotiations against actual orders
- **Commitment accountability**: Submits compromise proposals as conditional commitments, audits fulfillment/breakage via gemot's commitment system, includes reputation scores in briefings
- **Elimination warnings**: Flags powers at 0-1 SCs and proposals that would eliminate a third party
- **Coalition risk warnings**: Alerts target powers when bilateral agreements reference their territory
- **Affected party warnings**: Notes when a bilateral compromise impacts powers not in the negotiation

These features were shipped after v14 completed and have not yet been tested in a live experiment. The next run (v15) will be the first to validate commitment accountability end-to-end.

## Raw data

All experiment results are in `~/Documents/AI_Diplomacy/results/`:
- `control_v9/`, `gemot_v9_sonnet/` — v9 paired comparison (demo)
- `control_v10/`, `gemot_v10_sonnet/` — v10 paired comparison (research)
- `control_v13_live/`, `gemot_v13_live/` — v13 live paired comparison (demo)
- `gemot_v14_seasonal/` — v14 per-season experiment (Gini 0.244, 7/7 survival, plateau-then-break)
- `gemot_v8_sonnet/` — best absolute spread (2)
- `gemot_v13_sonnet/` — best absolute Gini (0.048)
- Earlier runs: `gemot_v{5,6,7}_sonnet/`

Deliberation data is on gemot.dev, tagged by group:
- `gemot-v8-sonnet` (22 deliberations)
- `gemot-v9-sonnet` (22 deliberations)
- `gemot-v10-sonnet` (22 deliberations)
- `gemot_v13_live` (22 deliberations)
- `gemot_v14_seasonal` (22 deliberations per season)
