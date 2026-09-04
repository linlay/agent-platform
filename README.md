# agent-platform

本仓库是 `agent-platform` 的 Go 版运行时实现，当前以 Java runtime 的 `.env` / `application.yml` 契约为事实源，支持目录驱动的 agents / teams / skills catalog、带隐藏协调器的 orchestrated Team、`run_query` / `run_status` / `run_interrupt` 独立根 run 工具组、`platform_control` system control plane、JWT 鉴权、resource ticket、chat 文件落盘、memory learn、Container Hub sandbox、LanceDB 本地混合检索 KBASE，以及最小 OpenAI 协议模型与统一 tool loop。

> 项目事实、架构与开发约束见 [AGENTS.md](./AGENTS.md)，补充说明见 [docs/](./docs)。

## 1. 项目简介

当前已提供的接口：

- `GET /api/agents`
- `GET/PUT /api/agents/order`
- `GET /api/agent?agentKey=...`
- `GET /api/skills?agentKey=...`
- `GET /api/teams`
- `GET /api/admin/skills`
- `POST /api/admin/skill-packages/import`
- `POST /api/admin/skill-packages/delete`
- `GET /api/admin/tools`
- `GET /api/chats`
- `GET /api/chat?chatId=...`
- `POST /api/chats/search`
- `POST /api/read`
- `GET /api/chat/export?chatId=...&format=markdown|html`
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
- `POST /api/resource/image/commit`
- `GET /api/file?agentKey=...&path=...`
- `GET /api/project/tree?agentKey=...`
- `GET /api/project/changes?agentKey=...&chatId=...`
- `GET /api/project/diff?agentKey=...&chatId=...&runId=...&path=...`

返回格式约定：

