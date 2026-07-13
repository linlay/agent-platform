#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ENGINE_DIR="$REPO_ROOT/native/kbase-lance-engine"
ARTIFACT_ROOT="${KBASE_LANCE_ENGINE_ARTIFACT_ROOT:-$REPO_ROOT/dist/kbase-lance-engine}"
OUTPUT_DIR=""
TARGET_OS=""
TARGET_ARCH=""
LOCAL_BUILD=false
OPTIONAL=false
REFRESH_DOWNLOAD=false
ARTIFACT_URL="${KBASE_LANCE_ENGINE_URL:-}"
EXPECTED_SHA="${KBASE_LANCE_ENGINE_SHA256:-}"

die() {
  echo "[kbase-lance-stage] $*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "sha256sum or shasum is required"
  fi
}

require_release_metadata() {
  [[ "${REQUIRE_KBASE_RELEASE_METADATA:-0}" == "1" ]]
}

write_cargo_metadata() {
  command -v cargo >/dev/null 2>&1 || die "cargo is required to generate release dependency metadata"
  local temp_path="$artifact_dir/cargo-metadata.json.tmp"
  cargo metadata --manifest-path "$ENGINE_DIR/Cargo.toml" --locked --format-version 1 >"$temp_path"
  mv "$temp_path" "$artifact_dir/cargo-metadata.json"
}

write_sidecar_sbom() {
  command -v syft >/dev/null 2>&1 || die "Syft is required because REQUIRE_KBASE_RELEASE_METADATA=1"
  local temp_path="$artifact_dir/sbom.cdx.json.tmp"
  syft "$artifact_path" -o "cyclonedx-json=$temp_path"
  mv "$temp_path" "$artifact_dir/sbom.cdx.json"
}

verify_artifact_checksum() {
  local checksum_path="$artifact_path.sha256"
  local actual_sha
  local actual_sha_normalized
  local expected_sha_normalized

  if [[ -z "$EXPECTED_SHA" ]]; then
    [[ -f "$checksum_path" ]] || die "missing checksum: $checksum_path"
    EXPECTED_SHA="$(awk 'NF {print $1; exit}' "$checksum_path")"
  fi
  [[ "$EXPECTED_SHA" =~ ^[[:xdigit:]]{64}$ ]] || die "invalid SHA-256 for $TARGET_OS/$TARGET_ARCH"
  actual_sha="$(sha256_file "$artifact_path")"
  actual_sha_normalized="$(printf '%s' "$actual_sha" | tr '[:upper:]' '[:lower:]')"
  expected_sha_normalized="$(printf '%s' "$EXPECTED_SHA" | tr '[:upper:]' '[:lower:]')"
  [[ "$actual_sha_normalized" == "$expected_sha_normalized" ]] || die "artifact SHA-256 mismatch: expected $EXPECTED_SHA, got $actual_sha"
  EXPECTED_SHA="$expected_sha_normalized"
  printf '%s  %s\n' "$EXPECTED_SHA" "$binary_name" >"$checksum_path"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT_DIR="${2:-}"; shift 2 ;;
    --os) TARGET_OS="${2:-}"; shift 2 ;;
    --arch) TARGET_ARCH="${2:-}"; shift 2 ;;
    --artifact-root) ARTIFACT_ROOT="${2:-}"; shift 2 ;;
    --url) ARTIFACT_URL="${2:-}"; shift 2 ;;
    --expected-sha256) EXPECTED_SHA="${2:-}"; shift 2 ;;
    --local-build) LOCAL_BUILD=true; shift ;;
    --optional) OPTIONAL=true; shift ;;
    --refresh-download) REFRESH_DOWNLOAD=true; shift ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "$OUTPUT_DIR" ]] || die "--output is required"
[[ -n "$TARGET_OS" ]] || die "--os is required"
[[ -n "$TARGET_ARCH" ]] || die "--arch is required"
case "$TARGET_OS" in darwin|linux|windows) ;; *) die "unsupported OS: $TARGET_OS" ;; esac
case "$TARGET_ARCH" in amd64|arm64) ;; *) die "unsupported architecture: $TARGET_ARCH" ;; esac

binary_name="kbase-lance-engine"
if [[ "$TARGET_OS" == windows ]]; then
  binary_name+=".exe"
