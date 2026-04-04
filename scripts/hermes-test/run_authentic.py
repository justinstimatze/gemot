#!/usr/bin/env python3
"""
Authentic Hermes + Gemot test: a single parent agent uses delegate_task
to spawn reviewer subagents, then uses gemot MCP tools to analyze
their disagreements. No scripting — the parent agent orchestrates.
"""

import os
import sys
import tempfile
from pathlib import Path
from dotenv import load_dotenv

load_dotenv(Path.home() / "Documents" / "gemot" / ".env")
load_dotenv(Path.home() / "Documents" / "AI_Diplomacy" / ".env")

sys.path.insert(0, str(Path.home() / "Documents" / "hermes-agent"))

hermes_home = tempfile.mkdtemp(prefix="hermes-auth-")
os.environ["HERMES_HOME"] = hermes_home

import yaml

(Path(hermes_home) / "config.yaml").write_text(
    yaml.dump(
        {
            "model": {"default": "anthropic/claude-sonnet-4-6"},
            "terminal": {"backend": "local"},
            "mcp_servers": {
                "gemot": {
                    "url": "https://gemot.dev/mcp",
                    "timeout": 180,
                },
            },
        }
    )
)

from run_agent import AIAgent

CODE = """
async def process_payment(user_id: str, amount: float, db: AsyncSession):
    user = await db.execute(select(User).where(User.id == user_id))
    user = user.scalar_one()

    if user.balance < amount:
        raise InsufficientFunds()

    user.balance -= amount
    await db.commit()

    await notify_payment_service(user_id, amount)
    return {"status": "success", "new_balance": user.balance}
""".strip()

PROMPT = f"""I need you to review this payment processing code from multiple specialist perspectives, then find where the reviewers disagree.

Here's the code:
```python
{CODE}
```

Do this in order:

1. Use delegate_task in batch mode with 3 tasks:
   - Task 1: "Review this code for security vulnerabilities. Cite lines, explain attack vectors." (include the code in context)
   - Task 2: "Review this code for reliability and error handling. Cite lines, explain what breaks." (include the code in context)
   - Task 3: "Review this code for performance issues. Cite lines, estimate impact." (include the code in context)

2. Once you have the 3 review results, use the gemot MCP tools:
   - Create a deliberation (topic: "Payment code review", type: "reasoning")
   - Submit each reviewer's findings as a position (agent_id: "security-reviewer", "reliability-reviewer", "performance-reviewer")
   - Call analyze
   - Poll get_deliberation until status is "open" again
   - Call get_context for one reviewer

3. Report to me:
   - What all reviewers agree on
   - Where they disagree (the cruxes)
   - What I should fix first
"""

print(f"{'=' * 70}")
print("Authentic Hermes + Gemot: Parent agent orchestrates everything")
print(f"{'=' * 70}\n")

agent = AIAgent(
    model="anthropic/claude-sonnet-4-6",
    api_key=os.environ.get("ANTHROPIC_API_KEY"),
    provider="anthropic",
    max_iterations=30,  # needs room for delegate + gemot calls
    quiet_mode=False,  # show tool calls
    skip_context_files=True,
    skip_memory=True,
    persist_session=False,
    ephemeral_system_prompt="You are a senior engineering lead. You delegate code reviews to specialists and use structured analysis tools to find where reviewers disagree. Follow the user's instructions step by step.",
)

result = agent.run_conversation(PROMPT)

print(f"\n{'=' * 70}")
print("RESULT")
print(f"{'=' * 70}")
print(result.get("response", ""))
