# agent-platform

本仓库是 `agent-platform` 的 Go 版运行时实现，当前以 Java runtime 的 `.env` / `application.yml` 契约为事实源，支持目录驱动的 agents / teams / skills catalog、带隐藏协调器的 orchestrated Team、JWT 鉴权、resource ticket、chat 文件落盘、memory learn、Container Hub sandbox，以及最小 OpenAI 协议模型与 backend tool loop。

> 项目事实、架构与开发约束见 [AGENTS.md](./AGENTS.md)，补充说明见 [docs/](./docs)。

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
- `POST /api/btw`
- `POST /api/submit`
- `POST /api/steer`
- `POST /api/interrupt`
- `POST /api/learn`
- `GET /api/viewport?viewportKey=...`
- `GET /api/resource?file=...`
- `POST /api/upload`

返回格式约定：

- `POST /api/query` 成功时默认返回真实流式 SSE event stream，服务端会按 provider 原始流式 chunk 逐步透传 `content.delta`，结束时追加 `data: [DONE]`；请求体传 `stream:false` 时返回普通 JSON，默认 `data` 只包含 `content`，可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`，错拼字段 `steam` 不会被识别。
- `POST /api/btw` 在已有 chat 下创建或继续隐藏只读分支，复用 `/api/query` 的 ReAct 与 SSE 协议，不更新父 chat JSONL、摘要、未读或后续上下文；扩展工具只有显式声明 `readOnly` 时才可执行。
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
- `GET /api/attach` 与 `POST /api/submit` / `steer` / `interrupt` 按公开 run owner 校验：普通 Agent 与 legacy Team 携带 `agentKey`，orchestrated Team 只携带 `teamId`，不得提交隐藏协调器 key。
- `POST /api/submit` 使用 awaiting 协议：请求体必须包含 `runId`、`awaitingId`，并按 run 类型携带 `agentKey` 或 `teamId`。
- 文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传继续使用 `POST /api/upload`，下载继续使用 `GET /api/resource?file=...`；upload ticket 中的 `path` 是智能体执行环境内的可读路径，`url` 只用于平台资源访问；`/ws` 只传文件引用与状态，不承载文件字节。当前 `/ws` 的 `/api/upload` 仅支持网关发送 `url + metadata`，由 platform 再通过 HTTP 拉取文件并落盘。
- 文件工具的 `file_read` / `file_glob` / `file_grep` 与 `file_write` / `file_edit` 白名单独立于 bash allowed paths，默认均为 `.,/tmp`；越权访问会走 `mode=approval`，可单次批准或用 `approve_rule_run` 在当前 run 内批准同一规则。

当前仍未与 Java 版完全对齐的能力主要集中在 frontend tool 完整闭环、MCP 实接，以及更深层的 memory / automation 执行编排细节；配置契约、catalog API、基础鉴权与 resource ticket 已按 Java 语义接入。

## 2. 快速开始

### 前置要求

- Go 1.22 或更新版本
- Docker / Docker Compose（如需容器运行）
- 可用的 provider / model 注册文件（放在 `runtime/registries/`）
- 相邻的 `../agent-platform-builtins/{ripgrep,dbx,httpx}` 本地产物仓库集合；可用绝对路径环境变量 `BUILTINS_ROOT` 覆盖

### 本地启动

```bash
cp .env.example .env
make run
```

`make run` 会先构建本机 release 镜像目录、校验并装入 rg/dbx/httpx，再加载根目录 `.env` 并从 `release-local/backend/agent-platform` 启动；未设置 `SERVER_PORT` 时默认监听 `11949`。内置程序位于 `release-local/bin/`，本机插件位于 `release-local/plugins/`。直接执行 `go run ./cmd/agent-platform` 不会自动加载 `.env`、装入 builtins 或扫描 `release-local/plugins/`；未设置 `SERVER_PORT` 或 `--port` 时应用代码默认监听 `8080`。

也可以显式拆开构建与启动：

```bash
make build-local
make run-local
```

`make build-local` 会把 runtime 写到 `release-local/backend/agent-platform`，按 `scripts/release-assets/builtins.lock.json` 把对应平台的 rg/dbx/httpx 写到 `release-local/bin/`，并创建本机插件目录 `release-local/plugins/`。builtins 缺失或 SHA-256 不匹配时构建失败。由于 runtime 位于 `backend/` 下，启动时只扫描服务包根目录的 `plugins/`，与 Desktop 服务包形态一致。`runtime/` 仍只用于 agents、chats、skills-market、registries、memory 等运行数据。

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

`agent-platform` 不会自行签发 gateway JWT。本地 mobile channel 联调时，需要先生成一对 RSA 密钥，并把预签名 token 写进本地忽略文件 `configs/channels.yml`。

```bash
# 1. 在 agent-platform 根目录生成开发用密钥
openssl genrsa -out configs/gateway-private-key.pem 2048
openssl rsa -in configs/gateway-private-key.pem -pubout -out configs/gateway-public-key.pem

