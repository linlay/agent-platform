# CLAUDE.md

## 1. 项目概览

`agent-platform-runner-go` 是 `agent-platform-runner` 的 Go 版运行时仓库，目标是在保持接口风格与部署方式尽量一致的前提下，逐步把 Java 版 agent runtime 迁移到 Go。

当前仓库的定位不是“功能完全对齐”，而是“最小可运行闭环”：

- 已具备独立 HTTP 服务与统一 JSON 包裹
- 已具备 `POST /api/query` 真流式 SSE 输出、heartbeat 与 `[DONE]` 结束帧
- 已具备 chat 摘要、事件流、原始消息、上传资源落盘
- 已具备 remember 最小落盘能力
- 已具备 OpenAI 兼容模型调用、backend tool 执行与 Container Hub sandbox 接入
- 已具备 `VERSION + scripts/release*.sh + dist/release` 的 program bundle 发布链路，可作为 `zenmind-desktop` builtin 分发
- 尚未对齐 Java 版的 frontend tool 完整链路、热重载、MCP 实接、鉴权、memory 检索、schedule 编排等高级能力

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统
- 配置：环境变量 + `configs/*.yml` 顶层键值读取
- 容器化：Docker 多阶段构建
- 本地编排：Docker Compose

当前没有引入数据库、消息队列、Web 框架或第三方路由库。

## 3. 架构设计

### 启动装配

```text
cmd/agent-platform-runner/main.go
  -> app.New()
  -> config.Load()
  -> chat.NewFileStore()
  -> memory.NewFileStore()
  -> engine.LoadModelRegistry()
  -> engine.NewContainerHubSandboxService()
  -> engine.NewRuntimeToolExecutor()
  -> catalog.NewFileRegistry()
  -> engine.NewLLMAgentEngine()
  -> server.New()
```

启动时的核心事实：

- chat 与 remember 都使用文件系统存储
- catalog 已从 `runtime/agents` / `runtime/teams` / `runtime/skills-market` 动态装载
- model registry 会从 `REGISTRIES_DIR/models` 读取模型配置
- sandbox 通过 Container Hub HTTP 接口执行
- server 层统一承接 `/api/*` 接口与 SSE 输出

### `/api/query` 主流程

```text
HTTP request
  -> QueryRequest 校验
  -> 归一化 runId / requestId / chatId / agentKey
  -> Chats.EnsureChat()
  -> Runs.Register()
  -> Agent.Stream()
  -> stream.NewWriter()
  -> 发送 request.query(runId) / chat.start / run.start
  -> 按上游 chunk 逐条发送 content.delta / tool.*
  -> 发送 content.snapshot
  -> 发送 run.complete
  -> Chats.OnRunCompleted()
  -> stream.WriteDone()
```

SSE 由 `internal/stream/sse.go` 提供，事件名统一写为 `message`，结束帧写 `data: [DONE]`；默认逐事件立刻 flush，也可通过 `AGENT_H2A_RENDER_*` 开启传输层缓冲。

### 当前模块边界

- `internal/server`：HTTP 路由、请求校验、响应封装、SSE 协调
- `internal/engine`：模型调用、tool 执行、sandbox、run 管理抽象
- `internal/chat`：chat 摘要、事件、raw messages、上传文件存储
- `internal/memory`：remember 输出与记忆文件落盘
- `internal/catalog`：目录驱动的 agent/team/skill/tool 注册信息
- `internal/config`：环境变量与 `configs/*.yml` 的加载、默认值、兼容性校验

## 4. 目录结构

```text
.
├── VERSION
├── cmd/
│   └── agent-platform-runner/
├── configs/
│   ├── bash.example.yml
│   ├── container-hub.example.yml
│   ├── cors.example.yml
│   └── local-public-key.example.pem
├── dist/
│   └── release/
├── docs/
├── internal/
│   ├── api/
│   ├── app/
│   ├── catalog/
│   ├── chat/
│   ├── config/
│   ├── engine/
│   ├── memory/
│   ├── server/
│   └── stream/
├── scripts/
│   ├── release.sh
│   ├── release-common.sh
│   ├── release-program.sh
│   └── release-assets/
├── Dockerfile
├── Makefile
├── compose.yml
└── README.md
```

主要职责：

