# Compatibility cleanup checklist

This document tracks the compatibility paths that are intentionally retained after the first modularization pass. Do not remove these paths until the matching evidence stays clean for the agreed observation window.

## A1 removal candidates

- Agent `sandboxConfig`: remove after catalog definitions use `runtimeConfig`.

## Required checks before deletion

- Run `scripts/audit-compat-data.sh <CHATS_DIR> [CONFIG_ROOT]` against representative local or production data.
- Confirm config scans have zero `sandboxConfig` hits outside tests and docs.

## Keep for a separate cleanup decision

- Legacy run id parsing in `internal/chat/run_id.go`.
- Legacy single gateway fallback in config, gateway registry, and WS resource download resolution.
- Deprecated environment variable hard failures.
- `SystemInitLegacy` fallback for pre-system-init historical chats.
