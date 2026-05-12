# Compatibility cleanup checklist

This document tracks the compatibility paths that are intentionally retained after the first modularization pass. Do not remove these paths until the matching evidence stays clean for the agreed observation window.

## A1 removal candidates

- WS `/api/upload` route alias: remove after gateways send `/api/pull` route types only.
- WS flat upload payload: remove after gateways send only nested `upload` payloads.
- Agent `sandboxConfig`: remove after catalog definitions use `runtimeConfig`.
- `toolType` viewport alias: remove after MCP, HITL, and tool registries use `viewportType`.
- `MemoryPrompt`: remove after prompt builders and preview code use `StaticMemoryPrompt` only.
- `system.debugPreCall`: remove after historical chat traces are migrated to `debug.preCall`.

## Required checks before deletion

- Run `scripts/audit-compat-data.sh <CHATS_DIR> [CONFIG_ROOT]` against representative local or production data.
- Confirm logs contain no `[compat-cleanup][ws-upload-alias]` entries for one release.
- Confirm logs contain no `[compat-cleanup][ws-upload-flat-payload]` entries for one release.
- Confirm config scans have zero `sandboxConfig`, `toolType`, `MemoryPrompt`, or `system.debugPreCall` hits outside tests and docs.

## Keep for a separate cleanup decision

- Legacy run id parsing in `internal/chat/run_id.go`.
- Legacy single gateway fallback in config, gateway registry, and WS resource download resolution.
- Deprecated environment variable hard failures.
- `SystemInitLegacy` fallback for pre-system-init historical chats.
