#!/usr/bin/env bash
# install-githooks.sh — point git at the repo's tracked hooks directory.
#
# Run once per clone. Idempotent — re-running just resets the config back
# to the tracked location.
#
# To uninstall: git config --unset core.hooksPath

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

HOOKS_DIR="scripts/githooks"
if [[ ! -d "$HOOKS_DIR" ]]; then
  echo "error: $HOOKS_DIR not found" >&2
  exit 1
fi

git config core.hooksPath "$HOOKS_DIR"
chmod +x "$HOOKS_DIR"/* 2>/dev/null || true

echo "Hooks installed from $HOOKS_DIR:"
ls -1 "$HOOKS_DIR" | sed 's/^/  /'
echo
echo "git core.hooksPath = $(git config core.hooksPath)"
