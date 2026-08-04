# chess-consensus

Three agents must agree on one chess move. The best move is dealt to exactly
one of them; the other two cannot see it at all. Whether the group finds it
measures something a Diplomacy run cannot: not whether agents coordinate, but
whether information actually moves between them.

## Why it works this way

The first version of this harness gave each agent the same full view of the
position and a different temperament — aggression, defence, calculation — and
asked whether deliberation cancelled those biases. The run data killed it:

- Deliberation and the plurality control **picked the same move on 95% of
  plies**. The entire treatment effect rested on 2 moves out of 40.
- **65% of positions showed zero discrimination** — every candidate scored the
  same loss against the reference engine.
- Nothing an agent knew was unavailable to the others, so deliberation had no
  information to transmit. "Does discussion help?" reduced to "does adding a
  monotone function of aggregate utility move the argmax?" — a question about
  hand-picked weights, answerable in closed form.

Hidden profiles fix all three. The paradigm comes from group decision research:
everyone shares a pool of decent-but-not-best options, and the genuinely best
option sits with one member. No individual can reach the right answer alone, and
a group that only aggregates first preferences fails *systematically* rather
than randomly. Human groups reliably flunk it — they re-discuss what everyone
already knows and discount what only one member brought.

The same suite, run with full information instead, reproduces the old problem
exactly:

| | played | plurality | gap |
|---|---|---|---|
| full information | 9.1 ACPL | 11.0 ACPL | 1.9 |
| hidden profile | 7.8 ACPL | 67.1 ACPL | **59.3** |

Same positions, same agents, same rules. Full information cannot detect an
effect; the hidden profile separates the arms by an order of magnitude.

## The deal

At each position the reference engine ranks the candidate moves. Then:

- **rank 1** — the gem — goes to exactly one agent, chosen by hashing the
  position so every arm gets an identical deal
- **ranks 2-3** go to nobody, which guarantees the shared pool is meaningfully
  worse than the gem
- **ranks 4-7** are the shared pool, visible to everyone
- **ranks 8+** are dealt round-robin as private distractors, so no agent's
  information set is conspicuously larger than another's

Each agent surveys only its own set. Two of three therefore choose from the
shared pool, so plurality voting degenerates to chance.

**The review penalty** is the parameter the experiment turns on. When a peer
tables a move outside your information set, you search it at reduced depth —
you have not done the work on that line. At penalty 0 information transfers for
free; raise it and the group has to work for it.

## Metrics

The hidden-profile scoreboard separates two very different failures:

| stage | what a low number means |
|---|---|
| **surfaced** | the holder never proposed the gem — its own taste buried the best move before discussion began. No decision rule can recover these. |
| **adopted** | the gem was tabled and the group still passed on it. This is a persuasion failure, and it is what the deliberation is supposed to fix. |

Plus, on identical positions, ACPL for: the rule that ran, plurality, random
dictator (the strategyproof baseline), each agent alone, and
`oracle:best-proposal` — the best move anyone actually proposed, which is the
ceiling for any procedure that only chooses among proposals. The gap between the
oracle and what was played is aggregation loss, cleanly separated from what the
agents failed to propose in the first place.

## Results

40 positions, agents at depth 10, reference at depth 14, `--llm off`,
`--gemot=false` (vote aggregation only — no analysis, since that needs an API
key). Identical deal in every row:

| review penalty | gem surfaced | gem adopted | persuasion | ACPL played | ACPL plurality |
|---|---|---|---|---|---|
| 0 | 38/40 (95%) | 36/40 (90%) | 95% | 7.1 | 69.8 |
| 2 | 38/40 (95%) | 34/40 (85%) | 89% | 5.2 | 67.3 |
| 4 | 38/40 (95%) | 35/40 (88%) | 92% | 7.8 | 67.1 |
| 6 | 38/40 (95%) | 30/40 (75%) | 79% | 16.7 | 71.1 |
| 8 | 38/40 (95%) | 30/40 (75%) | 79% | 13.9 | 68.1 |

Surfacing is flat by construction — the holder's own survey does not depend on
the review penalty — which is a useful invariant: if it moves, something is
leaking between positions. Persuasion holds near 90% up to penalty 4, then drops
to 79%. Controls at penalty 4: plurality 9/40, random dictator 15/40, both near
the 33% you get by chance from a three-way split.

**This is not yet a result about deliberation.** It is a result about a
closed-form persuasion rule, with gemot's analysis switched off. The numbers
that matter need `--llm full` and a live gemot server.

