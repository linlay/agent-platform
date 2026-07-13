#!/usr/bin/env bash
set -euo pipefail

# Formal program bundles must carry both dependency metadata and CycloneDX
# SBOMs. Local build/run targets use separate optional staging paths.
export REQUIRE_KBASE_RELEASE_METADATA="${REQUIRE_KBASE_RELEASE_METADATA:-1}"
export REQUIRE_RELEASE_SBOM="${REQUIRE_RELEASE_SBOM:-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROGRAM_RELEASE_ASSETS_DIR="$SCRIPT_DIR/release-assets/program"

# shellcheck disable=SC1091
. "$SCRIPT_DIR/release-common.sh"

require_release_tools
resolve_release_context

require_dir "$PROGRAM_RELEASE_ASSETS_DIR"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/unix/deploy.sh"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/unix/start.sh"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/unix/stop.sh"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/unix/program-common.sh"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/windows/deploy.ps1"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/windows/start.ps1"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/windows/stop.ps1"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/windows/program-common.ps1"
require_file "$PROGRAM_RELEASE_ASSETS_DIR/windows/tools.example.yml"
require_file "$SCRIPT_DIR/release-assets/builtins.lock.json"
require_file "$SCRIPT_DIR/stage-builtins.sh"
require_file "$SCRIPT_DIR/stage-kbase-lance-engine.sh"
require_file "$REPO_ROOT/.env.example"
require_dir "$REPO_ROOT/configs"
cd "$REPO_ROOT"

HOST_OS="$(detect_os)"
HOST_ARCH="$(detect_arch)"
KBASE_ARTIFACT_ROOT="${KBASE_LANCE_ENGINE_ARTIFACT_ROOT:-$REPO_ROOT/dist/kbase-lance-engine}"
REMOTE_TARGET_COUNT=0

is_local_sidecar_target() {
  [[ "$1" == "$HOST_OS" && "$2" == "$HOST_ARCH" ]]
}

sidecar_target_suffix() {
  printf '%s_%s\n' "$1" "$2" | tr '[:lower:]' '[:upper:]'
}

resolve_remote_sidecar() {
  local target_os="$1"
  local target_arch="$2"
  local suffix
  local url_var
  local sha_var
  local url
  local sha

  suffix="$(sidecar_target_suffix "$target_os" "$target_arch")"
  url_var="KBASE_LANCE_ENGINE_URL_${suffix}"
  sha_var="KBASE_LANCE_ENGINE_SHA256_${suffix}"
  url="${!url_var:-}"
  sha="${!sha_var:-}"

  if [[ -n "$url" || -n "$sha" ]]; then
    [[ -n "$url" && -n "$sha" ]] || die "non-local sidecar $target_os/$target_arch requires both $url_var and $sha_var"
  elif [[ "$REMOTE_TARGET_COUNT" -eq 1 && ( -n "${KBASE_LANCE_ENGINE_URL:-}" || -n "${KBASE_LANCE_ENGINE_SHA256:-}" ) ]]; then
    url="${KBASE_LANCE_ENGINE_URL:-}"
    sha="${KBASE_LANCE_ENGINE_SHA256:-}"
    [[ -n "$url" && -n "$sha" ]] || die "non-local sidecar $target_os/$target_arch requires both KBASE_LANCE_ENGINE_URL and KBASE_LANCE_ENGINE_SHA256"
  else
    die "non-local sidecar $target_os/$target_arch requires $url_var and $sha_var"
  fi

  SIDECAR_URL="$url"
  SIDECAR_SHA256="$sha"
}

stage_release_sidecar() {
  local bundle_root="$1"
  local target_os="$2"
  local target_arch="$3"
  local args=(
    --output "$bundle_root"
    --os "$target_os"
    --arch "$target_arch"
    --artifact-root "$KBASE_ARTIFACT_ROOT"
  )

  if is_local_sidecar_target "$target_os" "$target_arch"; then
    args+=(--local-build)
  else
    resolve_remote_sidecar "$target_os" "$target_arch"
    args+=(--url "$SIDECAR_URL" --expected-sha256 "$SIDECAR_SHA256" --refresh-download)
  fi

  "$SCRIPT_DIR/stage-kbase-lance-engine.sh" "${args[@]}"
}

copy_config_templates() {
  local bundle_root="$1"
  local asset

  shopt -s nullglob
  for asset in "$REPO_ROOT/configs/"*.example.yml "$REPO_ROOT/configs/"*.example.yaml "$REPO_ROOT/configs/"*.example.pem; do
    cp "$asset" "$bundle_root/configs/"
  done
  shopt -u nullglob
}

