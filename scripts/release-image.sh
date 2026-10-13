#!/usr/bin/env bash
set -euo pipefail

# Builds the linux/amd64 runtime image from the verified builtin cache and
# writes an offline deployment bundle under dist/release/. The server that
# receives the bundle only needs Docker: no source checkout, no compiler.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE_ASSETS_DIR="$SCRIPT_DIR/release-assets/image"

# shellcheck disable=SC1091
. "$SCRIPT_DIR/release-common.sh"

TARGET_OS=linux
TARGET_ARCH=amd64
PLATFORM="$TARGET_OS/$TARGET_ARCH"

[[ $# -eq 0 ]] || die "release-image accepts no arguments; the target is always $PLATFORM"
for tool in docker go python3 openssl gzip; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
resolve_release_context
cd "$REPO_ROOT"

cache_dir="$REPO_ROOT/build/builtins/$TARGET_OS-$TARGET_ARCH"
[[ -d "$cache_dir" ]] || die "missing builtin cache $cache_dir; run make sync-builtins-linux first"

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/agent-platform-image-release.XXXXXX")"
smoke_name="agent-platform-release-smoke-$$"
cleanup() {
  docker rm -f "$smoke_name" >/dev/null 2>&1 || true
  docker volume rm "$smoke_name" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

# The image is built from this verified copy, not from the cache directory, so
# what was checked against the lock is exactly what ships.
verified_builtins="$tmp_dir/verified-builtins"
echo "[release] verifying builtin cache $cache_dir"
"$SCRIPT_DIR/stage-builtins.sh" --output "$verified_builtins" \
  --os "$TARGET_OS" --arch "$TARGET_ARCH" --cache-dir "$cache_dir" >/dev/null

revision="$(git rev-parse HEAD)"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  revision+="-dirty"
fi
created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
reference="$APP_NAME:$VERSION"

# A proxy bound to the workstation's loopback is reached from the builder
# through Docker's host alias.
build_args=(--build-arg "VERSION=$VERSION" --build-arg "REVISION=$revision" --build-arg "CREATED=$created")
for name in HTTP_PROXY HTTPS_PROXY NO_PROXY; do
  lower="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
  value="${!name:-${!lower:-}}"
  [[ -n "$value" ]] || continue
  value="$(printf '%s' "$value" | sed -E 's#//(127\.0\.0\.1|localhost)([:/]|$)#//host.docker.internal\2#')"
  build_args+=(--build-arg "$name=$value")
done
build_args+=(--build-arg "GOPROXY=${GOPROXY:-$(go env GOPROXY)}")

echo "[release] building $reference for $PLATFORM (revision $revision)"
docker buildx build --platform "$PLATFORM" --load -t "$reference" "${build_args[@]}" \
  --build-context "builtins=$verified_builtins" "$REPO_ROOT"

# Boot the image the way the deployment Compose does, on the shipped example
# configs. Two example values cannot start as shipped and are replaced here
# exactly as DEPLOY.md tells the operator to: the placeholder public key and
# the document preview endpoints.
echo "[release] smoke-testing $reference"
smoke_configs="$tmp_dir/configs"
mkdir -p "$smoke_configs"
for template in "$REPO_ROOT"/configs/*.example.yml; do
  name="$(basename "$template")"
  cp "$template" "$smoke_configs/${name/.example/}"
done
openssl genrsa -out "$tmp_dir/smoke-key.pem" 2048 2>/dev/null
openssl rsa -in "$tmp_dir/smoke-key.pem" -pubout -out "$smoke_configs/local-public-key.pem" 2>/dev/null
python3 - "$smoke_configs/runtime.yml" <<'PY'
import pathlib, sys
path = pathlib.Path(sys.argv[1])
head, marker, tail = path.read_text(encoding="utf-8").partition("document-preview:")
if not marker or "enabled: true" not in tail:
    raise SystemExit("runtime.example.yml no longer has the expected document-preview block")
path.write_text(head + marker + tail.replace("enabled: true", "enabled: false", 1), encoding="utf-8")
PY
chmod -R a+rX "$smoke_configs"
docker run -d --name "$smoke_name" --platform "$PLATFORM" \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --tmpfs /tmp:exec,mode=1777 --tmpfs /home/agent:exec,uid=10001,gid=10001,mode=0700 \
  -e HOME=/home/agent \
  -v "$smoke_configs:/opt/configs:ro" -v "$smoke_name:/opt/runtime" \
  "$reference" >/dev/null
health=starting
for _ in $(seq 1 30); do
  health="$(docker inspect --format '{{.State.Status}}/{{.State.Health.Status}}' "$smoke_name")"
  case "$health" in
    running/healthy) break ;;
    running/starting) ;;
    *) break ;;
  esac
  sleep 4
done
if [[ "$health" != "running/healthy" ]]; then
  docker logs "$smoke_name" 2>&1 | tail -40 >&2
  die "image smoke test failed: container is $health"
fi
docker exec "$smoke_name" sh -c '
  set -e
  [ "$(id -u)" = 10001 ]
  /opt/bin/rg --version >/dev/null
  /opt/bin/kbx --version >/dev/null
  /opt/bin/pdftotext -v >/dev/null 2>&1
  /opt/connectors/builtin.dbx/bin/dbx --version >/dev/null
  /opt/connectors/builtin.httpx/bin/httpx --version >/dev/null
  ! touch /opt/.write-probe 2>/dev/null
' || die "image smoke test failed: builtin or hardening check"
image_id="$(docker image inspect --format '{{.Id}}' "$reference")"

bundle_name="$APP_NAME-image-$VERSION-$TARGET_OS-$TARGET_ARCH"
bundle_stage="$tmp_dir/$bundle_name"
archive_name="$APP_NAME-$VERSION-$TARGET_OS-$TARGET_ARCH.tar.gz"
mkdir -p "$bundle_stage/configs"

echo "[release] saving $reference"
docker image save "$reference" | gzip -n -9 >"$bundle_stage/$archive_name"

cp "$IMAGE_ASSETS_DIR/compose.yml" "$bundle_stage/compose.yml"
cp "$IMAGE_ASSETS_DIR/load.sh" "$bundle_stage/load.sh"
cp "$IMAGE_ASSETS_DIR/DEPLOY.md" "$bundle_stage/DEPLOY.md"
sed "s/__VERSION__/$VERSION/" "$IMAGE_ASSETS_DIR/env.example" >"$bundle_stage/.env.example"
cp "$REPO_ROOT"/configs/*.example.yml "$REPO_ROOT"/configs/*.example.pem "$bundle_stage/configs/"
chmod +x "$bundle_stage/load.sh"

python3 - "$bundle_stage" "$archive_name" "$verified_builtins/builtins.manifest.json" \
  "$VERSION" "$revision" "$created" "$PLATFORM" "$reference" "$image_id" <<'PY'
import hashlib, json, pathlib, sys
stage, archive, builtins_manifest, version, revision, created, platform, reference, image_id = sys.argv[1:]
stage = pathlib.Path(stage)
components = json.loads(pathlib.Path(builtins_manifest).read_text(encoding="utf-8")).get("components", [])
manifest = {
    "schemaVersion": 1,
    "name": "agent-platform",
    "version": version,
    "revision": revision,
    "created": created,
    "platform": platform,
    "image": {
        "reference": reference,
        "id": image_id,
        "archive": archive,
        "sizeBytes": (stage / archive).stat().st_size,
    },
    "builtins": [{"name": c.get("name"), "version": c.get("version")} for c in components],
}
(stage / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
lines = []
for path in sorted(p for p in stage.rglob("*") if p.is_file()):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    lines.append(f"{digest.hexdigest()}  {path.relative_to(stage).as_posix()}")
(stage / "checksums.sha256").write_text("\n".join(lines) + "\n", encoding="utf-8")
PY

mkdir -p "$RELEASE_DIR"
rm -rf "$RELEASE_DIR/$bundle_name"
mv "$bundle_stage" "$RELEASE_DIR/$bundle_name"
echo "[release] done: $RELEASE_DIR/$bundle_name"