# 2. 生成 platform -> gateway 使用的 RS256 JWT
go run ./scripts/gen-gateway-token.go -key configs/gateway-private-key.pem -sub local
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
      jwt-token: <paste-token-here>
```

注意事项：

- `JWT.sub` 必须和 `gateway.url` 中的 `userId` 完全一致；上例要求 `sub=local`
- `configs/gateway-private-key.pem` 和真实 `configs/channels.yml` 都是本地文件，不提交
- `.env` 只保留启动/部署 allowlist，不再作为 channel token 配置入口

### 测试

```bash
make test
```

默认 `make test` 同样会使用 `CGO_ENABLED=0`，并通过串行包测试加临时 `GOCACHE` 规避当前 macOS 环境里的并发 test/cache 异常；它也不会运行依赖真实 loopback 端口绑定的测试。需要显式验证真实本地 socket 流式链路时，使用：

```bash
RUN_SOCKET_TESTS=1 make test-integration
```

## 3. 配置说明

本地启动变量从 `.env.example` 复制到 `.env`。`.env` 不提交；`.env.example` 只保留启动/部署 allowlist。运行时配置使用 `configs/runtime.yml`，工具运行时配置使用 `configs/tools.yml`，AI 工具配置使用 `configs/ai-tools.yml`，默认值的单一事实源仍以代码和 `configs/*.example.yml` 模板为准。更完整的高级与排障配置参考见 [配置化说明](./docs/配置化说明.md)。

### 根 `.env.example`

根 `.env.example` 现在是面向最终用户的最小启动模板，只保留以下配置：

- `SERVER_PORT`
- `AP_RUNTIME_DIR` / `AP_RUNTIME_REGISTRIES_DIR` / `AP_RUNTIME_CHATS_DIR` / `AP_RUNTIME_MEMORY_DIR` / `AP_RUNTIME_PAN_DIR`
- `AP_CONTAINER_HUB_BASE_URL`
- `AP_CHAT_RESOURCE_TICKET_SECRET`
- `AP_DEBUG_LLM_CONSOLE`
- `AP_DEBUG_LLM_CHAT_RECORD`

除上述 allowlist 外，旧环境变量不再生效。resource ticket TTL 属于非敏感运行策略，使用 `configs/runtime.yml` 的 `resource.ticket-ttl-seconds` 配置。

Auth 默认开启，默认公钥文件为 `configs/local-public-key.pem`；相关默认值展示在 `configs/runtime.example.yml` 的 `auth` 节，根 `.env.example` 不再放 Auth 变量。

以下低频项统一改到 `configs/runtime.yml`：

- 低频 runtime 子目录：`paths.owner-dir`、`paths.agents-dir`、`paths.teams-dir`、`paths.root-dir`、`paths.automations-dir`、`paths.skills-market-dir`
- memory 深度调优：`memory.*`

Logging 默认值已经源码化，不提供 runtime YAML 入口；只保留 `AP_DEBUG_LLM_CONSOLE` 和 `AP_DEBUG_LLM_CHAT_RECORD` 作为现场调试 allowlist。LLM 交互日志、memory 参数和内部运行默认值的适用人群和注意事项统一见 [配置化说明](./docs/配置化说明.md)。

ACP CODER bridge 在 `configs/coder-settings.yml` 的 `acp-bridges` 中定义；agent 以 `runtimeConfig.acpBridgeId` 引用条目，`timeout-ms` 默认 `300000`。配置变更需重启 runtime。

Provider `apiKey` 按明文字符串读取：

- 未配置时保留为空值：`apiKey:`
- runtime 不再支持 provider `apiKey` 加密或解密；包括 `AES(...)` 在内的任何值都会作为普通字符串使用。

### `configs/` 目录

本仓库保留与参考仓库一致的结构化配置入口：

- `configs/ai-tools.example.yml`
- `configs/channels.example.yml`
- `configs/coder-prompts.example.yml`
- `configs/coder-settings.example.yml`
- `configs/kbase-prompts.example.yml`
- `configs/kbase-settings.example.yml`
- `configs/local-public-key.example.pem`
- `configs/prompts.example.yml`
- `configs/runtime.example.yml`
- `configs/tools.example.yml`

当前 Go runtime 实际会读取：

- `configs/ai-tools.yml`
- `configs/channels.yml`
- `configs/coder-prompts.yml`
- `configs/coder-settings.yml`
- `configs/kbase-prompts.yml`
- `configs/kbase-settings.yml`
- `configs/local-public-key.pem`
- `configs/prompts.yml`
- `configs/runtime.yml`
- `configs/tools.yml`

`configs/` 不是可配置目录，固定使用 runtime 根目录下的 `./configs`；容器内固定挂载到 `/opt/configs`。

**静态配置**：`configs/` 下所有文件都只在进程启动时读取一次；修改 `configs/*.yml` 或 `configs/*.pem` 后必须重启 runtime 才会生效。

**Support package 发现**：启动时会扫描 `agent-platform` 可执行程序旁的 `plugins/*/manifest.json`；如果可执行程序位于服务包的 `backend/` 目录下，则只扫描服务包根目录的 `plugins/*/manifest.json`。当前用于 KBASE PDF 抽取发现 `pdftotext`，不写 PATH，不修改 `configs/kbase-settings.yml`；配置里保留 `binary: pdftotext.exe` 或 `binary: pdftotext` 即可。

本地 JWT 公钥规则：

- 本地公钥文件固定为 `configs/local-public-key.pem`
- 该路径和文件名不是配置项；要使用本地公钥模式时，必须把公钥放在这个位置
- 配置了 `auth.jwks-uri` 时走 JWKS 模式，不读取本地公钥文件

配置优先级：

- 有环境变量入口的配置：代码默认值 `<` yml `<` 仍受支持的环境变量
- 纯 YAML 配置：代码默认值 `<` yml

详细配置见 [配置化说明](./docs/配置化说明.md)。

### Team 配置

`runtime/teams/*.yml` 保留为 legacy Team：请求使用 `teamId + agentKey`，未指定成员时使用 `defaultAgentKey`。

```yaml
name: Support
defaultAgentKey: support_agent
agentKeys:
  - support_agent
  - billing_agent
```

目录式 `runtime/teams/<teamId>/team.yml` 是 orchestrated Team，由运行时为每个 run 合成内部 `TEAM` 协调器：

```yaml
name: Research
description: 多角色研究与复核
agentKeys:
  - researcher
  - reviewer
orchestrator:
  modelConfig:
    modelKey: qwen3-max
  maxParallel: 2
```

目录中可选的 `SOUL.md` 与 `AGENTS.md` 只补充 Team 人格和工作规则，不能覆盖内置调度约束。orchestrated Team 请求只传 `teamId`；明确成员时协调器直接委派，意图不明确时广播全部成员并总结，也可通过隐藏的 `team_invoke` 分批并行、跨批串行。协调器 key 和两个隐藏工具不进入普通 Agent/Tool catalog，也不作为公开 run 身份返回。完整配置和协议见 [智能体配置说明](./docs/智能体配置说明.md)、[子智能体调度](./docs/子智能体调度.md) 与 [API与协议](./docs/API与协议.md)。

## 4. 部署

### 容器构建

```bash
docker build -t agent-platform:$(cat VERSION) .
```

### 本地编排

```bash
cp .env.example .env
make docker-up
```

`compose.yml` 使用同样的 runtime 根目录工作流：

- 镜像名默认为 `agent-platform:<VERSION>`，`make docker-up` 会读取根目录 `VERSION` 并注入给 Compose
- 使用 `env_file: .env`
- 本地 `make run` 使用 `SERVER_PORT` 作为监听端口
- 宿主机端口映射为 `${SERVER_PORT}:8080`
- 容器内应用监听端口固定为 `8080`
- 宿主机 runtime 根目录来自 `${AP_RUNTIME_DIR:-./runtime}`
- `AP_RUNTIME_REGISTRIES_DIR`、`AP_RUNTIME_CHATS_DIR`、`AP_RUNTIME_MEMORY_DIR`、`AP_RUNTIME_PAN_DIR` 可单独覆盖宿主机 bind source；未配置时自然落在 `${AP_RUNTIME_DIR}` 下
- 容器内 runtime 根目录固定为 `/opt/runtime`，应用通过 `AP_RUNTIME_DIR=/opt/runtime` 解析子目录
- `./configs` 只读挂载到 `/opt/configs`

Container Hub 默认基础挂载当前最多 7 个：

- `/workspace` -> `AP_RUNTIME_CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `paths.root-dir`（`rw`）
- `/skills` -> `paths.agents-dir/<agentKey>/skills`（仅 `run/agent`，`global` 默认不挂载），`ro`
- `/pan` -> `AP_RUNTIME_PAN_DIR`（`rw`）
- `/agent` -> `paths.agents-dir/<agentKey>`（`ro`，必挂载；目录缺失会 fail-fast）

目录型 agent 可在 `<agentDir>/.config/` 保存工具静态配置。平台会把它作为该 agent 的 XDG 配置根：host bash、agent terminal 使用宿主机路径，Container Hub 使用只读的 `/agent/.config`。dbx/httpx 会优先读取同名 agent 配置，缺失时回退系统配置；显式 `--config` 保持独占。
- `/owner` -> `paths.owner-dir`（`ro`，目录缺失时自动创建）
- `/memory` -> `AP_RUNTIME_MEMORY_DIR/<agentKey>`（`ro`，目录缺失时自动创建）

`runtimeConfig.sandboxMounts` 会真实影响 Container Hub session mounts：

- `platform + mode`：恢复按需平台挂载，或覆盖默认 `/agent`、`/owner`、`/memory` 模式；`platform: skills-market` 会显式挂载 `/skills-market`
- `destination + mode`：覆盖默认基础挂载模式
- `source + destination + mode`：新增自定义挂载，不能拿来覆盖默认基础挂载路径

`configs/runtime.example.yml` 的 `container-hub` 节展开 `base-url`、默认 environment 和运行策略默认值；代码默认值仍作为未配置时的兜底。除 `AP_CONTAINER_HUB_BASE_URL` 外，Container Hub token、environment id、超时和 sandbox 策略统一写入 `container-hub.*`，用于对接 `agent-container-hub` 的 `AUTH_TOKEN` Bearer 鉴权。

`context tags` 不是全局默认集合，而是每个 agent 从 `contextConfig.tags` 读取。当前支持/归一化后的标签有 `system`、`session`、`owner`、`agents`。`agents` 只表示向 prompt 注入 agent 摘要上下文，不授予 `agent_invoke`、channel 或 catalog 权限；`contextConfig.agents` 缺省时表示全部，也可用 YAML list 或逗号字符串指定部分 agent key。

`sandbox` 不再属于 `context tags`。只要 agent 声明了 `runtimeConfig.environmentId`，运行时就会自动注入 sandbox context。

部署时的敏感信息应通过环境变量或 Secret 注入，不要写入仓库文件。

### 版本化打包

面向 desktop builtin 分发时，使用 program bundle 发布链路：

```bash
make release-program
```

产物写入 `dist/release/`，包含 Go runtime、配置模板、启停脚本、`bin/{rg,dbx,httpx}`、builtins manifest 和 ripgrep 许可证。Desktop 宿主集成时执行资源同步：

```bash
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
- Query 无法调用模型：检查 `AP_RUNTIME_REGISTRIES_DIR/providers`、`AP_RUNTIME_REGISTRIES_DIR/models` 是否存在，并确认 provider `apiKey` / `baseUrl` 可用。
- Automation 看起来没有触发：先确认服务进程本身正在运行；如果是本地 `make run`，日志不会出现在 `docker compose logs` 里。随后检查 stdout 中是否有 `automation orchestrator started`、`[automation] registered ...`、`[automation] dispatch ...`。
- Query 看起来不像真流式：默认 SSE writer 会逐事件 flush；优先检查代理、浏览器、网关或调用方是否缓冲。
- `bash` 执行失败：检查 `AP_CONTAINER_HUB_BASE_URL`、`container-hub.default-environment-id`，以及 runtime 目录配置是否为宿主机真实路径。
- chat 没有持久化：检查 `AP_RUNTIME_CHATS_DIR` 是否可写。
- memory learn 未生效：确认 `/api/learn` 请求体、agent memory 配置与 `AP_RUNTIME_MEMORY_DIR` 可写性。
- 上传后无法下载：确认文件已落到 `AP_RUNTIME_CHATS_DIR/<chatId>/`，并检查 `/api/resource?file=...` 是否原样使用。

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
- [子智能体调度（含 TEAM 隐藏调度）](./docs/子智能体调度.md)
- [MCP与前端工具](./docs/MCP与前端工具.md)
- [会话存储与回放](./docs/会话存储与回放.md)
- [鉴权与安全边界](./docs/鉴权与安全边界.md)
- [版本化打包方案](./docs/版本化打包方案.md)
- [手工测试用例](./docs/手工测试用例.md)
