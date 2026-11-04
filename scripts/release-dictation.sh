#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$REPO_ROOT/scripts/release-common.sh"
APP_NAME=agent-dictation
ARCH="${ARCH:-$(detect_arch)}"
VERSION="${VERSION:-$(cat "$REPO_ROOT/VERSION")}"
RELEASE_DIR="${RELEASE_DIR:-$REPO_ROOT/dist/release}"
assets="$REPO_ROOT/scripts/release-assets/dictation"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cd "$REPO_ROOT"
target_matrix="$(parse_program_target_matrix)"
while read -r target_os target_arch; do
  bundle="$stage/$target_os-$target_arch/$APP_NAME"
  binary="$APP_NAME"; [[ "$target_os" != windows ]] || binary+='.exe'
  mkdir -p "$bundle/backend" "$bundle/scripts" "$RELEASE_DIR"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -o "$bundle/backend/$binary" ./cmd/agent-dictation
  cp "$assets/.env.example" "$bundle/.env.example"
  cp configs/dictation.example.yml "$bundle/dictation.example.yml"
  suffix=sh; family=unix; format=tar.gz
  if [[ "$target_os" == windows ]]; then suffix=ps1; family=windows; format=zip; fi
  cp "$assets/$family/program-common.$suffix" "$bundle/scripts/"
  for action in start stop deploy; do cp "$assets/$family/$action.$suffix" "$bundle/"; done
  if [[ "$family" == unix ]]; then chmod +x "$bundle/"*.sh "$bundle/scripts/"*.sh; fi
  asset="$APP_NAME-$VERSION-$target_os-$target_arch.$format"
  go run ./cmd/render-program-manifest --template "$assets/manifest.template.json" --output "$bundle/manifest.json" --version "$VERSION" --os "$target_os" --arch "$target_arch" --backend "backend/$binary" --asset "$asset"
  archive_bundle_dir "$(dirname "$bundle")" "$APP_NAME" "$RELEASE_DIR/$asset" "$format"
  write_release_checksum "$RELEASE_DIR/$asset"
done <<< "$target_matrix"
