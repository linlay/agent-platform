#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$ROOT_DIR/release-local"

log() {
  printf '[package-local] %s\n' "$*"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf '[package-local] missing required command: %s\n' "$1" >&2
    exit 1
  fi
}

require_cmd mvn

if [ ! -f "$ROOT_DIR/pom.xml" ]; then
  printf '[package-local] pom.xml not found\n' >&2
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
  printf '[package-local] jar not found in target/\n' >&2
  exit 1
fi

cp "$BACKEND_JAR" "$RELEASE_DIR/app.jar"

# 复制运行时数据目录
for dir in agents viewports tools skills data; do
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

# ---------- start.sh ----------
cat >"$RELEASE_DIR/start.sh" <<'STARTEOF'
#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="$APP_DIR/app.pid"
JAR_FILE="$APP_DIR/app.jar"
LOG_FILE="$APP_DIR/app.log"

if [ ! -f "$JAR_FILE" ]; then
  echo "[start] app.jar not found in $APP_DIR" >&2
  exit 1
fi

# 检查是否已在运行
if [ -f "$PID_FILE" ]; then
  OLD_PID=$(cat "$PID_FILE")
  if kill -0 "$OLD_PID" 2>/dev/null; then
    echo "[start] already running (PID $OLD_PID). Use stop.sh first." >&2
    exit 1
  fi
  rm -f "$PID_FILE"
fi

# 加载 .env
if [ -f "$APP_DIR/.env" ]; then
  echo "[start] loading $APP_DIR/.env"
  set -a
  # shellcheck disable=SC1090
  . "$APP_DIR/.env"
  set +a
fi

# JVM 参数
JAVA_OPTS="${JAVA_OPTS:--server -Xms256m -Xmx512m}"

# 目录类环境变量默认解析为安装目录的绝对路径
export AGENT_EXTERNAL_DIR="${AGENT_EXTERNAL_DIR:-$APP_DIR/agents}"
export AGENT_VIEWPORT_EXTERNAL_DIR="${AGENT_VIEWPORT_EXTERNAL_DIR:-$APP_DIR/viewports}"
export AGENT_TOOLS_EXTERNAL_DIR="${AGENT_TOOLS_EXTERNAL_DIR:-$APP_DIR/tools}"
export AGENT_SKILL_EXTERNAL_DIR="${AGENT_SKILL_EXTERNAL_DIR:-$APP_DIR/skills}"
export MEMORY_CHAT_DIR="${MEMORY_CHAT_DIR:-$APP_DIR/chats}"

# Spring 额外配置文件
SPRING_OPTS=""
if [ -f "$APP_DIR/application-local.yml" ]; then
  SPRING_OPTS="--spring.config.additional-location=file:$APP_DIR/application-local.yml"
fi

DAEMON=false
if [ "${1:-}" = "-d" ]; then
  DAEMON=true
fi

if [ "$DAEMON" = true ]; then
  echo "[start] starting in background, log: $LOG_FILE"
  # shellcheck disable=SC2086
  nohup java $JAVA_OPTS -jar "$JAR_FILE" $SPRING_OPTS >"$LOG_FILE" 2>&1 &
  APP_PID=$!
  echo "$APP_PID" >"$PID_FILE"
  echo "[start] started (PID $APP_PID)"
else
  echo "[start] starting in foreground (Ctrl+C to stop)"
  # 前台模式写入 PID 文件，退出时清理
  cleanup() { rm -f "$PID_FILE"; }
  trap cleanup EXIT INT TERM
  echo "$$" >"$PID_FILE"
  # shellcheck disable=SC2086
  exec java $JAVA_OPTS -jar "$JAR_FILE" $SPRING_OPTS
fi
STARTEOF
chmod +x "$RELEASE_DIR/start.sh"

# ---------- stop.sh ----------
cat >"$RELEASE_DIR/stop.sh" <<'STOPEOF'
#!/usr/bin/env bash
set -euo pipefail

APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PID_FILE="$APP_DIR/app.pid"
WAIT_SECONDS=30

if [ ! -f "$PID_FILE" ]; then
  echo "[stop] no PID file found, nothing to stop."
  exit 0
fi

APP_PID=$(cat "$PID_FILE")

if ! kill -0 "$APP_PID" 2>/dev/null; then
  echo "[stop] process $APP_PID not running, cleaning up PID file."
  rm -f "$PID_FILE"
  exit 0
fi

echo "[stop] sending SIGTERM to $APP_PID ..."
kill "$APP_PID"

ELAPSED=0
while kill -0 "$APP_PID" 2>/dev/null; do
  if [ "$ELAPSED" -ge "$WAIT_SECONDS" ]; then
    echo "[stop] process $APP_PID did not exit after ${WAIT_SECONDS}s, sending SIGKILL ..."
    kill -9 "$APP_PID" 2>/dev/null || true
    break
  fi
  sleep 1
  ELAPSED=$((ELAPSED + 1))
done

rm -f "$PID_FILE"
echo "[stop] stopped."
STOPEOF
chmod +x "$RELEASE_DIR/stop.sh"

# ---------- .env.example ----------
cat >"$RELEASE_DIR/.env.example" <<'ENVEOF'
# ============================================================
# Environment Configuration for Local Deployment
# Copy this file to .env and edit as needed.
# ============================================================

# --- Server ---
SERVER_PORT=8080

# --- Auth ---
AGENT_AUTH_ENABLED=false
# AGENT_AUTH_JWKS_URI=
# AGENT_AUTH_ISSUER=

