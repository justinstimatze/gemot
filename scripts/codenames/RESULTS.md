# Codenames results

Judgment-aggregation testbed for gemot: a spymaster gives a one-word clue; the
guessing **fleet** must infer which board words are its team's. Ground truth (the
key) scores guesses, but the clue→word inference is genuine judgment with **no
oracle** — the regime where gemot's thesis should pay off and where
chess/scheduling (computable answers) could not.

Substrate aligned to "Codenames as a Benchmark for Large Language Models"
(arXiv:2412.11373); official CGE board word pool vendored from
`github.com/stepmat/Codenames_GPT@ToG_2025`. Single-turn, paired: **all
aggregation arms consume the same guesser judgments**, so the only varying factor
is how those judgments are combined.

## Arms

- `solo:{literal,lateral,cautious}` — one guesser, one reading temperament.
- `majority-vote` — wisdom of crowds **without** deliberation: rank words by
  cross-guesser agreement (≥⌈N/2⌉), then mean rank.
- `gemot-structured` — the same guesses submitted to a live gemot deliberation
  (positions → votes → analyze → propose_compromise), synthesis parsed back into
  an ordered guess list.
- `oracle:intent` — the spymaster's actual intended words. Ceiling, not a player.

Metric: **mean-score** = team words secured before the turn ends (a civilian or
opponent word ends the turn keeping points so far; the assassin ends it at 0).
**intent** = fraction of the spymaster's intended words recovered.

## n=15, seed 2026 (haiku/sonnet/opus fleet, sonnet codemaster, temp 0.9)

| arm | mean-score | assassin% | intent |
|---|---|---|---|
| oracle:intent | 3.27 | 0% | 1.00 |
| **gemot-structured** | **1.67** | **0%** | **0.58** |
| solo:cautious | 1.47 | 0% | 0.46 |
| solo:lateral | 1.40 | 0% | 0.48 |
| majority-vote | 1.33 | 0% | 0.48 |
| solo:literal | 1.13 | 0% | 0.46 |

**Finding.** Structured deliberation led every non-oracle arm on both mean-score
and intent (+0.20 over the best solo, +0.34 over majority; 51% of the oracle
ceiling). Crucially, plain **majority-vote underperformed the best solo** — naive
crowd-aggregation did not help here, but *structured* deliberation did. This is a
sharper result than the scheduling testbed, where the win only appeared as a
crossover at larger N.

## Two fixes that made the measurement valid

An earlier n=5 run and the first n=15 run surfaced two failures, both fixed:

1. **Assassin-adjacent clues (codemaster).** On one board the clue `STATION`
   walked every guessing arm into `SPACE` (the assassin), wiping the run.
   Target-only validation (are the clue's targets team words?) cannot see this.
   Fix: after a clue passes target validation, simulate a guesser's read — rank
   board words by association to the clue — and reject + retry with feedback if
   the assassin lands within the words a guesser would reach. Result: assassin
   rate fell from 20% to **0%** across all arms.

2. **Guess parsing (gemot arm).** The synthesis arrives as one paragraph; a
   first attempt at "skip warning lines" matched markers over the whole text and
   discarded every guess whenever the reasoning mentioned the assassin/opponent/
   risk anywhere — zeroing the gemot arm on 7/15 boards. Fix: prefer an explicit
   `FINAL:` line, skip only at **clause** granularity with a narrow strong-marker
   set, match board words on token boundaries (no `ICE` inside `PRICE`), cap at
   the clue number. Covered by `gemot_parse_test.go`.

## Honest framing / limits

- "Structured deliberation improves a judgment-aggregating role" — **not**
  "Codenames is a deliberation benchmark".
- Single-turn. Multi-turn turns-to-clear (directly comparable to arXiv:2412.11373)
  is the planned follow-on.
- N=15 is directional, not conclusive; boards are not seed-identical to the paper
  (Go's RNG ≠ Python's) — alignment is at protocol/wordpool/metric level.
- No `gemot-chat` (unstructured deliberation) arm yet — that arm would isolate
  *structure* from *deliberation* and is the natural next comparison.
