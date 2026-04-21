# CLAUDE.md

## 1. 项目概览

`agent-platform-runner-go` 是 `agent-platform-runner` 的 Go 版运行时仓库，目标是在保持接口风格与部署方式尽量一致的前提下，逐步把 Java 版 agent runtime 迁移到 Go。

当前仓库的定位不是“功能完全对齐”，而是“最小可运行闭环”：

- 已具备独立 HTTP 服务与统一 JSON 包裹
- 已具备 `POST /api/query` 真流式 SSE 输出、heartbeat 与 `[DONE]` 结束帧
- 已具备 chat 摘要、事件流、原始消息、上传资源落盘
- 已具备 remember 落盘、memory learn/consolidate/feedback 能力
- 已具备可选的 embedding 向量语义检索（需配置 `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY`）
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
- `internal/memory`：记忆系统（SQLite 主存储 + FTS5 + 可选 embedding + 生命周期整理 + 反馈循环）
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
- `internal/memory`：记忆系统（SQLite 主存储、三层渐进式披露、动态预算、有效重要度、反馈循环）
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

### Agent sandboxConfig

`agent.yml` 当前可在 `sandboxConfig` 下声明 agent 级沙箱基础配置：

```yaml
sandboxConfig:
  environmentId: shell
  level: RUN
  env:
    HTTP_PROXY: "http://127.0.0.1:7890"
    HTTPS_PROXY: "http://127.0.0.1:7890"
    TZ: "Asia/Shanghai"
```

约束：

- `env` 只接受字面量字符串 map；不支持 `${VAR}` 展开
- key 必须非空，且不能包含空白字符或 `=`
- value 必须是字符串；空字符串允许并原样传给 Container Hub
- 最终合并顺序是 `agent.sandboxConfig.env < skill[i].SandboxEnv`，后声明的 skill 继续覆盖前者
- `/api/agents` 和 `/api/agent` 的 sandbox meta 不暴露 `env`，因为其中可能包含代理地址、凭据或私有 endpoint；`extraMounts` 仍继续对外暴露

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

### HITL 协议

- 人机协作统一保留 `mode` 字段，不引入 `kind`；当前三态是 `question` / `approval` / `form`。
- `/api/submit` 固定形状为 `{"runId":"...","awaitingId":"...","params":[...]}`。
- `params` 顶层永远是数组；前端不再提交 `mode`，后端按 `awaitingId` 反查当前等待态。
- `/api/submit.params` 与 `awaiting.ask.{questions|approvals|forms}` 固定按下标对应：`params[i] -> ask.items[i]`。
- `params` 每项允许携带 `id`，但 `id` 只作审计/日志用途；服务端不会据此分发，只校验数量和字段形状。
- `awaiting.payload` 已删除；问题、审批项、表单定义全部直接内联在 `awaiting.ask`。
- `_ask_user_approval_` 已下线；审批流只来自 Bash HITL builtin confirm。

三态契约：

- `mode=question`
  - 来源：`_ask_user_question_`
  - `awaiting.ask`：`{"awaitingId":"...","mode":"question","timeout":...,"runId":"...","questions":[...]}`
  - question 不再携带 `viewportType` / `viewportKey`
  - `/api/submit.params`：`[{"id":"q1","answer":"..."},{"id":"q2","answers":[...]}]`（`id` 可省略，仅作审计字段）
  - `awaiting.answer`：
    - answered：`{"awaitingId":"...","mode":"question","status":"answered","answers":[...]}`
    - error：`{"awaitingId":"...","mode":"question","status":"error","error":{"code":"user_dismissed|timeout|invalid_submit","message":"..."}}`
  - 整批取消：`params: []`，后端归一化为 `status:"error" + error.code:"user_dismissed"`

- `mode=approval`
  - 来源：Bash HITL builtin confirm
  - `awaiting.ask`：`{"awaitingId":"...","mode":"approval","timeout":...,"runId":"...","approvals":[{"id":"tool_bash","command":"chmod 777 ~/a.sh","description":"放开脚本权限","options":[{"label":"同意","value":"approve"},{"label":"同意（本次运行同前缀都放行）","value":"approve_prefix_run"},{"label":"拒绝","value":"reject"}],"allowFreeText":true,"freeTextPlaceholder":"可选：填写理由"}]}`
  - approval 不再携带 `viewportType` / `viewportKey`
  - 用户只能批准或拒绝，不能改命令内容
  - `/api/submit.params`：`[{"id":"tool_bash","decision":"approve|approve_prefix_run|reject","reason":"..."}]`（`id` 可省略，仅作审计字段）
  - `awaiting.answer`：
    - answered：`{"awaitingId":"...","mode":"approval","status":"answered","approvals":[{"id":"tool_bash","command":"...","decision":"approve","rawDecision":"approve_prefix_run","reason":"..."}]}`
    - error：`{"awaitingId":"...","mode":"approval","status":"error","error":{"code":"user_dismissed|timeout|invalid_submit","message":"..."}}`
  - 整批取消：`params: []`，归一化为 `status:"error" + error.code:"user_dismissed"`