# --- LLM Provider Keys ---
# Configure API keys in application-local.yml

# --- JVM ---
# JAVA_OPTS=-server -Xms256m -Xmx512m

# --- Bash Tool Security ---
# The bash tool requires explicit allowlists.
# If AGENT_BASH_ALLOWED_COMMANDS is empty, all _bash_ calls are rejected.
# working-directory does NOT imply path permission; set AGENT_BASH_ALLOWED_PATHS explicitly.
#
# Read-only monitoring example:
# AGENT_BASH_ALLOWED_COMMANDS=ls,pwd,cat,head,tail,top,free,df,git
# AGENT_BASH_PATH_CHECKED_COMMANDS=ls,cat,head,tail,git
# AGENT_BASH_ALLOWED_PATHS=/data/logs,/data/reports
# AGENT_BASH_WORKING_DIRECTORY=/data
#
# Developer assistant example:
# AGENT_BASH_ALLOWED_COMMANDS=ls,pwd,cat,head,tail,top,free,df,git,node,npm,npx,python3,mvn,java,curl,wget,jq,grep,find,echo,date
# AGENT_BASH_PATH_CHECKED_COMMANDS=ls,cat,head,tail,git
# AGENT_BASH_ALLOWED_PATHS=/home/user/projects
# AGENT_BASH_WORKING_DIRECTORY=/home/user/workspace

# --- Chat Event Callback ---
# AGENT_CHAT_EVENT_CALLBACK_ENABLED=true
# AGENT_CHAT_EVENT_CALLBACK_URL=http://127.0.0.1:38080/api/app/internal/chat-events
# AGENT_CHAT_EVENT_CALLBACK_SECRET=change-me

# --- Directories (defaults to paths relative to install dir) ---
# AGENT_EXTERNAL_DIR=./agents
# AGENT_VIEWPORT_EXTERNAL_DIR=./viewports
# AGENT_TOOLS_EXTERNAL_DIR=./tools
# AGENT_SKILL_EXTERNAL_DIR=./skills
# MEMORY_CHAT_DIR=./chats
ENVEOF

# ---------- DEPLOY.md ----------
cat >"$RELEASE_DIR/DEPLOY.md" <<'DEPLOYEOF'
# Local Deployment Guide

## Prerequisites

- **Java 21+** (required)
- **Python 3** (optional, for skills that use Python scripts)

## Quick Start

```bash
# 1. Copy .env.example and configure
cp .env.example .env
# Edit .env with your settings

# 2. Create application-local.yml with LLM provider API keys
touch application-local.yml
# Add provider configuration (see project docs for schema)

# 3. Create chat memory directory
mkdir -p chats

# 4. Start (foreground)
./start.sh

# 4a. Or start in background
./start.sh -d

# 5. Stop (when running in background)
./stop.sh
```

## Bash Tool Configuration

The bash tool requires explicit allowlists. If `AGENT_BASH_ALLOWED_COMMANDS` is
empty, `_bash_` rejects all commands. Use `.env` to configure command/path scope.

### Read-only Monitoring

Minimal permissions for agents that only need to inspect logs and system status:

```bash
AGENT_BASH_ALLOWED_COMMANDS=ls,pwd,cat,head,tail,top,free,df,git
AGENT_BASH_PATH_CHECKED_COMMANDS=ls,cat,head,tail,git
AGENT_BASH_ALLOWED_PATHS=/data/logs,/data/reports
AGENT_BASH_WORKING_DIRECTORY=/data
```

### Developer Assistant

Extended permissions for agents that help with development tasks:

```bash
AGENT_BASH_ALLOWED_COMMANDS=ls,pwd,cat,head,tail,top,free,df,git,node,npm,npx,python3,mvn,java,curl,wget,jq,grep,find,echo,date
AGENT_BASH_PATH_CHECKED_COMMANDS=ls,cat,head,tail,git
AGENT_BASH_ALLOWED_PATHS=/home/user/projects
AGENT_BASH_WORKING_DIRECTORY=/home/user/workspace
```

## Directory Structure

```
release-local/
├── app.jar              # Application JAR
├── start.sh             # Start script (-d for background)
├── stop.sh              # Stop script (graceful shutdown)
├── .env                 # Environment variables (create from .env.example)
├── .env.example         # Environment template
├── application-local.yml # Spring config override (create manually)
├── app.pid              # PID file (auto-managed)
├── app.log              # Log file (background mode)
├── agents/              # Agent JSON definitions
├── viewports/           # Viewport files
├── tools/               # Tool definition files
├── skills/              # Skill directories
├── data/                # Data files
├── chats/               # Chat memory (auto-created)
└── libs/                # SDK JARs
```

## JVM Tuning

Override JVM options via the `JAVA_OPTS` environment variable in `.env`:

```bash
# Default
JAVA_OPTS=-server -Xms256m -Xmx512m

# Production (adjust based on available memory)
JAVA_OPTS=-server -Xms512m -Xmx2g -XX:+UseG1GC
```
DEPLOYEOF

log "release-local package generated:"
log "  $RELEASE_DIR/app.jar"
log "  $RELEASE_DIR/start.sh"
log "  $RELEASE_DIR/stop.sh"
log "  $RELEASE_DIR/.env.example"
log "  $RELEASE_DIR/DEPLOY.md"
for dir in agents viewports tools skills data libs; do
  [ -d "$RELEASE_DIR/$dir" ] && log "  $RELEASE_DIR/$dir/"
done
