#!/usr/bin/env sh
set -eu

# Verifies the bundle checksums, loads the image into the local Docker engine
# and confirms the loaded image carries the revision recorded in manifest.json.

cd "$(dirname "$0")"

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum -c checksums.sha256
else
  shasum -a 256 -c checksums.sha256
fi

field() {
  sed -n "s/^ *\"$1\": *\"\\(.*\\)\",\\{0,1\\}\$/\\1/p" manifest.json | head -n 1
}
archive="$(field archive)"
reference="$(field reference)"
revision="$(field revision)"
[ -n "$archive" ] && [ -n "$reference" ] && [ -n "$revision" ] || { echo "manifest.json is incomplete" >&2; exit 1; }

docker image load -i "$archive"
# Image IDs differ between Docker image stores, so identity is checked through
# the archive checksum above and the revision label below.
loaded="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$reference")"
if [ "$loaded" != "$revision" ]; then
  echo "loaded image revision $loaded does not match manifest $revision" >&2
  exit 1
fi
echo "loaded $reference (revision $revision)"
echo "set AP_IMAGE_VERSION=${reference#*:} in .env"
