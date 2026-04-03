# Structured Disagreement Analysis for Hermes Subagents

When `delegate_task` subagents disagree, the parent agent guesses. Gemot finds the crux — the specific claim that divides them — so the parent can act on structure, not intuition.

## Demo

Three Hermes v0.6.0 agents. Same setup as `delegate_task` batch.

> *"Should we use adversarial debate or a specialist panel for automated code review? 200K LOC Python, payment handling."*

**debate-advocate**: *"Go adversarial. Panel review fails on payment code — each specialist reviews in isolation, nobody models the full execution chain..."*

**panel-advocate**: *"Use the panel. Two agents can't cover security, performance, and maintainability..."*

**cost-engineer**: *"Neither is production-ready without a baseline. If forced, consensus panel with PCI-DSS pre-filter..."*

### What gemot found

**Round 1** — 2 cruxes, 5 consensus items, 4 bridging proposals, compromise generated.

Consensus (stable across all 3 rounds):
- Majority voting on security findings is unacceptable — single REJECT blocks merge
- Cross-domain vulnerabilities are the hardest class to catch
- Neither architecture should be trusted without empirical validation

**Round 2** — Agents read the cruxes and adjusted. Panel-advocate conceded: *"adversarial debate does have a structural advantage for cross-boundary threat modeling."* Cost-engineer conceded: *"the point about composed attack chains across domain boundaries is real."*

6 cruxes (more specific), 5 consensus (stable), 7 bridging proposals.

**Round 3** — All three agreed on hybrid architecture. Remaining disagreement: who holds the merge gate before empirical data exists.

8 cruxes (refined), 5 consensus (stable), 10 bridging proposals, implementation-ready compromise.

### Parent agent output (unedited)

> **Consensus:** Run both systems in shadow mode first. The metric that decides: cross-domain false-negative rate. A single security REJECT blocks the merge.
>
> **Agreement:** Hybrid architecture wins — adversarial + panel in parallel.
>
> **Open question:** Who holds the primary gate before data exists.
>
> **Next step:** Deploy both in shadow mode, measure cross-domain recall, defer the gate decision until you have data.

## How it works

```python
# After delegate_task returns conflicting summaries:
aggregated, analysis = await asyncio.gather(
    synthesize_subagent_results(summaries),
    analyze_disagreements(query, summaries),  # gemot
)
# analysis.relevant_cruxes, .consensus_statements, .compromise_proposal
```

<details>
<summary>Implementation (~40 lines)</summary>

```python
async def analyze_disagreements(query, responses):
    try:
        async with httpx.AsyncClient() as c:
            r = await c.post(GEMOT_URL, json={"jsonrpc":"2.0","id":1,
                "method":"gemot/create_deliberation",
                "params":{"topic":query[:200],"type":"reasoning"}})
            did = r.json()["result"]["deliberation_id"]

            for i, (agent, text) in enumerate(responses.items()):
                await c.post(GEMOT_URL, json={"jsonrpc":"2.0","id":i+10,
                    "method":"gemot/submit_position",
                    "params":{"deliberation_id":did,"agent_id":agent,"content":text}})

            await c.post(GEMOT_URL, json={"jsonrpc":"2.0","id":100,
                "method":"gemot/analyze","params":{"deliberation_id":did}})

            for _ in range(60):
                await asyncio.sleep(3)
                r = await c.post(GEMOT_URL, json={"jsonrpc":"2.0","id":101,
                    "method":"gemot/get_deliberation","params":{"deliberation_id":did}})
                if r.json()["result"]["status"] == "open": break

            r = await c.post(GEMOT_URL, json={"jsonrpc":"2.0","id":102,
                "method":"gemot/get_context",
                "params":{"deliberation_id":did,"agent_id":next(iter(responses))}})
            return r.json()["result"]
    except Exception:
        return None
```
</details>

## When to bother

For 2-3 short summaries, just read them. Gemot helps when you need to know *why* they disagree, not just *that* they disagree. Also works for `mixture_of_agents`.

## Setup

```bash
# Self-hosted
git clone https://github.com/justinstimatze/gemot.git && cd gemot
go build -o gemot . && GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

```yaml
# Or MCP (Hermes auto-discovers tools)
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 120
```

## On #412

Build the voting engine natively — it's ~200 LOC. Gemot is for what voting can't tell you: *why* the vote split 2-1, and what specific question would resolve it.

**Next:** PR to `delegate_task` adding optional disagreement analysis. Off by default.

[github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
