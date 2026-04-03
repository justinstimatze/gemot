#!/usr/bin/env python3
"""
Hermes + Gemot integration test: Fine-tuning methodology debate.

3 Hermes agents with different expert perspectives deliberate on a
question the NousResearch community cares about. Gemot analyzes the
disagreement and finds the crux.

Usage:
    source ~/Documents/hermes-agent/venv/bin/activate
    python3 scripts/hermes-test/run_finetuning_test.py
"""

import json
import os
import sys
import tempfile
from pathlib import Path

# Load env
from dotenv import load_dotenv

load_dotenv(Path.home() / "Documents" / "gemot" / ".env")
load_dotenv(Path.home() / "Documents" / "AI_Diplomacy" / ".env")

GEMOT_SECRET = os.environ.get("GEMOT_API_SECRET", "")
GEMOT_A2A = "http://localhost:9997/a2a"  # local gemot
GEMOT_HEADERS = {"Authorization": f"Bearer {GEMOT_SECRET}"} if GEMOT_SECRET else {}

# Add hermes to path
sys.path.insert(0, str(Path.home() / "Documents" / "hermes-agent"))

# Isolated hermes home
hermes_home = tempfile.mkdtemp(prefix="hermes-gemot-ft-")
os.environ["HERMES_HOME"] = hermes_home

import yaml

config_path = Path(hermes_home) / "config.yaml"
config_path.write_text(
    yaml.dump(
        {
            "model": {"default": "anthropic/claude-sonnet-4-6"},
            "terminal": {"backend": "local"},
            "mcp_servers": {
                "gemot": {
                    "url": "http://localhost:9997/mcp",
                    "timeout": 120,
                },
            },
        }
    )
)

import httpx
from run_agent import AIAgent

TOPIC = "We're building a customer support chatbot. Should we fine-tune an open-weight model (Llama 3.3 8B) on our support tickets, or use a closed API (Claude Sonnet) with RAG over our knowledge base? We have 50K support tickets and a 2-person ML team."

AGENTS = [
    {
        "id": "open-weight-advocate",
        "system": "You are a strong advocate for open-weight models and self-hosted ML infrastructure. You believe in data sovereignty, long-term cost control, and the ability to customize models deeply. You're skeptical of API dependency. Be specific about numbers and trade-offs, and take a clear position.",
        "prompt": f"Give your expert position on this question in 3-4 sentences. Be specific and opinionated, not hedging:\n\n{TOPIC}",
    },
    {
        "id": "api-pragmatist",
        "system": "You are a pragmatic ML engineer who optimizes for time-to-value and team velocity. You believe most teams shouldn't train models when APIs can solve the problem faster. Be specific about the real costs and risks, and take a clear position.",
        "prompt": f"Give your expert position on this question in 3-4 sentences. Be specific and opinionated, not hedging:\n\n{TOPIC}",
    },
    {
        "id": "hybrid-architect",
        "system": "You are a systems architect who has deployed both approaches in production. You believe the answer depends on specific requirements that most people don't think about upfront. Push back on both extremes and identify the hidden factors. Be specific and take a clear position.",
        "prompt": f"Give your expert position on this question in 3-4 sentences. Be specific and opinionated, not hedging:\n\n{TOPIC}",
    },
]


def a2a(method, params):
    """Call gemot A2A endpoint."""
    r = httpx.post(
        GEMOT_A2A,
        json={
            "jsonrpc": "2.0",
            "id": 1,
            "method": f"gemot/{method}",
            "params": params,
        },
        headers=GEMOT_HEADERS,
        timeout=120,
    )
    result = r.json()
    if "error" in result:
        print(f"  A2A error: {result['error']}")
        return None
    return result.get("result")


