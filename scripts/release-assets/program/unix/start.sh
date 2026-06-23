#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

. "$SCRIPT_DIR/scripts/program-common.sh"

main() {
  local mode=""
  local layout_args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --daemon)
        mode="--daemon"
        shift
        ;;
      *)
        layout_args+=("$1")
        shift
        ;;
    esac
  done

  cd "$SCRIPT_DIR"
  program_apply_layout_flags "${layout_args[@]}"
  program_validate_bundle
  program_load_env
  program_apply_server_port_env
  program_prepare_runtime_dirs

  if [[ "$mode" == "--daemon" ]]; then
    program_prepare_log_file
    program_start_backend_daemon
    return
  fi

  program_prepare_log_file
  program_exec_backend
}

main "$@"
