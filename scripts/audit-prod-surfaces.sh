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
  # 429 means the endpoint is live and correctly enforcing its documented
  # 3/IP/day sandbox-creation limit — not drift. Expected if this script (or
  # anything else from this IP) has already called /try a few times today.
  429)     echo "[ OK ]  $HOST/try (HTTP 429 — rate limit correctly enforced, not a failure)";;
  *)       echo "[FAIL]  $HOST/try (HTTP $TRY_CODE — expected 200, 303, or 429)"; fail=1;;
esac

WATCH_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST/watch/audit-sentinel-no-such-code")
case "$WATCH_CODE" in
  404|410) echo "[ OK ]  $HOST/watch/<bogus> (HTTP $WATCH_CODE — expected for unknown code)";;
  *)       echo "[FAIL]  $HOST/watch/<bogus> (HTTP $WATCH_CODE — expected 404/410 for unknown code)"; fail=1;;
esac

echo

# 7. Is-Agentic readiness surface — independent ground truth for the items
#    tracked against Vercel's "Is Agentic" audit (see CHANGELOG.md's
#    "Agent-readiness" entries). Written because that audit tool's own
#    report appears to cache: identical evidence text was returned across
#    multiple "rescans" while these exact conditions were being fixed and
#    verified live in between. Run this instead of trusting a rescan click.
echo "--- Is-Agentic readiness ---"

# 7a. Machine-readable files must exist with the right content-type.
declare -A MD_FILES=(
  ["/llms.txt"]="text/markdown"
  ["/robots.txt"]="text/plain"
  ["/sitemap.xml"]="application/xml"
  ["/openapi.json"]="application/json"
  ["/.well-known/mcp.json"]="application/json"
  ["/.well-known/oauth-authorization-server"]="application/json"
  ["/.well-known/agent-card.json"]="application/json"
)
for path in "${!MD_FILES[@]}"; do
  want="${MD_FILES[$path]}"
  ct=$(curl -s -o /dev/null -w '%{content_type}' --max-time 5 "$HOST$path")
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST$path")
  if [[ "$code" == "200" && "$ct" == "$want"* ]]; then
    echo "[ OK ]  $HOST$path ($ct)"
  else
    echo "[FAIL]  $HOST$path (HTTP $code, content-type=$ct, want $want)"
    fail=1
  fi
done

# 7b. Agent-friendly 404: real 404 status, markdown body by default, JSON
#     when explicitly requested — never a 200 app shell.
NF_CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST/audit-sentinel-nonexistent-path")
NF_CT=$(curl -s -o /dev/null -w '%{content_type}' --max-time 5 "$HOST/audit-sentinel-nonexistent-path")
if [[ "$NF_CODE" == "404" && "$NF_CT" == "text/markdown"* ]]; then
  echo "[ OK ]  404 default          markdown body, HTTP 404"
else
  echo "[FAIL]  404 default          HTTP $NF_CODE, content-type=$NF_CT (want 404 + text/markdown)"
  fail=1
fi
NF_JSON_CT=$(curl -s -o /dev/null -w '%{content_type}' -H 'Accept: application/json' --max-time 5 "$HOST/audit-sentinel-nonexistent-path")
if [[ "$NF_JSON_CT" == "application/json"* ]]; then
  echo "[ OK ]  404 Accept:json      application/json"
else
  echo "[FAIL]  404 Accept:json      content-type=$NF_JSON_CT (want application/json)"
  fail=1
fi

# 7c. Wrong-method on a method-restricted API route must return 405 + JSON,
#     not fall through to the (markdown) 404 handler. Regression guard for
#     the round-3 fix — Go's ServeMux doesn't match a mismatched method to
#     a method-qualified pattern, so this silently regresses if the
#     any-method fallback registration (methodNotAllowedJSON) is ever
#     removed from one of these routes without the other.
for path in /a2a /oauth/token /webhook/stripe; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST$path")
  ct=$(curl -s -o /dev/null -w '%{content_type}' --max-time 5 "$HOST$path")
  if [[ "$code" == "405" && "$ct" == "application/json"* ]]; then
    echo "[ OK ]  GET $path (wrong method) -> 405 application/json"
  else
    echo "[FAIL]  GET $path (wrong method) -> HTTP $code, content-type=$ct (want 405 + application/json)"
    fail=1
  fi
done

# 7d. Markdown content negotiation (acceptmarkdown.com): Accept: text/markdown
#     must get text/markdown back, and Vary: Accept must be present.
for path in / /docs; do
  MD_CT=$(curl -s -o /dev/null -w '%{content_type}' -H 'Accept: text/markdown' --max-time 5 "$HOST$path")
  VARY=$(curl -sD- -o /dev/null -H 'Accept: text/markdown' --max-time 5 "$HOST$path" | grep -i '^vary:' || true)
  if [[ "$MD_CT" == "text/markdown"* && "$VARY" == *"Accept"* ]]; then
    echo "[ OK ]  $HOST$path Accept:markdown -> text/markdown, Vary: Accept present"
  else
    echo "[FAIL]  $HOST$path Accept:markdown -> content-type=$MD_CT, vary='$VARY'"
    fail=1
  fi
