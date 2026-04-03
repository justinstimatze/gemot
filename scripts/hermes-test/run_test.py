#!/usr/bin/env python3
"""
Test Hermes Agent ↔ Gemot integration.

Spawns 3 Hermes agents that deliberate on a topic using gemot's MCP tools.
Each agent submits a position, votes on others, and reads the analysis.

Usage:
    source ~/Documents/hermes-agent/venv/bin/activate
    ANTHROPIC_API_KEY=sk-ant-... python3 scripts/hermes-test/run_test.py
"""

import os
import sys
import tempfile
from pathlib import Path

# Add hermes to path
sys.path.insert(0, str(Path.home() / "Documents" / "hermes-agent"))

# Load .env files manually (shell `source` doesn't export to Python)
from dotenv import load_dotenv

load_dotenv(Path.home() / "Documents" / "gemot" / ".env")
load_dotenv(Path.home() / "Documents" / "AI_Diplomacy" / ".env")

GEMOT_URL = os.environ.get("GEMOT_LIVE_URL", "http://localhost:9997/mcp")
GEMOT_SECRET = os.environ.get("GEMOT_API_SECRET", "")

# Create isolated hermes home with gemot MCP config
hermes_home = tempfile.mkdtemp(prefix="hermes-gemot-test-")
config = {
    "model": {
        "default": "anthropic/claude-sonnet-4-6",
    },
    "terminal": {
        "backend": "local",
    },
    "mcp_servers": {
        "gemot": {
            "url": GEMOT_URL,
            "timeout": 120,
        },
    },
}

config_path = Path(hermes_home) / "config.yaml"
import yaml

config_path.write_text(yaml.dump(config))
print(f"Hermes home: {hermes_home}")
print(f"Config: {config_path}")

os.environ["HERMES_HOME"] = hermes_home

# Now import hermes
from run_agent import AIAgent

TOPIC = "Should AI agents be required to identify themselves as non-human in all interactions?"

AGENTS = [
    {
        "name": "safety-researcher",
        "system": "You are an AI safety researcher. You believe transparency is critical for AI trust.",
        "prompt": f"""You have access to gemot deliberation tools via MCP. Do the following:

1. Use the create_deliberation tool to create a deliberation with topic: "{TOPIC}" and type: "reasoning"
2. Use submit_position to share your position. Your view: mandatory AI identification is essential for informed consent and preventing manipulation. Be specific and substantive (2-3 sentences).
3. Report back the deliberation_id so other agents can join.

Use the MCP tools available to you. Do NOT make up responses — actually call the tools.""",
    },
    {
        "name": "startup-founder",
        "system": "You are an AI startup founder who values practical outcomes over theoretical concerns.",
    },
    {
        "name": "ethicist",
        "system": "You are a digital ethics professor who sees nuance in both positions.",
    },
]


def run_test():
    print(f"\n{'=' * 60}")
    print("Hermes ↔ Gemot Integration Test")
    print(f"Topic: {TOPIC}")
    print(f"{'=' * 60}\n")

    # Step 1: First agent creates the deliberation
    print(f"[1/5] Agent '{AGENTS[0]['name']}' creating deliberation...")
    agent1 = AIAgent(
        model="anthropic/claude-sonnet-4-6",
        api_key=os.environ.get("ANTHROPIC_API_KEY"),
        provider="anthropic",
        max_iterations=10,
        quiet_mode=True,
        skip_context_files=True,
        skip_memory=True,
        persist_session=False,
        ephemeral_system_prompt=AGENTS[0]["system"],
    )
    result1 = agent1.run_conversation(AGENTS[0]["prompt"])
    response1 = result1.get("response", "")
    print(f"  Response: {response1[:200]}...")

    # Extract deliberation_id from the response
    delib_id = None
    for word in response1.split():
        word = word.strip(".,;:\"'`()")
        if len(word) == 36 and word.count("-") == 4:
            delib_id = word
            break

    if not delib_id:
        print("\n  ERROR: Could not extract deliberation_id from response.")
        print(f"  Full response: {response1}")
        return

    print(f"  Deliberation ID: {delib_id}\n")

    # Step 2-3: Other agents join and submit positions
    for i, agent_def in enumerate(AGENTS[1:], start=2):
        print(f"[{i}/5] Agent '{agent_def['name']}' submitting position...")
        agent = AIAgent(
            model="anthropic/claude-sonnet-4-6",
            api_key=os.environ.get("ANTHROPIC_API_KEY"),
            provider="anthropic",
            max_iterations=10,
            quiet_mode=True,
            skip_context_files=True,
            skip_memory=True,
            persist_session=False,
            ephemeral_system_prompt=agent_def["system"],
        )
        prompt = f"""You have access to gemot deliberation tools via MCP.

Use submit_position to share your position on this topic: "{TOPIC}"

The deliberation_id is: {delib_id}
Your agent_id is: {agent_def["name"]}

Share a specific, substantive position (2-3 sentences). Then vote on the other positions using the vote tool (get_positions first to see them, then vote +1 or -1 on each).

Use the MCP tools. Do NOT make up responses."""
        result = agent.run_conversation(prompt)
        print(f"  Response: {result.get('response', '')[:200]}...\n")

    # Step 4: Analyze
    print("[4/5] Running analysis...")
    analyst = AIAgent(
        model="anthropic/claude-sonnet-4-6",
        api_key=os.environ.get("ANTHROPIC_API_KEY"),
        provider="anthropic",
        max_iterations=15,
        quiet_mode=True,
        skip_context_files=True,
        skip_memory=True,
        persist_session=False,
    )
    analyze_result = analyst.run_conversation(
        f"Use the analyze tool on deliberation {delib_id}. Then use get_context with agent_id 'safety-researcher' to see the results. Report: how many cruxes were found, what are they, and is there any shared ground?"
    )
    print(f"  Analysis: {analyze_result.get('response', '')[:500]}...\n")

    # Step 5: Summary
    print("[5/5] Getting final context...")
    summary = analyst.run_conversation(
        f"Use get_context for deliberation {delib_id} with agent_id 'ethicist'. Summarize: relevant cruxes, consensus statements, bridging statements, and the compromise proposal if any."
    )
    print(f"\n{'=' * 60}")
    print("FINAL RESULT")
    print(f"{'=' * 60}")
    print(summary.get("response", ""))


if __name__ == "__main__":
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("Set ANTHROPIC_API_KEY first")
        sys.exit(1)
    run_test()
