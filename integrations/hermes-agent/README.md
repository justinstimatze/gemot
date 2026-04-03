# Structured Disagreement Analysis for Mixture of Agents

## What this is

An optional add-on for Hermes's `mixture_of_agents` that identifies exactly where models disagree, instead of blending responses blindly. Runs in parallel with aggregation — doesn't slow down the main flow.

## The problem

MoA queries 4 frontier models and an aggregator synthesizes the result. When models disagree on something specific — say, whether step 3 of a proof holds — the aggregator sees four text blobs and makes a judgment call. That works most of the time. But you don't know what it missed.

The honest version: **for 4 responses on a straightforward question, a well-prompted aggregator is probably fine.** The gap shows up when:

- Responses are long/complex and the disagreement is buried in paragraph 7
- You need to **verify** the synthesis, not just trust it (code review, legal analysis, medical reasoning)
- You want to **iterate** — refine the query based on what specifically was contested
- You're aggregating **10+ perspectives** where prompted analysis degrades

## How it works

Gemot is a Go binary you run locally. No data leaves your machine.

```bash
# One-time setup
go install github.com/justinstimatze/gemot@latest
gemot http --addr :8080 &
```

Then, after MoA collects responses, submit them for structured analysis:

```python
# Inside mixture_of_agents_tool.py, after collecting reference responses:
# (This is real async Python using httpx — fits the MoA tool's existing patterns)

async def analyze_disagreements(query: str, responses: dict[str, str]) -> dict:
    """Optional: find where models actually disagree."""
    async with httpx.AsyncClient(base_url="http://localhost:8080") as c:
        # Create deliberation
        r = await c.post("/a2a", json={
            "jsonrpc": "2.0", "id": 1,
            "method": "gemot/create_deliberation",
            "params": {"topic": query[:200], "type": "reasoning"}
        })
        delib_id = r.json()["result"]["deliberation_id"]

        # Each model's response becomes a position
        for i, (model, text) in enumerate(responses.items()):
            await c.post("/a2a", json={
                "jsonrpc": "2.0", "id": i + 10,
                "method": "gemot/submit_position",
                "params": {
                    "deliberation_id": delib_id,
                    "agent_id": model,
                    "content": text[:10000]  # gemot's position limit
                }
            })

        # Analyze (runs ~30s in background)
        await c.post("/a2a", json={
            "jsonrpc": "2.0", "id": 100,
            "method": "gemot/analyze",
            "params": {"deliberation_id": delib_id}
        })

        # Poll until done (or run in parallel with aggregation)
        for _ in range(60):
            await asyncio.sleep(2)
            r = await c.post("/a2a", json={
                "jsonrpc": "2.0", "id": 101,
                "method": "gemot/get_deliberation",
                "params": {"deliberation_id": delib_id}
            })
            if r.json()["result"]["status"] == "open":
                break

        # Get structured analysis
        r = await c.post("/a2a", json={
            "jsonrpc": "2.0", "id": 102,
            "method": "gemot/get_context",
            "params": {"deliberation_id": delib_id, "agent_id": list(responses.keys())[0]}
        })
        return r.json()["result"]
```

The result gives you:

- `relevant_cruxes`: the specific claims that divide the models, classified as factual or value-based
- `consensus_statements`: what all models agree on
- `topic_summaries`: a map of what was discussed
- `compromise_proposal`: an LLM-generated synthesis that specifically addresses the crux

You can pass the cruxes to the aggregator for focused synthesis, show them to the user alongside the MoA result, or just log them for debugging.

## Why not just prompt the aggregator?

You can. Adding "first identify where the models disagree, then synthesize" to the aggregator prompt is free and fast. For most MoA queries, that's sufficient.

Gemot adds value when you need:

| Need | Prompted aggregator | Gemot |
|------|-------------------|-------|
| Quick disagreement summary | Good enough | Overkill |
| Auditable claim-by-claim analysis | Single LLM's interpretation | Structured extraction with source tracing |
| Consistent results across runs | Varies with sampling | Deterministic pipeline |
| Multi-round refinement | Stateless | Tracks positions across rounds |
| 10+ model responses | Degrades (context window) | Scales (per-position extraction) |
| Debugging why synthesis was wrong | Re-read 4 responses | Check which crux the aggregator resolved incorrectly |

The honest recommendation: start with prompted aggregation. Add gemot when you need auditability or when the simple approach isn't catching subtle disagreements.

## Self-hosting

Gemot is a single Go binary with no dependencies. All data stays local.

```bash
# Build from source
git clone https://github.com/justinstimatze/gemot.git
cd gemot && go build -o gemot .
./gemot http --addr :8080

# Or use the hosted version (data retained 48h for sandbox)
# https://gemot.dev/a2a
```

Requires an Anthropic API key for the analysis LLM calls (set `GEMOT_ANTHROPIC_KEY`). Analysis uses Sonnet by default (~$0.25 per run).

## Same pattern, other workflows

The disagreement analysis works anywhere Hermes uses multiple agents:

- **`delegate_task` batch**: 3 subagent reviewers disagree on a PR → gemot finds the specific contested claim
- **Research paper self-review**: Multiple review angles conflict → gemot identifies whether it's scope vs completeness
- **Any multi-perspective task**: Collect N agent outputs → find what they agree/disagree on → focus synthesis on the crux

## For Issue #412

If you're building a voting/consensus engine for Hermes, gemot's [governance templates](https://gemot.dev/docs) cover majority, supermajority, unanimous, jury, and consensus — with crux detection, liquid democracy, and integrity checks on top. Happy to discuss what the right integration surface would look like.

## Next step

We could submit a PR adding an optional `analyze_disagreements` flag to the MoA tool — ~50 lines, off by default, runs in parallel with aggregation. If the crux analysis improves MoA quality on hard problems, it earns its place. If not, it's easy to remove.

We tested Hermes v0.6.0 connecting to gemot via MCP Streamable HTTP — tool discovery and calls work. But for MoA specifically, the direct A2A HTTP approach shown above is simpler and doesn't require MCP config.

Gemot is open source (MIT): [github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot)