- `cmd/agent-platform-runner`：进程入口
- `configs/`：结构化配置模板与本地覆写入口
- `dist/release/`：版本化 program bundle 输出目录
- `docs/`：与参考仓库对齐的补充文档
- `internal/app`：应用装配
- `internal/api`：HTTP DTO 与统一响应结构
- `internal/config`：配置事实源与不兼容变量拦截
- `internal/server`：REST API 与 SSE 接口实现
- `internal/engine`：LLM、tool、sandbox、run manager 及 registry 读取
- `internal/chat`：会话与资源文件存储
- `internal/memory`：remember 文件存储
- `scripts/`：program bundle 发布脚本与 Desktop 启停资源

## 5. 数据结构

### Chat 存储

chat 根目录由 `CHATS_DIR` 控制，默认布局：

```text
<CHATS_DIR>/
  index.json
  <chatId>/
    events.jsonl
    raw_messages.jsonl
    <uploaded-file>
```

含义：

- `index.json`：chat 摘要索引，记录 `chatId`、`chatName`、`agentKey`、`lastRunId`、`readStatus` 等
- `events.jsonl`：运行事件流，如 `request.query(runId)`、`chat.start`、`run.start`、`content.delta`、`content.snapshot`、`run.complete`
- `raw_messages.jsonl`：用户与助手原始消息，新写入记录会带 `runId`
- 上传文件：直接存放在 chat 目录下，由 `/api/resource` 提供回读

### Remember 存储

remember 根目录由 `MEMORY_DIR` 控制：

```text
<MEMORY_DIR>/
  <chatId>.json
```

文件中包含 `requestId`、`chatId`、`chatName`、`items` 与 `stored` 等最小记忆结果。

### 主要 DTO

核心 DTO 位于 `internal/api/types.go`，包括：

- `ApiResponse[T]`
- `QueryRequest`
- `SubmitRequest` / `SubmitResponse`
- `SteerRequest` / `SteerResponse`
- `InterruptRequest` / `InterruptResponse`
- `RememberRequest` / `RememberResponse`
- `LearnRequest` / `LearnResponse`
- `ChatSummaryResponse`
- `ChatDetailResponse`
- `UploadResponse` / `UploadTicket`

## 6. API 定义

所有非 SSE 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

主要接口：

- `GET /api/agents`：返回目录驱动的 agent 列表，支持 `tag`
- `GET /api/agent?agentKey=...`：返回单个 agent 详情，包含 model / tool / skill / sandbox 元数据
- `GET /api/teams`：返回目录驱动的 team 列表
- `GET /api/skills`：返回目录驱动的 skill 列表，支持 `tag`
- `GET /api/tools`：返回 tool 列表，支持 `kind` 过滤
- `GET /api/tool?toolName=...`：返回单个 tool 详情
- `GET /api/chats`：返回 chat 摘要列表，支持 `lastRunId`、`agentKey`，`lastRunId` 兼容 base36 毫秒 runId 与旧版 `run_YYYY...` 格式
- `GET /api/chat?chatId=...`：返回 chat 详情，`includeRawMessages=true` 时附带 `rawMessages`
- `POST /api/read`：将 chat 标记为已读
- `POST /api/query`：返回 SSE；支持可选 `runId` 透传；缺失 `runId` 时服务端按 `base36(epochMillis)` 生成，缺失 `requestId` / `chatId` 时服务端自动生成，缺失 `agentKey` 时回退到默认 agent
- `POST /api/submit`：当前返回最小 ack，占位 awaiting 提交链路；请求体要求 `runId + awaitingId`
- `POST /api/steer`：当前返回最小 ack，占位运行中 steer 链路
- `POST /api/interrupt`：中断活跃 run 并返回 ack
- `POST /api/remember`：从 chat 快照生成最小 remember 文件
- `POST /api/learn`：当前固定返回 `accepted=false`、`status="not_connected"`
- `GET /api/viewport`：先查 `registries/viewports` 本地模板，再查 `registries/viewport-servers` 里的远端 viewport server，最后才回退 noop viewport client
- `GET /api/resource`：按 chat 目录中的相对路径回读静态资源，可结合 resource ticket 访问
- `POST /api/upload`：写入 chat 目录并返回 upload ticket

### `confirm_dialog` 共享 viewport 约定

- `_ask_user_question_` 与 `_ask_user_approval_` 都使用 `viewportType=builtin`、`viewportKey=confirm_dialog`。
- 两个工具的输入里都必须带 `mode`：
  - `mode=question`：对应 `_ask_user_question_`
  - `mode=approval`：对应 `_ask_user_approval_`
