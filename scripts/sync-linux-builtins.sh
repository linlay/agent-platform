#!/usr/bin/env bash
set -euo pipefail

# Runs scripts/sync-local-builtins.sh for linux/amd64 inside the pinned builder
# image, so a non-Linux workstation can prepare build/builtins/linux-amd64/.
# Inside the container the host target is linux/amd64, which keeps the
# canonical lock's native-host rule intact. Extra arguments are not accepted:
# the target is fixed.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE="${BUILTINS_BUILDER_IMAGE:-agent-platform-builtins-builder:bookworm}"
PLATFORM="linux/amd64"

die() {
  echo "[builtins-sync-linux] $*" >&2
  exit 1
}

[[ $# -eq 0 ]] || die "no arguments are accepted; the target is always $PLATFORM"
command -v docker >/dev/null 2>&1 || die "docker is required"

BUILTINS_ROOT="${BUILTINS_ROOT:-$REPO_ROOT/../agent-platform-builtins}"
[[ -d "$BUILTINS_ROOT" ]] || die "missing builtin collection: $BUILTINS_ROOT"
BUILTINS_ROOT="$(cd "$BUILTINS_ROOT" && pwd)"

# A proxy bound to the workstation's loopback is reached from the container
# through Docker's host alias.
container_proxy() {
  printf '%s' "$1" | sed -E 's#//(127\.0\.0\.1|localhost)([:/]|$)#//host.docker.internal\2#'
}
proxy_args=()
build_proxy_args=()
for name in HTTP_PROXY HTTPS_PROXY NO_PROXY; do
  lower="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
  value="${!name:-${!lower:-}}"
  [[ -n "$value" ]] || continue
  value="$(container_proxy "$value")"
  proxy_args+=(-e "$name=$value" -e "$lower=$value")
  build_proxy_args+=(--build-arg "$name=$value")
done
if [[ -n "${GOPROXY:-}" ]]; then
  proxy_args+=(-e "GOPROXY=$GOPROXY")
elif command -v go >/dev/null 2>&1; then
  proxy_args+=(-e "GOPROXY=$(go env GOPROXY)")
fi

docker buildx build --platform "$PLATFORM" --load -t "$IMAGE" \
  ${build_proxy_args[@]+"${build_proxy_args[@]}"} "$SCRIPT_DIR/builtins-builder"

tty_args=()
if [[ -t 0 && -t 1 ]]; then
  tty_args=(-it)
fi

# Toolchain caches live in named volumes: they must not mix with the
# workstation's own caches under build/builtins/, and bind mounts are slow.
docker run --rm --platform "$PLATFORM" ${tty_args[@]+"${tty_args[@]}"} \
  ${proxy_args[@]+"${proxy_args[@]}"} \
  -e GOCACHE=/cache/go-build \
  -e GOMODCACHE=/cache/go-mod \
  -v "$REPO_ROOT:/work/agent-platform" \
  -v "$BUILTINS_ROOT:/work/agent-platform-builtins" \
  -v ap-builtins-go-cache:/cache \
  -v ap-builtins-cargo-registry:/opt/cargo/registry \
  -v ap-builtins-kbx-target:/work/agent-platform/build/builtins/.cargo-target/kbx \
  -w /work/agent-platform \
  "$IMAGE" bash -c '
    set -euo pipefail
    git config --global --add safe.directory "*"
    exec scripts/sync-local-builtins.sh --target linux/amd64 --builtins-root /work/agent-platform-builtins
  '
