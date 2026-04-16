# agent-platform-runner-go

本仓库是 `agent-platform-runner` 的 Go 版运行时实现，当前以 Java runner 的 `.env` / `application.yml` 契约为事实源，支持目录驱动的 agents / teams / skills catalog、JWT 鉴权、resource ticket、chat 文件落盘、remember 输出、Container Hub sandbox，以及最小 OpenAI 兼容模型与 backend tool loop。

> 项目事实、架构与开发约束见 [CLAUDE.md](./CLAUDE.md)，补充说明见 [docs/](./docs)。

## 1. 项目简介

当前已提供的接口：

- `GET /api/agents`
- `GET /api/agent?agentKey=...`
- `GET /api/teams`
- `GET /api/skills`
- `GET /api/tools`
- `GET /api/tool?toolName=...`
- `GET /api/chats`
- `GET /api/chat?chatId=...`
- `POST /api/read`
- `POST /api/query`
- `POST /api/submit`
- `POST /api/steer`
- `POST /api/interrupt`
- `POST /api/remember`
- `POST /api/learn`
- `GET /api/viewport?viewportKey=...`
- `GET /api/resource?file=...`
- `POST /api/upload`

返回格式约定：

- `POST /api/query` 成功时返回真实流式 SSE event stream，服务端会按 provider 原始流式 chunk 逐步透传 `content.delta`，结束时追加 `data: [DONE]`。
- 其余 JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

- `code = 0` 表示成功，失败时 `code` 使用 HTTP 状态码数值。
- `GET /api/chat` 默认返回 `events`，`includeRawMessages=true` 时追加 `rawMessages`。
- `GET /api/viewport` 会先读取 `registries/viewports` 下的本地 `.html/.qlc` 模板，再尝试 `registries/viewport-servers` 中注册的远端 viewport server，命中失败时才返回 fallback 占位结果。
- `POST /api/submit` 使用 awaiting 协议：请求体必须包含 `runId` 与 `awaitingId`。

当前仍未与 Java 版完全对齐的能力主要集中在 frontend tool 完整闭环、MCP 实接，以及更深层的 memory / schedule 执行编排细节；配置契约、catalog API、基础鉴权与 resource ticket 已按 Java 语义接入。

## 2. 快速开始

### 前置要求

- Go 1.22 或兼容版本
- Docker / Docker Compose（如需容器运行）
- 可用的 provider / model 注册文件（放在 `runtime/registries/`）

### 本地启动

```bash
cp .env.example .env
make run
```

`make run` 会先加载根目录 `.env`，并按参考仓库同样的入口规则把 `HOST_PORT` 映射到本地监听端口。日常本地联调和 `docker compose` 都优先使用 `HOST_PORT`；`SERVER_PORT` 仅保留为兼容/高级覆盖项。`make run` 还会默认带上 `CGO_ENABLED=0`，以规避当前 macOS 环境里 `CGO=1` 的 `net/http` 二进制在进入 `main()` 前被系统直接 `signal: killed` 的问题。直接执行 `go run ./cmd/agent-platform-runner` 不会自动加载 `.env`，也不会自动注入这个默认值。

常用验证：

```bash
curl http://127.0.0.1:11949/api/agents
curl "http://127.0.0.1:11949/api/agent?agentKey=go_runner"
curl http://127.0.0.1:11949/api/chats
```

### 测试

```bash
make test
```

默认 `make test` 同样会使用 `CGO_ENABLED=0`，并通过串行包测试加临时 `GOCACHE` 规避当前 macOS 环境里的并发 test/cache 异常；它也不会运行依赖真实 loopback 端口绑定的测试。需要显式验证真实本地 socket 流式链路时，使用：

```bash
RUN_SOCKET_TESTS=1 make test-integration
```

## 3. 配置说明

所有本地配置从 `.env.example` 复制到 `.env`。`.env` 不提交；`.env.example` 只保留推荐给普通部署者的最终用户配置入口，默认值的单一事实源仍以代码和 `configs/*.example.yml` 模板为准。更完整的高级、排障和兼容性环境变量参考见 [docs/configuration-reference.md](./docs/configuration-reference.md)。

### 根 `.env.example`

根 `.env.example` 现在是面向最终用户的最小启动模板，默认保留以下高频配置：

