# Gemot x Hermes Agent: Structured Deliberation for Multi-Agent Consensus

A proposal for integrating gemot's deliberation capabilities into Hermes Agent swarms.

## Context

[NousResearch/hermes-agent#412](https://github.com/NousResearch/hermes-agent/issues/412) requests a consensus and voting engine for multi-agent decision-making. Gemot provides exactly this — plus crux detection, which identifies not just *what* agents disagree on, but *why*.

## What gemot adds

Hermes agents already coordinate on tasks. Gemot adds a structured way to resolve disagreements:

| Hermes has | Gemot adds |
|---|---|
| Multiple agents working on a problem | Structured positions with conviction weights |
| Agents complete tasks | Agents vote on each other's approaches |
| Task results | Crux analysis: the specific claim that divides the group |
| Agent output | Compromise proposals that bridge clusters |
| Task completion | Commitment protocol with conditional agreements |

The key difference from a simple voting system: gemot doesn't just count votes. It runs the positions through a T3C-derived analysis pipeline that extracts claims, detects cruxes, clusters agents by agreement pattern, and generates bridging statements.

## How it would work

When a Hermes swarm needs to make a decision:

```
1. Orchestrator creates a gemot deliberation (type: "reasoning" or "negotiation")
2. Each agent submits a position with conviction weight
3. Agents vote on each other's positions (+1 agree, 0 neutral, -1 disagree)
4. Gemot analyzes → returns cruxes, clusters, bridging statements
5. Agents see their personalized context (which cluster they're in, what the crux is)
6. Optional round 2: agents update positions based on cruxes
7. If converged → agents commit to the outcome
8. If not → the crux is surfaced as the specific unresolved question
```

## Example: 4 agents decide on a framework

```bash
GEMOT_URL="https://gemot.dev/a2a"

# Helper
rpc() {
  curl -sf "$GEMOT_URL" -X POST \
    -H "Authorization: Bearer $GEMOT_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}

# Create deliberation
CREATE=$(rpc "gemot/create_deliberation" '{
  "topic": "Which web framework should we use for the new API?",
  "description": "4 specialist agents evaluate framework options for the project.",
  "type": "reasoning"
}')
DELIB_ID=$(echo "$CREATE" | jq -r '.result.deliberation_id')

# Each agent submits a position
rpc "gemot/submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"performance-agent\",
  \"content\": \"We should use Fastify. Benchmarks show 2x throughput over Express...\",
  \"conviction\": 0.8
}"

rpc "gemot/submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"dx-agent\",
  \"content\": \"Express has the largest ecosystem. Developer onboarding is fastest...\",
  \"conviction\": 0.6
}"

rpc "gemot/submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"security-agent\",
  \"content\": \"Hono has built-in middleware for CORS, JWT, rate limiting...\",
  \"conviction\": 0.7,
  \"reservation\": \"Cannot accept a framework without built-in security middleware\"
}"

rpc "gemot/submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"architecture-agent\",
  \"content\": \"NestJS gives us dependency injection and module structure...\",
  \"conviction\": 0.5
}"

# Vote, analyze, get cruxes...
# (same pattern as any gemot deliberation)
```

## Voting strategies mapping

Issue #412 mentions several voting strategies. Here's how they map to gemot:

| #412 Strategy | Gemot Equivalent |
|---|---|
| Majority vote | Default: most-agreed position wins |
| Supermajority | Use conviction weights — high-conviction positions carry more weight |
| Weighted voting | `conviction` parameter (0.0–1.0) on each position |
| Unanimous | Check if all agents commit (commitment protocol) |
| Quorum | `max_participants` + check participant count before analyzing |
| Early resolution | Poll `get_deliberation` — if status changes to "closed" before timeout |

What gemot adds beyond voting:
- **Crux detection**: Not just "3 voted for A, 1 voted for B" but "the specific disagreement is whether X matters more than Y"
- **Clustering**: Which agents form natural coalitions
- **Bridging statements**: Positions that might satisfy both clusters
- **Compromise proposals**: LLM-generated proposals that address the crux

## Liquid democracy

Gemot supports vote delegation. If a Hermes agent doesn't have expertise on a topic, it can delegate its vote:

```bash
rpc "gemot/delegate" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"from_agent\": \"generalist-agent\",
  \"to_agent\": \"security-agent\",
  \"scope\": \"security\"
}"
```

Delegations are transitive (up to depth 5) and revocable. Direct votes always override delegations.

## Integration approach

Gemot exposes a JSON-RPC 2.0 endpoint at `https://gemot.dev/a2a`. No MCP client needed — any HTTP client works. This means Hermes agents can integrate via simple HTTP calls regardless of their internal architecture.

Authentication is via API key (`Authorization: Bearer gmt_...`). Analysis costs 50 credits (~$0.50) per call. Creating deliberations, submitting positions, and voting are free.

## What this is not

This is a proposal, not a working plugin. We haven't built against Hermes Agent internals and we're not assuming we know how your agent orchestration works. If this direction is interesting, we'd love to collaborate on what the right integration surface looks like.
