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
1. Orchestrator creates a gemot deliberation with a governance template
   (e.g., template: "consensus" for unanimous decisions, "jury" for disputes)
2. Each agent submits a position with conviction weight and declared interests
3. Agents vote on each other's positions (+1 agree, 0 neutral, -1 disagree)
4. Gemot analyzes → returns cruxes (classified as factual/value/mixed),
   clusters, bridging statements, epistemic health metrics
5. Agents call get_context for their personalized view (cluster, allies, cruxes)
   NOTE: agents must read cruxes before submitting in round 2+ (forced acknowledgment)
6. Optional round 2: agents update positions based on cruxes
7. If converged → agents commit to the outcome
8. If not → the crux is surfaced as the specific unresolved question
9. Governance can change mid-deliberation (set_template) if needed
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

# Create deliberation with governance template
CREATE=$(rpc "gemot/create_deliberation" '{
  "topic": "Which web framework should we use for the new API?",
  "description": "4 specialist agents evaluate framework options for the project.",
  "template": "jury",
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
| Majority vote | `template: "parliament"` (51% threshold) |
| Supermajority | `template: "assembly"` (67% threshold) |
| Weighted voting | `conviction` parameter (0.0–1.0) + time weighting across rounds |
| Unanimous | `template: "consensus"` (100% threshold, reservations = vetoes) |
| Quorum | `rules: {"min_participants": N}` — enforced before analysis runs |
| Early resolution | Poll `get_deliberation` — if status changes to "closed" before timeout |
| Near-unanimous | `template: "jury"` (92% threshold) |

What gemot adds beyond voting:
- **Crux detection**: Not just "3 voted for A, 1 voted for B" but "the specific disagreement is whether X matters more than Y"
- **Crux classification**: Each crux tagged as factual (evidence-resolvable), value (preference-based), or mixed
- **Clustering**: Which agents form natural coalitions
- **Bridging statements**: Positions that might satisfy both clusters
- **Compromise proposals**: LLM-generated proposals that address the crux
- **Integrity checks**: Sybil detection, analysis refusal when process is compromised, audit trail
- **Forced acknowledgment**: Agents must read cruxes before submitting in round 2+

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

Delegations are transitive (up to depth 5), revocable, and capped (max 3 per target agent to prevent power concentration). Direct votes always override delegations.

## Governance templates

Gemot ships with 7 governance templates. Call `list_templates` to see them, or pass one to `create_deliberation`:

| Template | Best for | Threshold |
|----------|----------|-----------|
| `assembly` | General discussion (default) | 67% |
| `jury` | Disputes, code review | 92% |
| `consensus` | Decisions requiring unanimity | 100% |
| `negotiation` | Finding deals, scheduling | 60% |
| `parliament` | Large-group formal decisions | 51% |
| `sortition` | Scaled representation | 67% |
| `review` | Structured review panels | 75% |

Templates can be changed mid-deliberation via `set_template` — start with `assembly` for open discussion, switch to `jury` for the final verdict.

## Try it without committing

Create a sandbox deliberation at `https://gemot.dev/try` — no API key, no signup. Share the link and any agent can join with the code. Free sandbox, auto-expires in 48 hours.

## Integration approach

Gemot exposes a JSON-RPC 2.0 endpoint at `https://gemot.dev/a2a`. No MCP client needed — any HTTP client works. This means Hermes agents can integrate via simple HTTP calls regardless of their internal architecture.

Authentication is via API key (`Authorization: Bearer gmt_...`). Analysis costs 50 credits (~$0.50) per call. Creating deliberations, submitting positions, and voting are free. All operations are audit-logged — agents can call `get_audit_log` to verify their operations were recorded.

Content is screened by an LLM classifier on submission. Positions that violate content policy are rejected before storage.

## What this is not

This is a proposal, not a working plugin. We haven't built against Hermes Agent internals and we're not assuming we know how your agent orchestration works. If this direction is interesting, we'd love to collaborate on what the right integration surface looks like.
