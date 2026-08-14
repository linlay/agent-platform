#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

bundle_root="$tmp_dir/agent-platform"
mkdir -p "$bundle_root/backend" "$bundle_root/configs" "$bundle_root/scripts"
cp "$REPO_ROOT/scripts/release-assets/program/unix/deploy.sh" "$bundle_root/deploy.sh"
cp "$REPO_ROOT/scripts/release-assets/program/unix/program-common.sh" "$bundle_root/scripts/program-common.sh"
cp "$REPO_ROOT/configs/ai-tools.example.yml" "$bundle_root/configs/ai-tools.example.yml"
printf '{}\n' >"$bundle_root/manifest.json"
printf 'AP_RUNTIME_DIR=\nAP_CONTAINER_HUB_BASE_URL=\n' >"$bundle_root/.env.example"
cat >"$bundle_root/backend/agent-platform" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "runtime-resource-sync" ]]; then
  if [[ -n "${AGENT_PLATFORM_TEST_CAPTURE_RESOURCE_ARGS:-}" ]]; then
    printf '%s\n' "$@" >"$AGENT_PLATFORM_TEST_CAPTURE_RESOURCE_ARGS"
  fi
  exit "${AGENT_PLATFORM_TEST_RESOURCE_EXIT_CODE:-0}"
fi
EOF
printf 'test-public-key\n' >"$tmp_dir/local-public-key.pem"
chmod +x "$bundle_root/deploy.sh" "$bundle_root/scripts/program-common.sh" "$bundle_root/backend/agent-platform"

run_deploy() {
  local output_dir="$1"
  shift
  "$bundle_root/deploy.sh" \
    --output-dir "$output_dir" \
    --ap-runtime-dir "$output_dir/runtime" \
    --container-hub-base-url "http://127.0.0.1:19090" \
    --public-key-source-file "$tmp_dir/local-public-key.pem" \
    "$@"
}

configured_output="$tmp_dir/configured"
run_deploy "$configured_output" --ai-image-generate-model-key image-model-key
configured_file="$configured_output/configs/ai-tools.yml"
image_generate_block="$(
  awk '
    /^image-generate:$/ { in_section = 1 }
    in_section && /^speech:$/ { exit }
    in_section { print }
  ' "$configured_file"
)"
[[ "$image_generate_block" == *$'  enabled: true'* ]] || {
  echo "[program-deploy-test] image-generate was not enabled" >&2
  exit 1
}
[[ "$image_generate_block" == *$'      model-key: image-model-key'* ]] || {
  echo "[program-deploy-test] image-generate model key was not rendered" >&2
  exit 1
}
[[ "$(grep -Fxc '      model-key:' "$configured_file")" -eq 3 ]] || {
  echo "[program-deploy-test] an unrelated AI tool model key changed" >&2
  exit 1
}

default_output="$tmp_dir/default"
run_deploy "$default_output"
cmp "$REPO_ROOT/configs/ai-tools.example.yml" "$default_output/configs/ai-tools.yml"

existing_output="$tmp_dir/existing"
mkdir -p "$existing_output/configs"
printf 'custom-ai-tools-config\n' >"$existing_output/configs/ai-tools.yml"
run_deploy "$existing_output" --ai-image-generate-model-key ignored-model-key
[[ "$(cat "$existing_output/configs/ai-tools.yml")" == "custom-ai-tools-config" ]] || {
  echo "[program-deploy-test] existing ai-tools.yml was overwritten" >&2
  exit 1
}

reset_output="$tmp_dir/reset-output"
reset_backup="$tmp_dir/config-backups/v0.3.26-to-v0.3.27/agent-platform"
mkdir -p "$reset_output/configs"
printf 'ENGINE=local\nOLD_FIELD=remove-me\nAP_CHAT_RESOURCE_TICKET_SECRET=ticket-secret\n' >"$reset_output/.env"
printf 'stale-yaml\n' >"$reset_output/configs/ai-tools.yml"
run_deploy "$reset_output" \
  --desktop-config-reset \
  --desktop-config-backup-dir "$reset_backup" \
  --desktop-version-from v0.3.26 \
  --desktop-version-to v0.3.27
grep -Fqx 'ENGINE=local' "$reset_backup/.env"
grep -Fqx 'OLD_FIELD=remove-me' "$reset_backup/.env"
grep -Fqx 'AP_CHAT_RESOURCE_TICKET_SECRET=ticket-secret' "$reset_output/.env"
! grep -Fq 'ENGINE=' "$reset_output/.env"
! grep -Fq 'OLD_FIELD=' "$reset_output/.env"
cmp "$REPO_ROOT/configs/ai-tools.example.yml" "$reset_output/configs/ai-tools.yml"

resource_source="$tmp_dir/current env.zip"
resource_previous_source="$tmp_dir/previous env.zip"
: >"$resource_source"
: >"$resource_previous_source"
resource_args="$tmp_dir/runtime-resource-args.txt"
AGENT_PLATFORM_TEST_CAPTURE_RESOURCE_ARGS="$resource_args" run_deploy "$reset_output" \
  --desktop-config-reset \
  --desktop-config-backup-dir "$reset_backup" \
  --desktop-version-from v0.3.26 \
  --desktop-version-to v0.3.27 \
  --ai-image-generate-model-key th-gpt-image-2 \
  --runtime-resource-source "$resource_source" \
  --runtime-resource-previous-source "$resource_previous_source" \
  --desktop-device-id desktop-device-123 \
  --runtime-resource-mode version-change
