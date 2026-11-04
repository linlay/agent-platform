#!/usr/bin/env bash
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/scripts/program-common.sh"
dictation_args "$@"
if existing="$(dictation_pid)"; then echo "agent-dictation already running: $existing"; exit 0; else status=$?; [[ "$status" == 1 ]] || exit "$status"; fi
[[ -x "$BIN" && -f "$CONFIG_DIR/dictation.yml" ]] || { echo "Deploy agent-dictation first" >&2; exit 1; }
set -a
[[ ! -f "$CONFIG_DIR/.env" ]] || . "$CONFIG_DIR/.env"
set +a
[[ -z "$PORT" ]] || export DICTATION_PORT="$PORT"
export DICTATION_LISTEN="127.0.0.1:${DICTATION_PORT:-11964}"
[[ -z "$TOKEN_FILE" ]] || export DICTATION_SSO_ID_TOKEN_FILE="$TOKEN_FILE"
# Refuse to disturb an occupied port or another instance of this program.
command -v lsof >/dev/null || { echo "lsof is required for the instance preflight" >&2; exit 1; }
if lsof -nP -iTCP:"${DICTATION_PORT:-11964}" -sTCP:LISTEN -t >/dev/null 2>&1; then echo "Dictation port is already occupied" >&2; exit 1; fi
if ps -axo args= | grep -F -- "$BIN --config " | grep -v '[g]rep' >/dev/null; then echo "Existing dictation instance has no matching PID record" >&2; exit 1; fi
if ps -axo args= | grep '[a]gent-dictation.*--config ' | grep -F -- "$CONFIG_DIR/dictation.yml" >/dev/null; then echo "Dictation configuration is already in use" >&2; exit 1; fi
umask 077
mkdir -p "$STATE_DIR" "$LOG_DIR"
cd "$CONFIG_DIR"
if [[ "$DAEMON" == 0 ]]; then exec "$BIN" --config "$CONFIG_DIR/dictation.yml"; fi
nohup "$BIN" --config "$CONFIG_DIR/dictation.yml" >> "$LOG_DIR/agent-dictation.log" 2>&1 < /dev/null &
process_id=$!
printf '%s\n' "$process_id" > "$PID_FILE"
sleep 0.2
kill -0 "$process_id" 2>/dev/null || { rm -f "$PID_FILE"; echo "agent-dictation failed; see its log" >&2; exit 1; }
