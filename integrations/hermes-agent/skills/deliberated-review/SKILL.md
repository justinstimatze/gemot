---
name: deliberated-review
description: Review code with multiple specialist subagents, then use gemot to find where they disagree and why. Requires gemot MCP server configured.
version: 2.0.0
tags: [code-review, multi-agent, deliberation, gemot]
metadata:
  hermes:
    related_skills: [requesting-code-review, subagent-driven-development]
    requires_toolsets: [terminal, files, mcp]
---

# Deliberated Code Review

When you need a thorough code review from multiple perspectives, delegate to specialist reviewers, cross-vote, and use gemot to find the crux of any disagreements.

## When to Use

- PR review where security, performance, and correctness might conflict
- Architecture decisions where subagents return different recommendations
- Any multi-perspective review where you need to know WHY reviewers disagree, not just THAT they disagree

## Process

### Step 1: Delegate specialist reviews

Use `delegate_task` to spawn 3 independent reviewers. Each reviews the same code from their expertise:

```
delegate_task([
  {"goal": "Review this code for security vulnerabilities. Cite specific lines, explain attack vectors, suggest fixes.", "context": "<the code>"},
  {"goal": "Review this code for reliability and correctness. Cite specific lines, explain what breaks, suggest fixes.", "context": "<the code>"},
  {"goal": "Review this code for performance issues. Cite specific lines, estimate impact, suggest fixes.", "context": "<the code>"}
])
```

### Step 2: Create a gemot deliberation

Call `mcp_gemot_create_deliberation` with:
- topic: a one-line summary of what's being reviewed (e.g., "Payment processing code review")
- template: "review"
- group_id: a consistent identifier for the project or workflow (e.g., "myproject-reviews")

### Step 3: Submit each reviewer's findings

For each subagent result, call `mcp_gemot_submit_position` with:
- deliberation_id: from step 2
- agent_id: the reviewer role (e.g., "security-reviewer")
- content: the reviewer's full findings

### Step 4: Cross-vote

Call `mcp_gemot_get_positions` to fetch all positions. Then have each reviewer vote on the other reviewers' findings:

For each position, each OTHER reviewer calls `mcp_gemot_vote` with:
- deliberation_id: from step 2
- agent_id: the voting reviewer's ID
- position_id: the position being voted on
- value: 1 (agree with this finding), 0 (no opinion), or -1 (disagree)

This feeds gemot's clustering and crux detection with real signal about where reviewers actually agree vs disagree.

### Step 5: Analyze

Call `mcp_gemot_analyze`. Wait for completion by polling `mcp_gemot_get_deliberation` until status returns to "open" (poll every 5 seconds, allow up to 5 minutes).

### Step 6: Read the results

Call `mcp_gemot_get_analysis_result` for the full analysis and `mcp_gemot_get_context` with any reviewer's agent_id. Report to the user:

1. **What all reviewers agree on** — consensus statements (things to definitely fix)
2. **Where they disagree** — cruxes (the specific claims that divide them), with controversy scores
3. **Proposed compromise** — gemot's suggested resolution
4. **What the user should decide** — value judgments that can't be resolved with evidence

### Step 7 (optional): Commit to fixes

If the user agrees on what to fix, call `mcp_gemot_commit` for each agreed fix:
- deliberation_id: from step 2
- agent_id: "team" or the user's identifier
- statement: the specific fix commitment (e.g., "Add SELECT FOR UPDATE to prevent race condition")

These commitments are tracked and can be verified in future reviews via `mcp_gemot_agent_reputation`.

### Step 8 (optional): Refine

If the user wants deeper analysis, have each reviewer call `mcp_gemot_get_context` to see the cruxes, then submit a refined position addressing the disagreements. Re-analyze. Cruxes get more specific each round.

## Example output to user

> **All reviewers agree:** The balance check on line 4 has a TOCTOU race condition. The `notify_payment_service` call after commit has no error handling. The `amount` parameter should be Decimal, not float.
>
> **They disagree on:** Whether to use `SELECT FOR UPDATE` (security wants it for atomicity, performance says it creates lock contention at scale). Controversy: 67%.
>
> **Gemot's take:** Use `SELECT FOR UPDATE` with a short lock timeout. The security risk of the race condition outweighs the performance cost at your current scale.
>
> **You decide:** Should `notify_payment_service` be inside or outside the transaction? Security says inside (atomicity), reliability says outside (don't hold DB connections during external calls). This is a value crux — depends on your failure mode preferences.

## Tool naming

All gemot MCP tools use the `mcp_gemot_` prefix:
- `mcp_gemot_create_deliberation`
- `mcp_gemot_submit_position`
- `mcp_gemot_get_positions`
- `mcp_gemot_vote`
- `mcp_gemot_analyze`
- `mcp_gemot_get_deliberation`
- `mcp_gemot_get_analysis_result`
- `mcp_gemot_get_context`
- `mcp_gemot_commit`
- `mcp_gemot_fulfill_commitment`
- `mcp_gemot_break_commitment`
- `mcp_gemot_agent_reputation`
