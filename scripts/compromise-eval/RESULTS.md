# compromise-eval results

Does gemot's structured deliberation pipeline (claim extraction → clustering →
crux detection → synthesis) beat the unstructured baseline — three agents in a
group chat — at synthesizing a compromise no individual proposed?

Domain: hidden-constraint scheduling. Each agent privately holds blocked slots +
soft preferences, stated in prose (so the analysis layer can extract them).
Instances are generated so the globally-feasible optimum exists, no agent
proposed it, and the best proposed feasible slot scores strictly below it — a gap
only a *synthesized* option can close. Metric: mean-norm = chosen soft-score /
global-optimum soft-score (infeasible = 0), averaged over instances.

Arms: deterministic selection baselines (solo / plurality / random-dictator /
`oracle:best-proposal`, the selection ceiling); `gemot-structured` (live gemot,
options-empty synthesis); `gemot-chat` (`GEMOT_ANALYZER=chat`, one unstructured
pass over the raw positions); `global-opt` (ceiling, = 1.0).

## Headline: a crossover in robustness, not small-group quality

Clean sweep — fixed 7×12 grid, block-rate 0.25, n=5, seed 2026, varying only
agent count:

| agents | oracle:best-proposal | gemot-chat | gemot-structured | winner |
|--------|----------------------|-----------|------------------|--------|
| 3  | 0.613 (80% feasible) | 0.751 (100%) | 0.760 (100%) | tied |
| 6  | 0.325 (60%)          | **0.509 (60%)** | 0.347 (60%) | chat |
| 10 | 0.080 (20%)          | 0.000 (0%) | **0.200 (20%)** | structured |

- Both LLM arms beat the selection ceiling at N=3 and N=6.
- Structure is **not** better at small/mid N — chat ties at 3 and wins at 6.
- Structure's value is **graceful degradation at scale**: chat collapses
  (0.75 → 0.51 → 0.00) because one raw pass can't track 10 agents' constraints;
  structured degrades gently (0.76 → 0.35 → 0.20) because aggregation survives.
- The crossover is ≈ N=10.

## What an earlier, confounded pilot got wrong

A first pilot varied grid size + block-rate together with agent count and
reported the crossover at N=6 (structured 0.880 > chat 0.786). That cell's
higher score was a difficulty artifact of its grid, not a scale effect. Holding
the grid fixed and varying only N moved the crossover to ~10 and showed chat
ahead at 6. The clean sweep supersedes the pilot.

## Forced-choice vs free-text synthesis (N=3, tiny 5×4 grid pilot)

| arm | mean-norm | note |
|-----|-----------|------|
| gemot-chat (unstructured)   | 0.967 | reasons over raw positions |
| gemot-structured (free text)| 0.567 | commits ~3/5; states the rule but doesn't execute it 2/5 |
| gemot-structured (forced choice) | 0.400 | commits 5/5 but picks worse |
| oracle:best-proposal        | 0.167 | selection ceiling |

Forcing a choice removed non-commitment but *lowered* quality: the pipeline
commits confidently to infeasible slots. The bottleneck is upstream — claim
extraction/clustering discard the concrete per-agent constraints, so no
decision rule at the compromise step can recover them.

## Caveats

- n=5 per cell, single seed (2026). N=10 numbers are small (0.20 ≈ 1 feasible of
  5) and noisy; the qualitative pattern (chat collapses, structured survives) is
  the robust claim, not the exact values.
- The 10-agent regime is hard for everything (oracle 0.08) — a stress point, not
  a typical operating range.
- Difficulty still shifts with N under a fixed grid (feasible set shrinks) — that
  is intended (the real effect of more factions), but it means cross-N
  comparisons are of *arms relative to each other within a cell*, not absolute.

## Reproduce

```bash
# offline (no key): deterministic arms + selection ceiling
go run ./scripts/compromise-eval --n 5 --seed 2026 --agents 6 --days 7 --perday 12 --block-rate 0.25

# live arm (needs a gemot server + credentialed key or sandbox tier)
go run ./scripts/compromise-eval --n 5 --seed 2026 --agents 6 --days 7 --perday 12 --block-rate 0.25 \
    --url http://localhost:8080/mcp --secret "$GEMOT_KEY" --arm-label gemot-structured   # structured server
# chat arm: run against a GEMOT_ANALYZER=chat server, --arm-label gemot-chat --template freeform
# forced choice: add --gemot-mode choice
```
