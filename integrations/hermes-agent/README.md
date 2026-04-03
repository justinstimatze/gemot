# Structured Disagreement Analysis for Hermes Subagents

## Real test: 3 Hermes agents deliberate on open-weight vs API

We gave 3 Hermes v0.6.0 agents different expert personas and asked them the kind of question a Hermes user might delegate to subagents:

> *"We're building a customer support chatbot. Should we fine-tune an open-weight model (Llama 3.3 8B) on our support tickets, or use a closed API (Claude Sonnet) with RAG over our knowledge base? We have 50K support tickets and a 2-person ML team."*

Each agent generated a position through Hermes's standard `AIAgent` interface:

**open-weight-advocate**: *"Fine-tune the open-weight model. With 50K tickets, you'll bake support tone, escalation patterns, and product-specific terminology directly into weights. Inference via vLLM runs at ~$0.0001/token versus Claude Sonnet's $3-15/MTok — at 10M tokens/month that's a $1,500-3,000/month delta that compounds forever..."*

**api-pragmatist**: *"Use Claude Sonnet with RAG. Don't fine-tune. With a 2-person ML team, fine-tuning will consume 4-8 weeks of engineering time before you've shipped a single feature. Claude with a well-structured RAG pipeline can be production-ready in 1-2 weeks..."*

**hybrid-architect**: *"Use Claude Sonnet with RAG. Don't fine-tune. Fine-tuning Llama 3.3 8B is an operational trap: you'll spend 60-70% of engineering bandwidth on training pipelines and retraining cycles. Fine-tuning encodes past knowledge into weights — every policy update requires a new training run; RAG gives you that for free..."*

We submitted these positions to gemot and ran analysis. Here's what it found:

### Cruxes detected

**1. "Fine-tuning on 50K support tickets primarily encodes style, not reasoning"**
- Controversy: 67%
- AGREE: api-pragmatist, hybrid-architect
- DISAGREE: open-weight-advocate
- *The core technical disagreement. Testable: run an eval comparing fine-tuned 8B on reasoning-heavy support cases vs Claude+RAG on the same cases.*

**2. "50K tickets meaningfully improve domain reasoning beyond surface-level style"**
- Controversy: 100%
- AGREE: open-weight-advocate
- DISAGREE: hybrid-architect
- *Factual — measurable by comparing fine-tuned performance on edge cases vs base model.*

**3. "At ~10M tokens/month, self-hosted inference savings justify switching from a closed API"**
- Controversy: 100%
- AGREE: open-weight-advocate
- DISAGREE: hybrid-architect
- *Factual — calculable. The open-weight advocate says the delta is $1,500-3,000/month; the hybrid architect says the threshold is ~$50K/month.*

**4. "Vendor lock-in risk outweighs self-hosting operational risk for a small team"**
- Controversy: 100%
- AGREE: open-weight-advocate
- DISAGREE: hybrid-architect
- *Value judgment — depends on risk tolerance. Not resolvable with evidence.*

### What this gives the parent agent

Instead of hedging ("Both approaches have merit..."), the parent agent can tell the user:

> "Your subagents agree that RAG is faster to deploy and easier to maintain. They disagree on whether fine-tuning encodes real reasoning or just style — **that's testable with an eval on your hardest support tickets.** They also disagree on the cost break-even point — **the open-weight advocate says $1.5K/month savings, the architect says you'd need $50K/month API spend to justify it. Check your actual projected volume.**"

That's three specific next steps, not a vague recommendation.

## How it works

After `delegate_task` returns conflicting subagent summaries, the parent agent submits them to gemot for structured analysis. Runs in parallel with any other post-processing:

```python
# The integration point — after delegate_task returns:
aggregated, crux_analysis = await asyncio.gather(
    synthesize_subagent_results(summaries),  # existing Hermes flow
    analyze_disagreements(query, summaries),  # gemot
)

if crux_analysis and crux_analysis.get("relevant_cruxes"):
    cruxes = crux_analysis["relevant_cruxes"]
    # Surface to user: "Subagents disagreed on 4 points. The key question is..."
```

