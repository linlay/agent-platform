#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_ROOT="$REPO_ROOT/build/builtins"
BUILTINS_ROOT="${BUILTINS_ROOT:-}"
ALL_TARGETS=false
TARGETS=()

die() {
  echo "[builtins-sync] $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/sync-local-builtins.sh [--all | --target <os>/<arch>] [--builtins-root <absolute-path>]

Builds the sibling builtin projects in an isolated work directory, verifies
their locally generated archives, and atomically updates
build/builtins/<os>-<arch>/. It never writes release-local/.

With no target selector, builds the current host target. --all requests the
six target matrix and therefore requires every relevant Rust target, linker,
and SDK to be provisioned on this machine. ripgrep is consumed from its locked
vendor artifact because the sibling collection currently carries no ripgrep
source checkout.
EOF
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo darwin ;;
    Linux) echo linux ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) die "cannot detect host OS" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) die "cannot detect host architecture" ;;
  esac
}

validate_target() {
  case "$1" in
    darwin/amd64|darwin/arm64|linux/amd64|linux/arm64|windows/amd64|windows/arm64) ;;
    *) die "unsupported target: $1" ;;
  esac
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      ALL_TARGETS=true
      TARGETS=()
      shift
      ;;
    --target)
      ALL_TARGETS=false
      target="${2:-}"
      validate_target "$target"
      TARGETS+=("$target")
      shift 2
      ;;
    --builtins-root)
      BUILTINS_ROOT="${2:-}"
      [[ -n "$BUILTINS_ROOT" && "$BUILTINS_ROOT" = /* ]] || die "--builtins-root must be absolute"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ "$ALL_TARGETS" == true ]]; then
  TARGETS=(
    darwin/amd64
    darwin/arm64
    linux/amd64
    linux/arm64
    windows/amd64
    windows/arm64
  )
fi

host_target="$(detect_os)/$(detect_arch)"
if [[ ${#TARGETS[@]} -eq 0 ]]; then
  TARGETS=("$host_target")
fi
mkdir -p "$BUILD_ROOT"
if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="$BUILD_ROOT/.gocache"
fi
if [[ -z "${GOMODCACHE:-}" ]]; then
  export GOMODCACHE="$BUILD_ROOT/.gomodcache"
fi
work_dir="$(mktemp -d "$BUILD_ROOT/.sync.XXXXXX")"
cleanup() {
  rm -rf "$work_dir"
}
trap cleanup EXIT

if [[ -z "$BUILTINS_ROOT" ]]; then
  BUILTINS_ROOT="$REPO_ROOT/../agent-platform-builtins"
fi
[[ "$BUILTINS_ROOT" = /* ]] || die "builtins root must be absolute"
BUILTINS_ROOT="$(cd "$BUILTINS_ROOT" && pwd)"
for component in ripgrep dbx httpx kbase-lance-engine poppler-pdftotext; do
  [[ -d "$BUILTINS_ROOT/$component" ]] || die "missing sibling builtin project: $BUILTINS_ROOT/$component"
done

collection_root="$work_dir/collection"
copy_project() {
  local name="$1"
  mkdir -p "$collection_root/$name"
  rsync -a --exclude 'dist' --exclude 'target' "$BUILTINS_ROOT/$name/" "$collection_root/$name/"
}

copy_project ripgrep
copy_project dbx
copy_project httpx
copy_project kbase-lance-engine
copy_project poppler-pdftotext

(
  cd "$collection_root/dbx"
  scripts/release/build.sh
)
(
  cd "$collection_root/httpx"
  scripts/release/build.sh
)
for target in "${TARGETS[@]}"; do
  case "$target" in
    darwin/arm64|windows/amd64)
      (
        cd "$collection_root/poppler-pdftotext"
        POPPLER_PDFTOTEXT_TARGET_MATRIX="$target" scripts/release/build.sh
      )
      ;;
  esac
done
for target in "${TARGETS[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  cargo_target_dir="$BUILD_ROOT/.cargo-target/$target_os-$target_arch"
  (
    cd "$collection_root/kbase-lance-engine"
    scripts/build-release.sh --os "$target_os" --arch "$target_arch" --cargo-target-dir "$cargo_target_dir"
  )
done

local_lock="$work_dir/builtins.local.lock.json"
prepare_lock_args=(
  ./cmd/prepare-local-builtins-lock
  --input "$REPO_ROOT/scripts/release-assets/builtins.lock.json"
  --output "$local_lock"
  --builtins-root "$collection_root"
)
for target in "${TARGETS[@]}"; do
  prepare_lock_args+=(--target "$target")
done
(
  cd "$REPO_ROOT"
  go run "${prepare_lock_args[@]}"
)

for target in "${TARGETS[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  stage_dir="$work_dir/$target_os-$target_arch"
  mkdir -p "$stage_dir"
  (
    cd "$REPO_ROOT"
    go run ./cmd/stage-builtins --repo-root "$REPO_ROOT" --lock "$local_lock" --output "$stage_dir" --os "$target_os" --arch "$target_arch" --builtins-root "$collection_root"
    go run ./cmd/stage-kbase-lance-engine --repo-root "$REPO_ROOT" --lock "$local_lock" --output "$stage_dir" --os "$target_os" --arch "$target_arch" --builtins-root "$collection_root"
  )
done

activated_targets=()
rollback_activation() {
  local target target_os target_arch destination backup
  for target in "${activated_targets[@]}"; do
    target_os="${target%%/*}"
    target_arch="${target#*/}"
    destination="$BUILD_ROOT/$target_os-$target_arch"
    backup="$BUILD_ROOT/.$target_os-$target_arch.previous"
    rm -rf "$destination"
    [[ ! -e "$backup" ]] || mv "$backup" "$destination"
  done
}

for target in "${TARGETS[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  destination="$BUILD_ROOT/$target_os-$target_arch"
  staged="$work_dir/$target_os-$target_arch"
  backup="$BUILD_ROOT/.$target_os-$target_arch.previous"
  rm -rf "$backup"
  if [[ -e "$destination" ]]; then
    mv "$destination" "$backup"
  fi
  if ! mv "$staged" "$destination"; then
    [[ ! -e "$backup" ]] || mv "$backup" "$destination"
    rollback_activation
    die "could not activate staged target $target"
  fi
  activated_targets+=("$target")
done
for target in "${activated_targets[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  rm -rf "$BUILD_ROOT/.$target_os-$target_arch.previous"
done
echo "[builtins-sync] updated ${#TARGETS[@]} target cache(s) under $BUILD_ROOT"