- `HOST_PORT`
- `SERVER_PORT`
- `AGENT_AUTH_ENABLED`
- `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE`
- `AGENT_AUTH_JWKS_URI`
- `AGENT_AUTH_ISSUER`
- `CHAT_RESOURCE_TICKET_ENABLED`
- `CHAT_IMAGE_TOKEN_SECRET`
- `AGENT_CONTAINER_HUB_*`
- `AGENT_WS_ENABLED`
- `AGENT_SSE_INCLUDE_TOOL_PAYLOAD_EVENTS`
- `AGENT_DEFAULT_*`
- `REGISTRIES_DIR` / `OWNER_DIR` / `AGENTS_DIR` / `TEAMS_DIR` / `ROOT_DIR` / `SCHEDULES_DIR` / `CHATS_DIR` / `MEMORY_DIR` / `SKILLS_MARKET_DIR` / `PAN_DIR`
- `PROVIDER_APIKEY_KEY_PART`

以下环境变量仍受 Go runner 支持，但为了降低最终用户理解成本，默认不再出现在 `.env.example` 中：

- 传输与渲染调试：`AGENT_SSE_HEARTBEAT_INTERVAL_MS`、`AGENT_H2A_RENDER_*`
- WebSocket 深度调优：`AGENT_WS_MAX_MESSAGE_SIZE`、`AGENT_WS_PING_INTERVAL_MS`、`AGENT_WS_WRITE_TIMEOUT_MS`、`AGENT_WS_WRITE_QUEUE_SIZE`、`AGENT_WS_MAX_OBSERVES_PER_CONN`
- 日志排障：`LOGGING_AGENT_*`
- memory / chat storage 深度调优：`AGENT_MEMORY_*`、`CHAT_STORAGE_*`

LLM 交互日志、SSE/H2A 传输参数、WebSocket 开关和 memory/chat storage 细粒度参数的默认值、适用人群和注意事项统一见 [docs/configuration-reference.md](./docs/configuration-reference.md)。

Provider `apiKey` 支持两种写法：

- 明文：`apiKey: sk-...`
- 弱对抗密文：`apiKey: AES(...)`

当使用 `AES(...)` 时，runner 会在加载 provider registry 时自动解密，并继续把还原后的真实 key 用于上游请求头。需要同时满足程序内置 code part 和环境变量 `PROVIDER_APIKEY_KEY_PART`。这套方案只用于“防直接看配置文件”，不等同于真正的 secret manager；明文 `apiKey` 仍然兼容，便于渐进迁移和回滚。旧的 `AES(v1:...)` 已不再支持，需要重新生成密文。

### `configs/` 目录

本仓库保留与参考仓库一致的结构化配置入口：

- `configs/bash.example.yml`
- `configs/container-hub.example.yml`
- `configs/cors.example.yml`
- `configs/local-public-key.example.pem`

当前 Go runner 实际会读取：

- `configs/bash.yml`
- `configs/container-hub.yml`
- `configs/cors.yml`
- `configs/local-public-key.pem`

`configs/` 不是可配置目录，固定使用 runner 根目录下的 `./configs`；容器内固定挂载到 `/opt/configs`。

本地 JWT 公钥规则：

- 默认值是 `configs/local-public-key.pem`
- `AGENT_AUTH_LOCAL_PUBLIC_KEY_FILE` 若是绝对路径，则原样使用
- 若是相对路径，则按项目根目录解析
- 若为了兼容仍写成单文件名 `local-public-key.pem`，会自动解析到 `configs/local-public-key.pem`

配置优先级：

- 无外部 yml: 代码默认值 `<` 环境变量
- 有 `configs/*.yml`: 代码默认值 `<` yml `<` 环境变量

### 当前明确拒绝的旧变量

以下遗留变量如果显式设置，服务会在启动时直接报错，而不是静默忽略：

- 旧 `RUNTIME_DIR`
- 旧 `AGENT_CONFIG_DIR`
- 旧 `AGENT_MEMORY_STORAGE_DIR`
- 旧 `MEMORY_CHATS_*`
- 旧 `*_EXTERNAL_DIR`

详细配置见 [docs/configuration-reference.md](./docs/configuration-reference.md)。

## 4. 部署

### 容器构建

```bash
docker build -t agent-platform-runner-go:latest .
```

### 本地编排

```bash
cp .env.example .env
docker compose config
docker compose up --build
```

`compose.yml` 与参考仓库保持同样的目录变量工作流：

