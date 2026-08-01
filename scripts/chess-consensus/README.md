# chess-consensus

Three agents per side must agree on one chess move. Each has its own Stockfish
view of the position and its own temperament — aggression, defence, or
calculation. They propose, argue, vote, run a gemot analysis, reconsider, and
one move comes out. Then the other side does the same.

This is the diplomacy experiment with a scoreboard. In Diplomacy we can measure
supply-centre spread and argue about whether a balanced board is a good outcome.
In chess a reference engine tells us, for every single decision, exactly how much
the group gave up. That turns "does structured deliberation improve collective
judgement?" from a question of interpretation into a number.

## The measurement

Every ply, the harness scores four things against a deep reference search of the
*same* position:

| decision maker | what it is |
|----------------|------------|
| `played` | the move the side actually made under its decision rule |
| `plurality` | the most-proposed move, had the agents never discussed |
| `solo:aggressor` etc. | what each agent would have played alone |

All of them are scored from the same node at the same depth, so the centipawn
losses are directly comparable. A single 60-ply game therefore yields ~30 paired
observations per side — enough signal to see an effect without running a
tournament.

The headline number is ACPL (average centipawn loss). If deliberation is doing
real work, `played` should beat every `solo:` column and should beat `plurality`
— beating the solo agents only shows that averaging three opinions helps, which
is just the wisdom of crowds. Beating `plurality` is the claim that *persuasion*
adds something over *aggregation*.

## Why the agents disagree

Every agent sees the same engine. What differs is what it wants on top of the
evaluation. Each temperament is a vector of weights over concrete features of a
move, denominated in centipawns:

```
aggressor:  check +45, forcing PV +28/ply, king proximity +11, sacrifice +25
defender:   king shield +28, en prise -75, material loss -60, castle +35
tactician:  forcing PV +16, check +10, en prise -30, promotion +20
```

A `check` weight of 45 means literally "I will advocate a move up to 0.45 pawns
worse if it gives check". That is the experimental lever: each agent carries a
known, quantified bias, and the question is whether three-way deliberation
cancels those biases or compounds them.

Features are computed from the move and the position it produces — no engine
involvement — so an agent's taste is independent of its search. By default all
three search to the same depth, which keeps the bias question clean. `--asymmetric`
gives them different search budgets instead, modelling agents of genuinely
unequal strength.

Each agent also carries a **reservation** — a hard constraint that overrides
preference. The defender will not endorse a move that hangs a piece no matter how
attractive it scores. Reservations are enforced in the vote, not merely declared
in the prose.

## The deliberation

One gemot deliberation per side per move, at round 1 (which sidesteps the
cooling-period rule that governs repeat analyses):

1. **Survey.** Each agent runs MultiPV on its own engine and re-ranks the
   candidates by its own taste.
2. **Propose.** Each submits a position: its move, its argument, its interests,
   its reservation, and a conviction score derived from how far its pick leads
   its own runner-up. The move, engine eval, and bias go in `metadata`.
3. **Cross-evaluate.** Every agent evaluates every move on the table, including
   ones it never shortlisted — via `searchmoves`, so it forms a real opinion
   rather than dismissing what it did not think of.
4. **Vote.** Each agent votes -2..+2 on each peer's proposal, derived from how
   much utility it gives up by playing that move instead of its own.
5. **Analyse.** `analyze action:run`, then poll for cruxes, consensus,
   bridging statements, and a compromise proposal.
6. **Reconsider.** Each agent re-ranks with peer support priced in and may
   switch. Switching is the persuasion event the experiment exists to measure.
7. **Tally.** Approval voting over the final positions: highest total wins, ties
   broken by first-choice count, then by summed *engine* evaluation — the
   unbiased tiebreak, so the procedure does not smuggle in a taste.
8. **Commit.** The chosen move is recorded as a gemot commitment.

Plies where all three agents propose the same move are skipped by default —
there is nothing to deliberate. `--always-deliberate` forces them, which matters
if you want a complete deliberation record rather than a cheap run.

## Running it

Stockfish is required. On Debian it installs to `/usr/games`, which is not on
`PATH`; the harness looks there anyway.

```bash
go run ./scripts/chess-consensus --gemot=false                  # offline, no API keys, ~40s
go run ./scripts/chess-consensus --url http://localhost:8080/mcp  # full deliberation
go run ./scripts/chess-consensus --white gemot --black plurality  # paired arms
go run ./scripts/chess-consensus --llm full --always-deliberate   # agents actually argue
```

Results land in `chess-consensus-<timestamp>/`: `run.json` (every proposal,
vote, analysis, and counterfactual), `REPORT.md`, and `game.pgn`.

### Modes

`--llm off` (default) is free and deterministic. Arguments are templated from
real engine data — every number in an agent's case came from a search it
actually ran — and votes come from the utility functions. This tests the
*aggregation* question honestly without an API bill.

`--llm args` has the model write the arguments. `--llm full` has it vote and
reconsider too, which is the only mode where an argument can genuinely change a
mind. Budget roughly 12 model calls per deliberated ply per side.

`--gemot=false` runs the same propose/vote/reconsider loop without the gemot
analysis. Comparing it against `--gemot=true` isolates what the analysis
contributes over plain vote aggregation.

In offline mode, `--persuasion` (0-1, default 0.6) scales how much peer support
is worth: a unanimous endorsement is worth `persuasion × 150` centipawns of
personal preference. `--persuasion 0` means no agent is ever moved, which is a
useful null control.

### Sides

`--white` and `--black` each take `gemot`, `plurality`, or `engine`. Note that
the counterfactual columns are computed for every side regardless of its arm, so
even a `gemot`-vs-`gemot` game gives you the full comparison — you do not need to
run separate control games to learn what plurality would have done.

## What a run looks like

From a 40-ply offline run at depth 10, reference depth 16:

```
| decision maker | ACPL | best-move rate | inacc. | mistakes | blunders |
|----------------|------|----------------|--------|----------|----------|
| solo:aggressor | 2.5  | 85%            | 1      | 0        | 0        |
| solo:tactician | 4.0  | 75%            | 2      | 0        | 0        |
| plurality      | 4.5  | 75%            | 2      | 0        | 0        |
| gemot (played) | 6.6  | 70%            | 3      | 0        | 0        |
| solo:defender  | 7.4  | 60%            | 3      | 0        | 0        |
```

Deliberation lost to plurality here. That is a finding, not a bug — and it is the
kind of finding this harness exists to produce. The Diplomacy work already showed
gemot amplifies coordination quality without determining its direction; chess
lets us ask the sharper version of that question and get a number back.

## Caveats

- **N=1.** One game is one game. The per-ply counterfactuals give far more
  statistical power than a single Diplomacy run, but a real result needs many
  games across varied openings and seeds.
- **The persuasion model is a model.** In `--llm off`, "being persuaded" is a
  centipawn-denominated pull toward peer-backed moves. It is a stand-in for
  argument, not argument itself. Findings about persuasion rates are findings
  about that model until `--llm full` reproduces them.
- **`en_prise` is one ply deep.** It catches outright hangs, not tactics. A
  legal king recapture counts as free, since the square would be defended
  otherwise.
- **Losses are capped at 1000cp** so a single missed mate does not swamp the
  average — the standard ACPL convention.
- **The agents are strong.** Stockfish at depth 10 rarely blunders, so ACPL
  differences are small in absolute terms. Lower `--depth` to widen the spread
  and make personality effects easier to see.
