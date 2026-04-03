# Gemot x Hermes: Structured Disagreement for Mixture of Agents

## The gap in MoA

Hermes's `mixture_of_agents` is one of its showcase features — Claude Opus, GPT-5.4 Pro, Gemini 3 Pro, and DeepSeek V3.2 all tackle a hard problem, then an aggregator synthesizes the best answer. It's powerful, and it gets attention.

But when the models disagree on something specific — say, whether a mathematical proof holds, or whether a code approach has a race condition — the aggregator blends their outputs without knowing *what exactly* they disagree on. It sees four text blobs and produces a fifth. Wang et al. (arXiv:2406.04692) flags this as a known limitation.

Gemot can tell you the crux: "Claude and GPT agree the proof is valid; Gemini and DeepSeek think step 3 has a gap in the induction hypothesis." That's actionable — you can check step 3 specifically, instead of re-reading four full responses.

## How it works

After MoA collects 4 model responses:

```python
# Standard MoA flow gives you 4 responses...
# Before aggregating, find what they actually disagree on:

delib = mcp_gemot_create_deliberation(
    topic=user_query,
    type="reasoning"
)

for model, response in moa_responses.items():
    mcp_gemot_submit_position(
        deliberation_id=delib.id,
        agent_id=model,
        content=response
    )

mcp_gemot_analyze(deliberation_id=delib.id)

context = mcp_gemot_get_context(
    deliberation_id=delib.id,
    agent_id="claude-opus"  # or whichever model
)

# context.relevant_cruxes tells you exactly where models diverge
# context.consensus_statements tells you what they all agree on
# context.compromise_proposal offers a synthesis that addresses the crux

# Pass the cruxes to the aggregator for focused synthesis:
aggregator_prompt = f"""
These models agree on: {context.consensus_statements}
They disagree specifically on: {context.relevant_cruxes}
Focus your synthesis on resolving the crux.
"""
```

This turns "4 models gave different answers" into "they agree on X, disagree on Y, and the specific question is Z."

## Same idea, different workflows

The pattern applies anywhere Hermes uses multiple agents:

### `delegate_task` batch mode

3 subagents review a PR. Two say PASS, one says REQUEST_CHANGES about a SQL injection risk. Instead of the parent agent guessing, gemot finds the crux: "Does the ORM's parameterization cover the raw SQL on line 47?" That's a specific question someone can answer.

### Research paper writing (Phase 6: Self-Review)

The paper gets reviewed from multiple angles — statistical rigor, narrative clarity, related work coverage. When reviews conflict ("the related work section is too long" vs "it's missing key references"), gemot identifies whether the disagreement is about scope or completeness — a much more useful signal than "reviewers disagree."

### Any multi-perspective task

Whenever you'd dispatch multiple agents and merge their outputs, gemot can tell you what they agree on, what they disagree on, and why. The pattern is always:

1. Collect multiple agent outputs
2. Submit each as a gemot position
3. Analyze → get cruxes
4. Use cruxes to focus the synthesis or escalate to the user

## Setup

4 lines in `~/.hermes/config.yaml`:

```yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 120
```

Hermes's MCP client auto-discovers gemot's 28 tools. They appear as `mcp_gemot_create_deliberation`, `mcp_gemot_submit_position`, etc.

For production, add an API key:
```yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    headers:
      Authorization: "Bearer gmt_your_key"
    timeout: 120
```

We tested Hermes v0.6.0 connecting to gemot via MCP Streamable HTTP — tool discovery and tool calls work.

## Voting strategies (Issue #412)

If #412 goes forward, here's how gemot's templates map:

| Strategy | Gemot Template | Notes |
|---|---|---|
| Majority | `parliament` (51%) | |
| Supermajority | `assembly` (67%) | |
| Near-unanimous | `jury` (92%) | Good for code review |
| Unanimous | `consensus` (100%) | Reservations act as vetoes |
| Weighted | `conviction` param (0.0–1.0) | Time-weighted across rounds |
| Quorum | `rules: {"min_participants": N}` | Enforced before analysis |

Beyond vote counting, gemot adds crux detection, crux classification (factual vs value), clustering, bridging statements, compromise proposals, liquid democracy (delegated votes, transitive up to depth 5), sybil detection, and audit trails.

## Try it

Create a sandbox at [gemot.dev/try](https://gemot.dev/try) — no API key, no signup. Or call the A2A JSON-RPC endpoint directly at `https://gemot.dev/a2a` (any HTTP client, no MCP needed).

Source: [github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot)