- 使用 `env_file: .env`
- 本地 `make run` 会优先把 `HOST_PORT` 作为监听端口
- 宿主机端口映射为 `${HOST_PORT}:8080`
- 容器内应用监听端口固定为 `8080`；`HOST_PORT` 负责对外暴露端口
- 容器内目录固定为 `/opt/registries`、`/opt/owner`、`/opt/agents`、`/opt/teams`、`/opt/root`、`/opt/schedules`、`/opt/chats`、`/opt/memory`、`/opt/pan`、`/opt/skills-market`
- `./configs` 只读挂载到 `/opt/configs`

Container Hub 默认基础挂载当前固定为 7 个：

- `/workspace` -> `CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `ROOT_DIR`（`rw`）
- `/skills` -> `AGENTS_DIR/<agentKey>/skills`（`run/agent`）或 `SKILLS_MARKET_DIR`（`global`），`ro`
- `/pan` -> `PAN_DIR`（`rw`）
- `/agent` -> `AGENTS_DIR/<agentKey>`（`ro`，必挂载；目录缺失会 fail-fast）
- `/owner` -> `OWNER_DIR`（`ro`，目录缺失时自动创建）
- `/memory` -> `MEMORY_DIR/<agentKey>`（`ro`，目录缺失时自动创建）

`sandboxConfig.extraMounts` 会真实影响 Container Hub session mounts：

- `platform + mode`：恢复按需平台挂载，或覆盖默认 `/agent`、`/owner`、`/memory` 模式
- `destination + mode`：覆盖默认基础挂载模式
- `source + destination + mode`：新增自定义挂载，不能拿来覆盖默认基础挂载路径

`context tags` 不是全局默认集合，而是每个 agent 从 `contextConfig.tags` 或 `contextTags` 读取。当前支持/归一化后的标签有 `system`、`context`、`owner`、`auth`、`sandbox`、`all-agents`、`memory`；其中 `agent_identity`、`run_session`、`scene`、`references`、`execution_policy`、`skills` 会归一化为 `context`，`memory_context` 会归一化为 `memory`。

部署时的敏感信息应通过环境变量或 Secret 注入，不要写入仓库文件。

## 5. 运维

### 查看日志

```bash
docker compose logs -f
```

计划任务目前没有单独日志文件，统一写进服务进程的 stdout：

- 用 `make run` 本地启动时，直接看启动它的终端输出
- 用 `docker compose up` 启动时，使用 `docker compose logs -f`

### 常见排查

- 服务无法启动：先检查环境里是否设置了已废弃的旧变量，或鉴权公钥 / JWKS 配置是否不完整。
- Query 无法调用模型：检查 `REGISTRIES_DIR/providers`、`REGISTRIES_DIR/models` 是否存在，并确认 provider `apiKey` / `baseUrl` 可用。
- 若 provider 使用 `apiKey: AES(...)`：确认 `.env` 或进程环境中已提供 `PROVIDER_APIKEY_KEY_PART`，且与当前密文匹配；旧 `AES(v1:...)` 需先重生成。
- Schedule 看起来没有触发：先确认服务进程本身正在运行；如果是本地 `make run`，日志不会出现在 `docker compose logs` 里。随后检查 stdout 中是否有 `schedule orchestrator started`、`[schedule] registered ...`、`[schedule] dispatch ...`。
- Query 看起来不像真流式：先检查是否启用了 `AGENT_H2A_RENDER_FLUSH_INTERVAL_MS`、`AGENT_H2A_RENDER_MAX_BUFFERED_CHARS` 或 `AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS` 这类传输层缓冲参数；默认 SSE writer 会逐事件 flush。
- `_sandbox_bash_` 执行失败：检查 `AGENT_CONTAINER_HUB_BASE_URL`、`default-environment-id`，以及 `.env` 中的目录变量是否为宿主机真实路径。
- chat 没有持久化：检查 `CHATS_DIR` 是否可写。
- remember 没有输出文件：确认请求体里同时传了 `requestId` 和 `chatId`。
- 上传后无法下载：确认文件已落到 `CHATS_DIR/<chatId>/`，并检查 `/api/resource?file=...` 是否原样使用。

## 文档索引

- [docs/agent-definition-reference.md](./docs/agent-definition-reference.md)
- [docs/configuration-reference.md](./docs/configuration-reference.md)
- [docs/manual-test-cases.md](./docs/manual-test-cases.md)
- [docs/versioned-release-bundle.md](./docs/versioned-release-bundle.md)
- [docs/改为 go 方案.md](./docs/%E6%94%B9%E4%B8%BA%20go%20%E6%96%B9%E6%A1%88.md)
