# 版本化离线打包方案

`agent-platform` 现已具备面向 `zenmind-desktop` builtin 分发的 program bundle 发布链路，目标是把 Go 二进制、配置模板、运行目录和启停脚本打成版本化 `tar.gz`。

## 1. 当前状态

目前已具备：

- 根目录 `VERSION`
- `make release` / `make release-program`
- `scripts/release.sh`
- `scripts/release-common.sh`
- `scripts/release-program.sh`
- `scripts/release-assets/program/{start.sh,stop.sh,README.txt}`
- `dist/release/` 版本化 bundle 输出

当前默认产物命名：

- `agent-platform-program-v0.1.0-darwin-arm64.tar.gz`

如需自定义目标矩阵，可通过 `PROGRAM_TARGET_MATRIX` 或 `PROGRAM_TARGETS` 覆盖。

## 2. 发布命令

标准发布命令：

```bash
make release-program
```

等价命令：

```bash
VERSION=$(cat VERSION) ARCH=arm64 bash scripts/release.sh
```

## 3. Bundle 结构

解压后的目录结构如下：

```text
agent-platform/
  agent-platform-runner
  .env.example
  README.txt
  start.sh
  stop.sh
  configs/
    bash.example.yml
    container-hub.example.yml
    cors.example.yml
    local-public-key.example.pem
  runtime/
    registries/
    owner/
    agents/
    teams/
    root/
    schedules/
    chats/
    memory/
    pan/
    skills-market/
```

说明：

- 顶层目录固定为 `agent-platform`，供 Desktop 安装时直接解压到 `userData/services/agent-platform/<version>/`
- `start.sh` 默认以守护进程模式启动，并把 PID / 日志写入 `.runtime/`
- `runtime/` 目录是安装目录内部的默认运行态数据目录

## 4. Desktop 集成约束

该 bundle 供 `zenmind-desktop` 作为 builtin 资源消费，Desktop 会在启动前自动完成：

- 写入 `SERVER_PORT` 默认值
- 注入 `AGENT_CONTAINER_HUB_BASE_URL=http://127.0.0.1:<port>`
- 分发 `configs/local-public-key.pem`
- 强制开启 `AGENT_AUTH_ENABLED=true`

因此 bundle 内只需要携带模板文件，不需要预置真实密钥或固定运行态数据。

## 5. 适用场景

当前推荐两种交付方式：

- Desktop builtin：使用 `make release-program` 产出 tar.gz，随后由 `zenmind-desktop` 的 `npm run sync:assets` 同步到 Electron 资源目录
- Compose / 开发联调：继续使用 `make run`、`make test`、`docker compose up --build`
