#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
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
if [ -f "$APP_DIR/application.yml" ]; then
  SPRING_OPTS="--spring.config.additional-location=file:$APP_DIR/application.yml"
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

# ---------- DEPLOY.md ----------
cat >"$RELEASE_DIR/DEPLOY.md" <<'DEPLOYEOF'
# Local Deployment Guide

## Prerequisites

- **Java 21+** (required)
- **Python 3** (optional, for skills that use Python scripts)

## Quick Start

```bash
# 1. Prepare runtime .env (setup usually creates release-local/.env)
# Edit .env with your settings

# 2. Ensure application.yml exists
# setup usually creates it from application.example.yml
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
├── .env                 # Environment variables (created by setup or manually)
├── application.yml # Spring config override (created by setup or manually)
├── app.pid              # PID file (auto-managed)
├── app.log              # Log file (background mode)
├── agents/              # Agent JSON definitions
├── viewports/           # Viewport files
├── tools/               # Tool definition files
├── skills/              # Skill directories
├── data/                # Data files
└── chats/               # Chat memory (auto-created)
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
log "  $RELEASE_DIR/DEPLOY.md"
for dir in agents viewports tools skills data; do
  [ -d "$RELEASE_DIR/$dir" ] && log "  $RELEASE_DIR/$dir/"
done
