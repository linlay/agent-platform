#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$ROOT_DIR/release"
DOCKERFILE="$ROOT_DIR/Dockerfile"

log() {
  printf '[package] %s\n' "$*"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf '[package] missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

require_cmd mvn

if [ ! -f "$ROOT_DIR/pom.xml" ]; then
  printf '[package] pom.xml not found\n' >&2
  exit 1
fi

if [ ! -f "$DOCKERFILE" ]; then
  printf '[package] Dockerfile not found in project root\n' >&2
  exit 1
fi

if [ -f "$ROOT_DIR/.env" ]; then
  log "load environment from $ROOT_DIR/.env"
  set -a
  # shellcheck disable=SC1090
  . "$ROOT_DIR/.env"
  set +a
fi

log "clean release directory: $RELEASE_DIR"
rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

log "build backend jar"
(
  cd "$ROOT_DIR"
  mvn -q -DskipTests package
)

BACKEND_JAR="$(find "$ROOT_DIR/target" -maxdepth 1 -type f -name '*.jar' ! -name '*original*.jar' | head -n 1)"
if [ -z "${BACKEND_JAR:-}" ]; then
  printf '[package] jar not found in target/\n' >&2
  exit 1
fi

cp "$BACKEND_JAR" "$RELEASE_DIR/app.jar"
cp "$DOCKERFILE" "$RELEASE_DIR/Dockerfile"
cp "$ROOT_DIR/settings.xml" "$RELEASE_DIR/settings.xml"

# 复制运行时数据目录
for dir in agents viewports tools skills; do
  if [ -d "$ROOT_DIR/$dir" ]; then
    cp -R "$ROOT_DIR/$dir" "$RELEASE_DIR/$dir"
    log "copied $dir/"
  fi
done

# 复制 SDK jar
if [ -d "$ROOT_DIR/libs" ]; then
  cp -R "$ROOT_DIR/libs" "$RELEASE_DIR/libs"
  log "copied libs/"
fi

# 生成 docker-compose.yml
cat >"$RELEASE_DIR/docker-compose.yml" <<'EOF'
services:
  agent-platform:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: agent-platform
    restart: unless-stopped
    ports:
      - "${SERVER_PORT:-8080}:8080"
    volumes:
      - ./agents:/opt/agents
      - ./viewports:/opt/viewports
      - ./tools:/opt/tools
      - ./skills:/opt/skills
      - ./chats:/opt/chats
      - ./application-local.yml:/opt/application.yml:ro
    env_file:
      - .env
EOF

# 生成 .env.example
cat >"$RELEASE_DIR/.env.example" <<'EOF'
# Server
SERVER_PORT=8080

# Auth
AGENT_AUTH_ENABLED=false
# AGENT_AUTH_JWKS_URI=
# AGENT_AUTH_ISSUER=

# LLM provider keys (configure in application-local.yml)

# Bash tool security (explicit allowlists required)
# AGENT_BASH_WORKING_DIRECTORY=/opt
# AGENT_BASH_ALLOWED_PATHS=/opt/agents,/opt/chats
# AGENT_BASH_ALLOWED_COMMANDS=ls,pwd,cat,head,tail,top,free,df,git
# AGENT_BASH_PATH_CHECKED_COMMANDS=ls,cat,head,tail,git
EOF

# 生成部署说明
cat >"$RELEASE_DIR/DEPLOY.md" <<'EOF'
# Release Deployment

1. Copy this `release` directory to the target host.
2. Create environment and config files:

   cp .env.example .env
   # Edit .env with production values

   # Create application-local.yml with LLM provider API keys
   touch application-local.yml

3. Create data directory for chat memory:

   mkdir -p chats

4. Start with Docker Compose:

   docker compose up -d --build
EOF

log "release package generated:"
log "  $RELEASE_DIR/app.jar"
log "  $RELEASE_DIR/Dockerfile"
log "  $RELEASE_DIR/settings.xml"
log "  $RELEASE_DIR/docker-compose.yml"
log "  $RELEASE_DIR/.env.example"
log "  $RELEASE_DIR/DEPLOY.md"
for dir in agents viewports tools skills libs; do
  [ -d "$RELEASE_DIR/$dir" ] && log "  $RELEASE_DIR/$dir/"
done
