#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
bundle="$fixture/program"
config="$fixture/config with space"
registry="$fixture/registry with ' quote"
mkdir -p "$bundle/scripts" "$bundle/backend" "$fixture/state"
cp "$root/scripts/release-assets/dictation/unix/"{start,stop,deploy}.sh "$bundle/"
cp "$root/scripts/release-assets/dictation/unix/program-common.sh" "$bundle/scripts/"
cp "$root/scripts/release-assets/dictation/.env.example" "$bundle/"
cp "$root/configs/dictation.example.yml" "$bundle/"
bash "$bundle/deploy.sh" --output-dir "$config" --registry-dir "$registry"
. "$config/.env"
[[ "$DICTATION_MODEL_FILE" == "$registry/models/dashscope-paraformer-realtime-v2.yml" ]]
[[ "$DICTATION_PROVIDER_FILE" == "$registry/providers/dashscope.yml" ]]
printf '\n# existing customization\n' >> "$config/dictation.yml"
cp "$config/dictation.yml" "$fixture/before.yml"
cp "$config/.env" "$fixture/before.env"
bash "$bundle/deploy.sh" --output-dir "$config" --registry-dir "$fixture/different"
cmp "$fixture/before.yml" "$config/dictation.yml"
cmp "$fixture/before.env" "$config/.env"
# A stale PID pointing at this test process must never cause a kill or a launch.
printf '%s\n' "$$" > "$fixture/state/agent-dictation.pid"
for action in start stop; do
  if bash "$bundle/$action.sh" --state-dir "$fixture/state" --config-dir "$config" 2>/dev/null; then
    echo "$action accepted another process's PID" >&2; exit 1
  fi
done
kill -0 "$$"
[[ "$(cat "$fixture/state/agent-dictation.pid")" == "$$" ]]
echo 'dictation deploy preserves configuration and rejects foreign process ownership'
