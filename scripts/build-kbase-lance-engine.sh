#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ENGINE_DIR="$REPO_ROOT/native/kbase-lance-engine"
ARTIFACT_ROOT="${KBASE_LANCE_ENGINE_ARTIFACT_ROOT:-$REPO_ROOT/dist/kbase-lance-engine}"
TARGET_OS=""
TARGET_ARCH=""
BUILD_ALL=false
REQUIRE_SBOM=false

die() {
  echo "[kbase-lance-build] $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/build-kbase-lance-engine.sh [options]
  --os darwin|linux|windows
  --arch amd64|arm64
  --all                 Build the six supported target hooks in sequence.
  --output DIR          Artifact root (default: dist/kbase-lance-engine).
  --require-sbom        Require Syft and emit a CycloneDX JSON SBOM.

Cross compilation never installs toolchains automatically. Each runner must
already provide the Rust target and a suitable linker/SDK; Linux targets may use
cargo-zigbuild when it is available.
EOF
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) die "cannot detect host OS; pass --os" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) die "cannot detect host architecture; pass --arch" ;;
  esac
}

validate_target() {
  case "$1" in darwin|linux|windows) ;; *) die "unsupported OS: $1" ;; esac
  case "$2" in amd64|arm64) ;; *) die "unsupported architecture: $2" ;; esac
}

rust_target() {
  case "$1/$2" in
    darwin/amd64) echo x86_64-apple-darwin ;;
    darwin/arm64) echo aarch64-apple-darwin ;;
    linux/amd64) echo x86_64-unknown-linux-gnu ;;
    linux/arm64) echo aarch64-unknown-linux-gnu ;;
    windows/amd64) echo x86_64-pc-windows-msvc ;;
    windows/arm64) echo aarch64-pc-windows-msvc ;;
    *) die "unsupported target: $1/$2" ;;
  esac
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

build_one() {
  local target_os="$1"
  local target_arch="$2"
  local triple
  local binary_name="kbase-lance-engine"
  local cargo_cmd=(cargo build --manifest-path "$ENGINE_DIR/Cargo.toml" --release --locked)
  local built_path
  local artifact_dir
  local artifact_path
  local digest

  validate_target "$target_os" "$target_arch"
  triple="$(rust_target "$target_os" "$target_arch")"
  if ! rustup target list --installed | grep -Fxq "$triple"; then
    die "Rust target $triple is not installed. Provision it on the build runner before release; this script will not install toolchains."
  fi
  if [[ "$target_os" == windows ]]; then
    binary_name+=".exe"
  fi
  cargo_cmd+=(--target "$triple")
  if [[ "$target_os" == linux ]] && command -v cargo-zigbuild >/dev/null 2>&1; then
    cargo_cmd[1]=zigbuild
  fi

  echo "[kbase-lance-build] building $target_os/$target_arch ($triple)"
  "${cargo_cmd[@]}"
  built_path="$ENGINE_DIR/target/$triple/release/$binary_name"
  [[ -f "$built_path" ]] || die "Cargo completed but binary is missing: $built_path"

  artifact_dir="$ARTIFACT_ROOT/$target_os-$target_arch"
  artifact_path="$artifact_dir/$binary_name"
  mkdir -p "$artifact_dir"
  cp "$built_path" "$artifact_path"
  chmod 0755 "$artifact_path"
  digest="$(sha256_file "$artifact_path")"
  printf '%s  %s\n' "$digest" "$binary_name" >"$artifact_path.sha256"

  cargo metadata --manifest-path "$ENGINE_DIR/Cargo.toml" --locked --format-version 1 \
    >"$artifact_dir/cargo-metadata.json"
  if command -v syft >/dev/null 2>&1; then
    syft "$artifact_path" -o "cyclonedx-json=$artifact_dir/sbom.cdx.json"
  elif [[ "$REQUIRE_SBOM" == true ]]; then
    die "Syft is required by --require-sbom but was not found"
  else
    echo "[kbase-lance-build] Syft not found; SBOM hook skipped (use --require-sbom in release CI)" >&2
  fi
  echo "[kbase-lance-build] artifact: $artifact_path ($digest)"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --os) TARGET_OS="${2:-}"; shift 2 ;;
    --arch) TARGET_ARCH="${2:-}"; shift 2 ;;
    --output) ARTIFACT_ROOT="${2:-}"; shift 2 ;;
    --all) BUILD_ALL=true; shift ;;
    --require-sbom) REQUIRE_SBOM=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -f "$ENGINE_DIR/Cargo.lock" ]] || die "Cargo.lock is required: $ENGINE_DIR/Cargo.lock"
command -v cargo >/dev/null 2>&1 || die "cargo is required to build the sidecar"
command -v rustup >/dev/null 2>&1 || die "rustup is required to inspect pre-provisioned targets"
command -v protoc >/dev/null 2>&1 || die "protoc is required by LanceDB's locked build dependencies; provision it on the build runner or use a verified prebuilt sidecar artifact"

if [[ "$BUILD_ALL" == true ]]; then
  while read -r target_os target_arch; do
    build_one "$target_os" "$target_arch"
  done <<'EOF'
darwin amd64
darwin arm64
linux amd64
linux arm64
windows amd64
windows arm64
EOF
else
  TARGET_OS="${TARGET_OS:-$(detect_os)}"
  TARGET_ARCH="${TARGET_ARCH:-$(detect_arch)}"
  build_one "$TARGET_OS" "$TARGET_ARCH"
fi