<details>
<summary>Full implementation of <code>analyze_disagreements</code></summary>

```python
from typing import Dict, Optional
import asyncio
import httpx

GEMOT_URL = "http://localhost:8080/a2a"  # self-hosted, see setup below

async def analyze_disagreements(
    query: str, responses: Dict[str, str]
) -> Optional[Dict]:
    """Find where subagents actually disagree. Non-blocking, graceful degradation."""
    try:
        async with httpx.AsyncClient() as c:
            # Create deliberation
            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 1,
                "method": "gemot/create_deliberation",
                "params": {"topic": query[:200], "type": "reasoning"}
            })
            delib_id = r.json()["result"]["deliberation_id"]

            # Each subagent's response becomes a position
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

            # Trigger analysis
            await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 100,
                "method": "gemot/analyze",
                "params": {"deliberation_id": delib_id}
            })

            # Poll until done
            for _ in range(60):
                await asyncio.sleep(3)
                r = await c.post(GEMOT_URL, json={
                    "jsonrpc": "2.0", "id": 101,
                    "method": "gemot/get_deliberation",
                    "params": {"deliberation_id": delib_id}
                })
                status = r.json()["result"]["status"]
                if status == "open":
                    break
                if status != "analyzing":
                    return None

            first_agent = next(iter(responses))
            r = await c.post(GEMOT_URL, json={
                "jsonrpc": "2.0", "id": 102,
                "method": "gemot/get_context",
                "params": {
                    "deliberation_id": delib_id,
                    "agent_id": first_agent
                }
            })
            return r.json()["result"]
    except Exception as e:
        logger.warning("Gemot analysis failed (non-fatal): %s", e)
        return None
```

</details>

## When is this worth it vs. just reading the summaries?

For 2-3 short subagent summaries, a human (or a well-prompted parent agent) can spot the disagreements. Gemot adds value when:

| Situation | Reading summaries | Gemot |
|-----------|-------------------|-------|
| **Debugging wrong synthesis** | Re-read all summaries | Check which crux the parent resolved incorrectly |
| **Consistent structure** | Depends on the parent agent | Same crux format every time |
| **Classifying disagreements** | Manual judgment | Automatic: factual (testable) vs value (preference) |
| **Multi-round** | Stateless | Subagents can refine positions after seeing cruxes |
| **5+ subagents** | Hard to track all disagreements | Scales with claim extraction |

Start without gemot. Add it when you need structured crux analysis or when the parent agent keeps producing vague syntheses.

## What we tested

- Hermes v0.6.0, three `AIAgent` instances with different system prompts
- Connected to gemot via MCP Streamable HTTP — tool discovery works automatically
- Positions submitted to gemot via A2A JSON-RPC (simpler than MCP for this use case)
- Full analysis pipeline: taxonomy (5 topics), claim extraction (15 claims), deduplication, crux detection (4 cruxes), classification

**What's verified**: agent position generation, MCP tool discovery, A2A position submission, full analysis producing real cruxes
**What's proposed**: the `delegate_task` integration pattern and the parent-agent crux synthesis

## Setup

Self-hosted (single Go binary, data stays between your machine and your LLM provider):

```bash
git clone https://github.com/justinstimatze/gemot.git
cd gemot && go build -o gemot .
GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

Or connect via MCP (Hermes auto-discovers all tools):

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 120
```

Analysis uses Sonnet (~$0.30 per run, ~90s).

## For Issue #412

Gemot's analysis pipeline — claim extraction, deduplication, multi-candidate crux generation, factual/value classification, integrity checks — has a fair number of edge cases. If #412 goes forward, happy to share what we've learned. Governance templates (majority, supermajority, unanimous, jury, consensus) with liquid democracy and sybil detection are also available — [details in the docs](https://gemot.dev/docs).

## Next step

We'd like to submit a PR adding optional disagreement analysis to `delegate_task` batch results. Off by default, runs in parallel. The test above is reproducible — [test script](scripts/hermes-test/run_finetuning_test.py).

Source: [github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
