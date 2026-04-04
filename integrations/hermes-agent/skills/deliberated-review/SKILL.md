---
name: deliberated-review
description: Review code with multiple specialist subagents, then use gemot to find where they disagree and why. Requires gemot MCP server configured.
version: 1.0.0
tags: [code-review, multi-agent, deliberation, gemot]
metadata:
  hermes:
    related_skills: [requesting-code-review, subagent-driven-development]
    requires_toolsets: [terminal, files, mcp]
---

# Deliberated Code Review

When you need a thorough code review from multiple perspectives, delegate to specialist reviewers and use gemot to find the crux of any disagreements.

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
- type: "reasoning"

### Step 3: Submit each reviewer's findings

For each subagent result, call `mcp_gemot_submit_position` with:
- deliberation_id: from step 2
- agent_id: the reviewer role (e.g., "security-reviewer")
- content: the reviewer's full findings

### Step 4: Analyze

Call `mcp_gemot_analyze`. Wait for completion by polling `mcp_gemot_get_deliberation` until status returns to "open".

### Step 5: Read the results

Call `mcp_gemot_get_context` with any reviewer's agent_id. Report to the user:

1. **What all reviewers agree on** — consensus statements (things to definitely fix)
2. **Where they disagree** — cruxes (the specific claims that divide them)
3. **Proposed compromise** — gemot's suggested resolution
4. **What the user should decide** — value judgments that can't be resolved with evidence

### Step 6 (optional): Refine

If the user wants deeper analysis, have each reviewer call `mcp_gemot_get_context` to see the cruxes, then submit a refined position addressing the disagreements. Re-analyze. Cruxes get more specific each round.

## Example output to user

> **All reviewers agree:** The balance check on line 4 has a TOCTOU race condition. The `notify_payment_service` call after commit has no error handling. The `amount` parameter should be Decimal, not float.
>
> **They disagree on:** Whether to use `SELECT FOR UPDATE` (security wants it for atomicity, performance says it creates lock contention at scale).
>
> **Gemot's take:** Use `SELECT FOR UPDATE` with a short lock timeout. The security risk of the race condition outweighs the performance cost at your current scale. Revisit if you exceed 1K transactions/second.
>
> **You decide:** Should `notify_payment_service` be inside or outside the transaction? Security says inside (atomicity), reliability says outside (don't hold DB connections during external calls). This is an architecture decision that depends on your failure mode preferences.
