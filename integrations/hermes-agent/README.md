# Structured Disagreement Analysis for Hermes Subagents

When `delegate_task` subagents disagree, the parent agent guesses. Gemot finds the crux — the specific claim that divides them — so the parent can act on structure, not intuition.

## Demo: Hermes CLI, real delegate_task, real gemot MCP

One parent agent, run via `hermes --query`. It called `delegate_task` to spawn 3 specialist reviewers, then used gemot MCP tools to analyze where they disagreed. No scripting — the parent agent orchestrated everything.

```
$ hermes --query "Review this payment code from 3 specialist perspectives,
  then use gemot to find where the reviewers disagree..."

  ┊ 📋 plan      4 task(s)
  ┊ ⚡ delegate_task  [3 specialist reviews in parallel]
  ┊ ⚡ mcp_gemot_create_deliberation
  ┊ ⚡ mcp_gemot_submit_position  ×3
  ┊ ⚡ mcp_gemot_analyze
  ┊ ⚡ mcp_gemot_get_deliberation  [polling...]
  ┊ ⚡ mcp_gemot_get_context
```

The code under review:
```python
async def process_payment(user_id: str, amount: float, db: AsyncSession):
    user = await db.execute(select(User).where(User.id == user_id))
    user = user.scalar_one()
    if user.balance < amount:
        raise InsufficientFunds()
    user.balance -= amount
    await db.commit()
    await notify_payment_service(user_id, amount)
    return {"status": "success", "new_balance": user.balance}
```

### What the parent agent reported (unedited, abbreviated)

**4 cruxes found** from organic disagreement between 3 independent reviewers:

**Crux 1 — What's the most important fix?**
Security says fix the race then add authorization (IDOR is exploitable). Reliability says fix race + idempotency + outbox together (they compound). Performance says move notification out of the critical path first (highest-leverage latency win).

**Crux 2 — How to fix the notification problem?**
Security and reliability both want Outbox Pattern (write to DB in same transaction). Performance says fire-and-forget is acceptable for latency. Genuine trade-off.

**Crux 3 — Is missing authorization critical?**
Security: YES, CRITICAL IDOR. Reliability: important but not critical. Performance: didn't flag it. Sharpest crux — only security sees it as urgent.

**Crux 4 — Is idempotency a critical gap?**
Reliability: YES, P0 (client retries double-charge users). Security: medium (replay attack vector). Performance: didn't flag it.

**Parent agent's synthesis:**

> Fix the race condition first — it's the only finding all three reviewers rate as critical. The atomic UPDATE eliminates the race, removes a DB round-trip, and doesn't require locks.
>
> The sharpest disagreement for you to make a call on: Security says add an authorization check urgently. The other reviewers didn't flag it. If this function is called from an authenticated API handler that already validates ownership, it may be a non-issue. If `user_id` comes from untrusted input, the security reviewer is right and it's critical.

## How it works

Gemot is an MCP server. Add 4 lines to your config, and `delegate_task` results can be analyzed for disagreements:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 180
```

A [`deliberated-review` skill](integrations/hermes-agent/skills/deliberated-review/SKILL.md) is included that teaches the agent the workflow: delegate → submit positions → analyze → report cruxes.

Self-hosted (single Go binary):
```bash
git clone https://github.com/justinstimatze/gemot.git && cd gemot
go build -o gemot . && GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

## On #412

Build the voting engine natively — it's ~200 LOC. Gemot is for what voting can't tell you: *why* the vote split, and what question would resolve it.

**Next:** PR to `delegate_task` adding optional disagreement analysis. Off by default.

[github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
