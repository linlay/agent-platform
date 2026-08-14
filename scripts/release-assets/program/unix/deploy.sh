#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

. "$SCRIPT_DIR/scripts/program-common.sh"

cd "$SCRIPT_DIR"
program_apply_deploy_flags "$@"
echo "[program-deploy] validating bundle"
program_validate_bundle
echo "[program-deploy] bundle validated"
echo "[program-deploy] backend binary: $BACKEND_BIN"
echo "[program-deploy] initializing config under $CONFIG_DIR"
chat_resource_ticket_secret=""
if [[ "$DEPLOY_DESKTOP_CONFIG_RESET" == "1" ]]; then
  program_reset_desktop_config "$DEPLOY_DESKTOP_CONFIG_BACKUP_DIR"
  chat_resource_ticket_secret="$(program_read_env_literal_value "$DEPLOY_DESKTOP_CONFIG_BACKUP_DIR/.env" "AP_CHAT_RESOURCE_TICKET_SECRET" || true)"
fi
program_initialize_deploy_config
if [[ "$DEPLOY_DESKTOP_CONFIG_RESET" == "1" && -n "$chat_resource_ticket_secret" ]]; then
  program_set_env_value "$ENV_FILE" "AP_CHAT_RESOURCE_TICKET_SECRET" "$chat_resource_ticket_secret"
fi
if [[ -n "$DEPLOY_RUNTIME_RESOURCE_SOURCE" ]]; then
  echo "[program-deploy] synchronizing Platform runtime resources"
  program_sync_runtime_resources
fi
if [[ "$DEPLOY_DESKTOP_CONFIG_RESET" == "1" ]]; then
  program_secure_config_tree "$CONFIG_ROOT"
fi
echo "[program-deploy] config initialized: $CONFIG_DIR"
if [[ "$DEPLOY_DESKTOP_CONFIG_RESET" == "1" ]]; then
  echo "[program-deploy] Desktop config rebuilt: $DEPLOY_DESKTOP_VERSION_FROM -> $DEPLOY_DESKTOP_VERSION_TO"
fi
echo "[program-deploy] deploy complete"
