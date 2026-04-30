#!/usr/bin/env bash
# verify-deploy.sh — confirm that prod is running the same version as local.
#
# Hits gemot.dev/health and gemot.dev/.well-known/agent-card.json and asserts
# both report the local Version constant. Catches the "forgot to fly deploy"
# class of drift.
#
# Usage:
#   ./scripts/verify-deploy.sh                    # check default https://gemot.dev
#   ./scripts/verify-deploy.sh https://staging... # check a different host

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

HOST="${1:-https://gemot.dev}"
HOST="${HOST%/}"

# Local source-of-truth version
LOCAL=$(grep -E '^const Version = ' internal/mcp/server.go | sed -E 's/.*"([^"]+)".*/\1/')
if [[ -z "$LOCAL" ]]; then
  echo "error: could not parse Version constant from internal/mcp/server.go" >&2
  exit 1
fi

echo "Local:                                 $LOCAL"

fail=0

# /health → JSON {status, service, version}
HEALTH_JSON="$(curl -fsS --max-time 10 "$HOST/health" 2>/dev/null || true)"
if [[ -z "$HEALTH_JSON" ]]; then
  echo "FAIL: $HOST/health unreachable"
  fail=1
else
  HEALTH_VER=$(echo "$HEALTH_JSON" | jq -r '.version // ""')
  HEALTH_STATUS=$(echo "$HEALTH_JSON" | jq -r '.status // ""')
  printf '%-38s %s (status=%s)\n' "$HOST/health version:" "$HEALTH_VER" "$HEALTH_STATUS"
  if [[ "$HEALTH_VER" != "$LOCAL" ]]; then
    echo "  ↳ DRIFT: live /health version $HEALTH_VER != local $LOCAL"
    fail=1
  fi
  if [[ "$HEALTH_STATUS" != "ok" ]]; then
    echo "  ↳ status not 'ok'"
    fail=1
  fi
fi

# Agent card — read .version field
CARD_JSON="$(curl -fsS --max-time 10 "$HOST/.well-known/agent-card.json" 2>/dev/null || true)"
if [[ -z "$CARD_JSON" ]]; then
  echo "FAIL: $HOST/.well-known/agent-card.json unreachable"
  fail=1
else
  CARD_VER=$(echo "$CARD_JSON" | jq -r '.version // ""')
  printf '%-38s %s\n' "$HOST/.well-known/agent-card.json:" "$CARD_VER"
  if [[ "$CARD_VER" != "$LOCAL" ]]; then
    echo "  ↳ DRIFT: live agent-card version $CARD_VER != local $LOCAL"
    fail=1
  fi
fi

if [[ $fail -ne 0 ]]; then
  echo
  echo "Deploy verification FAILED. Run 'fly deploy' or investigate."
  exit 1
fi

echo
echo "Deploy verification OK — $HOST is running $LOCAL."
