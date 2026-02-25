#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$ROOT_DIR/release-local"

if [ -x "$RELEASE_DIR/start-local.sh" ]; then
  exec "$RELEASE_DIR/start-local.sh" "$@"
fi

if [ -x "$RELEASE_DIR/start.sh" ]; then
  exec "$RELEASE_DIR/start.sh" "$@"
fi

echo "[start-local] release-local start script not found. Run ./package-local.sh first." >&2
exit 1