- `POST /api/query` 成功时默认返回真实流式 SSE event stream，服务端会按 provider 原始流式 chunk 逐步透传 `content.delta`，结束时追加 `data: [DONE]`；请求体传 `stream:false` 时返回普通 JSON，默认 `data` 只包含 `content`，可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`，错拼字段 `steam` 不会被识别。
- `POST /api/btw` 在已有 chat 下创建或继续隐藏只读分支，复用 `/api/query` 的 ReAct 与 SSE 协议，不更新父 chat JSONL、摘要、未读或后续上下文；扩展工具只有显式声明 `readOnly` 时才可执行。
- Desktop 通过普通 `/ws` 同时登记 `desktop-main` 与按需建立的 `desktop-btw` lane：前者承载普通 Run、全局 Push 和默认 Desktop target，后者只承载 BTW 请求与 BTW Run；同 lane 的新连接只替换本 lane 旧连接。
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
- `GET /api/attach` 与 `POST /api/submit` / `steer` / `interrupt` 按公开 run owner 校验：普通 Agent 携带 `agentKey`，Team 只携带 `teamId`，不得提交隐藏协调器 key 或 `agentKey`。
- `POST /api/submit` 使用 awaiting 协议：请求体必须包含 `runId`、`awaitingId`，并按 run 类型携带 `agentKey` 或 `teamId`。
- Platform 重启会从持久化 pending summary 恢复未超时/无限等待的 question 与永久 planning；approval/form 或已超时等待项会补齐 error answer、未执行 tool result 和 cancel completion，再清除 pending。
- 文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传继续使用 `POST /api/upload`，实际下载继续使用 `GET /api/resource?file=...`；新图片/产物结果的 `url` 是 `<chatId>/<relativePath>` 逻辑引用，由客户端转换成该 HTTP 请求，历史 `/api/resource?file=...` 保持只读兼容。`path` 只供智能体工具读取或继续发布，绝不进入 Markdown；`/ws` 只传文件引用与状态，不承载文件字节。
- `image_generate` 对 Agent 使用统一参数：无输入图时文生图，最多四张 Chat/本地输入图时图生图；生成和编辑端点及请求格式完全由模型 YAML 选择 Images JSON、Images Multipart 或 Chat Completions，不按模型名/provider 分支。可选 mask 支持 alpha、白区编辑和黑区编辑三种显式语义，仅在模型声明原生 `openai-alpha` 能力时执行局部重绘。
- 文件工具与 Bash 共享 `AccessPolicy`；effective `default` 无条件包含 `@temp` 读写根。`@temp` 在 Unix/macOS 覆盖启动时 `os.TempDir()` 与 canonical `/tmp`（macOS 的 `/tmp`、`/private/tmp` 等价），Windows 只覆盖启动时 `os.TempDir()`。临时根内结构化文件读写免路径/通用写审批，但写前读、并发校验、大小与历史约束保留；Bash 命令白名单、bashsec、opaque/complex 命令与执行审批模式不变。
- `mustUseSkills` 为本次 run 选中的每个 Skill 目录追加 trusted read + readonly roots：完整目录免读路径 HITL，未选中的 skills-center 兄弟目录不随之开放，任何 `accessLevel`、hostAccess 或 approval 都不能写入这些选中目录。Container 仍只读挂载整个 `/skills-center`，mount 可见性不等同于 AccessPolicy 授权。
- 专用 `mode: KBASE` 与普通 KBASE capability 都以 `runtimeConfig.workspaceRoot` 为唯一内容根；专用 mode 在 main/editing 两种 stage 提供相同的五个通用文本文件工具，当前 Chat 目录独立可读写。单次 `/api/query` 顶层 `editingMode:true` 只允许 KBASE Workspace mutation，未开启时 Workspace 仍可读但不可 write/edit；所有目录先服从 AccessPolicy/HITL，索引由 KBASE watcher 异步维护。普通 Agent 附加的 KBASE capability 与其他 mode 不支持该字段。
- `platform_control` 对所有显式配置它的 Agent 暴露同一固定 Schema 和全部注册 operation；动态环境只保留当前普通 native root run 的 `run.env.set/unset`，无需在 Agent 配置预声明 key。动态值仅存在于当前 Platform 进程内，只在新建 Host/Container 命令前生成独立快照，绝不调用 `os.Setenv`；Platform 重启后的 question/planning 续接使用新的空环境。

当前仍未与 Java 版完全对齐的能力主要集中在 MCP 全量生产验证，以及更深层的 memory / automation 执行编排细节；MCP 的 HTTP/stdio client、严格 `2025-11-25` 版本校验、session 生命周期与 tool sync 已接通。平台工具模型已统一，不再区分 frontend/action/backend/builtin。

## 2. 快速开始

### 前置要求

- Go 1.22 或更新版本
- Docker / Docker Compose（如需容器运行）
- 可用的 provider / model 注册文件（放在 `runtime/registries/`）
- 相邻的 `../agent-platform-builtins/{ripgrep,dbx,httpx,kbase-lance-engine,poppler-pdftotext}` 本地产物仓库集合；可用绝对路径环境变量 `BUILTINS_ROOT` 覆盖

### 本地启动

```bash
cp .env.example .env
./scripts/sync-local-builtins.sh
make run
```

`./scripts/sync-local-builtins.sh` 是 macOS/Linux 本地 builtin 构建入口；Windows AMD64 使用 `powershell -ExecutionPolicy Bypass -File scripts/sync-local-builtins.ps1 -Target windows/amd64`，不依赖 Git Bash。两个入口都会在隔离工作目录中按相邻项目的本地 `VERSION` 重建 `dbx`、`httpx`、`kbase-lance-engine` 和 `poppler-pdftotext` launcher/archive，生成只属于本次构建的临时 local lock，再原子更新 `build/builtins/<os>-<arch>/`。cache 激活后默认运行同一套正式 lock 状态机：精确 native host 上严格更高的干净版本可在输入精确 `yes` 后成为组件目标 release；落后平台的本地 `VERSION` 与 Git HEAD 匹配该目标后自动更新自己的 target，无需再次确认。正式 lock 中组件字段表示全平台目标，target 字段记录各平台实际 release/path/SHA；交叉构建只更新 cache，不能改正式 target，因而 macOS 不会改 Windows SHA。两个入口都不写 `release-local/`。`rg` 是唯一只校验并复制的预编译 vendor artifact。`make run` 只构建 Go runtime、加载根目录 `.env` 并从 `release-local/backend/agent-platform` 启动；它通过 `AP_BUILTINS_BIN` 将本机 `build/builtins/<host>/bin` 设为唯一可信 builtin 目录，sidecar 也从该目录解析，但绝不复制或编译 builtin。未设置 `SERVER_PORT` 时默认监听 `11949`。

`--all` 会要求本机已提供六个平台的 Rust target、对应 linker/SDK、`protoc` 与 `syft`；任一 target 不能构建时失败，且既有 `build/builtins` cache 不会被替换。正式 `make release-program` 只消费对应 target 的本机 cache，不会重新构建或回读 `../agent-platform-builtins`；cache 缺失、平台不匹配或 manifest 校验失败会直接终止发布。

也可以显式拆开构建与启动：

```bash
make build-local
make run-local
```

`make build-local` 只把 runtime 写到 `release-local/backend/agent-platform`，不会变更 `release-local/bin/`。builtin 缺失或本机构建失败由同步脚本失败报告。由于 runtime 位于 `backend/` 下，启动时只扫描服务包根目录的 `plugins/`，与 Desktop 服务包形态一致。`runtime/` 仍只用于 agents、chats、skills-center、registries、memory 等运行数据；Platform 会由 agents 与 skills-center 重建 `ru-agents/` 作为唯一 Agent 执行目录。

常用验证：

```bash
curl http://127.0.0.1:11949/api/agents
curl "http://127.0.0.1:11949/api/agents?includeChats=5"
curl "http://127.0.0.1:11949/api/agents?includeTeam=true&includeChats=5"
curl "http://127.0.0.1:11949/api/agent?agentKey=default_agent"
curl "http://127.0.0.1:11949/api/skills?agentKey=default_agent"
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
    mode: client
    transport: websocket
    protocol: platform-ws
    endpoint:
      url: ws://127.0.0.1:11945/ws/agent?userId=local&agentKey=personal&channel=mobile
      token: <paste-token-here>
