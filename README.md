# agent-platform

本仓库是 `agent-platform` 的 Go 版运行时实现，当前以 Java runtime 的 `.env` / `application.yml` 契约为事实源，支持目录驱动的 agents / teams / skills catalog、JWT 鉴权、resource ticket、chat 文件落盘、memory learn、Container Hub sandbox，以及最小 OpenAI 协议模型与 backend tool loop。

> 项目事实、架构与开发约束见 [CLAUDE.md](./CLAUDE.md)，补充说明见 [docs/](./docs)。

## 1. 项目简介

当前已提供的接口：

- `GET /api/agents`
- `GET /api/agent?agentKey=...`
- `GET /api/teams`
- `GET /api/admin/skills`
- `GET /api/admin/tools`
- `GET /api/chats`
- `GET /api/chat?chatId=...`
- `POST /api/chats/search`
- `POST /api/read`
- `GET /api/chat/export?chatId=...`
- `GET /api/archives`
- `GET /api/archive?chatId=...`
- `POST /api/archives/search`
- `POST /api/query`
- `POST /api/submit`
- `POST /api/steer`
- `POST /api/interrupt`
- `POST /api/learn`
- `GET /api/viewport?viewportKey=...`
- `GET /api/resource?file=...`
- `POST /api/upload`

返回格式约定：

- `POST /api/query` 成功时默认返回真实流式 SSE event stream，服务端会按 provider 原始流式 chunk 逐步透传 `content.delta`，结束时追加 `data: [DONE]`；请求体传 `stream:false` 时返回普通 JSON，默认 `data` 只包含 `content`，可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`，错拼字段 `steam` 不会被识别。
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
- `GET /api/viewport` 会先读取 `runtime/viewports` 下的本地 `.html/.qlc` 模板，再尝试 `registries/viewport-servers` 中注册的远端 viewport server，命中失败时才返回 fallback 占位结果。
- `GET /api/attach` 与 `POST /api/submit` / `steer` / `interrupt` 都必须携带 `agentKey`，服务端按 `runId` 定位 run 后校验 `agentKey` 匹配。
- `POST /api/submit` 使用 awaiting 协议：请求体必须包含 `agentKey`、`runId` 与 `awaitingId`。
- 文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传继续使用 `POST /api/upload`，下载继续使用 `GET /api/resource?file=...`；`/ws` 只传文件引用与状态，不承载文件字节。当前 `/ws` 的 `/api/upload` 仅支持网关发送 `url + metadata`，由 platform 再通过 HTTP 拉取文件并落盘。
- 文件工具的 `file_read` / `file_glob` / `file_grep` 与 `file_write` / `file_edit` 白名单独立于 bash allowed paths，默认均为 `.,/tmp`；越权访问会走 `mode=approval`，可单次批准或用 `approve_rule_run` 在当前 run 内批准同一规则。

当前仍未与 Java 版完全对齐的能力主要集中在 frontend tool 完整闭环、MCP 实接，以及更深层的 memory / automation 执行编排细节；配置契约、catalog API、基础鉴权与 resource ticket 已按 Java 语义接入。

## 2. 快速开始

### 前置要求

- Go 1.22 或更新版本
- Docker / Docker Compose（如需容器运行）
- 可用的 provider / model 注册文件（放在 `runtime/registries/`）

### 本地启动

```bash
cp .env.example .env
make run
```

`make run` 会先加载根目录 `.env`，并使用 `SERVER_PORT` 作为本地监听端口；未设置时默认监听 `11949`。`make run` 还会默认带上 `CGO_ENABLED=0`，以规避当前 macOS 环境里 `CGO=1` 的 `net/http` 二进制在进入 `main()` 前被系统直接 `signal: killed` 的问题。直接执行 `go run ./cmd/agent-platform` 不会自动加载 `.env`，未设置 `SERVER_PORT` 或 `--port` 时应用代码默认监听 `8080`。

常用验证：

```bash
curl http://127.0.0.1:11949/api/agents
curl "http://127.0.0.1:11949/api/agents?includeChats=5"
curl "http://127.0.0.1:11949/api/agent?agentKey=default_agent"
curl http://127.0.0.1:11949/api/chats
```

HTTP 模式调用 `/api/query` 的可运行 curl：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false}'
```

