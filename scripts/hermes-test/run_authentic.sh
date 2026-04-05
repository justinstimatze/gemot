#!/usr/bin/env bash
# Authentic Hermes + Gemot test via the actual Hermes CLI.
# Uses --query for non-interactive mode — same code path as the TUI.
set -euo pipefail

cd /home/justin/Documents/hermes-agent
source venv/bin/activate

# Use isolated hermes home with gemot MCP configured
export HERMES_HOME=$(mktemp -d /tmp/hermes-auth-XXXXXX)

# Load API key from AI_Diplomacy .env
ANTHRO_KEY=$(grep ANTHROPIC_API_KEY /home/justin/Documents/AI_Diplomacy/.env | cut -d= -f2)

cat > "$HERMES_HOME/.env" <<EOF
ANTHROPIC_API_KEY=$ANTHRO_KEY
EOF

cat > "$HERMES_HOME/config.yaml" <<'YAML'
model:
  default: "anthropic/claude-sonnet-4-6"
  provider: "anthropic"
terminal:
  backend: "local"
mcp_servers:
  gemot:
    url: "https://gemot.dev/mcp"
    timeout: 180
YAML

echo "HERMES_HOME=$HERMES_HOME"
echo "=================================================="
echo "Running Hermes CLI with --query (same as TUI)"
echo "=================================================="

CODE='async def process_payment(user_id: str, amount: float, db: AsyncSession):
    user = await db.execute(select(User).where(User.id == user_id))
    user = user.scalar_one()
    if user.balance < amount:
        raise InsufficientFunds()
    user.balance -= amount
    await db.commit()
    await notify_payment_service(user_id, amount)
    return {"status": "success", "new_balance": user.balance}'

python3 cli.py --query "
I need you to review this payment code from multiple specialist perspectives, then find where the reviewers disagree.

\`\`\`python
$CODE
\`\`\`

Step 1: Use delegate_task in batch mode with 3 tasks — a security review, a reliability review, and a performance review of this code. Include the code in each task's context.

Step 2: Use the gemot MCP tools (mcp_gemot_deliberation action:create, mcp_gemot_participate action:submit_position, mcp_gemot_analyze action:run, mcp_gemot_participate action:get_context) to find where the reviewers disagree.

Step 3: Tell me what they agree on, where they disagree, and what I should fix first.
"
