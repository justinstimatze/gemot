#!/usr/bin/env python3
"""
Round 2: Hermes agents refine positions after seeing gemot cruxes.
Demonstrates multi-round deliberation — gemot's unique value over prompting.

Requires: run_finetuning_test.py completed first (deliberation exists with round 1 analysis).
"""

import os
import sys
import time
import tempfile
from pathlib import Path

from dotenv import load_dotenv

load_dotenv(Path.home() / "Documents" / "gemot" / ".env")
load_dotenv(Path.home() / "Documents" / "AI_Diplomacy" / ".env")

GEMOT_SECRET = os.environ.get("GEMOT_API_SECRET", "")
GEMOT_A2A = "http://localhost:9997/a2a"
GEMOT_HEADERS = {"Authorization": f"Bearer {GEMOT_SECRET}"} if GEMOT_SECRET else {}

sys.path.insert(0, str(Path.home() / "Documents" / "hermes-agent"))
hermes_home = tempfile.mkdtemp(prefix="hermes-r2-")
os.environ["HERMES_HOME"] = hermes_home
import yaml

(Path(hermes_home) / "config.yaml").write_text(
    yaml.dump(
        {
            "model": {"default": "anthropic/claude-sonnet-4-6"},
            "terminal": {"backend": "local"},
        }
    )
)

import httpx
from run_agent import AIAgent

DELIB_ID = "8d75c811-3181-46e0-bd6f-7a798fe84413"

AGENTS = [
    {
        "id": "open-weight-advocate",
        "system": "You are an ML efficiency researcher who strongly favors open-weight models and self-hosting. But you're intellectually honest — if a crux identifies a genuine weakness in your position, acknowledge it and adjust.",
    },
    {
        "id": "api-pragmatist",
        "system": "You are a pragmatic ML engineer who favors APIs for small teams. But you're intellectually honest — if a crux identifies a genuine weakness in your position, acknowledge it and adjust.",
    },
    {
        "id": "hybrid-architect",
        "system": "You are a systems architect who has deployed both approaches. You think the answer depends on specifics. You're intellectually honest — if a crux identifies a genuine weakness in your position, acknowledge it and adjust.",
    },
]


def a2a(method, params):
    r = httpx.post(
        GEMOT_A2A,
        json={"jsonrpc": "2.0", "id": 1, "method": f"gemot/{method}", "params": params},
        headers=GEMOT_HEADERS,
        timeout=120,
    )
    result = r.json()
    if "error" in result:
        print(f"  A2A error: {result['error']['message'][:100]}")
        return None
    return result.get("result")


