#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
RELEASE_DIR="$ROOT_DIR/release-local"

if [ -x "$RELEASE_DIR/start-local.sh" ]; then
  exec "$RELEASE_DIR/start-local.sh" "$@"
fi

if [ -x "$RELEASE_DIR/start.sh" ]; then
  exec "$RELEASE_DIR/start.sh" "$@"
fi

echo "[start-local] release-local start script not found." >&2
echo "[start-local] run ./release-scripts/mac/package-local.sh first to generate release-local artifacts." >&2
exit 1
