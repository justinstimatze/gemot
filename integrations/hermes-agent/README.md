# Structured Disagreement Analysis for Hermes Subagents

Vote counting is easy (#412 Phase 1 is ~200 LOC and should be built natively). The hard part is what happens when votes reveal a split but don't explain why. That's what this addresses.

## Test: 3 Hermes agents, open-weight vs API

3 Hermes v0.6.0 agents with different expert personas. Standard `AIAgent` interface, Sonnet, different system prompts — same setup as a `delegate_task` batch.

> *"We're building a customer support chatbot. Should we fine-tune Llama 3.3 8B on our 50K support tickets, or use Claude Sonnet with RAG? 2-person ML team."*

**open-weight-advocate**: *"Fine-tune. Inference via vLLM at ~$0.0001/token vs Sonnet's $3-15/MTok — at 10M tokens/month that's a $1,500-3,000/month delta that compounds forever..."*

**api-pragmatist**: *"Claude + RAG. Fine-tuning will consume 4-8 weeks before you've shipped a single feature. Claude with RAG can be production-ready in 1-2 weeks..."*

**hybrid-architect**: *"Claude + RAG. Fine-tuning is an operational trap: 60-70% of engineering bandwidth on training pipelines. Every policy update requires a new training run; RAG gives you that for free..."*

Submitted to gemot, analyzed. 4 cruxes:

**1. "Fine-tuning on 50K tickets primarily encodes style, not reasoning"**
- 67% controversy. AGREE: api-pragmatist, hybrid-architect. DISAGREE: open-weight-advocate.
- Testable: eval fine-tuned 8B on reasoning-heavy cases vs Claude+RAG.

**2. "50K tickets meaningfully improve domain reasoning beyond style"**
- 100%. AGREE: open-weight-advocate. DISAGREE: hybrid-architect.
- Measurable: compare fine-tuned edge-case performance vs base model.

**3. "At ~10M tokens/month, self-hosted savings justify leaving the API"**
- 100%. AGREE: open-weight-advocate. DISAGREE: hybrid-architect.
- Calculable. The advocates disagree on the break-even: $1.5K/month vs $50K/month.

**4. "Vendor lock-in risk > self-hosting risk for a small team"**
- 100%. AGREE: open-weight-advocate. DISAGREE: hybrid-architect.
- Value judgment. Not resolvable with evidence.

### Parent agent synthesis

A Hermes parent agent read the crux JSON and produced this (unedited):

> **Agreed on:** The API route is cheaper until ~$50K/month — so unless you're processing millions of tickets, self-hosted fine-tuning won't save money.
>
> **Key disagreements:**
> - **Does fine-tuning actually help?** open-weight-advocate says 15–25% accuracy gains. api-pragmatist says it's mostly style. **Most important unknown to resolve.**
> - **Vendor lock-in?** Values call about risk tolerance.
>
> **Check:**
> 1. Fine-tune on a slice of tickets, test vs Claude+RAG on your hardest cases.
> 2. Estimate realistic API bill — if well under $50K/month, start with Claude+RAG.

### Round 2: refinement after cruxes

Fed cruxes back to each agent. Positions shifted:

- **open-weight-advocate** conceded economics: *"I overstated the operational-risk argument — if below $50K/month API spend, vendor lock-in is an edge case..."*
- **api-pragmatist** conceded quality: *"If fine-tuning produces 15–25% accuracy gains in intent classification, that's not just style..."*
- **hybrid-architect** held on threshold: *"The $50K/month threshold lands — opportunity cost of fine-tuning infra dominates for a 2-person team..."*

Re-analyzed. Cruxes narrowed from 4 to 3. Remaining disagreements are more specific. Prompted aggregation can't do this — it's stateless.

## Integration

After `delegate_task` returns conflicting summaries:

```python
aggregated, crux_analysis = await asyncio.gather(
    synthesize_subagent_results(summaries),
    analyze_disagreements(query, summaries),  # gemot, ~90s
)

if crux_analysis and crux_analysis.get("relevant_cruxes"):
    cruxes = crux_analysis["relevant_cruxes"]
    # "Subagents disagreed on 4 points. The key question is..."
```

<details>
<summary><code>analyze_disagreements</code> implementation</summary>

```python
from typing import Dict, Optional
import asyncio
import httpx

GEMOT_URL = "http://localhost:8080/a2a"

async def analyze_disagreements(
    query: str, responses: Dict[str, str]
) -> Optional[Dict]:
    try:
        async with httpx.AsyncClient() as c:
            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 1,
                "method": "gemot/create_deliberation",
                "params": {"topic": query[:200], "type": "reasoning"}
            })
            delib_id = r.json()["result"]["deliberation_id"]

            for i, (agent_id, text) in enumerate(responses.items()):
                await c.post(GEMOT_URL, json={
                    "jsonrpc": "2.0", "id": i + 10,
                    "method": "gemot/submit_position",
                    "params": {
                        "deliberation_id": delib_id,
                        "agent_id": agent_id,
                        "content": text
                    }
                })

            await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 100,
                "method": "gemot/analyze",
                "params": {"deliberation_id": delib_id}
            })

            for _ in range(60):
                await asyncio.sleep(3)
                r = await c.post(GEMOT_URL, json={
                    "jsonrpc": "2.0", "id": 101,
                    "method": "gemot/get_deliberation",
                    "params": {"deliberation_id": delib_id}
                })
                if r.json()["result"]["status"] == "open":
                    break

            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 102,
                "method": "gemot/get_context",
                "params": {
                    "deliberation_id": delib_id,
                    "agent_id": next(iter(responses))
                }
            })
            return r.json()["result"]
    except Exception:
        return None  # non-fatal, delegate_task works without it
```

</details>

## When to use this

For 2-3 short summaries, just read them. Gemot helps when:

| | Reading summaries | Gemot |
|-|-------------------|-------|
| **Debugging wrong synthesis** | Re-read everything | Check which crux was resolved wrong |
| **Factual vs value** | Manual judgment | Automatic classification |
| **Multi-round** | Stateless | Agents refine after seeing cruxes |
| **5+ subagents** | Hard to track | Scales with extraction |

Same pattern works for `mixture_of_agents` — submit model responses as positions, find where they diverge.

## What we tested

- Hermes v0.6.0 `AIAgent`, MCP Streamable HTTP tool discovery, A2A position submission
- Full 2-round deliberation: 4 cruxes → 3 after refinement
- Parent agent synthesis from crux data (unedited output above)
- Test scripts: [`run_finetuning_test.py`](scripts/hermes-test/run_finetuning_test.py), [`run_round2.py`](scripts/hermes-test/run_round2.py)

**Verified**: deliberation, analysis, parent synthesis, multi-round.
**Proposed**: wiring into `delegate_task` (tested with direct `AIAgent`).

## Setup

Self-hosted (single binary, data stays with your LLM provider):

```bash
git clone https://github.com/justinstimatze/gemot.git && cd gemot
go build -o gemot . && GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

Or MCP:
```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 120
```

Sonnet, ~$0.30/run, ~90s.

## On #412's open questions

**"Tiebreaking is hard — what happens when 2 of 4 vote A and 2 vote B?"**
Gemot finds the crux. Instead of adding a 5th voter, you get: "the disagreement is whether 10K rows is enough for full fine-tuning" — resolve that and the tie breaks itself.

**"Voting quality depends on judge quality"**
Gemot extracts claims from the work itself, not from judge opinions. No judge prompt to get wrong.

**"Should judges be separate agents or the same agents?"**
Neither. The producing agents submit their work as positions. Gemot finds where they disagree from the content. No separate judge step.

Phase 1-2 of #412 (vote counting, quorum, strategies) should be native Python — it's fast, deterministic, no external deps. Gemot is for Phase 3 territory: convergence detection, multi-round refinement, and the cases where voting says "it's a tie" and you need to know why. [Docs](https://gemot.dev/docs).

## Next

PR to add optional disagreement analysis to `delegate_task` batch results. Off by default, parallel.

[github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
