#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "[release-clean-test] this regression runs on a Darwin arm64 release runner" >&2
  exit 1
fi

version="$(tr -d '[:space:]' <"$REPO_ROOT/VERSION")"
archive="$REPO_ROOT/dist/release/agent-platform-${version}-darwin-arm64.tar.gz"
artifact_dir="$REPO_ROOT/dist/kbase-lance-engine/darwin-arm64"

rm -rf "$REPO_ROOT/dist"
(
  cd "$REPO_ROOT"
  unset ARCH PROGRAM_TARGET_MATRIX PROGRAM_TARGETS
  REQUIRE_KBASE_RELEASE_METADATA=1 \
    REQUIRE_RELEASE_SBOM=1 \
    make release-program
)

[[ -f "$artifact_dir/kbase-lance-engine" ]]
[[ -f "$artifact_dir/kbase-lance-engine.sha256" ]]
[[ -s "$artifact_dir/cargo-metadata.json" ]]
[[ -s "$artifact_dir/sbom.cdx.json" ]]
[[ -f "$archive" ]]
[[ -f "$archive.sha256" ]]
[[ -f "$archive.sizes.json" ]]
[[ -f "$archive.sbom.cdx.json" ]]

(
  cd "$REPO_ROOT"
  go run ./cmd/verify-program-bundle --archive "$archive" --os darwin --arch arm64
)

for path in \
  agent-platform/bin/kbase-lance-engine \
  agent-platform/licenses/kbase-lance-engine/THIRD_PARTY_COMPONENTS.json \
  agent-platform/sbom/kbase-lance-engine.cdx.json; do
  tar -tzf "$archive" | grep -Fqx "$path"
done

echo "[release-clean-test] passed: $archive"
