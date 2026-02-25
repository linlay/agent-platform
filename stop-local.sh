#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$ROOT_DIR/release-local"

if [ -x "$RELEASE_DIR/stop-local.sh" ]; then
  exec "$RELEASE_DIR/stop-local.sh" "$@"
fi

if [ -x "$RELEASE_DIR/stop.sh" ]; then
  exec "$RELEASE_DIR/stop.sh" "$@"
fi

echo "[stop-local] release-local stop script not found. Run ./package-local.sh first." >&2
exit 1
