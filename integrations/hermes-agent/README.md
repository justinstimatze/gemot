I noticed #412 covers a lot of the same ground as what I've been working on. Gemot is a deliberation server for multi-agent workflows (MIT, Go). The voting part is straightforward — any team could implement that inline. The piece that might be interesting is the crux analysis: when agents disagree, an LLM pipeline finds the specific claim they split on — the thing that, if resolved, would flip the outcome. Adapted from the [Talk to the City](https://github.com/AIObjectives/tttc-light-js) approach. That pipeline and the resolution mechanics took some iteration and might save someone time.

Hermes was helpful while building this — testing against real agent coordination patterns made the design better. Thanks for making it open.

Code's at [github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) — `internal/analysis/` has the crux detection, `internal/deliberation/` has the resolution logic. Take whatever's useful.