done

# 7e. MCP handshake: initialize with the correct dual Accept header must
#     succeed and (round 3) return a plain application/json body, not an
#     SSE-wrapped one — some simple/strict MCP clients only parse the former.
MCP_INIT_HEADERS=$(curl -sD- -o /dev/null -X POST "$HOST/mcp" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"audit","version":"1.0"}}}' \
  --max-time 8 || echo "")
if echo "$MCP_INIT_HEADERS" | tr -d '\r' | grep -qi '^content-type: application/json$'; then
  echo "[ OK ]  MCP initialize      plain application/json response"
else
  ct=$(echo "$MCP_INIT_HEADERS" | grep -i '^content-type:' || echo "content-type: (missing)")
  echo "[FAIL]  MCP initialize      $ct (want application/json, not text/event-stream)"
  fail=1
fi

# 7f. Rate-limit headers on a genuinely public, unauthenticated endpoint —
#     /try, not /balance (which requires an API key most auditors won't have).
#     A real GET, not -I/HEAD: /try only accepts GET or POST and 405s a HEAD
#     before it ever sets the rate-limit headers, which would misreport this
#     as a gemot bug rather than a test-tooling one.
TRY_HEADERS=$(curl -sD- -o /dev/null -H 'Accept: application/json' --max-time 5 "$HOST/try" || echo "")
if echo "$TRY_HEADERS" | grep -qi '^ratelimit-limit:'; then
  echo "[ OK ]  /try                RateLimit-* headers present (unauthenticated)"
else
  echo "[FAIL]  /try                RateLimit-* headers missing"
  fail=1
fi

# 7g. OpenAPI spec: valid JSON, and the versioning policy is formalized
#     structurally (x-versioning-policy), not just described in prose.
OPENAPI_JSON=$(curl -fsS --max-time 5 "$HOST/openapi.json" 2>/dev/null || echo "")
if [[ -n "$OPENAPI_JSON" ]] && echo "$OPENAPI_JSON" | jq -e '."x-versioning-policy".header' >/dev/null 2>&1; then
  vheader=$(echo "$OPENAPI_JSON" | jq -r '."x-versioning-policy".header')
  echo "[ OK ]  openapi.json        x-versioning-policy.header=$vheader"
else
  echo "[FAIL]  openapi.json        x-versioning-policy missing or spec unreachable"
  fail=1
fi

# 7h/7i. Gemot-Version header and homepage JSON-LD. Both capture curl's
#        output into a variable FIRST, then grep the buffer — piping curl
#        directly into `grep -q` under `pipefail` is a real footgun: -q
#        exits as soon as it finds a match, SIGPIPEing curl mid-transfer,
#        and pipefail reports curl's SIGPIPE exit (nonzero) for the whole
#        pipeline even though grep found exactly what it was looking for.
HOMEPAGE_HEADERS=$(curl -sD- -o /dev/null --max-time 5 "$HOST/" || echo "")
if echo "$HOMEPAGE_HEADERS" | grep -qi '^gemot-version:'; then
  echo "[ OK ]  Gemot-Version       present on /"
else
  echo "[FAIL]  Gemot-Version       missing on /"
  fail=1
fi

HOMEPAGE_HTML=$(curl -fsS --max-time 5 "$HOST/" 2>/dev/null || echo "")
# Bash-native substring match, not `| grep -q`: the homepage is ~47KB, large
# enough that grep -q's early exit can SIGPIPE the writing end of the pipe
# before it finishes, and pipefail then reports THAT nonzero exit for the
# whole pipeline even though grep found its match — a false [FAIL] on a
# genuinely present tag. [[ == *pattern* ]] does the match with no subshell
# or pipe at all.
if [[ "$HOMEPAGE_HTML" == *"application/ld+json"* ]]; then
  echo "[ OK ]  homepage            JSON-LD present"
else
  echo "[FAIL]  homepage            JSON-LD missing"
  fail=1
fi

# 7j. Trust anchor pages.
for path in /about /contact /privacy; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$HOST$path")
  if [[ "$code" == "200" ]]; then
    echo "[ OK ]  $HOST$path"
  else
    echo "[FAIL]  $HOST$path (HTTP $code)"
    fail=1
  fi
done

echo
if [[ $fail -ne 0 ]]; then
  echo "Audit found drift or broken surfaces. Investigate before relying on discoverability."
  exit 1
fi
echo "Audit clean — all public surfaces match local Version $LOCAL, Is-Agentic readiness surface intact."
