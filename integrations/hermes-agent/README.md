# Voting Engine + Disagreement Analysis for Hermes Multi-Agent Workflows

When `delegate_task` subagents disagree, gemot gives you two things: a **structured vote** that picks a winner, and **crux analysis** that explains why the vote split.

## Demo 1: Vote-Based Decision (Cache Layer Selection)

Three agents designed competing cache approaches. Gemot ran a structured vote and auto-resolved when one approach hit the 67% approval threshold.

```
$ hermes --query "Evaluate 3 cache approaches, vote to pick the best..."

  ┊ 🔀 delegate  3 parallel tasks  64s
  ✓ [1/3] Redis write-through
  ✓ [2/3] In-process LRU + TTL
  ✓ [3/3] CDN edge caching (stale-while-revalidate)
  ┊ ⚡ mcp_gemot_create_deliberation  (template: assembly)
  ┊ ⚡ mcp_gemot_submit_position  ×3
  ┊ ⚡ mcp_gemot_vote  ×6  (cross-voting)
  ┊ ⚡ mcp_gemot_get_deliberation  → status: open, resolution: ✓
```

**Result** — auto-resolved in <1 second after votes:

```
WINNER: Redis write-through (100% approval)

Approach              | agree | disagree | pass | approval
----------------------|-------|----------|------|----------
Redis write-through   |   1   |    0     |   1  |  1.00  ← WINNER
In-process LRU + TTL  |   1   |    1     |   0  |  0.50
CDN edge caching      |   0   |    1     |   1  |  0.00
```

No analysis needed. No polling loop. Votes go in, decision comes out. The `resolution` field on the deliberation is machine-readable — agents can check it programmatically and act.

## Demo 2: Disagreement Analysis (Code Review)

When votes are close or you need to understand *why* agents disagree, trigger analysis. Three specialist reviewers examined a payment function. Gemot found 4 cruxes.

```
  ┊ 🔀 delegate  3 parallel tasks  31s
  ┊ ⚡ mcp_gemot_create_deliberation  (template: review)
  ┊ ⚡ mcp_gemot_submit_position  ×3
  ┊ ⚡ mcp_gemot_vote  ×6
  ┊ ⚡ mcp_gemot_analyze
  ┊ ⚡ mcp_gemot_get_deliberation  [polling ~25 calls, ~2 min]
  ┊ ⚡ mcp_gemot_get_analysis_result
```

**4 cruxes found** (controversy 0.67–1.0):
1. How to fix deduct+notify atomicity (outbox vs fire-and-forget)
2. Where to enforce idempotency (DB constraint vs Redis cache)
3. notify_payment_service() hardening (move outside session vs timeout)
4. Audit logging framing (compliance vs operational)

Analysis also returns a machine-readable `recommended_action` field (`vote_on_compromise`, `submit_position_on_cruxes`, `consensus_reached`, etc.) so agents know what to do next without external LLM inference.

## How it works

Gemot is an MCP server. Add 4 lines to your config:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 300
```

**For decisions (use case #1/#2/#3/#5 from the issue):**
1. `create_deliberation` → `submit_position` per option → `vote` → check `resolution` field

**For understanding disagreements (use case #4/#6):**
1. Same as above, plus `analyze` → `get_analysis_result` for cruxes and clusters

Templates control the voting strategy:
- `assembly` (67%) — supermajority
- `negotiation` (60%) — near-majority
- `consensus` (100%) — unanimous required
- `review` (75%) — strong majority
- `jury` (92%) — near-unanimous
- `roberts_rules` (51%) — parliamentary procedure with motions, seconds, and amendments

Resolution is non-blocking: votes auto-resolve when threshold is met, but agents can continue voting (resolution updates live) and analysis works regardless. Agents can also set deadlines (`deadline_minutes`) and withdraw from deliberations.

A [`deliberated-review` skill](skills/deliberated-review/SKILL.md) is included for the code review workflow.

Self-hosted (single Go binary):
```bash
git clone https://github.com/justinstimatze/gemot.git && cd gemot
go build -o gemot . && DATABASE_URL=postgres://... GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

## On #412

This addresses all 6 use cases from the issue:

| Use case | How gemot handles it |
|----------|---------------------|
| Quality gating on fan-out | Submit options → vote → auto-resolve |
| Go/no-go decisions | Submit "merge"/"block" → vote → threshold check |
| Research synthesis | Submit findings → vote on relevance → winner |
| Conflict resolution | Vote first, then `analyze` for crux detection |
| Cascade routing | Submit "use cheap model"/"escalate" → vote |
| Weighted expertise | `conviction` field (0.0–1.0) on positions |

Resolution is a field, not a status change — deliberations stay open for further voting or analysis. This matches the issue's "vote changing" and "early resolution" requirements.

Voting strategies map to templates: majority → `negotiation` (60%), supermajority → `assembly` (67%), unanimous → `consensus` (100%). Quorum via `min_participants` rule.

What gemot adds beyond voting: when the vote is close (e.g., 2-1), `analyze` tells you *why* — the specific claim that divides the voters. This turns a thin majority into an actionable insight.

[github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
