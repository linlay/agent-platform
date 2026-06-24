#!/usr/bin/env bash
set -euo pipefail

PROGRAM_COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUNDLE_ROOT="$(cd "$PROGRAM_COMMON_DIR/.." && pwd)"
APP_NAME="agent-platform"
MANIFEST_FILE="$BUNDLE_ROOT/manifest.json"
ENV_EXAMPLE_FILE="$BUNDLE_ROOT/.env.example"
CONFIG_ROOT="$BUNDLE_ROOT"
ENV_FILE="$CONFIG_ROOT/.env"
BACKEND_BIN="$BUNDLE_ROOT/backend/$APP_NAME"
CONFIG_DIR="$CONFIG_ROOT/configs"
RUNTIME_ROOT="$BUNDLE_ROOT/runtime"
RUNTIME_ROOT_EXPLICIT=0
RUN_DIR="$BUNDLE_ROOT/run"
LOG_DIR="$RUN_DIR"
LOG_FILE="$LOG_DIR/$APP_NAME.log"
PID_FILE="$RUN_DIR/$APP_NAME.pid"
PROGRAM_PORT=""
BACKEND_ARGS=()

program_die() {
  echo "[program] $*" >&2
  exit 1
}

program_require_file() {
  local path="$1"
  [[ -f "$path" ]] || program_die "required file not found: $path"
}

program_require_dir() {
  local path="$1"
  [[ -d "$path" ]] || program_die "required directory not found: $path"
}

program_validate_bundle() {
  program_require_file "$MANIFEST_FILE"
  program_require_file "$ENV_EXAMPLE_FILE"
  [[ -x "$BACKEND_BIN" ]] || program_die "backend binary is not executable: $BACKEND_BIN"
}

program_refresh_paths() {
  ENV_FILE="$CONFIG_ROOT/.env"
  CONFIG_DIR="$CONFIG_ROOT/configs"
  LOG_FILE="$LOG_DIR/$APP_NAME.log"
  PID_FILE="$RUN_DIR/$APP_NAME.pid"
}

program_apply_layout_flags() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --config-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --config-dir"
        CONFIG_ROOT="$2"
        shift 2
        ;;
      --runtime-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --runtime-dir"
        RUNTIME_ROOT="$2"
        RUNTIME_ROOT_EXPLICIT=1
        shift 2
        ;;
      --state-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --state-dir"
        RUN_DIR="$2"
        shift 2
        ;;
      --log-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --log-dir"
        LOG_DIR="$2"
        shift 2
        ;;
      --port)
        [[ $# -ge 2 ]] || program_die "missing value for --port"
        PROGRAM_PORT="$2"
        shift 2
        ;;
      *)
        program_die "unsupported argument: $1"
        ;;
    esac
  done
  program_refresh_paths
}