## Picking this up with a key

Everything below runs today except the two things that matter most, because the
environment this was built in had no `ANTHROPIC_API_KEY`:

- **`llm.go` has never executed.** `NewLLM`, `complete`, `Argue`, `Vote`, and
  `LLM.Reconsider` compile and are wired in, but no call has ever been made.
  Expect plumbing bugs — prompt assembly, fenced-JSON parsing, the
  out-of-range vote clamp, the "model picked a move that isn't on the table"
  rejection — on the first keyed run.
- **gemot's analysis has never run on this data.** The MCP round trip is
  verified end to end (deliberations created, positions submitted with
  metadata, votes recorded); only `analyze action:run` fails, and it fails
  loudly rather than silently reporting an empty result.

The first question worth answering: **does the crux name the information
asymmetry?** If the analysis says the disagreement is about whether the agents
have evaluated the same lines, the mechanism is working and that is the result.
If the cruxes come back as generic style disagreements, the hidden profile is
not reaching the analysis layer and the deal needs rethinking before anything
else gets built on it.

## Running it

Stockfish is required. On Debian it lands in `/usr/games`, which is not on
`PATH`; the harness looks there anyway.

```bash
# 1. Build a position suite (reusable across every arm and seed)
go run ./scripts/chess-consensus --generate-suite 40 --seed 2026 \
    --ref-depth 12 --min-gap 40 --max-gap 250 --suite-out suite40.json

# 2. Run the experiment
go run ./scripts/chess-consensus --mode suite --suite suite40.json --gemot=false
go run ./scripts/chess-consensus --mode suite --suite suite40.json \
    --url http://localhost:8080/mcp --llm full

# 3. Sweep the parameter that matters
for rp in 0 2 4 6 8; do
  go run ./scripts/chess-consensus --mode suite --suite suite40.json \
      --review-penalty $rp --out rp$rp
done
```

`--information full` disables the deal and gives every agent the whole board —
the null condition, and the sanity check that the hidden profile is doing the
work.

`--mode game` is the original self-play demo: two councils alternating moves
over a real game, always full information (game mode has no precomputed
reference ranking to deal from). It makes a better demo than experiment, for all
the reasons above.

## Experimental hygiene

Engines are reset with `ucinewgame` before every position. Without that, hash
state carries between positions, and since different arms issue different
sequences of searches, the same agent returns slightly different evaluations
depending on what ran before it — silently unpairing the comparison. The bug was
visible as a surfacing rate that drifted with the review penalty, a quantity it
cannot legitimately depend on.

Plurality ties are broken by a hash of the position, never by evaluation. An
earlier version broke them by summed engine evaluation, which handed the control
the answer: under a hidden profile every agent proposes a different move, every
move has exactly one proposer, and the gem holder's evaluation is always the
highest — so the control would have "found" information it never received.
There is a regression test for this.

## Known limits

- **The persuasion rule is still a model.** With `--llm off`, being persuaded is
  a centipawn-denominated pull toward peer-backed moves. Findings about
  persuasion rates are findings about that rule until `--llm full` reproduces
  them.
- **Compute is not matched.** Three agents at depth 10 spend more than one agent
  at depth 10. The `solo:` columns are counterfactuals over the same searches,
  not a fair fight against a single agent given the group's whole budget.
- **The reference is the same engine family**, only a few plies deeper, so its
  errors correlate with the agents'. The noise floor is not characterised.
- **Losses are capped at 1000cp**, the standard ACPL convention, so a single
  catastrophe cannot swamp an average. `--max-gap` bounds the deal's difficulty
  for the same reason.
- **n=40 on one seed.** The suite makes larger n cheap; nothing here has been
  replicated.

## Next: faction chess

The Diplomacy work's most interesting finding was majority tyranny — Austria and
Russia reaching genuine agreement that eliminated Turkey. Chess as set up here
has no third party to harm, so that result has no analogue.

Faction chess would restore it: each agent owns a subset of the pieces and is
scored on its own faction's outcome, while the team shares one result. Interests
genuinely conflict, team-level ground truth still exists, and the measurable
question becomes whether deliberation produces team-optimal or faction-optimal
play — the v10 finding with a number attached.

The seams are in place: `Personality` already carries interests and a
reservation, `Agent` already scores candidates through a pluggable feature
vector, and the counterfactual machinery already reports per-decision-maker
outcomes. What faction chess adds is a per-agent objective and a scorer that
tracks each faction's material and activity separately from the team's
evaluation.
