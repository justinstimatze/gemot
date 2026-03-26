#!/usr/bin/env bash
# Wasteland ↔ Gemot A2A integration examples
# These use the JSON-RPC 2.0 endpoint at /a2a — no MCP client needed.
#
# Set your API key:
#   export GEMOT_KEY=gmt_...

set -euo pipefail
GEMOT_URL="${GEMOT_URL:-https://gemot.dev/a2a}"

# ── Helper ──────────────────────────────────────────────────────────
rpc() {
  local method="$1" params="$2"
  curl -s "$GEMOT_URL" -X POST \
    -H "Authorization: Bearer $GEMOT_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$method\",\"params\":$params}"
}

echo "=== Wasteland PR Dispute: Contributor vs Validator ==="
echo

# 1. Create deliberation for the dispute
echo "Creating deliberation..."
CREATE=$(rpc "create_deliberation" '{
  "topic": "PR #847: Add retry logic to task runner",
  "description": "Contributor added exponential backoff retry. Validator rejected: concerns about thundering herd and missing circuit breaker.",
  "type": "reasoning",
  "visibility": "private",
  "max_participants": 5
}')
DELIB_ID=$(echo "$CREATE" | jq -r '.result.deliberation_id')
echo "  Deliberation: $DELIB_ID"

# 2. Contributor's rig submits position
echo "Contributor submitting position..."
CONTRIB_POS=$(rpc "submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"contributor-rig-12\",
  \"content\": \"The retry logic uses exponential backoff with jitter (base 1s, max 30s, jitter ±500ms). This handles transient network failures gracefully. A circuit breaker is overkill for this use case — we only retry 3 times before giving up, so thundering herd is bounded by the parallelism limit (8 concurrent tasks).\",
  \"conviction\": 0.7,
  \"reservation\": \"Will not add a circuit breaker dependency for a 3-retry loop\"
}")
CONTRIB_POS_ID=$(echo "$CONTRIB_POS" | jq -r '.result.position_id')

# 3. Validator's rig submits position
echo "Validator submitting position..."
VALIDATOR_POS=$(rpc "submit_position" "{
  \"deliberation_id\": \"$DELIB_ID\",
  \"agent_id\": \"validator-rig-7\",
  \"content\": \"Exponential backoff without a circuit breaker is dangerous at scale. When the upstream service goes down, all 8 concurrent tasks will retry simultaneously. 8 * 3 retries = 24 requests in the first second. With 100 task runners in production, that's 2,400 requests. Need at minimum a shared failure counter or a circuit breaker.\",
  \"conviction\": 0.8,
  \"reservation\": \"Cannot approve without addressing the thundering herd at production scale\"
}")
VALIDATOR_POS_ID=$(echo "$VALIDATOR_POS" | jq -r '.result.position_id')

# 4. Cross-vote
echo "Voting..."
rpc "vote" "{\"deliberation_id\":\"$DELIB_ID\",\"agent_id\":\"contributor-rig-12\",\"position_id\":\"$VALIDATOR_POS_ID\",\"value\":-1}" > /dev/null
rpc "vote" "{\"deliberation_id\":\"$DELIB_ID\",\"agent_id\":\"validator-rig-7\",\"position_id\":\"$CONTRIB_POS_ID\",\"value\":-1}" > /dev/null

# 5. Analyze
echo "Analyzing (this takes ~30-60s)..."
ANALYZE=$(rpc "analyze" "{\"deliberation_id\":\"$DELIB_ID\"}")
echo "  Analysis started: $(echo "$ANALYZE" | jq -r '.result.status // .result.message // "ok"')"

# Poll for completion
for i in $(seq 1 60); do
  sleep 5
  STATUS=$(rpc "get_deliberation" "{\"deliberation_id\":\"$DELIB_ID\"}")
  CURRENT=$(echo "$STATUS" | jq -r '.result.status')
  SUB=$(echo "$STATUS" | jq -r '.result.sub_status // "none"')
  echo "  [$i] Status: $CURRENT / $SUB"
  if [ "$CURRENT" != "analyzing" ]; then
    break
  fi
done

# 6. Get context for each rig
echo
echo "=== Contributor's View ==="
rpc "get_context" "{\"deliberation_id\":\"$DELIB_ID\",\"agent_id\":\"contributor-rig-12\"}" | jq '.result'

echo
echo "=== Validator's View ==="
rpc "get_context" "{\"deliberation_id\":\"$DELIB_ID\",\"agent_id\":\"validator-rig-7\"}" | jq '.result'

# 7. Propose compromise
echo
echo "=== Compromise Proposal ==="
rpc "propose_compromise" "{\"deliberation_id\":\"$DELIB_ID\"}" | jq '.result'

echo
echo "Done. Deliberation ID: $DELIB_ID"
echo "Use this ID to reference the dispute in the stamp metadata."
