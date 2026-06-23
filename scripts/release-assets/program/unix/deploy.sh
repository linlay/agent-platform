#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

. "$SCRIPT_DIR/scripts/program-common.sh"

cd "$SCRIPT_DIR"
program_apply_layout_flags "$@"
echo "[program-deploy] validating bundle"
program_validate_bundle
echo "[program-deploy] bundle validated"
echo "[program-deploy] backend binary: $BACKEND_BIN"
echo "[program-deploy] initializing config under $CONFIG_DIR"
program_initialize_config
echo "[program-deploy] config initialized: $CONFIG_DIR"
echo "[program-deploy] loading env: $ENV_FILE"
program_load_env
echo "[program-deploy] env loaded"
echo "[program-deploy] preparing runtime dirs under $RUNTIME_ROOT and $RUN_DIR"
program_prepare_runtime_dirs
echo "[program-deploy] runtime directories prepared under $RUNTIME_ROOT and $RUN_DIR"
echo "[program-deploy] deploy complete"
