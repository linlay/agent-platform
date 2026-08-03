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
printf '#!/usr/bin/env bash\n' >"$bundle_root/backend/agent-platform"
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

echo "[program-deploy-test] passed"
