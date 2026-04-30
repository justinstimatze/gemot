#!/usr/bin/env bash
# bump-version.sh — sync gemot's version across all surfaces in one shot.
#
# Updates:
#   - internal/mcp/server.go     (Version constant)
#   - server.json                (MCP registry submission)
#
# The agent-card.json served at /.well-known/agent-card.json reads Version at
# render time (see internal/mcp/agent_card.go) so it can never drift.
#
# Does NOT touch CHANGELOG.md — promote the Unreleased section by hand so
# the release notes stay deliberate.
#
# Usage:
#   ./scripts/bump-version.sh 0.12.0
#   ./scripts/bump-version.sh --check        # print current versions, no changes

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

SERVER_GO="internal/mcp/server.go"
SERVER_JSON="server.json"

current_server_go() {
  grep -E '^const Version = ' "$SERVER_GO" | sed -E 's/.*"([^"]+)".*/\1/'
}
current_server_json() {
  jq -r '.version' "$SERVER_JSON"
}

print_current() {
  printf '  %-50s %s\n' "$SERVER_GO"   "$(current_server_go)"
  printf '  %-50s %s\n' "$SERVER_JSON" "$(current_server_json)"
}

if [[ "${1:-}" == "--check" || "${1:-}" == "-c" ]]; then
  echo "Current versions:"
  print_current
  exit 0
fi

NEW_VERSION="${1:-}"
if [[ -z "$NEW_VERSION" ]]; then
  echo "Usage: $0 <new-version>     e.g. $0 0.12.0"
  echo "       $0 --check"
  echo
  echo "Current versions:"
  print_current
  exit 1
fi

if ! [[ "$NEW_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo "error: '$NEW_VERSION' is not a valid semver version (expected MAJOR.MINOR.PATCH)" >&2
  exit 1
fi

# Warn (don't block) on apparent backward bumps. `sort -V` handles
# semver ordering well enough for the common cases — pre-release
# suffixes will sort lexicographically, which is fine for catching
# obvious 0.12.0 → 0.11.0 mistakes.
CURRENT="$(current_server_go)"
if [[ -n "$CURRENT" && "$CURRENT" != "$NEW_VERSION" ]]; then
  newest=$(printf '%s\n%s\n' "$CURRENT" "$NEW_VERSION" | sort -V | tail -1)
  if [[ "$newest" == "$CURRENT" ]]; then
    echo "warning: $NEW_VERSION appears to be older than current $CURRENT — proceeding anyway"
  fi
fi

# Clean up the temp file even on failure mid-script.
tmp=""
cleanup() { [[ -n "$tmp" && -f "$tmp" ]] && rm -f "$tmp"; }
trap cleanup EXIT

echo "Before:"
print_current

# Go: const Version = "X.Y.Z"
sed -i -E "s/^(const Version = )\"[^\"]+\"/\1\"$NEW_VERSION\"/" "$SERVER_GO"

# server.json: use jq to keep formatting/key order intact
tmp="$(mktemp)"
jq --arg v "$NEW_VERSION" '.version = $v' "$SERVER_JSON" > "$tmp" && mv "$tmp" "$SERVER_JSON"
tmp=""

echo "After:"
print_current

# Sanity: both should now match
if [[ "$(current_server_go)" != "$NEW_VERSION" ]] || \
   [[ "$(current_server_json)" != "$NEW_VERSION" ]]; then
  echo "error: post-write check failed — versions did not all update to $NEW_VERSION" >&2
  exit 1
fi

echo
echo "Bumped to $NEW_VERSION across server.go and server.json."
echo "(agent-card.json reads Version at render time — no manual update needed.)"
echo "Next: promote CHANGELOG.md Unreleased section, build, test, deploy."