按需带用量或全过程：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false,"includeUsage":true,"includeFullText":true}'
```

### Mobile Gateway 本地联调

`agent-platform` 不会自行签发 gateway JWT。本地 mobile channel 联调时，需要先生成一对 RSA 密钥，并把预签名 token 写进 `.env`。

```bash
# 1. 在 agent-platform 根目录生成开发用密钥
openssl genrsa -out configs/gateway-private-key.pem 2048
openssl rsa -in configs/gateway-private-key.pem -pubout -out configs/gateway-public-key.pem

# 2. 生成 platform -> gateway 使用的 RS256 JWT
go run ./scripts/gen-gateway-token.go -key configs/gateway-private-key.pem -sub local
```

如果沿用 `configs/channels.example.yml` 中的 `${MOBILE_GATEWAY_JWT_TOKEN}` 示例，可在本地 `.env` 自行添加这个变量；它不再出现在 `.env.example`：

```bash
MOBILE_GATEWAY_JWT_TOKEN=<paste-token-here>
```

然后在本地忽略文件 `configs/channels.yml` 中加入 mobile channel：

```yaml
channels:
  mobile:
    name: 手机 App
    type: gateway
    default-agent: ""
    agents: "*"
    gateway:
      url: ws://127.0.0.1:11945/ws/agent?userId=local&agentKey=personal&channel=mobile
      jwt-token: ${MOBILE_GATEWAY_JWT_TOKEN}
