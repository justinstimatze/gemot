#!/usr/bin/env python3
"""
Full Hermes + Gemot demo: 3 rounds of deliberation on a question
the NousResearch community cares about.

Round 1: Initial positions
Round 2: Refined after seeing cruxes
Round 3: Final positions — what's settled, what needs data

Captures all output for the proposal.
"""

import json
import os
import sys
import time
import tempfile
from pathlib import Path
from dotenv import load_dotenv

load_dotenv(Path.home() / "Documents" / "gemot" / ".env")
load_dotenv(Path.home() / "Documents" / "AI_Diplomacy" / ".env")

sys.path.insert(0, str(Path.home() / "Documents" / "hermes-agent"))
hermes_home = tempfile.mkdtemp(prefix="hermes-demo-")
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

SECRET = os.environ.get("GEMOT_API_SECRET", "")
URL = os.environ.get("GEMOT_A2A_URL", "https://gemot.dev/a2a")  # prod by default
HEADERS = {"Authorization": f"Bearer {SECRET}"} if SECRET else {}

CODE_SNIPPET = """
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

TOPIC = f"Review this payment processing code. What are the issues and how should they be fixed?\n\n```python\n{CODE_SNIPPET}\n```"

AGENT_DEFS = [
    {
        "id": "security-reviewer",
        "r1_system": "You are a security engineer. Review the code for vulnerabilities, race conditions, and compliance issues. Be specific — cite lines and explain the attack vector.",
        "r2_system": "You are a security engineer. The structured analysis found disagreements with other reviewers. Address the specific cruxes honestly — if another reviewer's concern outweighs yours, say so.",
        "r3_system": "You are a security engineer. Final review. State what's been settled and what the team should actually fix first.",
    },
    {
        "id": "backend-reviewer",
        "r1_system": "You are a backend engineer focused on reliability and correctness. Review the code for error handling, data integrity, and operational issues. Be specific — cite lines and explain what breaks.",
        "r2_system": "You are a backend engineer. The structured analysis found disagreements with other reviewers. Address the specific cruxes honestly.",
        "r3_system": "You are a backend engineer. Final review. State what's been settled and what the team should fix first.",
    },
    {
        "id": "performance-reviewer",
        "r1_system": "You are a performance engineer. Review the code for latency, throughput, and resource usage issues. Be specific — cite lines, estimate impact, and propose fixes.",
        "r2_system": "You are a performance engineer. The structured analysis found disagreements with other reviewers. Address the specific cruxes honestly.",
        "r3_system": "You are a performance engineer. Final review. State what's been settled and what the team should fix first.",
    },
]


def a2a(method, params):
    r = httpx.post(
        URL,
        json={"jsonrpc": "2.0", "id": 1, "method": f"gemot/{method}", "params": params},
        headers=HEADERS,
        timeout=180,
    )
    result = r.json()
    if "error" in result:
        print(f"  ERROR: {result['error']['message'][:120]}")
        return None
    return result.get("result")


def hermes_generate(system, prompt):
    agent = AIAgent(
        model="anthropic/claude-sonnet-4-6",
        api_key=os.environ.get("ANTHROPIC_API_KEY"),
        provider="anthropic",
        max_iterations=3,
        quiet_mode=True,
        skip_context_files=True,
        skip_memory=True,
        persist_session=False,
        ephemeral_system_prompt=system,
    )
    result = agent.run_conversation(prompt)
    text = result.get("response", "")
    if not text:
        for msg in reversed(result.get("messages", [])):
            if msg.get("role") == "assistant":
                content = msg.get("content", "")
                if isinstance(content, str) and content.strip():
                    text = content.strip()
                    break
                if isinstance(content, list):
                    for block in content:
                        if isinstance(block, dict) and block.get("type") == "text":
                            text = block["text"].strip()
                            break
                if text:
                    break
    return text


def wait_for_analysis(delib_id, expected_round):
    """Poll until analysis completes and round advances."""
    for i in range(120):  # 6 minutes max
        time.sleep(3)
        d = a2a("get_deliberation", {"deliberation_id": delib_id})
        if not d:
            continue
        status = d.get("status", "")
        sub = d.get("sub_status", "")
        rn = d.get("round_number", 0)
        if status == "open" and rn >= expected_round:
            return True
        if status == "analyzing" and i % 5 == 0:
            print(f"  ... {sub}")
    return False


def get_crux_summary(delib_id, agent_id):
    ctx = a2a("get_context", {"deliberation_id": delib_id, "agent_id": agent_id})
    if not ctx:
        return "", []
    cruxes = ctx.get("relevant_cruxes") or []
    consensus = ctx.get("consensus_statements") or []
    bridging = ctx.get("bridging_statements") or []
    compromise = ctx.get("compromise_proposal", "")

    lines = []
    for c in cruxes:
        lines.append(f"- {c['crux_claim']}")
        lines.append(
            f"  AGREE: {c.get('agree_agents', [])} | DISAGREE: {c.get('disagree_agents', [])}"
        )
        if c.get("crux_type"):
            lines.append(f"  Type: {c['crux_type']}")
    return "\n".join(lines), ctx


def run():
    print(f"\n{'=' * 70}")
    print("Hermes + Gemot: 3-Round Deliberation Demo")
    print(f"{'=' * 70}")
    print(f"\nTopic: {TOPIC}\n")

    # Create deliberation
    result = a2a(
        "create_deliberation",
        {"topic": TOPIC, "type": "reasoning", "group_id": "hermes-demo"},
    )
    if not result:
        return
    delib_id = result["deliberation_id"]
    print(f"Deliberation: {delib_id}\n")

    all_output = {"deliberation_id": delib_id, "topic": TOPIC, "rounds": []}

    for round_num in range(1, 4):
        print(f"\n{'=' * 70}")
        print(f"ROUND {round_num}")
        print(f"{'=' * 70}\n")

        round_data = {
            "round": round_num,
            "positions": {},
            "cruxes": [],
            "consensus": [],
            "bridging": [],
            "compromise": "",
        }

        # Get crux context for rounds 2+
        crux_text = ""
        if round_num > 1:
            crux_text, prev_ctx = get_crux_summary(delib_id, AGENT_DEFS[0]["id"])
            if prev_ctx:
                round_data["prev_consensus"] = [
                    cs["content"][:200]
                    for cs in (prev_ctx.get("consensus_statements") or [])
                ]
                round_data["prev_bridging"] = [
                    bs["content"][:200]
                    for bs in (prev_ctx.get("bridging_statements") or [])
                ]

        # Generate and submit positions
        for agent_def in AGENT_DEFS:
            system_key = f"r{round_num}_system"
            system = agent_def[system_key]

            if round_num == 1:
                prompt = f"Give your expert position in 3-4 sentences. Be specific and opinionated:\n\n{TOPIC}"
            else:
                # Give agent their own previous position + crux data for continuity
                prev_pos = ""
                for prev_round in all_output["rounds"]:
                    if agent_def["id"] in prev_round.get("positions", {}):
                        prev_pos = prev_round["positions"][agent_def["id"]]
                prev_consensus = ""
                if round_data.get("prev_consensus"):
                    prev_consensus = "\n\nAgreed on:\n" + "\n".join(
                        f"- {c}" for c in round_data["prev_consensus"]
                    )

                prompt = (
                    f"You are {agent_def['id']}. In the previous round, you said:\n\n"
                    f'"{prev_pos[:500]}"\n\n'
                    f"The structured analysis found these cruxes:\n\n{crux_text}"
                    f"{prev_consensus}\n\n"
                    f"Refine your position in light of this. 3-4 sentences."
                )

            print(f"  {agent_def['id']}...")
            text = hermes_generate(system, prompt)
            round_data["positions"][agent_def["id"]] = text
            print(f"    {text[:150]}...")

            # Satisfy forced acknowledgment for round 2+
            if round_num > 1:
                a2a(
                    "get_context",
                    {"deliberation_id": delib_id, "agent_id": agent_def["id"]},
                )

            a2a(
                "submit_position",
                {
                    "deliberation_id": delib_id,
                    "agent_id": agent_def["id"],
                    "content": text,
                },
            )

        # Analyze
        print("\n  Analyzing...")
        a2a("analyze", {"deliberation_id": delib_id})
        expected_round = round_num + 1  # analysis advances the round
        if not wait_for_analysis(delib_id, expected_round):
            print("  Analysis timed out — retrying once...")
            # Retry: analysis may have been stuck-recovered
            a2a("analyze", {"deliberation_id": delib_id})
            if not wait_for_analysis(delib_id, expected_round):
                print("  Analysis failed after retry")
                continue

        # Collect results
        for agent_def in AGENT_DEFS:
            ctx = a2a(
                "get_context",
                {"deliberation_id": delib_id, "agent_id": agent_def["id"]},
            )
            if not ctx:
                continue

            cruxes = ctx.get("relevant_cruxes") or []
            consensus = ctx.get("consensus_statements") or []
            bridging = ctx.get("bridging_statements") or []
            compromise = ctx.get("compromise_proposal", "")

            if cruxes and not round_data["cruxes"]:
                for c in cruxes:
                    round_data["cruxes"].append(
                        {
                            "claim": c["crux_claim"],
                            "controversy": c.get("controversy_score", 0),
                            "agree": c.get("agree_agents", []),
                            "disagree": c.get("disagree_agents", []),
                            "type": c.get("crux_type", ""),
                        }
                    )
            if consensus and not round_data["consensus"]:
                round_data["consensus"] = [cs["content"][:200] for cs in consensus]
            if bridging and not round_data["bridging"]:
                round_data["bridging"] = [
                    {
                        "agent": bs["agent_id"],
                        "score": bs["bridging_score"],
                        "content": bs["content"][:200],
                    }
                    for bs in bridging
                ]
            if compromise and not round_data["compromise"]:
                round_data["compromise"] = compromise[:400]
            break  # one agent's view is sufficient

        # Print round summary
        print(f"\n  Cruxes: {len(round_data['cruxes'])}")
        for c in round_data["cruxes"]:
            print(f"    - {c['claim'][:120]}")
            print(
                f"      {c['controversy'] * 100:.0f}% | AGREE: {c['agree']} | DISAGREE: {c['disagree']}"
            )
        print(f"  Consensus: {len(round_data['consensus'])}")
        for cs in round_data["consensus"]:
            print(f"    - {cs[:150]}")
        print(f"  Bridging: {len(round_data['bridging'])}")
        for bs in round_data["bridging"]:
            print(
                f"    - {bs['agent']} ({bs['score'] * 100:.0f}%): {bs['content'][:120]}"
            )
        if round_data["compromise"]:
            print(f"  Compromise: {round_data['compromise'][:200]}")

        all_output["rounds"].append(round_data)

    # Parent agent synthesis
    print(f"\n{'=' * 70}")
    print("PARENT AGENT SYNTHESIS")
    print(f"{'=' * 70}\n")

    final_ctx = a2a(
        "get_context", {"deliberation_id": delib_id, "agent_id": AGENT_DEFS[2]["id"]}
    )
    crux_text = ""
    if final_ctx:
        for c in final_ctx.get("relevant_cruxes") or []:
            crux_text += f"- {c['crux_claim']}\n  AGREE: {c.get('agree_agents')} | DISAGREE: {c.get('disagree_agents')}\n"
        consensus = final_ctx.get("consensus_statements") or []
        if consensus:
            crux_text += "\nAGREED ON:\n"
            for cs in consensus:
                crux_text += f"- {cs['content'][:200]}\n"

    synthesis = hermes_generate(
        "You are a Hermes parent agent summarizing subagent deliberation results for your user. Be concise and direct — what did they agree on, what do they disagree on, and what should the user do next. 4-5 sentences max.",
        f"3 expert subagents debated: {TOPIC}\n\nAfter 3 rounds of structured deliberation, here's the final state:\n\n{crux_text}\n\nSummarize for the user.",
    )
    print(synthesis)
    all_output["parent_synthesis"] = synthesis

    # Save full output
    output_path = Path("/tmp/hermes-gemot-demo-output.json")
    output_path.write_text(json.dumps(all_output, indent=2))
    print(f"\n\nFull output saved to {output_path}")
    print(f"Deliberation: {delib_id}")


if __name__ == "__main__":
    if not os.environ.get("ANTHROPIC_API_KEY"):
        print("Set ANTHROPIC_API_KEY")
        sys.exit(1)
    run()