def run():
    print(f"\n{'=' * 70}")
    print("Round 2: Agents refine positions after seeing cruxes")
    print(f"{'=' * 70}\n")

    # Step 1: Parent agent synthesizes crux data for the user
    print("[1/4] Parent agent reads cruxes and synthesizes for user...")
    ctx = a2a(
        "get_context", {"deliberation_id": DELIB_ID, "agent_id": "open-weight-advocate"}
    )
    if not ctx:
        print("  No context available — run run_finetuning_test.py first")
        return

    crux_summary = ""
    for c in ctx.get("relevant_cruxes", []):
        agree = ", ".join(c.get("agree_agents", []))
        disagree = ", ".join(c.get("disagree_agents", []))
        crux_summary += f"- {c['crux_claim']}\n  AGREE: {agree} | DISAGREE: {disagree}\n  Type: {c.get('crux_type', 'unknown')}\n\n"

    parent = AIAgent(
        model="anthropic/claude-sonnet-4-6",
        api_key=os.environ.get("ANTHROPIC_API_KEY"),
        provider="anthropic",
        max_iterations=3,
        quiet_mode=True,
        skip_context_files=True,
        skip_memory=True,
        persist_session=False,
        ephemeral_system_prompt="You are a Hermes parent agent summarizing subagent disagreements for your user on Telegram. Be concise and actionable — tell the user what the subagents agreed on, what they disagreed on, and what the user should check or decide. 3-4 sentences max.",
    )
    result = parent.run_conversation(
        f"Your subagents (open-weight-advocate, api-pragmatist, hybrid-architect) debated whether to fine-tune an open-weight model or use Claude Sonnet + RAG for a customer support chatbot (50K tickets, 2-person team). Here are the cruxes gemot identified:\n\n{crux_summary}\nSummarize this for the user. Be specific and actionable."
    )
    synthesis = result.get("response", "")
    if not synthesis:
        for msg in reversed(result.get("messages", [])):
            if msg.get("role") == "assistant" and isinstance(msg.get("content"), str):
                synthesis = msg["content"]
                break
    print(f"\n  PARENT AGENT SYNTHESIS:\n  {synthesis}\n")

    # Step 2: Each agent reads their cruxes (satisfies forced acknowledgment)
    print("[2/4] Agents read cruxes (forced acknowledgment)...")
    for agent_def in AGENTS:
        ctx = a2a(
            "get_context", {"deliberation_id": DELIB_ID, "agent_id": agent_def["id"]}
        )
        cruxes = ctx.get("relevant_cruxes", []) if ctx else []
        print(f"  {agent_def['id']}: {len(cruxes)} cruxes read")

    # Step 3: Each agent submits a refined position
    print("\n[3/4] Agents submit refined positions...")
    for agent_def in AGENTS:
        ctx = a2a(
            "get_context", {"deliberation_id": DELIB_ID, "agent_id": agent_def["id"]}
        )
        cruxes = ctx.get("relevant_cruxes", []) if ctx else []

        crux_text = ""
        for c in cruxes:
            crux_text += f"- {c['crux_claim']} (AGREE: {c.get('agree_agents')}, DISAGREE: {c.get('disagree_agents')})\n"

        agent = AIAgent(
            model="anthropic/claude-sonnet-4-6",
            api_key=os.environ.get("ANTHROPIC_API_KEY"),
            provider="anthropic",
            max_iterations=3,
            quiet_mode=True,
            skip_context_files=True,
            skip_memory=True,
            persist_session=False,
            ephemeral_system_prompt=agent_def["system"],
        )
        result = agent.run_conversation(
            f"You previously argued about whether to fine-tune open-weight vs use Claude+RAG for customer support. The crux analysis found these specific disagreements:\n\n{crux_text}\n\nGiven these cruxes, refine your position. Acknowledge where the other side has a point and adjust your recommendation if warranted. Be specific. 3-4 sentences."
        )
        refined = result.get("response", "")
        if not refined:
            for msg in reversed(result.get("messages", [])):
                if msg.get("role") == "assistant" and isinstance(
                    msg.get("content"), str
                ):
                    refined = msg["content"]
                    break

        # Submit refined position to gemot
        a2a(
            "submit_position",
            {
                "deliberation_id": DELIB_ID,
                "agent_id": agent_def["id"],
                "content": refined,
            },
        )
        print(f"\n  {agent_def['id']} (refined): {refined[:200]}...")

    # Step 4: Re-analyze
    print("\n[4/4] Re-analyzing (round 2)...")
    a2a("analyze", {"deliberation_id": DELIB_ID})
    for i in range(60):
        time.sleep(3)
        d = a2a("get_deliberation", {"deliberation_id": DELIB_ID})
        if d and d.get("status") == "open":
            print(f"  Analysis complete (round {d.get('round_number')})")
            break
        sub = d.get("sub_status", "") if d else ""
        if sub:
            print(f"  [{i}] {sub}")

    # Show round 2 results
    print(f"\n{'=' * 70}")
    print("ROUND 2 RESULTS")
    print(f"{'=' * 70}")
    ctx = a2a(
        "get_context", {"deliberation_id": DELIB_ID, "agent_id": "open-weight-advocate"}
    )
    if ctx:
        cruxes = ctx.get("relevant_cruxes", [])
        print(f"\nCruxes: {len(cruxes)}")
        for i, c in enumerate(cruxes):
            print(f"  {i + 1}. {c['crux_claim']}")
            print(f"     Controversy: {c.get('controversy_score', 0) * 100:.0f}%")
            print(f"     AGREE: {c.get('agree_agents', [])}")
            print(f"     DISAGREE: {c.get('disagree_agents', [])}")
            if c.get("crux_type"):
                print(f"     Type: {c['crux_type']}")
        if ctx.get("compromise_proposal"):
            print(f"\n  Compromise: {ctx['compromise_proposal'][:400]}")
        if ctx.get("consensus_statements"):
            print(f"\n  Consensus ({len(ctx['consensus_statements'])} statements)")
    else:
        print("  No results available")


if __name__ == "__main__":
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("Set ANTHROPIC_API_KEY")
        sys.exit(1)
    run()