captured_resource_args=()
while IFS= read -r captured_resource_arg; do
  captured_resource_args+=("$captured_resource_arg")
done <"$resource_args"
expected_resource_args=(
  runtime-resource-sync
  --ap-runtime-dir "$reset_output/runtime"
  --runtime-resource-source "$resource_source"
  --desktop-version-from v0.3.26
  --desktop-version-to v0.3.27
  --desktop-device-id desktop-device-123
  --mode version-change
  --runtime-resource-previous-source "$resource_previous_source"
)
[[ "${captured_resource_args[*]}" == "${expected_resource_args[*]}" ]] || {
  echo "[program-deploy-test] runtime resource arguments were not forwarded exactly" >&2
  exit 1
}

set +e
AGENT_PLATFORM_TEST_RESOURCE_EXIT_CODE=19 run_deploy "$reset_output" \
  --desktop-version-from v0.3.26 \
  --desktop-version-to v0.3.27 \
  --desktop-device-id desktop-device-123 \
  --runtime-resource-source "$resource_source" \
  --runtime-resource-mode version-change >/dev/null 2>&1
resource_failure_status=$?
set -e
[[ "$resource_failure_status" -eq 19 ]] || {
  echo "[program-deploy-test] runtime resource failure did not fail deploy" >&2
  exit 1
}

printf 'FAILED_ONLY=diagnostic\n' >>"$reset_output/.env"
run_deploy "$reset_output" \
  --desktop-config-reset \
  --desktop-config-backup-dir "$reset_backup" \
  --desktop-version-from v0.3.26 \
  --desktop-version-to v0.3.27
grep -Fqx 'ENGINE=local' "$reset_backup/.env"
grep -Fqx 'FAILED_ONLY=diagnostic' "${reset_backup}.failed/.env"
grep -Fqx 'AP_CHAT_RESOURCE_TICKET_SECRET=ticket-secret' "$reset_output/.env"
! grep -Fq 'FAILED_ONLY=' "$reset_output/.env"

set +e
missing_value_output="$(
  run_deploy "$tmp_dir/missing-value" --ai-image-generate-model-key 2>&1
)"
missing_value_status=$?
set -e
[[ "$missing_value_status" -ne 0 ]] || {
  echo "[program-deploy-test] missing image-generate model key unexpectedly succeeded" >&2
  exit 1
}
[[ "$missing_value_output" == *"missing value for --ai-image-generate-model-key"* ]] || {
  echo "[program-deploy-test] missing value returned an unexpected error" >&2
  exit 1
}

# The start wrapper must preserve a Desktop identity path containing spaces.
. "$bundle_root/scripts/program-common.sh"
identity_file="$tmp_dir/CuteJ Data/.cutej/.desktop/state/desktop/sso-access-token.txt"
program_apply_layout_flags \
  --config-dir "$tmp_dir/CuteJ Data/config" \
  --state-dir "$tmp_dir/run" \
  --log-dir "$tmp_dir/logs" \
  --port 17078 \
  --identity-file "$identity_file"
program_update_backend_args
[[ "${BACKEND_ARGS[0]}" == "--config-dir" ]]
[[ "${BACKEND_ARGS[2]}" == "--port" ]]
[[ "${BACKEND_ARGS[4]}" == "--identity-file" ]]
[[ "${BACKEND_ARGS[5]}" == "$identity_file" ]]

fake_backend="$tmp_dir/fake agent-platform"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''%s\n'\'' "$@" >"$AGENT_PLATFORM_TEST_CAPTURE_ARGS"' \
  'if [[ "${AGENT_PLATFORM_TEST_STAY_ALIVE:-}" == "1" ]]; then sleep 5; fi' \
  >"$fake_backend"
chmod +x "$fake_backend"
BACKEND_BIN="$fake_backend"

foreground_args="$tmp_dir/foreground-args.txt"
(
  AGENT_PLATFORM_TEST_CAPTURE_ARGS="$foreground_args" \
    AGENT_PLATFORM_TEST_STAY_ALIVE="" \
    program_exec_backend
)
captured_foreground_args=()
while IFS= read -r argument; do
  captured_foreground_args+=("$argument")
done <"$foreground_args"
[[ "${captured_foreground_args[*]}" == "${BACKEND_ARGS[*]}" ]]

daemon_args="$tmp_dir/daemon-args.txt"
export AGENT_PLATFORM_TEST_CAPTURE_ARGS="$daemon_args"
export AGENT_PLATFORM_TEST_STAY_ALIVE=1
mkdir -p "$RUN_DIR" "$LOG_DIR"
program_prepare_log_file
program_start_backend_daemon
captured_daemon_args=()
while IFS= read -r argument; do
  captured_daemon_args+=("$argument")
done <"$daemon_args"
[[ "${captured_daemon_args[*]}" == "${BACKEND_ARGS[*]}" ]]
daemon_pid="$(cat "$PID_FILE")"
kill "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
rm -f "$PID_FILE"
unset AGENT_PLATFORM_TEST_CAPTURE_ARGS AGENT_PLATFORM_TEST_STAY_ALIVE

echo "[program-deploy-test] passed"
