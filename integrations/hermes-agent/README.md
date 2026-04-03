# Structured Disagreement Analysis for Mixture of Agents

## A concrete example

MoA asks 4 frontier models: *"Is this Python code thread-safe?"*

```python
# The code under review
def increment(counters: dict, key: str):
    counters[key] = counters.get(key, 0) + 1
```

The models disagree:
- **Claude Opus**: "Safe — dict operations are atomic under the GIL"
- **GPT-5.4**: "Safe — `dict.__setitem__` is a single bytecode operation"
- **Gemini 3 Pro**: "Unsafe — `get` then `setitem` is two operations; another thread can modify between them"
- **DeepSeek V3.2**: "Unsafe — the GIL doesn't make multi-step read-modify-write atomic"

The MoA aggregator synthesizes: *"The code has potential thread-safety concerns. While individual dict operations are atomic under CPython's GIL, the compound read-modify-write pattern may not be..."*

That's correct but vague. Which line? What specifically is the risk? Can I check it?

Gemot finds the crux:

> **"The read-modify-write pattern `d[key] = d.get(key, 0) + 1` is atomic under CPython's GIL"**
>
> AGREE (safe): claude-opus, gpt-5.4-pro
> DISAGREE (unsafe): gemini-3-pro, deepseek-v3.2
> Type: **factual** — checkable by reading CPython bytecode

That's actionable. You can `dis.dis(increment)` and see it compiles to `LOAD_ATTR (get)` → `CALL` → `BINARY_OP (add)` → `STORE_SUBSCR` — four bytecodes, not one. The GIL can release between any of them. Gemini and DeepSeek are right.

The aggregator would have gotten there eventually with enough prompting. Gemot gets there structurally, every time, and tells you it's a factual question you can verify.

## How it works

After MoA collects reference responses, run disagreement analysis in parallel with aggregation — zero additional wall-clock time:

```python
from typing import Dict, Optional
import asyncio
import httpx

GEMOT_URL = "http://localhost:8080/a2a"  # self-hosted, see setup below

async def analyze_disagreements(
    query: str, responses: Dict[str, str]
) -> Optional[Dict]:
    """Find where models actually disagree. Runs in parallel with aggregation."""
    try:
        async with httpx.AsyncClient() as c:
            # Create deliberation
            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 1,
                "method": "gemot/create_deliberation",
                "params": {"topic": query[:200], "type": "reasoning"}
            })
            delib_id = r.json()["result"]["deliberation_id"]

            # Each model's response becomes a position
            for i, (model, text) in enumerate(responses.items()):
                await c.post(GEMOT_URL, json={
                    "jsonrpc": "2.0", "id": i + 10,
                    "method": "gemot/submit_position",
                    "params": {
                        "deliberation_id": delib_id,
                        "agent_id": model,
                        "content": text
                    }
                })

            # Trigger analysis
            await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 100,
                "method": "gemot/analyze",
                "params": {"deliberation_id": delib_id}
            })

            # Poll until done
            for _ in range(60):
                await asyncio.sleep(2)
                r = await c.post(GEMOT_URL, json={
                    "jsonrpc": "2.0", "id": 101,
                    "method": "gemot/get_deliberation",
                    "params": {"deliberation_id": delib_id}
                })
                status = r.json()["result"]["status"]
                if status == "open":  # analysis complete, round advanced
                    break
                if status != "analyzing":
                    return None  # unexpected state

            # Get structured results
            first_model = next(iter(responses))
            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 102,
                "method": "gemot/get_context",
                "params": {
                    "deliberation_id": delib_id,
                    "agent_id": first_model
                }
            })
            return r.json()["result"]
    except Exception as e:
        logger.warning("Gemot analysis failed (non-fatal): %s", e)
        return None  # graceful degradation — MoA works fine without it


# In the MoA flow — run both concurrently:
aggregated, crux_analysis = await asyncio.gather(
    aggregate_with_model(aggregator_model, reference_responses, query),
    analyze_disagreements(query, reference_responses),
)

if crux_analysis and crux_analysis.get("relevant_cruxes"):
    cruxes = crux_analysis["relevant_cruxes"]
    logger.info(
        "Models disagreed on %d point(s): %s",
        len(cruxes), cruxes[0]["crux_claim"][:100]
    )
    # Optionally: feed cruxes back to the aggregator for focused re-synthesis
```

## When is this worth it vs. prompting the aggregator?

You can add "identify where the models disagree, then synthesize" to the aggregator prompt for free. For most MoA queries, that's sufficient.

Gemot adds value when:

| Situation | Prompted aggregator | Gemot |
|-----------|-------------------|-------|
| **Debugging wrong synthesis** | Re-read 4 responses manually | Check which crux the aggregator resolved incorrectly |
| **Consistent results** | Varies with sampling temperature | Deterministic pipeline, same structure every time |
| **Multi-round refinement** | Stateless — start over | Tracks positions across rounds, conviction time-weighting |
| **10+ model responses** | Degrades (context window pressure) | Scales (per-position extraction) |
| **Audit trail needed** | Single LLM's interpretation | Structured claims with source tracing |

Start with prompted aggregation. Add gemot when the simple approach isn't catching subtle disagreements, or when you need to debug why a synthesis was wrong.

## Setup

Gemot is a single Go binary. Self-hosted, data stays between your machine and your LLM provider (Anthropic by default — same privacy model as Hermes itself).

```bash
git clone https://github.com/justinstimatze/gemot.git
cd gemot && go build -o gemot .
GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

Analysis uses Sonnet by default (~$0.25 per run). Runs in ~30s.

Alternatively, use the hosted version at `https://gemot.dev/a2a` (free sandbox, 48h retention) or connect via MCP:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 120
```

## Same pattern, other workflows

Anywhere Hermes aggregates multiple agent outputs:

- **`delegate_task` batch**: 3 subagent reviewers disagree → gemot finds the specific contested claim, not just "reviewers disagree"
- **Research paper self-review**: Review angles conflict → gemot identifies whether it's a scope disagreement or a completeness gap
- **Any multi-perspective task**: N agent outputs → structured agreement/disagreement map → focused synthesis

## For Issue #412

Gemot's analysis pipeline — claim extraction, deduplication, multi-candidate crux generation, factual/value classification, integrity checks — has 160+ tests and a fair number of edge cases we've already hit. If #412 goes forward, happy to share what we've learned. Governance templates (majority, supermajority, unanimous, jury, consensus) with liquid democracy and sybil detection are also available — [details in the docs](https://gemot.dev/docs).

## Next step

We'd like to submit a PR adding an optional `analyze_disagreements` flag to the MoA tool. ~50 lines, off by default, runs in parallel with aggregation. If crux analysis improves MoA quality on hard problems, it earns its place. If not, easy to revert.

We tested Hermes v0.6.0 connecting to gemot via MCP Streamable HTTP — tool discovery and calls work. For MoA specifically, the direct HTTP approach above is simpler.

Source: [github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
