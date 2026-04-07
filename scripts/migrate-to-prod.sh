#!/usr/bin/env bash
# migrate-to-prod.sh — copies deliberation data from local Postgres to Fly.io prod.
#
# Usage:
#   ./scripts/migrate-to-prod.sh --group t3c-gemotvis
#   ./scripts/migrate-to-prod.sh --deliberation <uuid>
#
# Requires: fly CLI authenticated, local DATABASE_URL set (or in .env).
# Opens a Fly proxy tunnel, runs migrate-demos, then closes the tunnel.

set -euo pipefail

# Load .env if present
if [ -f .env ]; then
  export $(grep -v '^#' .env | grep DATABASE_URL | xargs)
fi

LOCAL_DB="${DATABASE_URL:?DATABASE_URL not set}"
PROXY_PORT=15432
FLY_APP="gemot"
FLY_DB_APP="gemot-db"

if [ $# -eq 0 ]; then
  echo "Usage: $0 --group <group_id> | --deliberation <uuid> [--dry-run]"
  echo ""
  echo "Copies deliberation data from local Postgres to Fly.io prod."
  echo "Options are passed directly to scripts/migrate-demos/."
  exit 1
fi

# Get prod DB credentials from the running app
echo "Fetching prod DATABASE_URL..."
PROD_INTERNAL=$(fly ssh console -a $FLY_APP -C "printenv DATABASE_URL" 2>/dev/null | head -1)
if [ -z "$PROD_INTERNAL" ]; then
  echo "ERROR: Could not fetch DATABASE_URL from $FLY_APP"
  exit 1
fi

# Extract user:pass from the internal URL, rewrite to use local proxy
PROD_USERPASS=$(echo "$PROD_INTERNAL" | sed 's|postgres://\([^@]*\)@.*|\1|')
PROD_DB="postgres://${PROD_USERPASS}@localhost:${PROXY_PORT}/gemot?sslmode=disable"

echo "Starting Fly proxy tunnel to $FLY_DB_APP on port $PROXY_PORT..."
fly proxy $PROXY_PORT:5432 -a $FLY_DB_APP &
PROXY_PID=$!
trap "kill $PROXY_PID 2>/dev/null || true" EXIT

# Wait for proxy to be ready
sleep 3

echo "Migrating from local → prod..."
go run scripts/migrate-demos/ --from "$LOCAL_DB" --to "$PROD_DB" "$@"

echo "Done."
