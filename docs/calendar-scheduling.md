# Calendar Scheduling Demo

Five agents negotiate a group meeting time through gemot without sharing raw calendar data.

## Why this matters

Group scheduling is the universal coordination problem. Everyone's had the "when works for everyone?" thread that takes 20 messages. With gemot, each person's agent submits availability windows — no Doodle poll, no calendar sharing, no event names leaking. Gemot finds the crux (morning people vs afternoon people), proposes a compromise, and everyone commits.

## The scenario

Five team members need a 1-hour sync:
- **Alice**: Early bird, prefers mornings. Hard conflict Thursday PM.
- **Bob**: Afternoon person. All-day offsite Wednesday.
- **Carol**: Flexible but must leave by 2 PM daily (school pickup). Prefers Tue/Thu.
- **Dave**: Later timezone, can't start before 11 AM. Only works Mon/Wed/Fri.
- **Eve**: Part-time, only works Mon-Wed. Prefers late morning.

The crux: morning people (Alice, Eve) vs afternoon people (Bob) vs midday-only people (Carol, Dave). Monday 11 AM - 12 PM is the only slot where all five overlap — but gemot has to *discover* that from the positions and votes.

## How it works

1. **Create a negotiation-type deliberation** for the target week
2. **Each agent submits availability windows** as a position:
   - Available time slots (not event names — privacy preserving)
   - `conviction` reflects preference strength (0.7 = "I really prefer mornings")
   - `reservation` declares hard constraints ("Cannot meet Thursday PM")
3. **Agents cross-vote** on each other's proposals (20 votes for 5 agents)
4. **Analysis** clusters agents by scheduling preference and identifies the crux
5. **Compromise proposal** generates the best slot given all constraints
6. **Agents commit** — first agent commits conditionally ("if at least 3 of 5 also commit")

## Running

```bash
# Requires a gemot API key with credits
export GEMOT_API_SECRET=gmt_...
export GEMOT_LIVE_URL=https://gemot.dev/mcp  # or local

go run scripts/calendar-scheduling/main.go
```

Output goes to stderr (human-readable progress) and stdout (JSON summary).

## What makes this interesting

- **Privacy**: Agents share availability windows, not calendar contents. No event names, no attendee lists.
- **Preferences vs constraints**: `conviction` weights preferences, `reservation` declares hard blocks. Analysis respects both.
- **Clustering**: With 5 agents, gemot finds natural groups (morning people, afternoon people) and reports which cluster each agent belongs to.
- **ZOPA detection**: The "negotiation" type triggers zone-of-possible-agreement analysis — do the constraints even allow a solution?
- **Conditional commitments**: "I'll accept this if at least 3 of 5 also commit" — gemot tracks these dependencies.
- **Scales naturally**: The same pattern works for 2 people or 50. More agents = richer clustering and more interesting cruxes.

## The vote matrix

Each agent votes on every other agent's availability:

| Voter → | Alice | Bob | Carol | Dave | Eve |
|---------|-------|-----|-------|------|-----|
| Alice   | —     | 0   | +1    | 0    | +1  |
| Bob     | 0     | —   | -1    | +1   | -1  |
| Carol   | +1    | -1  | —     | +1   | +1  |
| Dave    | 0     | +1  | +1    | —    | 0   |
| Eve     | +1    | -1  | +1    | 0    | —   |

This creates two clusters: {Alice, Carol, Eve} (morning/midday) vs {Bob, Dave} (afternoon/late). The compromise has to bridge them.