- `mode=form`
  - 来源：Bash HITL html form
  - `awaiting.ask`：`{"awaitingId":"...","mode":"form","viewportType":"html","viewportKey":"leave_form","timeout":...,"runId":"...","forms":[{"id":"form-1","html?":"...","initialPayload":{...}}],"viewportPayload":{"forms":[{"id":"form-1","command":"...","initialPayload":{...}}]}}`
  - form 是唯一保留 `viewportType:"html"` + `viewportKey` 的形态
  - `/api/submit.params`：
    - submit：`[{"id":"form-1","payload":{...}}]`（`id` 可省略，仅作审计字段）
    - reject：`[{"id":"form-1","reason":"..."}]`（`id` 可省略，仅作审计字段）
    - cancel：`[{"id":"form-1"}]`（`id` 可省略，仅作审计字段）
  - `awaiting.answer`：
    - answered：`{"awaitingId":"...","mode":"form","status":"answered","forms":[{"id":"form-1","command":"...","action":"submit|reject|cancel","payload?":{...},"reason?":"..."}]}`
    - error：`{"awaitingId":"...","mode":"form","status":"error","error":{"code":"user_dismissed|timeout|invalid_submit","message":"..."}}`
  - 整批取消：`params: []`，归一化为 `status:"error" + error.code:"user_dismissed"`

事件顺序约定：

- question：`tool.start -> awaiting.ask -> tool.args* -> tool.end -> request.submit -> awaiting.answer -> tool.result`
- approval / form：`tool.start -> tool.args* -> tool.end -> awaiting.ask -> request.submit -> awaiting.answer -> tool.result`
- `request.submit` 透传前端原始数组，便于审计；`awaiting.answer` 才是后端归一化后的结构化结果。
- 历史 `events.jsonl` 中旧的 `cancelled/reason` 形状不再兼容；新前端会按未知旧态回退展示。

### Agent 调度 task

- `_agent_invoke_` 是批量调度原语，不走 `ToolExecutor.Invoke`；主 agent 识别到该 tool call 后，会先保留主时间线上的 `tool.start/args/end/snapshot`，再由 server 侧编排层并发启动 `1~3` 个子 agent。
- 编排层会先顺序 emit 每个 `task.start`，并为同一批任务附上相同 `groupId`；随后由多个 goroutine 并发消费子 stream，再汇聚回主 goroutine 发出带精确 `taskId` 的子流 delta。
- 子 agent 复用现有 `task.start / task.complete / task.cancel / task.fail` 协议；`task.start` 额外携带 `groupId`、`subAgentKey` 和 `mainToolId`，终态 task 事件额外携带 `status`。
- `runId` 始终保持主 RunID；前端通过 `taskId` 把子流事件归到子面板，通过 `mainToolId` 把主时间线上的 `_agent_invoke_` 节点和聚合卡片关联起来。
- 全部子任务结束后，编排层会按输入顺序聚合子结果，并仅向主 `mainToolID` 单次 `InjectToolResult`；主上下文只消费这份聚合后的 `tool.result` 文本。
- 当前版本严格禁止嵌套：`_agent_invoke_` 只对主 `REACT/ONESHOT` agent 可用，子 agent 也只能是 `REACT/ONESHOT`，且子 agent 的可用工具集中会滤掉 `_agent_invoke_`；`tasks` 长度必须满足 `1 ≤ n ≤ 3`。
- 三流分离是硬约束：子 agent 中间的 reasoning、tool 调用、content delta 只进入 SSE/events replay，不进入主 `llmRunStream.messages`，也不进入 `raw_messages`；主上下文只消费子 agent 的最终 `tool.result` 文本。
- `dispatcher.activeTaskID` 仍然是单值；子流的 `content.start / reasoning.start / tool.start / action.start` 如果输入 `taskId` 为空，会自动兜底为当前活跃 taskId，因此不需要引入 task 栈。

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
- `remember` 已升级为完整 memory 系统；embedding 语义检索需配置 `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY`，未配置时降级为 FTS + importance 排序。详见 [docs/memory-system-design.md](docs/memory-system-design.md)。
- program bundle 当前默认产出 `darwin-arm64` 目标，供 `zenmind-desktop` builtin 直接消费。
- 若环境中显式设置了废弃旧变量，应用会直接启动失败。
