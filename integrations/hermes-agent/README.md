# Structured Disagreement Analysis for Hermes Subagents

When `delegate_task` subagents disagree, the parent agent guesses. Gemot finds the crux — the specific claim that divides them — so the parent can act on structure, not intuition.

## Verified Demo: Hermes CLI + real gemot MCP

One parent agent, run via `hermes --query`. It called `delegate_task` to spawn 3 specialist reviewers, then used gemot MCP tools to analyze where they disagreed. Full session recorded at [scripts/hermes-test/run_authentic.sh](../../scripts/hermes-test/run_authentic.sh).

```
$ hermes --query "Review this payment code from 3 specialist perspectives,
  then use gemot to find where the reviewers disagree..."

  ┊ 🔀 delegate  3 parallel tasks  45.6s
  ✓ [1/3] security review   (43.86s)
  ✓ [3/3] performance review (44.08s)
  ✓ [2/3] reliability review (45.51s)
  ┊ ⚡ mcp_gemot_create_deliberation
  ┊ ⚡ mcp_gemot_submit_position  ×3
  ┊ ⚡ mcp_gemot_analyze
  ┊ ⚡ mcp_gemot_get_deliberation  [polling ~28 calls, ~3 min]
  ┊ ⚡ mcp_gemot_get_analysis_result
  ┊ ⚡ mcp_gemot_get_context  ×3
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

### What gemot returned (from `get_analysis_result` + `get_context`)

**6 consensus items** — all 3 reviewers independently flagged these:
1. TOCTOU race condition on balance check/deduct (CRITICAL)
2. Input validation: negative amounts, NaN bypass, zero amounts
3. Float arithmetic for money (salami-slicing, ledger drift)
4. Commit/notify split creates partial-failure scenario
5. No authorization check (any caller can charge any user_id)
6. No idempotency key

**4 cruxes** — where reviewers genuinely disagree:

**Crux 1 — Locking strategy.** Reliability wants `SELECT FOR UPDATE` (correctness over throughput). Performance wants optimistic locking with retry (better throughput for low-conflict workloads). Both technically correct; depends on load profile.

**Crux 2 — Async vs synchronous notify (sharpest disagreement).** Performance wants `asyncio.create_task()` to unblock the hot path. Reliability says that makes things worse — fire-and-forget loses all delivery guarantees after commit. Resolution: transactional outbox (write intent in same transaction, background worker delivers).

**Crux 3 — Authentication urgency.** Security: unconditional blocker, ship nothing without it. Performance: adds per-request latency that must be designed carefully. Not actually a disagreement about *whether* — only about *how*.

**Crux 4 — Idempotency priority.** Reliability: hard deploy blocker. Security: HIGH but potentially shippable with documented mitigation.

### Rough edges observed

- **Tool name auto-repair**: Hermes MCP tools get a `mcp_` prefix. The agent kept calling `gemot_create_deliberation` and Hermes auto-repaired to `mcp_gemot_create_deliberation`. Every single gemot call was auto-repaired. The included skill file uses the correct `mcp_gemot_` prefix to avoid this.
- **Polling loop**: Analysis takes ~3 minutes. The agent polled `get_deliberation` 28 times. One poll timed out at 300s during the extracting phase (the analysis itself continued server-side and completed fine on the next poll). A webhook/callback mechanism would be better.
- **First-call errors**: `create_deliberation` and `submit_position` each failed once before the tool name auto-repair kicked in.

## How it works

Gemot is an MCP server. Add 4 lines to your config:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 300
```

A [`deliberated-review` skill](skills/deliberated-review/SKILL.md) is included that teaches the agent the workflow: delegate → submit positions → analyze → report cruxes. The skill uses the `review` template, which configures the analysis for structured review panels.

The full flow takes 3-5 minutes (delegate_task ~1 min, gemot analysis ~2-3 min, polling ~1 min). Best for architecture decisions and thorny PRs, not every commit. Analysis costs ~$0.30 per run (Sonnet).

Self-hosted (single Go binary):
```bash
git clone https://github.com/justinstimatze/gemot.git && cd gemot
go build -o gemot . && DATABASE_URL=postgres://... GEMOT_ANTHROPIC_KEY=sk-ant-... ./gemot http --addr :8080
```

## On #412

Build the voting engine natively — it's ~200 LOC. Gemot is for what voting can't tell you: *why* the vote split, and what question would resolve it.

This demo was single-round — 4 useful cruxes without iteration. Gemot supports multi-round refinement (agents read cruxes, adjust positions, re-analyze) for deeper architecture discussions.

**Next:** PR to `delegate_task` adding optional disagreement analysis. Off by default.

[github.com/justinstimatze/gemot](https://github.com/justinstimatze/gemot) (MIT)