```

注意事项：

- `JWT.sub` 必须和 `gateway.url` 中的 `userId` 完全一致；上例要求 `sub=local`
- `configs/gateway-private-key.pem` 和真实 `configs/channels.yml` 都是本地文件，不提交
- `zenmind-gateway-server` 本地联调时请使用 `make run`，它会自动加载 `.env`

### 测试

```bash
make test
```

默认 `make test` 同样会使用 `CGO_ENABLED=0`，并通过串行包测试加临时 `GOCACHE` 规避当前 macOS 环境里的并发 test/cache 异常；它也不会运行依赖真实 loopback 端口绑定的测试。需要显式验证真实本地 socket 流式链路时，使用：

```bash
RUN_SOCKET_TESTS=1 make test-integration
```

## 3. 配置说明

本地环境变量配置从 `.env.example` 复制到 `.env`。`.env` 不提交；`.env.example` 只保留推荐给普通部署者的最终用户环境变量入口。工具运行时配置使用 `configs/tools.yml`，AI 工具配置使用 `configs/ai-tools.yml`，默认值的单一事实源仍以代码和 `configs/*.example.yml` 模板为准。更完整的高级与排障配置参考见 [配置化说明](./docs/配置化说明.md)。

### 根 `.env.example`

根 `.env.example` 现在是面向最终用户的最小启动模板，默认保留以下高频配置：

- `SERVER_PORT`
- `AP_CHAT_RESOURCE_TICKET_SECRET`
- `AP_CHAT_RESOURCE_TICKET_TTL_SECONDS`
- `AP_CONTAINER_HUB_BASE_URL`
- `AP_DEBUG_LLM_CONSOLE`
- `AP_DEBUG_LLM_CHAT_RECORD`
- `AGENT_DEFAULT_*`
- `RUNTIME_DIR` / `REGISTRIES_DIR` / `CHATS_DIR` / `MEMORY_DIR` / `PAN_DIR`

其中 `AP_*` 变量是 Agent Platform 专属配置的正式入口；迁移期仍兼容对应旧名，若新旧同名配置同时存在，以 `AP_*` 为准。

Auth 默认开启，默认公钥文件为 `configs/local-public-key.pem`；相关默认值展示在 `configs/runtime.example.yml` 的 `auth` 节，根 `.env.example` 不再放 Auth 变量。

以下环境变量仍受 Go runtime 支持，但为了降低最终用户理解成本，默认不再出现在 `.env.example` 中：

- 低频 runtime 子目录覆盖：`OWNER_DIR`、`AGENTS_DIR`、`TEAMS_DIR`、`ROOT_DIR`、`AUTOMATIONS_DIR`、`SKILLS_MARKET_DIR`
- 传输与渲染调试：SSE heartbeat 固定 30s，H2A 缓冲可通过 `AGENT_H2A_RENDER_*` 调整
- WebSocket 深度调优：`AGENT_WS_MAX_MESSAGE_SIZE`、`AGENT_WS_PING_INTERVAL`、`AGENT_WS_WRITE_TIMEOUT`、`AGENT_WS_WRITE_QUEUE_SIZE`、`AGENT_WS_MAX_OBSERVES_PER_CONN`
- 日志排障：`LOGGING_AGENT_*`
- memory / chat storage 深度调优：`AGENT_MEMORY_*`、`CHAT_STORAGE_*`

LLM 交互日志、SSE/H2A 传输参数、WebSocket 调优项和 memory/chat storage 细粒度参数的默认值、适用人群和注意事项统一见 [配置化说明](./docs/配置化说明.md)。

Provider `apiKey` 按明文字符串读取：

- 未配置时保留为空值：`apiKey:`
- runtime 不再支持 provider `apiKey` 加密或解密；包括 `AES(...)` 在内的任何值都会作为普通字符串使用。

### `configs/` 目录

本仓库保留与参考仓库一致的结构化配置入口：

- `configs/ai-tools.example.yml`
- `configs/tools.example.yml`
- `configs/local-public-key.example.pem`
- `configs/channels.example.yml`
- `configs/prompts.example.yml`
- `configs/runtime.example.yml`

当前 Go runtime 实际会读取：

- `configs/ai-tools.yml`
- `configs/channels.yml`
- `configs/tools.yml`
- `configs/local-public-key.pem`
- `configs/prompts.yml`
- `configs/runtime.yml`

`configs/` 不是可配置目录，固定使用 runtime 根目录下的 `./configs`；容器内固定挂载到 `/opt/configs`。

**静态配置**：`configs/` 下所有文件都只在进程启动时读取一次；修改 `configs/*.yml` 或 `configs/*.pem` 后必须重启 runtime 才会生效。

本地 JWT 公钥规则：

- 本地公钥文件固定为 `configs/local-public-key.pem`
- 该路径和文件名不是配置项；要使用本地公钥模式时，必须把公钥放在这个位置
- 配置了 `auth.jwks-uri` 或 `AP_AUTH_JWKS_URI` 时走 JWKS 模式，不读取本地公钥文件

配置优先级：

- 有环境变量入口的配置：代码默认值 `<` yml `<` 仍受支持的环境变量
- 纯 YAML 配置：代码默认值 `<` yml

详细配置见 [配置化说明](./docs/配置化说明.md)。

## 4. 部署

### 容器构建

```bash
docker build -t agent-platform:latest .
```

### 本地编排

```bash
cp .env.example .env
docker compose config
docker compose up --build
```

`compose.yml` 使用同样的 runtime 根目录工作流：

- 使用 `env_file: .env`
- 本地 `make run` 使用 `SERVER_PORT` 作为监听端口
- 宿主机端口映射为 `${SERVER_PORT}:8080`
- 容器内应用监听端口固定为 `8080`
- 宿主机 runtime 根目录来自 `${RUNTIME_DIR:-./runtime}`
- `REGISTRIES_DIR`、`CHATS_DIR`、`MEMORY_DIR`、`PAN_DIR` 可单独覆盖宿主机 bind source；未配置时自然落在 `${RUNTIME_DIR}` 下
- 容器内 runtime 根目录固定为 `/opt/runtime`，应用通过 `RUNTIME_DIR=/opt/runtime` 解析子目录
- `./configs` 只读挂载到 `/opt/configs`

Container Hub 默认基础挂载当前最多 7 个：

- `/workspace` -> `CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `ROOT_DIR`（`rw`）
- `/skills` -> `AGENTS_DIR/<agentKey>/skills`（仅 `run/agent`，`global` 默认不挂载），`ro`
- `/pan` -> `PAN_DIR`（`rw`）
- `/agent` -> `AGENTS_DIR/<agentKey>`（`ro`，必挂载；目录缺失会 fail-fast）
- `/owner` -> `OWNER_DIR`（`ro`，目录缺失时自动创建）
- `/memory` -> `MEMORY_DIR/<agentKey>`（`ro`，目录缺失时自动创建）

`runtimeConfig.sandboxMounts` 会真实影响 Container Hub session mounts：

- `platform + mode`：恢复按需平台挂载，或覆盖默认 `/agent`、`/owner`、`/memory` 模式；`platform: skills-market` 会显式挂载 `/skills-market`
- `destination + mode`：覆盖默认基础挂载模式
- `source + destination + mode`：新增自定义挂载，不能拿来覆盖默认基础挂载路径

`configs/runtime.example.yml` 的 `container-hub` 节展开 `base-url`、默认 environment 和运行策略默认值；代码默认值仍作为未配置时的兜底。`auth-token` 仅保留注释说明，优先通过 `AP_CONTAINER_HUB_AUTH_TOKEN` 或部署 Secret 注入，也仍支持 `container-hub.auth-token` 对接 `agent-container-hub` 的 `AUTH_TOKEN` Bearer 鉴权。

`context tags` 不是全局默认集合，而是每个 agent 从 `contextConfig.tags` 或 `contextTags` 读取。当前支持/归一化后的标签有 `system`、`context`、`owner`、`auth`、`all-agents`、`memory`；其中 `agent_identity`、`run_session`、`scene`、`references`、`execution_policy`、`skills` 会归一化为 `context`，`memory_context` 会归一化为 `memory`。

`sandbox` 不再属于 `context tags`。只要 agent 声明了 `runtimeConfig.environmentId`，运行时就会自动注入 sandbox context。

部署时的敏感信息应通过环境变量或 Secret 注入，不要写入仓库文件。

### 版本化打包

面向 `zenmind-desktop` builtin 分发时，使用 program bundle 发布链路：

```bash
make release-program
```

产物写入 `dist/release/`，包含 Go 二进制、配置模板、runtime 目录骨架和启停脚本。Desktop 集成时再到 `zenmind-desktop` 执行资源同步：

```bash
cd ../zenmind-desktop
npm run sync:assets
```

完整打包细节见 [版本化打包方案](./docs/版本化打包方案.md)。

## 5. 运维

### 查看日志

```bash
docker compose logs -f
```

计划任务目前没有单独日志文件，统一写进服务进程的 stdout：

- 用 `make run` 本地启动时，直接看启动它的终端输出
- 用 `docker compose up` 启动时，使用 `docker compose logs -f`

### 常见排查

- 服务无法启动：先检查当前配置文件、鉴权公钥与 JWKS 配置是否完整。
- Query 无法调用模型：检查 `REGISTRIES_DIR/providers`、`REGISTRIES_DIR/models` 是否存在，并确认 provider `apiKey` / `baseUrl` 可用。
- Automation 看起来没有触发：先确认服务进程本身正在运行；如果是本地 `make run`，日志不会出现在 `docker compose logs` 里。随后检查 stdout 中是否有 `automation orchestrator started`、`[automation] registered ...`、`[automation] dispatch ...`。
- Query 看起来不像真流式：先检查是否启用了 `AGENT_H2A_RENDER_FLUSH_INTERVAL`、`AGENT_H2A_RENDER_MAX_BUFFERED_CHARS` 或 `AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS` 这类传输层缓冲参数；默认 SSE writer 会逐事件 flush。
- `bash` 执行失败：检查 `AP_CONTAINER_HUB_BASE_URL`、`default-environment-id`，以及 `.env` 中的目录变量是否为宿主机真实路径。
- chat 没有持久化：检查 `CHATS_DIR` 是否可写。
- memory learn 未生效：确认 `/api/learn` 请求体、agent memory 配置与 `MEMORY_DIR` 可写性。
- 上传后无法下载：确认文件已落到 `CHATS_DIR/<chatId>/`，并检查 `/api/resource?file=...` 是否原样使用。

## 文档索引

- [智能体配置说明](./docs/智能体配置说明.md)
- [配置化说明](./docs/配置化说明.md)
- [工具目录权限](./docs/工具目录权限.md)
- [真流式和H2A](./docs/真流式和H2A.md)
- [记忆系统](./docs/记忆系统.md)
- [运行时和沙箱](./docs/运行时和沙箱.md)
- [API与协议](./docs/API与协议.md)
- [HITL协议](./docs/HITL协议.md)
- [自动化](./docs/自动化.md)
- [子智能体调度](./docs/子智能体调度.md)
- [MCP与前端工具](./docs/MCP与前端工具.md)
- [会话存储与回放](./docs/会话存储与回放.md)
- [鉴权与安全边界](./docs/鉴权与安全边界.md)
- [版本化打包方案](./docs/版本化打包方案.md)
- [手工测试用例](./docs/手工测试用例.md)
