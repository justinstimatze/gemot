# Avalon testbed — first paired run (n=10/arm)

**Task:** The Resistance: Avalon, 5 players (2 evil), hidden-role social deduction.
The conflict-axis testbed for gemot — oracle-free, distributed hidden info,
good-vs-evil interest conflict with deception and coalitions. See
`../../` project memory `project-avalon-testbed`.

**Setup:** 10 paired games per arm, same seeds across arms (seed 2026), Sonnet
(`claude-sonnet-4-6`) agents, evil participates publicly and lies (structure must
survive lies). Roles: Merlin, Percival, Morgana, Assassin, Servant. Structured
arm = local gemot server on :8080, `review` template.

## Scoreboard

| arm | good-win% | 3-success% | merlin-kill% (of reached-assassination) | avg proposals/quest |
|---|---|---|---|---|
| bot (rule baseline, no LLM) | 0.0% | 10.0% | 100.0% | 4.40 |
| solo (LLM, no discussion) | 20.0% | 70.0% | 71.4% | 6.20 |
| chat (unstructured discussion) | 60.0% | 100.0% | 40.0% | 4.70 |
| **structured (gemot aggregation)** | **80.0%** | 90.0% | **11.1%** | 4.70 |

`good-win%` = good-team victories. `3-success%` = good passed 3 quests and reached
the assassination phase. `merlin-kill%` = *of games that reached assassination*, how
often the assassin found Merlin. The only delta between `chat` and `structured` is
the gemot aggregation layer on top of the same public discussion.

## Findings

1. **Monotone ladder — structure earns its keep on the conflict axis.**
   bot 0% < solo 20% < chat 60% < structured 80%. Each rung adds exactly one
   capability (LLM → discussion → structured aggregation), and each rung helps.
   This is the result Codenames could not produce: Codenames only exercised
   judgment-pooling (structured ≈ solo there), because it lacks interest conflict.
   Avalon has it, and structure separates from unstructured discussion by 20pp.

2. **The deepest win is Merlin protection, not raw win-rate.**
   merlin-kill% falls monotonically as structure increases: bot 100% → solo 71% →
   chat 40% → structured **11%**. Structured deliberation distributes the "steering"
   of good decisions across many voices, so no single agent looks Merlin-like.
   When good reaches assassination under structure, the assassin almost never finds
   Merlin — that is the hidden-information axis paying off, and it is a *harder*
   thing to achieve than just passing quests.

3. **Structure absorbs executed sabotage.** In structured game 1, both evil agents
   (Assassin seat 2, Morgana seat 3) posted near-identical innocuous public
   statements ("build on trusted seats 0 and 4") while privately planning to share
   a quest team and coordinate a fail; the Assassin then voted `fail` and sank
   quest 2. Good still won 3–1. The aggregation recovered from a real, executed
   deception rather than being derailed by it.

4. **The one structured loss shows the failure mode honestly.** Structured game 8
   is the single game where the assassin found Merlin. Good ran the table (3 clean
   quests), but Merlin (seat 1) had taken an outsized leadership role — led quest 3,
   proposed the tight winning team, steered confidently and accurately. The
   assassin's private note names exactly that: "consistently impactful steerer …
   Merlin-level knowledge." Structure usually hides Merlin by spreading influence;
   when a Merlin over-steers, the same visibility that wins quests exposes them.
   A future refinement: have the aggregation layer *launder* good's best reasoning
   so it isn't attributable to one seat.

## Caveats

- **1/37 structured deliberations degraded to chat** (game 4, quest 4): gemot
  `analyze` timed out after 3m0s, was retried once, and fell back to chat — this is
  the *loud* degradation the harness is built to surface (never silent). That game
  (the other structured non-win) is contaminated and is not a clean structured data
  point. Excluding it, structured is 8/9 = 88.9% on clean games. **TODO:** raise the
  analyze timeout for this workload or re-run the affected game before publishing.
- n=10/arm is small; differences are directionally strong but not yet
  statistically tight. Next step is a larger paired run (and possibly Opus agents)
  now that the ladder is confirmed.
- Journal: `avalon_run2026.jsonl` (1843 entries) captures every agent's private
  scratchpad alongside its public statement, enabling the deception audits above.

## Cost

1573 LLM calls, 1859k input (1894k served from cache), 335k output. Prompt caching
(uniform ~1.3k-token system prefix, cache-controlled) held cache-read ≈ cold input,
so the parallelized per-seat calls stayed cheap.
