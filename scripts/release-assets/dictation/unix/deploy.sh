#!/usr/bin/env bash
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/scripts/program-common.sh"
dictation_args "$@"
umask 077
mkdir -p "$CONFIG_DIR"
[[ -e "$CONFIG_DIR/dictation.yml" ]] || cp "$DICTATION_ROOT/dictation.example.yml" "$CONFIG_DIR/dictation.yml"
if [[ ! -e "$CONFIG_DIR/.env" ]]; then
  cp "$DICTATION_ROOT/.env.example" "$CONFIG_DIR/.env"
  if [[ -n "$REGISTRY_DIR" ]]; then
    # Quote values for the POSIX env file; registry contents are never copied.
    model="$REGISTRY_DIR/models/dashscope-paraformer-realtime-v2.yml"
    provider="$REGISTRY_DIR/providers/dashscope.yml"
    printf '\nDICTATION_MODEL_FILE=%q\nDICTATION_PROVIDER_FILE=%q\n' "$model" "$provider" >> "$CONFIG_DIR/.env"
  fi
fi