fi
artifact_dir="$ARTIFACT_ROOT/$TARGET_OS-$TARGET_ARCH"
artifact_path="$artifact_dir/$binary_name"

if [[ "$LOCAL_BUILD" == true ]]; then
  ARTIFACT_URL=""
  EXPECTED_SHA=""
  build_args=(
    --os "$TARGET_OS"
    --arch "$TARGET_ARCH"
    --output "$ARTIFACT_ROOT"
  )
  if require_release_metadata; then
    build_args+=(--require-sbom)
  fi
  "$SCRIPT_DIR/build-kbase-lance-engine.sh" "${build_args[@]}"
fi

if [[ -n "$ARTIFACT_URL" && ( "$REFRESH_DOWNLOAD" == true || ! -f "$artifact_path" ) ]]; then
  [[ -n "$EXPECTED_SHA" ]] || die "KBASE_LANCE_ENGINE_SHA256/--expected-sha256 is required for direct downloads"
  command -v curl >/dev/null 2>&1 || die "curl is required for direct download"
  mkdir -p "$artifact_dir"
  temp_path="$artifact_path.download"
  rm -f "$temp_path"
  curl --fail --location --silent --show-error "$ARTIFACT_URL" --output "$temp_path"
  actual_sha="$(sha256_file "$temp_path")"
  actual_sha_normalized="$(printf '%s' "$actual_sha" | tr '[:upper:]' '[:lower:]')"
  expected_sha_normalized="$(printf '%s' "$EXPECTED_SHA" | tr '[:upper:]' '[:lower:]')"
  if [[ "$actual_sha_normalized" != "$expected_sha_normalized" ]]; then
    rm -f "$temp_path"
    die "download SHA-256 mismatch: expected $EXPECTED_SHA, got $actual_sha"
  fi
  mv "$temp_path" "$artifact_path"
  printf '%s  %s\n' "$EXPECTED_SHA" "$binary_name" >"$artifact_path.sha256"
fi

if [[ ! -f "$artifact_path" ]]; then
  if [[ "$OPTIONAL" == true && -z "$ARTIFACT_URL" ]]; then
    rm -f "$OUTPUT_DIR/bin/$binary_name"
    echo "[kbase-lance-stage] optional sidecar artifact is absent for $TARGET_OS/$TARGET_ARCH; local non-KBASE/SQLite development can continue. KBASE auto mode stays on SQLite and explicit Lance mode reports engine_unavailable." >&2
    exit 0
  fi
  die "missing sidecar artifact for $TARGET_OS/$TARGET_ARCH: $artifact_path. Build it with scripts/build-kbase-lance-engine.sh --os $TARGET_OS --arch $TARGET_ARCH, or provide KBASE_LANCE_ENGINE_URL and KBASE_LANCE_ENGINE_SHA256."
fi

verify_artifact_checksum

if [[ "$LOCAL_BUILD" != true ]] && require_release_metadata; then
  write_cargo_metadata
  write_sidecar_sbom
fi

args=(
  --repo-root "$REPO_ROOT"
  --output "$OUTPUT_DIR"
  --os "$TARGET_OS"
  --arch "$TARGET_ARCH"
  --binary "$artifact_path"
)
if [[ "$LOCAL_BUILD" == true ]]; then
  args+=(--local-build)
fi
args+=(--expected-sha256 "$EXPECTED_SHA")
if [[ -n "$ARTIFACT_URL" ]]; then
  args+=(--artifact-source "$ARTIFACT_URL")
fi
if [[ -s "$artifact_dir/cargo-metadata.json" ]]; then
  args+=(--cargo-metadata "$artifact_dir/cargo-metadata.json")
elif require_release_metadata; then
  die "cargo-metadata.json is required because REQUIRE_KBASE_RELEASE_METADATA=1"
fi

cd "$REPO_ROOT"
go run ./cmd/stage-kbase-lance-engine "${args[@]}"

if [[ -s "$artifact_dir/sbom.cdx.json" ]]; then
  mkdir -p "$OUTPUT_DIR/sbom"
  cp "$artifact_dir/sbom.cdx.json" "$OUTPUT_DIR/sbom/kbase-lance-engine.cdx.json"
elif require_release_metadata; then
  die "sidecar SBOM is required because REQUIRE_KBASE_RELEASE_METADATA=1"
fi