- 前端确认流 SSE 语义：
  - `tool.start` / `tool.snapshot` 保持纯净，不再携带 `viewportKey` / `toolTimeout`
  - `_ask_user_question_` 事件顺序：
    `tool.start -> await.ask -> tool.args* -> tool.end -> await.payload -> [用户 /api/submit] -> request.submit -> tool.result`
  - `_ask_user_approval_` 事件顺序：
    `tool.start -> tool.args* -> tool.end -> await.ask -> [用户 /api/submit] -> request.submit -> tool.result`
  - `request.submit` 是用户提交后的事件，不再表示“前端应该显示确认框”
  - `tool.result` 是工具规范化后的真实执行结果，不等同于原始 submit payload
- `await.ask` 使用独立字段命名：
  - `awaitingId`、`viewportType`、`viewportKey`、`mode`、`toolTimeout`、`runId`
- `await.payload` 只在 `question` 模式下出现：
  - 顶层直接输出 `questions: []`
  - 不再嵌套 `payload`
  - 不再重复携带 `mode`
- `question` 模式：
  - `await.ask` 不带 `questions`
  - `questions` 只出现在 `await.payload`
- `approval` 模式：
  - 不再发 `await.payload`
  - `questions` 直接内联在 `await.ask`
- `mode=question`：
  - 顶层字段为 `questions`
  - 每个问题支持 `type`、`header`、`placeholder`、`allowFreeText`、`freeTextPlaceholder`
  - `select` 题的 `options` 结构是 `{ label, description? }`，不包含 `value`
  - 单个问题里，选项回答与自由输入互斥，最终都写入 `answer`
  - `/api/submit` 提交结构：`{"runId":"...","awaitingId":"...","params":{"answers":[{"question":"...","answer":...}]}}`
  - `tool.result` 规范化结构：`{"mode":"question","answers":[{"question":"...","answer":...}]}`
- `mode=approval`：
  - 前端事件里的 `await.ask` 统一输出 `questions: [{ question, header?, description?, options, allowFreeText?, freeTextPlaceholder? }]`
  - `questions` 数组长度固定为 1
  - `options` 结构是 `{ label, value, description? }`
  - 预设选项与自由输入互斥
  - `/api/submit` 提交结构：`{"runId":"...","awaitingId":"...","params":{"value":"..."}}` 或 `{"runId":"...","awaitingId":"...","params":{"freeText":"..."}}`
  - `tool.result` 规范化结构：`{"mode":"approval","value":"..."}` 或 `{"mode":"approval","freeText":"..."}`

## 7. 开发要点

- 配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准，`README.md` 只解释，不重复维护默认值。
- 根目录不放 `application.yml`；当前 Go 版默认值直接固化在代码里。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem` 必须忽略提交。
- 参考仓库中的文件命名和文档结构可以复用，但内容必须以 Go 版当前实现为准，不能复制 Java 版未落地能力。
- 新增 API 时优先保持返回包裹、字段命名和错误语义与参考仓库同风格。
- 测试目前以 `go test ./...` 为主，重点覆盖 `internal/server` 和 `internal/config`。

## 8. 开发流程

本地开发常用流程：

```bash
cp .env.example .env
make run
make test
make release-program
```

当前本地 `make run` / `make test` 默认会带 `CGO_ENABLED=0`，其中 `make test` 还会串行执行包测试并使用临时 `GOCACHE`，避免 macOS 上 `CGO=1` 的 `net/http` 二进制在进入 `main()` 前被系统直接杀掉，以及并发 test/cache 导致的异常。需要显式验证真实 loopback 端口测试时，使用 `RUN_SOCKET_TESTS=1 make test-integration`。

容器验证：

```bash
docker compose up --build
docker compose logs -f
```

Desktop builtin 联调：

```bash
make release-program
cd ../zenmind-desktop
npm run sync:assets
```

涉及文档或目录规范调整时，优先同步：

1. `README.md` 的用户使用说明
2. `CLAUDE.md` 的项目事实
3. `docs/` 下的专题文档
4. `.gitignore` 的敏感文件治理规则

## 9. 已知约束与注意事项

- 当前仍不是 Java 版的功能完全等价实现，但 `.env` 契约、catalog API、基础鉴权与 resource ticket 已对齐。
- `skills`、`teams`、`agents` 已支持定时轮询式目录热刷新。
- `submit` / `steer` / `interrupt` 还没有形成 Java 版那样的运行中双向编排控制面。
- `viewport` 已支持本地模板和远端 `viewports/get` 拉取；协议能力仍是最小子集，未完全对齐 Java 版前端协议。
- `remember` 是文件输出最小实现，不包含 embedding、召回、排序等能力。
- program bundle 当前默认产出 `darwin-arm64` 目标，供 `zenmind-desktop` builtin 直接消费。
- 若环境中显式设置了废弃旧变量，应用会直接启动失败。
