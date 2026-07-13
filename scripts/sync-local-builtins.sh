#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_ROOT="$REPO_ROOT/build/builtins"
RELEASE_ROOT="$REPO_ROOT/release-local"
BUILTINS_ROOT=""
ALL_TARGETS=true
TARGETS=()

die() {
  echo "[builtins-sync] $*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/sync-local-builtins.sh [--all | --target <os>/<arch>] [--builtins-root <absolute-path>]

Stages verified builtin releases for the six supported targets into
build/builtins/<os>-<arch>/, then mirrors the current host target into the
flat release-local/bin/ service-package directory. The script never builds a
builtin; every artifact must already be present and match the platform lock.
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
[[ ${#TARGETS[@]} -gt 0 ]] || die "at least one target is required"

host_target="$(detect_os)/$(detect_arch)"
mkdir -p "$BUILD_ROOT" "$RELEASE_ROOT"
if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="$BUILD_ROOT/.gocache"
fi
work_dir="$(mktemp -d "$BUILD_ROOT/.sync.XXXXXX")"
release_stage="$(mktemp -d "$RELEASE_ROOT/.builtins.XXXXXX")"
cleanup() {
  rm -rf "$work_dir" "$release_stage"
}
trap cleanup EXIT

for target in "${TARGETS[@]}"; do
  target_os="${target%%/*}"
  target_arch="${target#*/}"
  stage_dir="$work_dir/$target_os-$target_arch"
  mkdir -p "$stage_dir"
  (
    cd "$REPO_ROOT"
    if [[ -n "$BUILTINS_ROOT" ]]; then
      go run ./cmd/stage-builtins --repo-root "$REPO_ROOT" --output "$stage_dir" --os "$target_os" --arch "$target_arch" --builtins-root "$BUILTINS_ROOT"
      go run ./cmd/stage-kbase-lance-engine --repo-root "$REPO_ROOT" --output "$stage_dir" --os "$target_os" --arch "$target_arch" --builtins-root "$BUILTINS_ROOT"
    else
      go run ./cmd/stage-builtins --repo-root "$REPO_ROOT" --output "$stage_dir" --os "$target_os" --arch "$target_arch"
      go run ./cmd/stage-kbase-lance-engine --repo-root "$REPO_ROOT" --output "$stage_dir" --os "$target_os" --arch "$target_arch"
    fi
  )
done

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
    die "could not activate staged target $target"
  fi
  rm -rf "$backup"
done

host_stage="$BUILD_ROOT/${host_target//\//-}"
if [[ -d "$host_stage" ]]; then
  cp -R "$host_stage/bin" "$release_stage/bin"
  cp "$host_stage/builtins.manifest.json" "$release_stage/builtins.manifest.json"
  [[ ! -d "$host_stage/licenses" ]] || cp -R "$host_stage/licenses" "$release_stage/licenses"
  [[ ! -d "$host_stage/sbom" ]] || cp -R "$host_stage/sbom" "$release_stage/sbom"

  for name in bin builtins.manifest.json licenses sbom; do
    source="$release_stage/$name"
    destination="$RELEASE_ROOT/$name"
    [[ -e "$source" ]] || continue
    backup="$RELEASE_ROOT/.$name.previous"
    rm -rf "$backup"
    if [[ -e "$destination" ]]; then
      mv "$destination" "$backup"
    fi
    if ! mv "$source" "$destination"; then
      [[ ! -e "$backup" ]] || mv "$backup" "$destination"
      die "could not activate host service-package $name"
    fi
    rm -rf "$backup"
  done
  echo "[builtins-sync] activated $host_target in $RELEASE_ROOT/bin"
else
  echo "[builtins-sync] host target $host_target was not requested; build cache updated only"
fi
