#!/usr/bin/env bash
set -euo pipefail

VERSION="${RIPGREP_VERSION:-15.1.0}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VENDOR_DIR="$REPO_ROOT/third_party/ripgrep/$VERSION"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/agent-platform-ripgrep.XXXXXX")"

trap 'rm -rf "$TMP_DIR"' EXIT

asset_for_target() {
  local target="$1"
  case "$target" in
    darwin/amd64) printf 'ripgrep-%s-x86_64-apple-darwin.tar.gz\n' "$VERSION" ;;
    darwin/arm64) printf 'ripgrep-%s-aarch64-apple-darwin.tar.gz\n' "$VERSION" ;;
    linux/amd64) printf 'ripgrep-%s-x86_64-unknown-linux-musl.tar.gz\n' "$VERSION" ;;
    linux/arm64) printf 'ripgrep-%s-aarch64-unknown-linux-gnu.tar.gz\n' "$VERSION" ;;
    windows/amd64) printf 'ripgrep-%s-x86_64-pc-windows-msvc.zip\n' "$VERSION" ;;
    windows/arm64) printf 'ripgrep-%s-aarch64-pc-windows-msvc.zip\n' "$VERSION" ;;
    *) echo "unsupported target: $target" >&2; return 1 ;;
  esac
}

sha_for_target() {
  local target="$1"
  case "$target" in
    darwin/amd64) printf '64811cb24e77cac3057d6c40b63ac9becf9082eedd54ca411b475b755d334882\n' ;;
    darwin/arm64) printf '378e973289176ca0c6054054ee7f631a065874a352bf43f0fa60ef079b6ba715\n' ;;
    linux/amd64) printf '1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599\n' ;;
    linux/arm64) printf '2b661c6ef508e902f388e9098d9c4c5aca72c87b55922d94abdba830b4dc885e\n' ;;
    windows/amd64) printf '124510b94b6baa3380d051fdf4650eaa80a302c876d611e9dba0b2e18d87493a\n' ;;
    windows/arm64) printf '00d931fb5237c9696ca49308818edb76d8eb6fc132761cb2a1bd616b2df02f8e\n' ;;
    *) echo "unsupported target: $target" >&2; return 1 ;;
  esac
}

verify_sha256() {
  local file="$1"
  local expected="$2"
  local actual
  actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  if [[ "$actual" != "$expected" ]]; then
    echo "sha256 mismatch for $file" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    return 1
  fi
}

extract_rg() {
  local archive="$1"
  local target="$2"
  local dest_dir="$3"
  local target_os="${target%%/*}"
  local rg_name="rg"
  local extract_dir="$TMP_DIR/extract-${target//\//-}"
  local found

  rm -rf "$extract_dir"
  mkdir -p "$extract_dir" "$dest_dir"
  if [[ "$target_os" == "windows" ]]; then
    rg_name="rg.exe"
    unzip -q "$archive" -d "$extract_dir"
  else
    tar -xzf "$archive" -C "$extract_dir"
  fi

  found="$(find "$extract_dir" -type f -name "$rg_name" -print -quit)"
  if [[ -z "$found" ]]; then
    echo "could not find $rg_name in $archive" >&2
    return 1
  fi
  cp "$found" "$dest_dir/$rg_name"
  if [[ "$target_os" != "windows" ]]; then
    chmod +x "$dest_dir/$rg_name"
  fi
}

targets=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
  "windows/arm64"
)

mkdir -p "$VENDOR_DIR"
for target in "${targets[@]}"; do
  asset="$(asset_for_target "$target")"
  expected_sha="$(sha_for_target "$target")"
  url="https://github.com/BurntSushi/ripgrep/releases/download/$VERSION/$asset"
  archive="$TMP_DIR/$asset"
  dest="$VENDOR_DIR/${target//\//-}"

  echo "[vendor] downloading $asset"
  curl -fL --retry 3 -o "$archive" "$url"
  verify_sha256 "$archive" "$expected_sha"
  extract_rg "$archive" "$target" "$dest"
done

curl -fL --retry 3 -o "$VENDOR_DIR/LICENSE-MIT" "https://raw.githubusercontent.com/BurntSushi/ripgrep/$VERSION/LICENSE-MIT"
curl -fL --retry 3 -o "$VENDOR_DIR/UNLICENSE" "https://raw.githubusercontent.com/BurntSushi/ripgrep/$VERSION/UNLICENSE"

echo "[vendor] ripgrep $VERSION written to $VENDOR_DIR"