build_program_bundle() {
  local target_os="$1"
  local target_arch="$2"
  local binary_name
  local archive_format
  local bundle_archive
  local tmp_dir
  local stage_root
  local bundle_root
  local backend_dir
  local scripts_dir
  local backend_path
  local backend_entry
  local sidecar_name="kbase-lance-engine"
  local sidecar_path

  binary_name="$(binary_name_for_os "$target_os")"
  archive_format="$(archive_format_for_os "$target_os")"
  bundle_archive="$RELEASE_DIR/$(program_bundle_filename "$VERSION" "$target_os" "$target_arch" "$archive_format")"

  echo "[release] program VERSION=$VERSION TARGET_OS=$target_os ARCH=$target_arch"

  tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/agent-platform-program-release.XXXXXX")"
  trap 'rm -rf "$tmp_dir"' RETURN

  stage_root="$tmp_dir/stage"
  bundle_root="$stage_root/$APP_NAME"
  backend_dir="$bundle_root/backend"
  scripts_dir="$bundle_root/scripts"
  backend_path="$backend_dir/$binary_name"
  backend_entry="backend/$binary_name"
  if [[ "$target_os" == "windows" ]]; then
    sidecar_name+=".exe"
  fi
  sidecar_path="$bundle_root/bin/$sidecar_name"

  mkdir -p "$backend_dir" "$scripts_dir" "$bundle_root/configs"

  echo "[release] building program binary for $target_os..."
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build \
    -o "$backend_path" \
    ./cmd/agent-platform

  echo "[release] assembling program bundle for $target_os..."
  cp "$REPO_ROOT/.env.example" "$bundle_root/.env.example"
  write_program_manifest "$bundle_root/manifest.json" "$target_os" "$target_arch" "$backend_entry" "$(basename "$bundle_archive")"
  copy_config_templates "$bundle_root"
  if [[ "$target_os" == "windows" ]]; then
    cp "$PROGRAM_RELEASE_ASSETS_DIR/windows/tools.example.yml" "$bundle_root/configs/tools.example.yml"
  fi
  "$SCRIPT_DIR/stage-builtins.sh" \
    --output "$bundle_root" \
    --os "$target_os" \
    --arch "$target_arch"
  stage_release_sidecar "$bundle_root" "$target_os" "$target_arch"

  if [[ "$target_os" == "windows" ]]; then
    cp "$PROGRAM_RELEASE_ASSETS_DIR/windows/deploy.ps1" "$bundle_root/deploy.ps1"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/windows/start.ps1" "$bundle_root/start.ps1"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/windows/stop.ps1" "$bundle_root/stop.ps1"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/windows/program-common.ps1" "$scripts_dir/program-common.ps1"
  else
    cp "$PROGRAM_RELEASE_ASSETS_DIR/unix/deploy.sh" "$bundle_root/deploy.sh"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/unix/start.sh" "$bundle_root/start.sh"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/unix/stop.sh" "$bundle_root/stop.sh"
    cp "$PROGRAM_RELEASE_ASSETS_DIR/unix/program-common.sh" "$scripts_dir/program-common.sh"
    chmod +x \
      "$backend_path" \
      "$bundle_root/deploy.sh" \
      "$bundle_root/start.sh" \
      "$bundle_root/stop.sh" \
      "$scripts_dir/program-common.sh"
  fi

  mkdir -p "$RELEASE_DIR"
  archive_bundle_dir "$stage_root" "$APP_NAME" "$bundle_archive" "$archive_format"
  go run ./cmd/verify-program-bundle \
    --archive "$bundle_archive" \
    --os "$target_os" \
    --arch "$target_arch"
  write_release_checksum "$bundle_archive"
  write_release_size_report "$bundle_archive.sizes.json" "$backend_path" "$sidecar_path" "$bundle_archive"
  write_release_sbom "$bundle_root" "$bundle_archive.sbom.cdx.json"

  echo "[release] done: $bundle_archive"
}

targets=()
while read -r target_os target_arch; do
  [[ -n "$target_os" ]] || continue
  [[ -n "$target_arch" ]] || die "missing ARCH for program target $target_os"
  targets+=("$target_os/$target_arch")
  if ! is_local_sidecar_target "$target_os" "$target_arch"; then
    REMOTE_TARGET_COUNT=$((REMOTE_TARGET_COUNT + 1))
  fi
done < <(parse_program_target_matrix)

for target in "${targets[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  require_archive_tool_for_os "$target_os"
  build_program_bundle "$target_os" "$target_arch"
done