```

注意事项：

- `JWT.sub` 必须和 `endpoint.url` 中的 `userId` 完全一致；上例要求 `sub=local`
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

Platform 运行形态只由 `--runtime-mode=standalone|desktop` 指定，默认 `standalone`。Desktop 宿主启动内置 Platform 时固定传入 `desktop`；Platform 不根据端口、父进程、WS `source` 或 YAML 猜测运行形态。`desktop_action` / `desktop_cdp` 优先使用当前 run 绑定的反向 WebSocket target；Desktop 模式下，无绑定或旧连接在发送前已失效的 run 会补绑当前 `desktop-main`，Standalone 仍只认 run target。两种模式都不调用本地 HTTP bridge，也不重放已经发送的动作。

MCP server 配置位于 `${AP_RUNTIME_REGISTRIES_DIR}/mcp-servers/*.yml`，支持默认的 `streamable-http` 与 `stdio`。两种 transport 都只接受协议版本 `2025-11-25`；本地 Registry 在启动和热重载时同步校验，远端初始化、工具发现与重试统一后台执行，不阻塞 Platform 监听、管理端保存或 watcher。stdio 子进程必须使用标准 MCP，旧 `tools-dir/service.yml`、`type: external`、`external:` 与 `kind: external-service` 会在启动或热重载时硬失败。配置示例和迁移边界见 [MCP与工具交互](./docs/MCP与工具交互.md)。

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

- 低频 runtime 子目录：`paths.owner-dir`、`paths.agents-dir`、`paths.ru-agents-dir`、`paths.teams-dir`、`paths.root-dir`、`paths.automations-dir`、`paths.skills-center-dir`
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

**KBASE PDF 抽取**：正式服务包在已锁定的 darwin-arm64 与 windows-amd64 平台随 Host builtin 分发 `pdftotext`（Poppler 26.07.0）。运行时把服务包根目录的 `bin/` 前置到 PATH，因此 `configs/kbase-settings.yml` 保持 `binary: pdftotext`；显式的绝对路径或自定义命令仍由使用者负责提供。两个 payload 均完成来源、archive、manifest 与依赖闭包校验；Windows 的实际抽取 smoke 应在 Windows release runner 上执行后再对外发布。

本地 JWT 公钥规则：

- 本地公钥文件固定为 `configs/local-public-key.pem`
- 该路径和文件名不是配置项；要使用本地公钥模式时，必须把公钥放在这个位置
- 配置了 `auth.jwks-uri` 时走 JWKS 模式，不读取本地公钥文件

配置优先级：

- 有环境变量入口的配置：代码默认值 `<` yml `<` 仍受支持的环境变量
- 纯 YAML 配置：代码默认值 `<` yml

详细配置见 [配置化说明](./docs/配置化说明.md)。

### Team 配置

Team 只接受目录式 `runtime/teams/<teamId>/team.yml`，运行时为每个 run 合成内部 `TEAM` 协调器。平铺 `runtime/teams/*.yml|yaml` 和 `defaultAgentKey` 已移除，会使启动失败。

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

目录中可选的 `SOUL.md` 与 `AGENTS.md` 只补充 Team 人格和工作规则，不能覆盖内置调度约束。Team 请求只传 `teamId`，传入 `agentKey` 返回 400；隐藏总控统一通过 embedded builtin `agent_delegate` 委派一个或多个冻结 roster 成员，并用 `plan_add_tasks/plan_get_tasks/plan_update_task` 管理复杂任务。flat plan 按数组顺序且同时最多一个 `in_progress`，当前阶段内部仍可通过一次 `agent_delegate` 并行执行多个成员。成员结果全部回注总控，根回答只由总控生成。协调器 key 和隐藏工具不进入普通 Agent/Tool catalog，也不作为公开 run 身份返回。完整配置和协议见 [智能体配置说明](./docs/智能体配置说明.md)、[子智能体调度](./docs/子智能体调度.md) 与 [API与协议](./docs/API与协议.md)。

普通主 Agent 还可分别显式挂载 `run_query`、`run_status`、`run_interrupt`，用于发起、查询和中断标准独立 Agent/Team 根 run。它们与 `agent_invoke` 不同：不复用父 `chatId/runId`，query 在目标 run 注册后立即返回，父 run 中断不取消目标；后续控制只允许同一调用 Agent 与 subject 操作自己通过 `run_query` 创建的 run。目标不使用白名单或 `contextConfig.agents`，精确 catalog 名称存在即可调用；`run_query` 的工具描述负责把“当前智能体”“本智能体”“你自己”解析为 system prompt 的 `Agent Identity.key`，不得用候选摘要替代。目标 run 禁止再次调用任一 run 工具。旧 `agent_run_query`、`agent_run_status`、`agent_run_interrupt` 已删除，Agent 配置引用旧名会硬失败。完整契约见 [子智能体调度](./docs/子智能体调度.md)。

`platform_control` 必须由 Agent 的 `toolConfig.tools` 显式挂载；挂载后即可调用全部注册 operation，模型可见 Schema 不按 Agent 裁剪。动态环境只提供 `run.env.set/unset`，作用于当前普通 native root run，并只注入后续新建的 Host/Container Tool 子进程；只有当前 run 成功 set 的 key 才能 unset。set value 作为普通 Tool 参数在会话、trace 和导出中可见，不得用于传递 Secret。Skill 与 `mustUseSkills` 不会替 Agent 挂载 Tool。旧 `platform_config` 和旧 `platform-control.profiles/bindings` 加载时硬失败，遗留 `runtimeConfig.runEnv` 静默忽略。完整契约见 [Platform 控制工具设计与实现](./docs/Platform控制工具设计.md)。

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

Container Hub 使用严格双根协议，基础挂载包括：

- `/workspace` -> 当前 Agent 的 canonical `runtimeConfig.workspaceRoot`，`rw`
- `/chat` -> `AP_RUNTIME_CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `paths.root-dir`（`rw`）
- `/skills` -> `paths.ru-agents-dir/<agentKey>/skills`（仅 `run/agent`，`global` 默认不挂载），`ro`
- `/pan` -> `AP_RUNTIME_PAN_DIR`（`rw`）
- `/agent` -> `paths.ru-agents-dir/<agentKey>`（`ro`，必挂载；目录缺失会 fail-fast）
- `/owner` -> `paths.owner-dir`（`ro`，目录缺失时自动创建）
- `/memory` -> `AP_RUNTIME_MEMORY_DIR/<agentKey>`（`ro`，目录缺失时自动创建）

容器 session 与未显式指定 cwd 的命令固定使用 `/workspace`。协议为 `dual-root-v2`。当 ChatsRoot 位于 Workspace 内时，Platform 下发 `/workspace/<ChatsRoot-relative>` mask，Hub 按 Workspace bind → mask tmpfs → current Chat bind 的顺序创建容器，确保 Chat 只从 `/chat` 可见。`/workspace`、`/chat`、mask 及其子路径是保留挂载目标，`runtimeConfig.sandboxMounts` 不能覆盖。session 复用身份包含 environment、canonical Workspace、canonical Chat、mask 和完整 mount fingerprint。

目录型 agent 可在源目录 `.config/` 保存专属覆盖，Skill `.config/` 提供可分发默认值；Platform 合并到 `ru-agents/<agentKey>/.config/`。平台冻结四个保留变量：`AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR`、`AP_ACCESS_TOKEN`。Host 注入前三者对应的生成配置目录、真实 Workspace（无 Workspace 时省略）和 Chat；Workspace Terminal 只注入 Agent 配置目录与真实 Workspace，不注入 `AP_CHAT_DIR`；Container 固定为 `/agent/.config`、`/workspace` 与 `/chat`。第四个只在普通 Agent Host Bash 创建前从有效 identity 文件即时读取，默认文件为 `<AP_RUNTIME_DIR>/identity/access-token`，可由最高优先级的 `--identity-file <absolute-path>` 覆盖，不进入 Terminal 或 Container。agent `runtimeConfig.env`、skill `.runtime-env.json`、run dynamic env 与调用级 env 均不得覆盖。动态层只通过 `platform_control run.env.set/unset` 修改当前普通 native root run 的进程内 Scope；Host Bash、直接短进程和 Container 新 command 获取快照，子 Agent、Team、Terminal、MCP、ACP、Proxy、Channel、LSP、sidecar 和已启动进程不继承或更新，Platform 重启后的续接 run 从空动态层开始。HTTPX 的 chat state/secret 位于 `$AP_CHAT_DIR/.state/httpx` 与 `$AP_CHAT_DIR/.secret/httpx`，缺少合法 `AP_CHAT_DIR` 时不回退 global。完整组装、冲突和迁移规则见 [Agent 运行时组装](./docs/Agent运行时组装.md)。

`runtimeConfig.sandboxMounts` 会真实影响 Container Hub session mounts：

- `platform + mode`：恢复按需平台挂载，或覆盖默认 `/agent`、`/owner`、`/memory` 模式；`platform: skills-center` 会显式挂载 `/skills-center`
- `destination + mode`：覆盖非保留的默认基础挂载模式
- `source + destination + mode`：新增自定义挂载，不能拿来覆盖默认基础挂载路径

发布前运行 `make audit-workspace-chat`，只读列出旧 `working-directory`、无 Workspace 的 CODER/sandbox/KBASE、`workspaceRoot:@chat`、旧 `kbaseConfig.source`、非法 Workspace/Chats 关系和保留挂载冲突；对合法但无 Workspace 且挂载路径工具的普通 Agent，还会输出非阻断诊断，提示必须显式提供 Bash `cwd`、glob/grep `path` 和语义根路径。普通 Workspace 可以包含 ChatsRoot；KBASE Workspace 仍要求完全分离。

`configs/runtime.example.yml` 的 `container-hub` 节展开 `base-url`、默认 environment 和运行策略默认值；代码默认值仍作为未配置时的兜底。除 `AP_CONTAINER_HUB_BASE_URL` 外，Container Hub token、environment id、超时和 sandbox 策略统一写入 `container-hub.*`，用于对接 `agent-container-hub` 的 `AUTH_TOKEN` Bearer 鉴权。

`context tags` 不是全局默认集合，而是每个 agent 从 `contextConfig.tags` 读取。当前支持/归一化后的标签有 `system`、`session`、`owner`、`agents`。`agents` 只表示向 prompt 注入 `Runtime Context: Sub-Agent Candidates` 候选摘要，不授予 `agent_invoke`、run 工具、channel 或 catalog 权限，也不是 `run_query` 目标白名单；当前 Agent 始终从候选中排除，`contextConfig.agents` 缺省时表示其余全部 Agent，也可用 YAML list 或逗号字符串指定部分 agent key。

`sandbox` 不再属于 `context tags`。只要 agent 声明了 `runtimeConfig.environmentId`，运行时就会自动注入 sandbox context。

部署时的敏感信息应通过环境变量或 Secret 注入，不要写入仓库文件。

### 版本化打包

面向 desktop builtin 分发时，使用 program bundle 发布链路：

```bash
./scripts/sync-local-builtins.sh
make release-program
```

Windows AMD64 对应命令为：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/sync-local-builtins.ps1 -Target windows/amd64
make release ARCH=amd64
```

产物写入 `dist/release/`，包含纯 Go runtime、配置模板、启停脚本、`bin/{rg,dbx,httpx,kbase-lance-engine,pdftotext}`、`libexec/poppler-pdftotext/`、builtins manifest、许可证 notice、压缩包 SHA-256 与大小报告。program manifest 声明 `desktop.runtimeResources: "v1"`；`deploy.sh` / `deploy.ps1` 将 Desktop 传入的 env.zip 与稳定设备标识交给统一的 `agent-platform runtime-resource-sync` 子命令，由 Platform 迁移已有 runtime 的 Agent、Skill、Tool、Team 与 Registry。包内四类一级资源及同路径 Registry 是发行方权威版本，同名目标会覆盖；新版包声明 `provider-register.json` 时，还会重新生成并注入 Provider API key。`release-program` 只复制并复验 `build/builtins/<os>-<arch>/` 中由 `sync-local-builtins.sh` 原子生成的 cache，不会构建 Rust sidecar，也不会读取相邻 `agent-platform-builtins`。Docker 构建需要预先执行 `./scripts/sync-local-builtins.sh --target linux/<arch>`，使匹配的 Linux cache 位于 `build/builtins/`。Desktop 宿主集成时执行资源同步：

```bash
npm run sync:assets
```

完整打包细节见 [版本化打包方案](./docs/版本化打包方案.md)。

KBASE 已下沉为可组合的 Agent 公共能力：`mode: KBASE` 仍是强制启用、严格工具边界的专用预设，`REACT`、`PLAN-EXECUTE` 和原生非 ACP `CODER` 也可以通过 `kbaseConfig.enabled: true` 挂载同一套索引、watcher、检索和引用能力。所有 enabled KBASE 都以 `runtimeConfig.workspaceRoot` 为唯一内容根；旧 `kbaseConfig.source` 会硬失败。完整配置和兼容矩阵见 [智能体配置说明](./docs/智能体配置说明.md)。

KBASE 固定使用 LanceDB generation 检索；SQLite `control.db` 只保存 generation、文件状态、refresh run 和恢复日志，不保存检索数据。SQLite runtime store 仅支持当前 schema：启动时仅会认领标记为 `application_id=0,user_version=0` 且完整结构匹配的库，其余库不会被迁移或改写。专用 `mode: KBASE` 的存储不匹配会隔离该 Agent；普通 Agent 的附加知识库会保留 Agent 可运行并把能力标为 degraded。详见 [KBASE LanceDB 检索与控制面](./docs/KBASE-LanceDB迁移.md)。当前 KBASE 仍只生成文本 chunk 与文本 embedding，不宣称具备图片、音频或视频语义检索。

KBASE Editing 使用通用文本文件规则，不按索引格式硬编码扩展名或 UTF-8；删除、重命名、建目录、Bash 和二进制 Office/PDF 通用写入仍不开放。目录权限由 AccessPolicy/HITL 决定，Workspace 写入由 watcher 异步索引。完整约定见 [KBASE 编辑模式](./docs/KBASE编辑模式.md)。

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
- 上传后无法下载：确认文件已落到 `AP_RUNTIME_CHATS_DIR/<chatId>/`，并检查 `/api/resource?file=...` 是否使用响应中的 ChatScope `url`。

## 文档索引

- [智能体配置说明](./docs/智能体配置说明.md)
- [配置化说明](./docs/配置化说明.md)
- [工具目录权限](./docs/工具目录权限.md)
- [真流式和H2A](./docs/真流式和H2A.md)
- [记忆系统](./docs/记忆系统.md)
- [运行时和沙箱](./docs/运行时和沙箱.md)
- [Platform 控制工具设计与实现](./docs/Platform控制工具设计.md)
- [运行时资源迁移](./docs/运行时资源迁移.md)
- [API与协议](./docs/API与协议.md)
- [HITL协议](./docs/HITL协议.md)
- [自动化](./docs/自动化.md)
- [智能体调度（含 `agent_invoke`、TEAM 隐藏调度与独立 run 工具组）](./docs/子智能体调度.md)
- [MCP与工具交互](./docs/MCP与工具交互.md)
- [会话存储与回放](./docs/会话存储与回放.md)
- [鉴权与安全边界](./docs/鉴权与安全边界.md)
- [版本化打包方案](./docs/版本化打包方案.md)
- [手工测试用例](./docs/手工测试用例.md)
