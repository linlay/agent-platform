#!/usr/bin/env bash
set -euo pipefail
DICTATION_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_DIR="$DICTATION_ROOT"
STATE_DIR="$DICTATION_ROOT/run"
LOG_DIR="$DICTATION_ROOT/run"
PORT=""
TOKEN_FILE=""
REGISTRY_DIR=""
DAEMON=0
dictation_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --daemon) DAEMON=1; shift ;;
      --config-dir|--output-dir) CONFIG_DIR="$2"; shift 2 ;;
      --state-dir) STATE_DIR="$2"; shift 2 ;;
      --log-dir) LOG_DIR="$2"; shift 2 ;;
      --port) PORT="$2"; shift 2 ;;
      --sso-id-token-file) TOKEN_FILE="$2"; shift 2 ;;
      --registry-dir) REGISTRY_DIR="$2"; shift 2 ;;
      *) echo "Unsupported dictation argument: $1" >&2; exit 1 ;;
    esac
  done
  [[ "$CONFIG_DIR" == /* ]] || CONFIG_DIR="$PWD/$CONFIG_DIR"
  [[ "$STATE_DIR" == /* ]] || STATE_DIR="$PWD/$STATE_DIR"
  [[ "$LOG_DIR" == /* ]] || LOG_DIR="$PWD/$LOG_DIR"
  BIN="$DICTATION_ROOT/backend/agent-dictation"
  PID_FILE="$STATE_DIR/agent-dictation.pid"
}
dictation_pid() {
  [[ -f "$PID_FILE" ]] || return 1
  local process_id command
  process_id="$(cat "$PID_FILE")"
  [[ "$process_id" =~ ^[0-9]+$ ]] && kill -0 "$process_id" 2>/dev/null || return 1
  command="$(ps -p "$process_id" -o args=)"
  [[ "$command" == "$BIN --config "* ]] || { echo "PID belongs to another executable; refusing control" >&2; return 2; }
  printf '%s\n' "$process_id"
}