def run_test():
    print(f"\n{'=' * 70}")
    print("Hermes + Gemot: Fine-Tuning Methodology Debate")
    print(f"{'=' * 70}")
    print(f"\nQuestion: {TOPIC}\n")

    # Step 1: Create deliberation via A2A
    print("[1/5] Creating deliberation...")
    result = a2a(
        "create_deliberation",
        {
            "topic": TOPIC,
            "type": "reasoning",
            "group_id": "hermes-finetuning-test",
        },
    )
    if not result:
        print("Failed to create deliberation")
        return
    delib_id = result["deliberation_id"]
    print(f"  Deliberation: {delib_id}\n")

    # Step 2: Each Hermes agent generates their position
    positions = {}
    for i, agent_def in enumerate(AGENTS):
        print(f"[2.{i + 1}/5] Hermes agent '{agent_def['id']}' generating position...")
        agent = AIAgent(
            model="anthropic/claude-sonnet-4-6",
            api_key=os.environ.get("ANTHROPIC_API_KEY"),
            provider="anthropic",
            max_iterations=5,
            quiet_mode=True,
            skip_context_files=True,
            skip_memory=True,
            persist_session=False,
            ephemeral_system_prompt=agent_def["system"],
        )
        result = agent.run_conversation(agent_def["prompt"])
        response = result.get("response", "").strip()
        if not response:
            # Try extracting from messages
            for msg in reversed(result.get("messages", [])):
                if msg.get("role") == "assistant" and msg.get("content"):
                    if isinstance(msg["content"], str):
                        response = msg["content"].strip()
                    elif isinstance(msg["content"], list):
                        for block in msg["content"]:
                            if isinstance(block, dict) and block.get("type") == "text":
                                response = block["text"].strip()
                    if response:
                        break

        positions[agent_def["id"]] = response
        print(f"  {agent_def['id']}: {response[:150]}...\n")

    # Step 3: Submit positions to gemot via A2A
    print("[3/5] Submitting positions to gemot...")
    position_ids = {}
    for agent_id, content in positions.items():
        result = a2a(
            "submit_position",
            {
                "deliberation_id": delib_id,
                "agent_id": agent_id,
                "content": content,
            },
        )
        if result:
            position_ids[agent_id] = result["position_id"]
            print(f"  Submitted {agent_id}")

    # Step 4: Cross-vote (each agent votes on others)
    print("\n[4/5] Voting and analyzing...")
    for voter_id in positions:
        for pos_agent, pos_id in position_ids.items():
            if pos_agent != voter_id:
                # Efficiency disagrees with quality/pragmatist, etc.
                # Let gemot figure out the clustering — vote based on agent perspective
                vote = (
                    1 if voter_id == pos_agent else 0
                )  # pass on others, let content analysis work
                a2a(
                    "vote",
                    {
                        "deliberation_id": delib_id,
                        "agent_id": voter_id,
                        "position_id": pos_id,
                        "value": vote,
                    },
                )

    # Analyze
    result = a2a("analyze", {"deliberation_id": delib_id})
    print("  Analysis started...")

    # Poll
    for i in range(60):
        import time

        time.sleep(3)
        result = a2a("get_deliberation", {"deliberation_id": delib_id})
        if result and result.get("status") == "open":
            print(f"  Analysis complete (round {result.get('round_number')})")
            break
        sub = result.get("sub_status", "") if result else ""
        if sub:
            print(f"  [{i}] {sub}")

    # Step 5: Get results
    print("\n[5/5] Results\n")
    print(f"{'=' * 70}")

    for agent_id in positions:
        ctx = a2a(
            "get_context",
            {
                "deliberation_id": delib_id,
                "agent_id": agent_id,
            },
        )
        if not ctx:
            continue

        print(f"\n--- {agent_id} ---")
        print(f"Cluster: {ctx.get('cluster_id')}")
        print(f"Allies: {ctx.get('nearest_allies', [])}")
        print(f"Disagreements with: {ctx.get('biggest_disagreements_with', [])}")

        if ctx.get("relevant_cruxes"):
            print(f"\nCruxes ({len(ctx['relevant_cruxes'])}):")
            for j, crux in enumerate(ctx["relevant_cruxes"]):
                print(f"  {j + 1}. {crux['crux_claim']}")
                print(
                    f"     Controversy: {crux.get('controversy_score', 0) * 100:.0f}%"
                )
                if crux.get("agree_agents"):
                    print(f"     AGREE: {crux['agree_agents']}")
                if crux.get("disagree_agents"):
                    print(f"     DISAGREE: {crux['disagree_agents']}")
                if crux.get("crux_type"):
                    print(f"     Type: {crux['crux_type']}")
                print(f"     {crux.get('explanation', '')[:200]}")

        if ctx.get("topic_summaries"):
            print(f"\nTopics ({len(ctx['topic_summaries'])}):")
            for ts in ctx["topic_summaries"]:
                print(f"  - {ts['topic']}")

        if ctx.get("compromise_proposal"):
            print(f"\nCompromise: {ctx['compromise_proposal'][:300]}")

        if ctx.get("strategic_nudge"):
            print(f"\nStrategic nudge: {ctx['strategic_nudge'][:200]}")

        break  # One agent's view is enough for the proposal

    print(f"\n{'=' * 70}")
    print(f"Deliberation ID: {delib_id}")
    print(f"Positions: {json.dumps(positions, indent=2)}")


if __name__ == "__main__":
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("Set ANTHROPIC_API_KEY")
        sys.exit(1)
    run_test()