program_initialize_config() {
  mkdir -p "$CONFIG_DIR"
  if [[ ! -f "$ENV_FILE" ]]; then
    cp "$ENV_EXAMPLE_FILE" "$ENV_FILE"
  fi
  if [[ -d "$BUNDLE_ROOT/configs" ]]; then
    local example source target name
    for example in "$BUNDLE_ROOT"/configs/*.example.yml; do
      [[ -f "$example" ]] || continue
      name="$(basename "$example" .example.yml)"
      target="$CONFIG_DIR/$name.yml"
      if [[ "$name" == "channels" ]]; then
        [[ -f "$target" ]] || : >"$target"
        continue
      fi
      [[ -f "$target" ]] || cp "$example" "$target"
    done
    for source in "$BUNDLE_ROOT"/configs/*.example.pem; do
      [[ -f "$source" ]] || continue
      name="$(basename "$source" .example.pem)"
      target="$CONFIG_DIR/$name.pem"
      [[ -f "$target" ]] || cp "$source" "$target"
    done
  fi
}

program_load_env() {
  [[ -f "$ENV_FILE" ]] || program_die "missing .env (copy from .env.example first)"
  set -a
  # shellcheck disable=SC1091
  . "$ENV_FILE"
  set +a
}

program_apply_server_port_env() {
  if [[ -z "${SERVER_PORT:-}" ]]; then
    export SERVER_PORT="11949"
  fi
}

program_expand_runtime_path() {
  local value="$1"
  if [[ "$value" == "~" ]]; then
    printf '%s\n' "${HOME:-$BUNDLE_ROOT}"
    return
  fi
  if [[ "$value" == "~/"* ]]; then
    printf '%s/%s\n' "${HOME:-$BUNDLE_ROOT}" "${value:2}"
    return
  fi
  if [[ "$value" == /* ]]; then
    printf '%s\n' "$value"
    return
  fi
  printf '%s\n' "$BUNDLE_ROOT/$value"
}

program_resolve_runtime_root() {
  if [[ "$RUNTIME_ROOT_EXPLICIT" -eq 0 && -n "${AP_RUNTIME_DIR:-}" ]]; then
    RUNTIME_ROOT="$(program_expand_runtime_path "$AP_RUNTIME_DIR")"
  fi
}

program_prepare_runtime_dirs() {
  program_resolve_runtime_root
  mkdir -p \
    "$RUN_DIR" \
    "$LOG_DIR" \
    "$RUNTIME_ROOT/registries/providers" \
    "$RUNTIME_ROOT/registries/models" \
    "$RUNTIME_ROOT/registries/mcp-servers" \
    "$RUNTIME_ROOT/registries/viewport-servers" \
    "$RUNTIME_ROOT/tools" \
    "$RUNTIME_ROOT/viewports" \
    "$RUNTIME_ROOT/owner" \
    "$RUNTIME_ROOT/agents" \
    "$RUNTIME_ROOT/teams" \
    "$RUNTIME_ROOT/root" \
    "$RUNTIME_ROOT/automations" \
    "$RUNTIME_ROOT/chats" \
    "$RUNTIME_ROOT/memory" \
    "$RUNTIME_ROOT/pan" \
    "$RUNTIME_ROOT/skills-market"
}

program_update_backend_args() {
  BACKEND_ARGS=(--config-dir "$CONFIG_ROOT" --runtime-dir "$RUNTIME_ROOT")
  if [[ -n "$PROGRAM_PORT" ]]; then
    BACKEND_ARGS+=(--port "$PROGRAM_PORT")
  fi
}

program_prepare_log_file() {
  mkdir -p "$RUN_DIR"
  : >"$LOG_FILE"
}

program_read_pid_file() {
  local pid_file="$1"
  [[ -f "$pid_file" ]] || return 1
  local pid
  pid="$(cat "$pid_file")"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n' "$pid"
}

program_clear_stale_pid_file() {
  local pid_file="$1"
  local label="$2"

  if [[ ! -f "$pid_file" ]]; then
    return
  fi

  local pid
  pid="$(program_read_pid_file "$pid_file" || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" >/dev/null 2>&1; then
    program_die "$label is already running with pid $pid"
  fi

  rm -f "$pid_file"
}

program_stop_pid_file() {
  local pid_file="$1"
  local label="$2"

  if [[ ! -f "$pid_file" ]]; then
    echo "[program-stop] pid file not found for $label: $pid_file"
    return
  fi

  local pid
  pid="$(program_read_pid_file "$pid_file" || true)"
  [[ -n "$pid" ]] || program_die "pid file must contain a numeric pid: $pid_file"

  if ! kill -0 "$pid" >/dev/null 2>&1; then
    rm -f "$pid_file"
    echo "[program-stop] process $pid for $label is not running; removed stale pid file"
    return
  fi

  kill "$pid"
  for _ in $(seq 1 30); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      rm -f "$pid_file"
      echo "[program-stop] stopped $label (pid=$pid)"
      return
    fi
    sleep 1
  done

  program_die "process $pid for $label did not stop within 30s"
}

program_start_backend_daemon() {
  local pid

  mkdir -p "$(dirname "$PID_FILE")"
  program_update_backend_args
  program_clear_stale_pid_file "$PID_FILE" "$APP_NAME"
  nohup "$BACKEND_BIN" "${BACKEND_ARGS[@]}" </dev/null >>"$LOG_FILE" 2>&1 &
  pid=$!
  printf '%s\n' "$pid" >"$PID_FILE"
  sleep 1
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    rm -f "$PID_FILE"
    program_die "backend failed to start; see $LOG_FILE"
  fi

  echo "[program-start] started $APP_NAME in daemon mode (pid=$pid)"
  echo "[program-start] log file: $LOG_FILE"
}

program_exec_backend() {
  program_update_backend_args
  exec "$BACKEND_BIN" "${BACKEND_ARGS[@]}"
}

program_stop_backend() {
  program_stop_pid_file "$PID_FILE" "$APP_NAME"
}
