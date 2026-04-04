# Structured Disagreement Analysis for Hermes Subagents

When `delegate_task` subagents disagree, the parent agent guesses. Gemot finds the crux — the specific claim that divides them — so the parent can act on structure, not intuition.

## Verified Demo: Hermes CLI + real gemot MCP

One parent agent, run via `hermes --query`. It called `delegate_task` to spawn 3 specialist reviewers, then used gemot MCP tools to analyze where they disagreed. Reviewers cross-voted on each other's positions before analysis. Full session recorded — 3 verified end-to-end runs on April 4, 2026.

```
$ hermes --query "Review this payment code from 3 specialist perspectives,
  then use gemot to find where the reviewers disagree..."

  ┊ 🔀 delegate  3 parallel tasks  31s
  ✓ [1/3] security review
  ✓ [2/3] reliability review
  ✓ [3/3] performance review
  ┊ ⚡ mcp_gemot_create_deliberation  (template: review)
  ┊ ⚡ mcp_gemot_submit_position  ×3
  ┊ ⚡ mcp_gemot_get_positions
  ┊ ⚡ mcp_gemot_vote  ×6  (cross-voting)
  ┊ ⚡ mcp_gemot_analyze
  ┊ ⚡ mcp_gemot_get_deliberation  [polling ~25 calls, ~2 min]
  ┊ ⚡ mcp_gemot_get_analysis_result
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

### What gemot returned (from `get_analysis_result` + `get_context`)

26 claims extracted across 3 positions. 5 topics identified. 4 cruxes detected (controversy 0.67–1.0). All 3 reviewers in separate clusters — maximum perspective diversity.

**Consensus** — all 3 reviewers independently flagged:
- TOCTOU race condition on balance check/deduct (CRITICAL)
- `commit()` before `notify_payment_service()` breaks atomicity
- No idempotency key (network retries cause double charges)
- `float` for monetary values (rounding exploits, ledger drift)
- No audit trail / payment transaction record
- No authorization check on `user_id`

**4 cruxes** — where reviewers genuinely disagree:

**Crux 1 (0.67) — How to fix deduct+notify atomicity.** Reliability wants transactional outbox. Performance says fire-and-forget async with timeout is sufficient. Security wants both in same transaction.

**Crux 2 (1.0) — Where to enforce idempotency.** Reliability says DB-level unique constraint on idempotency key. Performance says Redis cache with TTL. Real trade-off: durability vs latency.

**Crux 3 (1.0) — notify_payment_service() hardening.** Reliability says move it outside the DB session entirely. Performance says `asyncio.wait_for` with timeout is pragmatic enough.

**Crux 4 (1.0) — Audit logging vs payment ledger.** Security frames it as PCI-DSS compliance. Reliability frames it as operational (dispute resolution, incident investigation). Same fix, different urgency framing.

The engine correctly refused to synthesize consensus — the reviewers agree on *what's wrong*, they only disagree on *how to fix it*.

### Rough edges observed

- **Tool name auto-repair**: Hermes MCP tools get a `mcp_` prefix. The LLM keeps generating `gemot_vote` instead of `mcp_gemot_vote`. Hermes auto-repairs every call (~80 repairs per run). Functionally correct but noisy.
- **Polling loop**: Analysis takes ~2 minutes. The agent polled `get_deliberation` ~25 times. A webhook/callback mechanism would be better.
- **Vote type coercion**: Hermes sends vote values as strings (`"1"`) not integers. We added coercion to handle both — fixed in this version.

## How it works

Gemot is an MCP server. Add 4 lines to your config:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 300
```

A [`deliberated-review` skill](skills/deliberated-review/SKILL.md) is included that teaches the agent the workflow: delegate → submit → vote → analyze → report cruxes. The skill uses the `review` template, which configures the analysis for structured review panels.

The full flow takes 3-5 minutes (delegate_task ~30s, cross-voting ~5s, gemot analysis ~2 min, polling ~1 min). Best for architecture decisions and thorny PRs, not every commit. Analysis costs ~$0.30 per run (Sonnet).

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
