#!/usr/bin/env bash
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/scripts/program-common.sh"
dictation_args "$@"
if process_id="$(dictation_pid)"; then
  kill "$process_id"
  for ((n=0;n<50;n++)); do
    kill -0 "$process_id" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$process_id" 2>/dev/null; then echo "agent-dictation is still stopping" >&2; exit 1; fi
else
  status=$?; [[ "$status" == 1 ]] || exit "$status"
fi
rm -f "$PID_FILE"
