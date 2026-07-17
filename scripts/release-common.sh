#!/usr/bin/env bash
set -euo pipefail

APP_NAME="agent-platform"
PROGRAM_NAME="agent-platform"

die() {
  echo "[release] $*" >&2
  exit 1
}

require_file() {
  local path="$1"
  [[ -f "$path" ]] || die "required file not found: $path"
}

require_dir() {
  local path="$1"
  [[ -d "$path" ]] || die "required directory not found: $path"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) die "cannot detect ARCH from $(uname -m); pass ARCH=amd64|arm64" ;;
  esac
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) die "cannot detect target OS from $(uname -s); pass PROGRAM_TARGET_MATRIX" ;;
  esac
}

validate_arch() {
  case "$1" in
    amd64|arm64) ;;
    *) die "ARCH must be amd64 or arm64 (got: $1)" ;;
  esac
}

validate_target_os() {
  case "$1" in
    linux|darwin|windows) ;;
    *) die "TARGET_OS must be linux, darwin, or windows (got: $1)" ;;
  esac
}

require_release_tools() {
  command -v go >/dev/null 2>&1 || die "go is required"
}

sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
    return
  fi
  die "sha256sum or shasum is required"
}

write_release_checksum() {
  local path="$1"
  local digest
  digest="$(sha256_file "$path")"
  printf '%s  %s\n' "$digest" "$(basename "$path")" >"$path.sha256"
}

write_release_size_report() {
  local dest="$1"
  local backend="$2"
  local sidecar="$3"
  local archive="$4"
  local backend_bytes
  local sidecar_bytes
  local archive_bytes
  backend_bytes="$(wc -c <"$backend" | tr -d '[:space:]')"
  sidecar_bytes="$(wc -c <"$sidecar" | tr -d '[:space:]')"
  archive_bytes="$(wc -c <"$archive" | tr -d '[:space:]')"
  cat >"$dest" <<EOF
{
  "backendBytes": $backend_bytes,
  "sidecarBytes": $sidecar_bytes,
  "archiveBytes": $archive_bytes
}
EOF
}

write_release_sbom() {
  local source_dir="$1"
  local dest="$2"
  rm -f "$dest"
  if command -v syft >/dev/null 2>&1; then
    syft "dir:$source_dir" -o "cyclonedx-json=$dest"
    return
  fi
  if [[ "${REQUIRE_RELEASE_SBOM:-0}" == "1" ]]; then
    die "Syft is required because REQUIRE_RELEASE_SBOM=1"
  fi
  echo "[release] Syft not found; bundle SBOM hook skipped (set REQUIRE_RELEASE_SBOM=1 in release CI)" >&2
}

resolve_release_context() {
  VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION" 2>/dev/null || echo "dev")}"
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION must match vX.Y.Z (got: $VERSION)"

  ARCH="${ARCH:-$(detect_arch)}"
  validate_arch "$ARCH"

  RELEASE_DIR="$REPO_ROOT/dist/release"
}

archive_format_for_os() {
  local target_os="$1"
  validate_target_os "$target_os"
  if [[ -n "${PROGRAM_ARCHIVE_FORMAT:-}" ]]; then
    printf '%s\n' "$PROGRAM_ARCHIVE_FORMAT"
    return
  fi
  case "$target_os" in
    windows) printf 'zip\n' ;;
    *) printf 'tar.gz\n' ;;
  esac
}

require_archive_tool_for_os() {
  local target_os="$1"
  local archive_format
  archive_format="$(archive_format_for_os "$target_os")"

  case "$archive_format" in
    tar.gz) command -v tar >/dev/null 2>&1 || die "tar is required for $target_os bundles" ;;
    zip) command -v zip >/dev/null 2>&1 || die "zip is required for $target_os bundles" ;;
    *) die "unsupported archive format: $archive_format" ;;
  esac
}

archive_bundle_dir() {
  local stage_root="$1"
  local bundle_dir_name="$2"
  local output_path="$3"
  local format="$4"

  mkdir -p "$(dirname "$output_path")"
  rm -f "$output_path"

  case "$format" in
    tar.gz)
      # macOS tar can emit AppleDouble entries (for example
      # ._agent-platform) for Finder/xattr metadata. They would become a
      # second archive root and make the bundle invalid for every consumer.
      COPYFILE_DISABLE=1 tar -czf "$output_path" -C "$stage_root" "$bundle_dir_name"
      ;;
    zip)
      (
        cd "$stage_root"
        zip -qr "$output_path" "$bundle_dir_name"
      )
      ;;
    *)
      die "unsupported archive format: $format"
      ;;
  esac
}

binary_name_for_os() {
  local target_os="$1"
  validate_target_os "$target_os"
  if [[ "$target_os" == "windows" ]]; then
    printf '%s.exe\n' "$PROGRAM_NAME"
    return
  fi
  printf '%s\n' "$PROGRAM_NAME"
}

program_bundle_filename() {
  local version="$1"
  local target_os="$2"
  local target_arch="$3"
  local archive_format="$4"
  printf '%s-%s-%s-%s.%s\n' "$APP_NAME" "$version" "$target_os" "$target_arch" "$archive_format"
}

parse_program_targets() {
  local raw="${PROGRAM_TARGETS:-$(detect_os)}"
  raw="${raw//,/ }"
  for target in $raw; do
    validate_target_os "$target"
    printf '%s\n' "$target"
  done
}

parse_program_target_matrix() {
  local raw="${PROGRAM_TARGET_MATRIX:-}"
  local target_spec
  local target_os
  local target_arch

  if [[ -n "$raw" ]]; then
    raw="${raw//,/ }"
    for target_spec in $raw; do
      [[ "$target_spec" == */* ]] || die "PROGRAM_TARGET_MATRIX entries must look like <os>/<arch> (got: $target_spec)"
      target_os="${target_spec%%/*}"
      target_arch="${target_spec#*/}"
      validate_target_os "$target_os"
      validate_arch "$target_arch"
      printf '%s %s\n' "$target_os" "$target_arch"
    done
    return
  fi

  if [[ -n "${PROGRAM_TARGETS:-}" ]]; then
    while IFS= read -r target_os; do
      [[ -n "$target_os" ]] || continue
      printf '%s %s\n' "$target_os" "$ARCH"
    done < <(parse_program_targets)
    return
  fi

  printf '%s %s\n' "$(detect_os)" "$ARCH"
}

write_program_manifest() {
  local dest="$1"
  local target_os="$2"
  local target_arch="$3"
  local backend_entry="$4"
  local asset_file_name="$5"

  (
    cd "$REPO_ROOT"
    go run ./cmd/render-program-manifest \
      --template "$REPO_ROOT/scripts/release-assets/manifest.template.json" \
      --output "$dest" \
      --version "$VERSION" \
      --os "$target_os" \
      --arch "$target_arch" \
      --backend "$backend_entry" \
      --asset "$asset_file_name"
  )
}
