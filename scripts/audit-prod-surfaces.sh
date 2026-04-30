#!/usr/bin/env bash
# audit-prod-surfaces.sh — comprehensive audit of every public discovery
# surface gemot has, looking for drift, dead links, or stale metadata.
#
# Includes everything verify-deploy.sh checks, plus:
#   - MCP registry entry (if published) and its declared version
#   - HTTP status of key public pages
#   - HTTP status of the docs / pricing pages linked from README
#   - Sanity: /try, /watch route shapes
#
# Designed to be safe to run periodically (cron, CI weekly job, ad-hoc).
# Exits non-zero if any drift or broken surface is detected.
#
# Usage:
#   ./scripts/audit-prod-surfaces.sh                    # default https://gemot.dev
#   ./scripts/audit-prod-surfaces.sh https://staging...

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

HOST="${1:-https://gemot.dev}"
HOST="${HOST%/}"

LOCAL=$(grep -E '^const Version = ' internal/mcp/server.go | sed -E 's/.*"([^"]+)".*/\1/')
SERVER_JSON_NAME=$(jq -r '.name' server.json)

echo "================================================================"
echo " gemot prod-surface audit — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "================================================================"
echo "Host:                    $HOST"
echo "Local Version:           $LOCAL"
echo "MCP registry name:       $SERVER_JSON_NAME"
echo

fail=0

# 1. Health endpoint
HEALTH_JSON="$(curl -fsS --max-time 10 "$HOST/health" 2>/dev/null || echo "")"
if [[ -z "$HEALTH_JSON" ]]; then
  echo "[FAIL]  /health unreachable"
  fail=1
else
  ver=$(echo "$HEALTH_JSON" | jq -r '.version // ""')
  status=$(echo "$HEALTH_JSON" | jq -r '.status // ""')
  if [[ "$ver" == "$LOCAL" && "$status" == "ok" ]]; then
    echo "[ OK ]  /health           version=$ver status=$status"
  else
    echo "[FAIL]  /health           version=$ver status=$status (local=$LOCAL)"
    fail=1
  fi
fi

# 2. Agent card
CARD_JSON="$(curl -fsS --max-time 10 "$HOST/.well-known/agent-card.json" 2>/dev/null || echo "")"
if [[ -z "$CARD_JSON" ]]; then
  echo "[FAIL]  /.well-known/agent-card.json unreachable"
  fail=1
else
  ver=$(echo "$CARD_JSON" | jq -r '.version // ""')
  skill_count=$(echo "$CARD_JSON" | jq -r '.skills | length')
  if [[ "$ver" == "$LOCAL" ]]; then
    echo "[ OK ]  agent-card.json   version=$ver skills=$skill_count"
  else
    echo "[FAIL]  agent-card.json   version=$ver (local=$LOCAL)"
    fail=1
  fi
fi

# 3. MCP endpoint — initialize handshake
MCP_RESP="$(curl -fsS -X POST "$HOST/mcp" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"audit","version":"1.0"}}}' \
  --max-time 8 2>/dev/null || echo "")"
if echo "$MCP_RESP" | grep -q '"name":"gemot"'; then
  mcp_ver=$(echo "$MCP_RESP" | grep -oE '"version":"[^"]+"' | head -1 | sed -E 's/.*"version":"([^"]+)".*/\1/')
  if [[ "$mcp_ver" == "$LOCAL" ]]; then
    echo "[ OK ]  MCP /initialize   version=$mcp_ver"
  else
    echo "[FAIL]  MCP /initialize   version=$mcp_ver (local=$LOCAL)"
    fail=1
  fi
else
  echo "[FAIL]  MCP /initialize handshake did not return a valid serverInfo"
  fail=1
fi

# 4. MCP registry entry (best-effort — errors are downgraded to warnings
#    because the registry is in preview and the namespace may not be
#    published yet)
REG_RESP="$(curl -fsS --max-time 10 "https://registry.modelcontextprotocol.io/v0/servers?search=$SERVER_JSON_NAME" 2>/dev/null || echo "")"
# Registry response shape: {"servers":[{"server":{...}, "_meta":{...}}, ...]}
# Each entry wraps the server fields under .server, with publish metadata
# (isLatest, publishedAt, status) under ._meta.io.modelcontextprotocol.registry/official.
# Pick the entry with isLatest=true to handle older versions still being active.
if [[ -z "$REG_RESP" ]]; then
  echo "[WARN]  MCP registry      query failed (preview API may be down — non-fatal)"
elif echo "$REG_RESP" | jq -e --arg n "$SERVER_JSON_NAME" '.servers[]? | select(.server.name==$n)' >/dev/null 2>&1; then
  reg_ver=$(echo "$REG_RESP" | jq -r --arg n "$SERVER_JSON_NAME" '.servers[] | select(.server.name==$n) | select(._meta["io.modelcontextprotocol.registry/official"].isLatest==true) | .server.version' | head -1)
  if [[ -z "$reg_ver" ]]; then
    # No isLatest=true entry; fall back to first match
    reg_ver=$(echo "$REG_RESP" | jq -r --arg n "$SERVER_JSON_NAME" '.servers[] | select(.server.name==$n) | .server.version' | head -1)
  fi
  if [[ "$reg_ver" == "$LOCAL" ]]; then
    echo "[ OK ]  MCP registry      version=$reg_ver"
  else
    echo "[WARN]  MCP registry      version=$reg_ver (local=$LOCAL — re-publish with mcp-publisher)"
  fi
else
  echo "[WARN]  MCP registry      $SERVER_JSON_NAME not listed (run mcp-publisher to publish)"
fi

# 5. Public pages — must return 200. Sorted iteration so output is stable
#    across runs (bash associative-array iteration order is unspecified).
PAGES_200=(/ /pricing /docs)
for path in "${PAGES_200[@]}"; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST$path")
  if [[ "$code" == "200" ]]; then
    echo "[ OK ]  $HOST$path"
  else
    echo "[FAIL]  $HOST$path (HTTP $code)"
    fail=1
  fi
done

# 6. Routes that 404 without a code parameter (by design) — the audit
#    just confirms the route exists at all and the public page renders
#    when given a sentinel that we know does not match a real code.
#    /try must redirect (303) on bare GET; /watch/<bogus> must 404 cleanly.
TRY_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST/try")
case "$TRY_CODE" in
  200|303) echo "[ OK ]  $HOST/try (HTTP $TRY_CODE)";;
  *)       echo "[FAIL]  $HOST/try (HTTP $TRY_CODE — expected 200 or 303)"; fail=1;;
esac

WATCH_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST/watch/audit-sentinel-no-such-code")
case "$WATCH_CODE" in
  404|410) echo "[ OK ]  $HOST/watch/<bogus> (HTTP $WATCH_CODE — expected for unknown code)";;
  *)       echo "[FAIL]  $HOST/watch/<bogus> (HTTP $WATCH_CODE — expected 404/410 for unknown code)"; fail=1;;
esac

echo
if [[ $fail -ne 0 ]]; then
  echo "Audit found drift or broken surfaces. Investigate before relying on discoverability."
  exit 1
fi
echo "Audit clean — all public surfaces match local Version $LOCAL."
