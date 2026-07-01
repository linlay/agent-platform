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
RUN_DIR="$BUNDLE_ROOT/run"
LOG_DIR="$RUN_DIR"
LOG_FILE="$LOG_DIR/$APP_NAME.log"
PID_FILE="$RUN_DIR/$APP_NAME.pid"
PROGRAM_PORT=""
BACKEND_ARGS=()
DEPLOY_AP_RUNTIME_DIR=""
DEPLOY_CONTAINER_HUB_BASE_URL=""
DEPLOY_AI_VISION_GENERAL_MODEL_KEY=""
DEPLOY_AI_VISION_OCR_MODEL_KEY=""
DEPLOY_AI_WEB_FETCH_MODEL_KEY=""
DEPLOY_CODER_MODEL_KEY=""
DEPLOY_CODER_REASONING_EFFORT=""
DEPLOY_PUBLIC_KEY_SOURCE_FILE=""

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

program_require_arg_value() {
  local name="$1"
  local value="$2"
  local stripped="${value//[[:space:]]/}"
  [[ -n "$stripped" ]] || program_die "missing required deploy argument: $name"
}

program_reject_deploy_start_arg() {
  program_die "$1 is a start/runtime argument; pass it to start.sh instead of deploy.sh"
}

program_apply_deploy_flags() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --output-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --output-dir"
        program_require_arg_value "--output-dir" "$2"
        CONFIG_ROOT="$2"
        shift 2
        ;;
      --ap-runtime-dir)
        [[ $# -ge 2 ]] || program_die "missing value for --ap-runtime-dir"
        DEPLOY_AP_RUNTIME_DIR="$2"
        shift 2
        ;;
      --container-hub-base-url)
        [[ $# -ge 2 ]] || program_die "missing value for --container-hub-base-url"
        DEPLOY_CONTAINER_HUB_BASE_URL="$2"
        shift 2
        ;;
      --ai-vision-general-model-key)
        [[ $# -ge 2 ]] || program_die "missing value for --ai-vision-general-model-key"
        DEPLOY_AI_VISION_GENERAL_MODEL_KEY="$2"
        shift 2
        ;;
      --ai-vision-ocr-model-key)
        [[ $# -ge 2 ]] || program_die "missing value for --ai-vision-ocr-model-key"
        DEPLOY_AI_VISION_OCR_MODEL_KEY="$2"
        shift 2
        ;;
      --ai-web-fetch-model-key)
        [[ $# -ge 2 ]] || program_die "missing value for --ai-web-fetch-model-key"
        DEPLOY_AI_WEB_FETCH_MODEL_KEY="$2"
        shift 2
        ;;
      --coder-model-key)
        [[ $# -ge 2 ]] || program_die "missing value for --coder-model-key"
        DEPLOY_CODER_MODEL_KEY="$2"
        shift 2
        ;;
      --coder-reasoning-effort)
        [[ $# -ge 2 ]] || program_die "missing value for --coder-reasoning-effort"
        DEPLOY_CODER_REASONING_EFFORT="$2"
        case "$DEPLOY_CODER_REASONING_EFFORT" in
          NONE|LOW|MEDIUM|HIGH) ;;
          *) program_die "--coder-reasoning-effort must be one of NONE, LOW, MEDIUM, HIGH" ;;
        esac
        shift 2
        ;;
      --public-key-source-file)
        [[ $# -ge 2 ]] || program_die "missing value for --public-key-source-file"
        DEPLOY_PUBLIC_KEY_SOURCE_FILE="$2"
        shift 2
        ;;
      --config-dir|--state-dir|--log-dir|--port|--daemon)
        program_reject_deploy_start_arg "$1"
        ;;
      --force)
        program_die "unsupported deploy argument: --force"
        ;;
      *)
        program_die "unsupported deploy argument: $1"
        ;;
    esac
  done

  program_refresh_paths
  program_require_arg_value "--ap-runtime-dir" "$DEPLOY_AP_RUNTIME_DIR"
  program_require_arg_value "--container-hub-base-url" "$DEPLOY_CONTAINER_HUB_BASE_URL"
  program_require_arg_value "--public-key-source-file" "$DEPLOY_PUBLIC_KEY_SOURCE_FILE"
  program_require_file "$DEPLOY_PUBLIC_KEY_SOURCE_FILE"
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

program_set_env_value() {
  local file="$1"
  local name="$2"
  local value="$3"
  local tmp

  tmp="$file.tmp.$$"
  if ! awk -v name="$name" -v value="$value" '
    BEGIN { found = 0 }
    {
      if ($0 ~ "^[[:space:]]*#?[[:space:]]*" name "=") {
        print name "=" value
        found = 1
        next
      }
      print
    }
    END {
      if (!found) {
        print name "=" value
      }
    }
  ' "$file" >"$tmp"; then
    rm -f "$tmp"
    program_die "failed to update $name in $file"
  fi
  mv "$tmp" "$file"
}

program_render_env_file() {
  local target="$1"

  cp "$ENV_EXAMPLE_FILE" "$target"
  program_set_env_value "$target" "AP_RUNTIME_DIR" "$DEPLOY_AP_RUNTIME_DIR"
  program_set_env_value "$target" "AP_CONTAINER_HUB_BASE_URL" "$DEPLOY_CONTAINER_HUB_BASE_URL"
}

program_set_ai_tools_model_key() {
  local file="$1"
  local section="$2"
  local profile="$3"
  local value="$4"
  local tmp

  tmp="$file.tmp.$$"
  if ! awk -v section="$section" -v profile="$profile" -v value="$value" '
    BEGIN {
      current_section = ""
      in_profiles = 0
      current_profile = ""
      replaced = 0
    }
    /^[^[:space:]#][^:]*:/ {
      current_section = $1
      sub(/:$/, "", current_section)
      in_profiles = 0
      current_profile = ""
    }
    current_section == section && /^  profiles:/ {
      in_profiles = 1
      print
      next
    }
    current_section == section && in_profiles && /^    [A-Za-z0-9_-]+:/ {
      current_profile = $1
      sub(/:$/, "", current_profile)
      print
      next
    }
    current_section == section && in_profiles && current_profile == profile && /^      model-key:/ {
      print "      model-key: " value
      replaced = 1
      next
    }
    { print }
    END {
      if (!replaced) {
        exit 42
      }
    }
  ' "$file" >"$tmp"; then
    rm -f "$tmp"
    program_die "failed to update $section.profiles.$profile.model-key in $file"
  fi
  mv "$tmp" "$file"
}

program_render_ai_tools_file() {
  local source="$1"
  local target="$2"

  cp "$source" "$target"
  if [[ -n "$DEPLOY_AI_VISION_GENERAL_MODEL_KEY" ]]; then
    program_set_ai_tools_model_key "$target" "vision-recognize" "general" "$DEPLOY_AI_VISION_GENERAL_MODEL_KEY"
  fi
  if [[ -n "$DEPLOY_AI_VISION_OCR_MODEL_KEY" ]]; then
    program_set_ai_tools_model_key "$target" "vision-recognize" "ocr" "$DEPLOY_AI_VISION_OCR_MODEL_KEY"
  fi
  if [[ -n "$DEPLOY_AI_WEB_FETCH_MODEL_KEY" ]]; then
    program_set_ai_tools_model_key "$target" "web-fetch" "general" "$DEPLOY_AI_WEB_FETCH_MODEL_KEY"
  fi
}

program_set_coder_default_value() {
  local file="$1"
  local key="$2"
  local value="$3"
  local tmp

  tmp="$file.tmp.$$"
  if ! awk -v key="$key" -v value="$value" '
    BEGIN {
      in_default_agent = 0
      replaced = 0
    }
    /^[^[:space:]#][^:]*:/ {
      in_default_agent = ($1 == "default-agent:")
    }
    in_default_agent && $0 ~ "^  " key ":" {
      print "  " key ": " value
      replaced = 1
      next
    }
    { print }
    END {
      if (!replaced) {
        exit 42
      }
    }
  ' "$file" >"$tmp"; then
    rm -f "$tmp"
    program_die "failed to update default-agent.$key in $file"
  fi
  mv "$tmp" "$file"
}

program_render_coder_settings_file() {
  local source="$1"
  local target="$2"

  cp "$source" "$target"
  if [[ -n "$DEPLOY_CODER_MODEL_KEY" ]]; then
    program_set_coder_default_value "$target" "modelKey" "$DEPLOY_CODER_MODEL_KEY"
  fi
  if [[ -n "$DEPLOY_CODER_REASONING_EFFORT" ]]; then
    program_set_coder_default_value "$target" "reasoningEffort" "$DEPLOY_CODER_REASONING_EFFORT"
  fi
}

program_install_local_public_key() {
  local target="$CONFIG_DIR/local-public-key.pem"

  [[ -f "$target" ]] || cp "$DEPLOY_PUBLIC_KEY_SOURCE_FILE" "$target"
}

program_initialize_deploy_config() {
  mkdir -p "$CONFIG_DIR"
  if [[ ! -f "$ENV_FILE" ]]; then
    program_render_env_file "$ENV_FILE"
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
      if [[ -f "$target" ]]; then
        continue
      fi
      case "$name" in
        ai-tools)
          program_render_ai_tools_file "$example" "$target"
          ;;
        coder-settings)
          program_render_coder_settings_file "$example" "$target"
          ;;
        *)
          cp "$example" "$target"
          ;;
      esac
    done
    for source in "$BUNDLE_ROOT"/configs/*.example.pem; do
      [[ -f "$source" ]] || continue
      name="$(basename "$source" .example.pem)"
      [[ "$name" == "local-public-key" ]] && continue
      target="$CONFIG_DIR/$name.pem"
      [[ -f "$target" ]] || cp "$source" "$target"
    done
  fi
  program_install_local_public_key
}

program_load_env() {
  [[ -f "$ENV_FILE" ]] || program_die "missing .env (copy from .env.example first)"
  set -a
  # shellcheck disable=SC1091
  . "$ENV_FILE"
  set +a
}

program_apply_server_port_env() {
  if [[ -n "${SERVER_PORT:-}" ]]; then
    export SERVER_PORT
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
  if [[ -n "${AP_RUNTIME_DIR:-}" ]]; then
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
  BACKEND_ARGS=(--config-dir "$CONFIG_ROOT")
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
