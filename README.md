# agent-platform

原生模型支持 `OPENAI_RESPONSES` 协议；本地 JSONL 保存每次模型调用的可选 `responseId` 与 `reasoning_content` 加密条目，续聊不依赖服务端 response ID。配置、格式及兼容边界见 [Responses 协议](docs/Responses协议.md)。

原生 `ANTHROPIC` 模型的显式思考配置统一使用 `thinking.type: adaptive`、`thinking.display: summarized` 与 `output_config.effort`，并读取模型级 `maxOutputTokens` 作为默认输出预算与能力上限，见 [Anthropic 自适应思考](docs/配置化说明.md#anthropic-自适应思考)。

本仓库是 `agent-platform` 的 Go 版运行时实现，配置使用 Go 代码默认值、`configs/*.yml` 和环境变量 allowlist／启动参数，支持目录驱动的 agents / teams / skills catalog、带隐藏协调器的 orchestrated Team、`chat_start` / `chat_get_status` / `chat_interrupt` Chat 会话工具组、`builtin.platform-control` 平台控制连接器、JWT 鉴权、resource ticket、chat 文件落盘、可配置的 memx Memory、Container Hub sandbox、受管 KBX 知识库读取与 Platform 目录监听维护，以及最小 OpenAI 协议模型与统一 tool loop。

> 项目事实、架构与开发约束见 [AGENTS.md](./AGENTS.md)，补充说明见 [docs/](./docs)。

项目型智能体仍通过 `/api/admin/agents/create` 创建，调用方传 `isProject:true` 和具体项目目录 `definition.runtimeConfig.workspaceRoot`；模型 catalog validate/apply 使用同名参数，详见 [智能体创建](docs/智能体配置说明.md#智能体创建)。

Platform 提供调用方中立的标准连接器目录、CLI/MCP 执行、凭据管理和短期执行授权，不持有应用或页面模型，见 [连接器执行协议](docs/连接器执行协议.md)。已发布产物通过独立 Chat API 按现有会话访问权限读取。

网站 AWCP 操作采用手册渐进披露：按需读取动作目录和单项说明，再通过固定 `invoke` 调用；不向模型运行核心注入页面工具 Schema 或专属状态机。参见 [Desktop 反向 Provider](docs/MCP与工具交互.md#desktop-反向-provider)。

## 1. 项目简介

当前已提供的接口：

- `GET /api/agents`
- `GET/PUT /api/agents/order`
- `GET /api/agent?agentKey=...`
- `GET /api/skills?agentKey=...`：全局技能目录、当前 Agent 的 `configured` 标记和用户 `pinned`；agentKey 可选；技能显示名称、请求语言与版本规则见 [技能展示元数据](docs/技能展示元数据.md)
- `PUT /api/skills`：单条 `{id,pinned}` 更新当前用户置顶，`pinned` 必须显式提供布尔值
- `GET /api/teams`
- `GET /api/admin/skills`
- `POST /api/admin/skill-packages/import`
- `POST /api/admin/skill-packages/delete`
- `GET /api/admin/tools`
- `GET /api/chats`
- `GET /api/chat?chatId=...`
- `POST /api/chats/search`
- `POST /api/read`
- `GET /api/chat/export?chatId=...&format=markdown|snapshot`
- `GET /api/archives`
- `GET /api/archive?chatId=...`
- `POST /api/archives/search`
- `POST /api/query`
- `POST /api/submit`
- `POST /api/steer`
- `POST /api/interrupt`
- `GET /api/view?chatId=...&connectorId=...&key=...[&hash=...]`（[VIEW 连接器](docs/VIEW连接器.md)）
- `GET /api/viewport?viewportKey=...`（旧协议兼容）
- `GET /api/resource?file=...`
- `POST /api/upload`
- `POST /api/resource/image/commit`
- `GET /api/file?agentKey=...&path=...`
- `GET /api/project/tree?agentKey=...`
- `GET /api/project/changes?agentKey=...&chatId=...`
- `GET /api/project/diff?agentKey=...&chatId=...&runId=...&path=...`

连接器使用接口与管理接口分离：Composer 从 `/api/agent.connectors` 复用关联 ID，`/api/connectors` 提供候选和本地状态，`/api/agents/connectors` 用于单项切换；tools/skills/connectors 统一为 ID 数组，toolBindings 留在管理详情，平台预置不进入候选；管理端保留完整目录和挂载来源，预置只读。契约与生效边界见 [连接器](docs/连接器.md#使用目录与管理目录)。

连接器使用目录、挂载读取与单项切换同时支持 HTTP 和主 WebSocket，共用业务校验与响应投影；WebClient 在 Platform 模式复用既有主 WS，管理和认证仍用 HTTP。

返回格式约定：

- `POST /api/query` 成功时默认返回真实流式 SSE event stream，服务端会按 provider 原始流式 chunk 逐步透传 `content.delta`，Native Host Bash 还能在退出前发送临时 `tool.output`；每个工具仍由唯一 `tool.result` 收口。请求体传 `stream:false` 时返回普通 JSON，默认 `data` 只包含 `content`，可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`，错拼字段 `steam` 不会被识别。
- `POST /api/query` 携带 `lane:"btw"` 在已有 chat 下创建或继续隐藏只读分支，复用 `/api/query` 的 ReAct 与 SSE 协议，不更新父 chat JSONL、摘要、未读或后续上下文；扩展工具只有显式声明 `readOnly` 时才可执行。
- Desktop 通过普通 `/ws` 登记 main、btw、explain 三条 lane（source 为 `desktop-main`、`desktop-btw`、`desktop-explain`），统一用 `/api/query` 并按连接身份分流；握手 `connected.data.lane` 确认服务端识别结果。main 承载普通 Run、全局 Push 和默认 Desktop target，btw/explain 承载隐藏分支；同 lane 的新连接只替换本 lane 旧连接。HTTP 的 `lane` 默认 main、支持 btw，explain 仅限 Desktop WS。除 HITL Submit 外，Run 控制仍校验创建时的 transport 及适用的身份/设备/lane；`/api/submit` 支持跨设备和 HTTP/WS 提交，保留既有认证、Agent/Team owner、等待项、参数校验和重复提交仲裁，不改绑原 Run 控制归属；每条连接最多一个 Run stream，切换前 detach。
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
- `GET /api/viewport` 仅提供平台内置审批模板；外部自定义模板使用 [VIEW 连接器](docs/VIEW连接器.md) 与 `/api/view`，不再读取旧本地目录或远端 viewport registry。
- `GET /api/attach` 与 `POST /api/submit` / `steer` / `interrupt` 按公开 run owner 校验：普通 Agent 携带 `agentKey`，Team 只携带 `teamId`，不得提交隐藏协调器 key 或 `agentKey`。
- `POST /api/submit` 使用 awaiting 协议：请求体必须包含 `runId`、`awaitingId`，并按 run 类型携带 `agentKey` 或 `teamId`。
- Chat 支持跨普通、CODER/KBASE 等 mode 的统一置顶，独立保存到 `chat-pinned.json`；未置顶列表在截断前排除置顶项，展示排序不修改内容时间。协议与存储见 [API与协议](./docs/API与协议.md) 和 [会话存储与回放](./docs/会话存储与回放.md)。
- Platform 重启会从持久化 pending summary 恢复未超时/无限等待的 question 与永久 planning；approval/form 或已超时等待项会补齐 error answer、未执行 tool result 和 cancel completion，再清除 pending。活动 Run 的等待项由原执行流程收尾，会话读取不提前补写超时结果。
- 工具执行中取消会先收尾工具结果，再保存 Run 终态；活动异步工具在整批共享 2 秒期限内保留真实返回，无法确认时明确记录副作用未知。旧的缺失结果历史不会自动重写，人工恢复流程见 [会话存储与回放](./docs/会话存储与回放.md)。
- 文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传继续使用 `POST /api/upload`，实际下载继续使用 `GET /api/resource?file=...`；上传、生成图片和发布产物的 `url` 是相对于当前 Chat、按路径段编码的 `<relativePath>`，不含 `chatId`；Markdown 原样使用返回的 `url`，客户端资源 adapter 加入当前 `chatId` 后转换成该 HTTP 请求。发布产物 URL 指向发布副本。历史 endpoint Markdown 不迁移且不再预览，HTTP 数据面仍保留。`path` 只供智能体工具读取或继续发布，绝不进入 Markdown；`/ws` 只传文件引用与状态，不承载文件字节。
- 产物发布成功后逐个发送 `artifact.published`，仅已认证 Desktop Main WS 接收，不依赖当前 Chat 或网关；BTW、Explain 及其他 WS 不接收，attach/回放不重发。`resource.pushed` 只表示实际上传网关成功。
- `image_generate` 对 Agent 使用统一参数：无输入图时文生图，最多四张 Chat/本地输入图时图生图；生成和编辑端点及请求格式完全由模型 YAML 选择 Images JSON、Images Multipart 或 Chat Completions，不按模型名/provider 分支。生成与编辑可分别配置 `omitResponseFormat`，省略上游不接受的返回格式参数。可选 mask 支持 alpha、白区编辑和黑区编辑三种显式语义，仅在模型声明原生 `openai-alpha` 能力时执行局部重绘。
- 文件工具与 Bash 共享 `AccessPolicy`；普通和临时脚本在 `default` 按调用与内容版本审批，`auto_approve` 自动审计，`full_access` 允许；本 Run 在 Chat 目录中通过文件工具写入且内容未变的自写脚本免执行审批，选中技能入口保留独立凭据。有效 `.state` 等私有目录在三档均拒绝普通工具读写。连接器凭据仅注入已验证的直接子进程，业务 Hook 独立生效。Host 并发批准继续按 toolID 隔离。见 [工具目录权限](docs/工具目录权限.md) 与 [权限与审批边界](docs/AccessPolicy与HITL改造.md)。
- Skill 调度遵守用户任务范围与交付形态，见 [Skill 调度范围](docs/Agent运行时组装.md#skill-调度范围)。
- `mustUseSkills` 为本次 run 选中的每个 Skill 目录追加 trusted read + readonly roots：完整目录免读路径 HITL，未选中的 skills-center 兄弟目录不随之开放，任何 `accessLevel`、hostAccess 或 approval 都不能写入这些选中目录。Container 仍只读挂载整个 `/skills-center`，mount 可见性不等同于 AccessPolicy 授权。
- Agent YAML 已配置普通 Skill 与本次 `mustUseSkills` 选中 Skill 的 `scripts/**` 入口，经本 Run 内存凭据（canonical 路径与 SHA-256）及执行前复验匹配后免入口 HITL；凭据不落盘、不跨 Run 继承，外围 Shell 和写入限制保持独立。见 [工具目录权限](docs/工具目录权限.md#技能脚本入口执行凭据)。
- 专用 `mode: KBASE` 与普通 KBASE capability 都以 `runtimeConfig.workspaceRoot` 为唯一内容根；专用 mode 的 main/editing 两种 stage 使用同一组配置工具，工具由 Platform 预置与 Agent 声明合并并应用排除项，当前 Chat 目录独立可读写。单次 `/api/query` 顶层 `editingMode:true` 只允许 KBASE Workspace mutation，未开启时 Workspace 仍可读但不可 write/edit；所有目录先服从 AccessPolicy/HITL，索引由 KBASE watcher 异步维护。普通 Agent 附加的 KBASE capability 与其他 mode 不支持该字段。
- `builtin.platform-control` 显式挂载后提供 Catalog、Chat、诊断和七个 Desktop 域工具；Catalog 支持资源能力枚举及 Provider/MCP 组件只读发现，连接器与 MCP 分开表示，列表需跟进 nextCursor；配置修改及删除 Chat 必须一次性人工审批。`run_env` 由全局 preset 示例提供，动态值只作用于当前普通 native root Run 的后续命令。详见 [平台控制连接器](docs/Platform控制工具设计.md)。

Native 模型流式正文与推理各自达到 4,000 Unicode 字符后检测持续精确复读，命中会取消请求且不自动重试；详见 [流式复读取消](docs/配置化说明.md#流式复读取消)。

MCP 支持 HTTP/stdio client、SDK 协议版本协商、session 生命周期与 tool sync；全量生产验证和更深层的 automation 执行编排仍有待完善。

## 2. 快速开始

### 前置要求

- Go 1.22 或更新版本
- Docker / Docker Compose（如需容器运行）
- 可用的 provider / model 注册文件（放在 `runtime/registries/`）
- 相邻的 `../agent-platform-builtins/{ripgrep,kbx,memx,poppler-pdftotext,dbx,httpx}` 本地产物仓库集合；默认自动寻找相邻项目，Git worktree 也会查找主仓库的相邻项目；可用绝对路径环境变量 `BUILTINS_ROOT` 覆盖

### 本地启动

```bash
cp .env.example .env
./scripts/sync-local-builtins.sh
make run
```

`./scripts/sync-local-builtins.sh` 是本地 builtin 构建入口（当前 KBX 支持 macOS AMD64/ARM64、Windows AMD64）；Windows AMD64 使用 `powershell -ExecutionPolicy Bypass -File scripts/sync-local-builtins.ps1 -Target windows/amd64`，不依赖 Git Bash。两个入口都会在隔离工作目录中按相邻项目的本地 `VERSION`（KBX 使用 `Cargo.toml` 的 package version）重建 `dbx`、`httpx`、`kbx`、`memx` 和 `poppler-pdftotext` launcher/archive，生成只属于本次构建的临时 local lock，再原子更新 `build/builtins/<os>-<arch>/`。cache 激活后默认运行同一套正式 lock 状态机：精确 native host 上严格更高的干净版本可在输入精确 `yes` 后成为组件目标 release；落后平台的本地 `VERSION` 与 Git HEAD 匹配该目标后自动更新自己的 target，无需再次确认。正式 lock 中组件字段表示全平台目标，target 字段记录各平台实际 release/path/SHA；交叉构建只更新 cache，不能改正式 target，因而 macOS 不会改 Windows SHA。两个入口都不写 `release-local/`。`rg` 是唯一只校验并复制的预编译 vendor artifact。`make run` 只构建 Go runtime、加载根目录 `.env` 并从 `release-local/backend/agent-platform` 启动；它通过 `AP_BUILTINS_BIN` 将本机 `build/builtins/<host>/bin` 设为唯一可信 builtin 目录，KBX 和 memx 也从该目录解析，但绝不复制或编译 builtin。未设置 `SERVER_PORT` 时默认监听 `11949`。

`--all` 会要求本机已提供六个平台的 Rust target、对应 linker/SDK、`protoc` 与 `syft`；任一 target 不能构建时失败，且既有 `build/builtins` cache 不会被替换。正式 `make release-program` 只消费对应 target 的本机 cache，不会重新构建或回读 `../agent-platform-builtins`；cache 缺失、平台不匹配或 manifest 校验失败会直接终止发布。

也可以显式拆开构建与启动：

```bash
make build-local
make run-local
```

Windows 可用构建环境变量 `BUNDLE_GIT_BASH=false` 排除 Git Bash，默认 `true`。该变量同时适用于 builtin sync、Platform release 和继承环境的 Desktop 构建脚本；不修改正式 lock 或运行时 Shell 配置。已有完整 cache 时可直接执行 `make release BUNDLE_GIT_BASH=false`。详见 [Git Bash 可选打包](docs/WindowsGitBash实施进度.md#可选打包-git-bash)。

`memx` 由相邻项目的 `scripts/build-release.sh` 与 Go 构建辅助程序生成版本化归档，Windows 同步直接调用该 Go 程序，不增加 memx 的 Python 依赖。发布缓存必须包含至少 0.2.0 的 `bin/memx`（Windows 为 `memx.exe`）；运行时还检查 maintenanceVersion=2 和 configDirEnv=true。分层 memx 的版本重新编号不代表 Platform 已完成新调用契约接入，当前状态见 [记忆分层改造方案](docs/记忆分层改造方案.md)。同步后执行 `make test-memory-integration`，用本机 cache 验证真实 CLI；该检查也是 `make test` 的前置条件，缺失或不兼容会失败。配置与授权边界见 [记忆系统](docs/记忆系统.md)。

`make build-local` 只把 runtime 写到 `release-local/backend/agent-platform`，不会变更 `release-local/bin/`。builtin 缺失或本机构建失败由同步脚本失败报告。由于 runtime 位于 `backend/` 下，启动时只扫描服务包根目录的 `plugins/`，与 Desktop 服务包形态一致。`runtime/` 包含 agents、connectors-center、chats、skills-center、registries、memory 等运行数据；Platform 会由 agents、skills-center 与挂载的 connectors-center 包重建 `ru-agents/` 作为唯一 Agent 执行目录。

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

默认 `make test` 同样会使用 `CGO_ENABLED=0`，先用本机 builtin cache 执行 memx 子进程集成测试（本地 mock 模型，不访问线上模型），再通过串行包测试加临时 `GOCACHE` 规避当前 macOS 环境里的并发 test/cache 异常。需要显式验证服务端真实本地 socket 流式链路时，使用：

```bash
RUN_SOCKET_TESTS=1 make test-integration
```

## 3. 配置说明

本地启动变量从 `.env.example` 复制到 `.env`。`.env` 不提交；`.env.example` 只保留启动/部署 allowlist。运行时配置使用 `configs/runtime.yml`，工具运行时与 AI 工具配置统一使用 `configs/tools.yml`，默认值的单一事实源仍以代码和 `configs/*.example.yml` 模板为准。更完整的高级与排障配置参考见 [配置化说明](./docs/配置化说明.md)。

Platform 运行形态只由 `--runtime-mode=standalone|desktop` 指定，默认 `standalone`。Desktop 宿主启动内置 Platform 时固定传入 `desktop`；Platform 不根据端口、父进程、WS `source` 或 YAML 猜测运行形态。七个 `desktop_*` 域工具与网页控制工具（`workpanel_*`、`surface_*`、`awcp_*`）优先使用当前 run 绑定的反向 WebSocket target；Desktop 模式下，无绑定或旧连接在发送前已失效的 run 会补绑当前 `desktop-main`，Standalone 仍只认 run target。两种模式都不调用本地 HTTP bridge，也不重放已经发送的动作。

外部连接器原包安装在 `<AP_RUNTIME_DIR>/connectors-center/<id>`；启动和普通管理命令忽略旧 connectors/connector-state 及旧包根状态，不自动迁移；内置原包由 Platform 随包提供，不可修改或删除。Agent 挂载后，两类包统一组装到 `<AP_RUNTIME_DIR>/ru-connectors/<id>/<contentDigest>`，持久化授权状态保存在通用 `.state` 根下的 `.state/connectors/<id>/`。两类连接器统一由 Agent 的 `connectorConfig.connectors` 挂载；挂载自动增加本 Agent 运行包 bin PATH、导入全部技能元数据并接入 MCP 工具，同时授权运行包内 CLI 的全部子命令和参数，在所有 accessLevel 下免除入口执行审批；匹配的业务 Hook 仍按规则审批，需要凭据时仅支持已验证的直接子进程；技能正文和资源随包复制，从本 Agent 的 `@connectors/<id>/skills/...` 读取，不再重复放进同级 skills 目录；skillId 使用原始技能名，不添加连接器前缀，同一 Agent 内技能重名时返回冲突诊断。包允许 `bin/libs`，连接器技能不能被 `mustUseSkills` 选中。`dbx/httpx` 的清单和完整技能源码位于相邻 `agent-platform-connectors/{dbx,httpx}/connector/`，由各项目与二进制一起打包，Platform 锁定并校验完整包；旧 `registries/mcp-servers` 目录直接忽略，不影响启动；不提供旧目录迁移，连接器按当前格式重新安装，Agent 挂载使用新字段。MCP 的 HTTP/stdio 优先请求 `2025-11-25`，兼容 SDK 支持的 `2025-06-18`、`2025-03-26` 和 `2024-11-05`，并保持后台 tool sync。外部包支持通过 `DELETE /api/admin/connectors/detail?id=<id>` 删除；需先解除 Agent 引用并等待旧运行挂载释放，授权和 CLI 状态保留，重载失败恢复原包。ZIP 导入后异步 CLI 准备（有 bin 跳过 init）、独立准备状态与登录凭据、MCP OAuth PKCE 与令牌刷新见 [连接器安装与授权](./docs/连接器安装与授权.md)；包结构与旧目录处理见 [连接器](./docs/连接器.md)，协议细节见 [MCP与工具交互](./docs/MCP与工具交互.md)。

连接器锁统一存放于 `<AP_RUNTIME_DIR>/.lock/`，按布局初始化、装配、安装、来源操作和版本租约分开管理；`ru-connectors` 保留共享运行包与 `.shared-v1` 标记，不执行旧布局迁移。目录结构与锁路径切换要求见 [Runtime 锁目录](docs/连接器共享包与Desktop迁移.md#runtime-锁目录)。

连接器资源包通过清单声明组件、图标、认证方式与授权页面展示。包内程序、安装脚本和远程服务定义的分发边界见 [连接器打包与分发](./docs/连接器打包与分发.md)。

连接器认证模式包括 `no_auth`、`token`、`oneid-token`、`oauth`、`mcp` 与 `null`。`no_auth` 无需配置；需要凭证的模式使用包外来源，认证操作不触碰连接器定义文件。`auth_bindings` 声明 HTTP Header 或 Host CLI/stdio 环境模板，HTTP 发送前、进程启动前读取票据；自管 CLI 继续通过 `configEnv` 使用独立目录。MCP 支持多资源授权、客户端注册信息落盘、元数据发现回退、PKCE、刷新及追加权限提示，详见 [凭证消费映射与多组件授权](./docs/连接器安装与授权.md#凭证消费映射与多组件授权)。

### 根 `.env.example`

根 `.env.example` 现在是面向最终用户的最小启动模板，只保留以下配置：

- `SERVER_PORT`
- `AP_RUNTIME_DIR` / `AP_RUNTIME_REGISTRIES_DIR` / `AP_RUNTIME_CHATS_DIR` / `AP_RUNTIME_MEMORY_DIR` / `AP_RUNTIME_KBASE_DIR` / `AP_RUNTIME_PAN_DIR` / `AP_RUNTIME_STATE_DIR`
- `AP_CONTAINER_HUB_BASE_URL`
- `AP_CHAT_RESOURCE_TICKET_SECRET`
- `AP_DEBUG_LLM_CONSOLE`
- `AP_DEBUG_LLM_CHAT_RECORD`

目录只允许通过上述环境变量配置，其余子目录固定从 `AP_RUNTIME_DIR` 派生；YAML `paths` 节已删除，出现即报错。`AP_RUNTIME_STATE_DIR` 留空时使用 `<AP_RUNTIME_DIR>/.state`。除上述 allowlist 外，旧环境变量不再生效。resource ticket TTL 属于非敏感运行策略，使用 `configs/runtime.yml` 的 `resource.ticket-ttl-seconds` 配置。

Auth 默认开启，默认公钥文件为 `configs/local-public-key.pem`；相关默认值展示在 `configs/runtime.example.yml` 的 `auth` 节，根 `.env.example` 不再放 Auth 变量。

Memory 深度调优使用 `configs/runtime.yml` 中的 `memory.*`。Agent 的 `memoryConfig.enabled` 控制记忆采集；上下文独立通过 `contextConfig.tags` 的 `memory-global`（总体 Summary）和 `memory-agent`（当前 Agent Summary）选择，不配置标签就不注入。

Logging 默认值已经源码化，不提供 runtime YAML 入口；只保留 `AP_DEBUG_LLM_CONSOLE` 和 `AP_DEBUG_LLM_CHAT_RECORD` 作为现场调试 allowlist。LLM 交互日志、memory 参数和内部运行默认值的适用人群和注意事项统一见 [配置化说明](./docs/配置化说明.md)。

ACP CODER bridge 在 `configs/agent-settings.yml` 的 `acp-bridges` 中定义；agent 以顶层 `engine: acp` 加 `runtimeConfig.acpBridgeId` 引用条目，`timeout-ms` 默认 `300000`。配置变更需重启 runtime。ACP bridge 须能访问同一 Workspace，地址配置保持兼容；Platform 将 canonical `runtimeConfig.workspaceRoot` 作为私有 `params.cwd` 传给本机 bridge，用户不能覆盖，普通 PROXY/CHANNEL 不转发宿主 cwd。详见 [ACP 工作目录契约](docs/智能体配置说明.md#本机-acp-工作目录契约)。

Provider `apiKey` 按明文字符串读取：

- 未配置时保留为空值：`apiKey:`
- runtime 不再支持 provider `apiKey` 加密或解密；包括 `AES(...)` 在内的任何值都会作为普通字符串使用。

### `configs/` 目录

本仓库保留与参考仓库一致的结构化配置入口：

- `configs/tools.example.yml`
- `configs/channels.example.yml`
- `configs/agent-prompt.example.yml`
- `configs/agent-settings.example.yml`
- `configs/local-public-key.example.pem`
- `configs/runtime.example.yml`

当前 Go runtime 实际会读取：

- `configs/tools.yml`
- `configs/channels.yml`
- `configs/agent-prompt.yml`（环境提示词与 Run 语言见 [Agent 配置合并](docs/Agent配置合并.md#runtime-context-语言与模板)）
- `configs/agent-settings.yml`
- `configs/local-public-key.pem`
- `configs/runtime.yml`

`configs/` 不是可配置目录，固定使用 runtime 根目录下的 `./configs`；容器内固定挂载到 `/opt/configs`。

**静态配置**：`configs/` 下所有文件都只在进程启动时读取一次；修改 `configs/*.yml` 或 `configs/*.pem` 后必须重启 runtime 才会生效。

KBX 抽取由受管 CLI 负责。Agent 知识库由 Platform 监听目录、后台调用 update/embed，refresh 返回可等待的 refreshId；要求受管 KBX 支持维护 JSON v1，见 [KBX 接入](docs/KBX接入.md)。配置归属与升级步骤见 [Agent 配置合并](docs/Agent配置合并.md)。

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

普通主 Agent 可通过 `builtin.task-control` 挂载 `chat_start`、`chat_get_status`、`chat_interrupt`，用于发起、查询和中断标准独立 Agent/Team 根 run。它们与 `agent_invoke` 不同：不复用父 `chatId/runId`，query 在目标 run 注册后立即返回，父 run 中断不取消目标；后续控制只允许同一调用 Agent 与 subject 操作自己通过 `chat_start` 创建的 run。目标不使用候选白名单，精确 catalog 名称存在即可调用；`chat_start` 的工具描述负责把“当前智能体”“本智能体”“你自己”解析为 system prompt 的 `Agent Identity.key`，不得用目录中其他 Agent 的 key 替代。目标 run 禁止再次调用任一 Chat 工具。支持可选 `accessLevel/mustUseSkills/chatName`；省略时继承父 Run 受理当时的 access level，显式同级或降级直接启动，显式高于父 Run 当前档位必须由用户在父 Chat 中人工批准本次启动（不提供免审开关），显式新 Chat 名称与 `chatId` 互斥；状态返回当前档位与等待摘要。中文“会话”和“对话”都指 Chat；要求新开会话／对话时调用 `chat_start` 并省略 `chatId`，未指定目标时使用当前 Agent key。完整契约见 [子智能体调度](./docs/子智能体调度.md)。

原生连接器由 `connector.json` 的 `type: native` 和连接器 ID 对应的源码工具表装配，不使用 `native.json`。`builtin.platform-control` 提供平台治理能力；`run_env` 独立提供当前普通 native root Run 的 list/set/unset/update/explain，由通过 preset 或 Agent 显式声明挂载，分发示例列入全局 preset-tools，可通过 excludeTools 排除。动态值仅影响后续命令、不继承到子任务或其他 Run。旧 platform-control 配置段已移除。详见 [Run 环境工具](docs/Run环境工具.md)。

四个内嵌原生连接器 platform-control/task-control/kanban-control/web-control 统一从 `0.4.0` 开始使用 Platform 版本系列，发布时统一维护源码清单版本；外部 dbx/httpx 保持独立项目版本。详见 [内嵌连接器来源](docs/连接器.md#desktop-内嵌连接器来源)。

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
- `AP_RUNTIME_REGISTRIES_DIR`、`AP_RUNTIME_CHATS_DIR`、`AP_RUNTIME_MEMORY_DIR`、`AP_RUNTIME_KBASE_DIR`、`AP_RUNTIME_PAN_DIR`、`AP_RUNTIME_STATE_DIR` 可单独覆盖宿主机 bind source；未配置时自然落在 `${AP_RUNTIME_DIR}` 下
- 容器内 runtime 根目录固定为 `/opt/runtime`，应用通过 `AP_RUNTIME_DIR=/opt/runtime` 解析子目录
- `./configs` 只读挂载到 `/opt/configs`

Container Hub 使用严格双根协议，基础挂载包括：

- `/workspace` -> 当前 Agent 的 canonical `runtimeConfig.workspaceRoot`，`rw`
- `/chat` -> `AP_RUNTIME_CHATS_DIR/<chatId>`（`rw`）
- `/root` -> `<AP_RUNTIME_DIR>/root`（`rw`）
- `/skills` -> `<AP_RUNTIME_DIR>/ru-agents/<agentKey>/skills`（仅 `run/agent`，`global` 默认不挂载），`ro`
- `/pan` -> `AP_RUNTIME_PAN_DIR`（`rw`）
- `/agent` -> `<AP_RUNTIME_DIR>/ru-agents/<agentKey>`（`ro`，必挂载；目录缺失会 fail-fast）
- `/owner` -> `<AP_RUNTIME_DIR>/owner`（`ro`，目录缺失时自动创建）
- `/memory` -> `AP_RUNTIME_MEMORY_DIR`（`ro`，目录缺失时自动创建）

容器 session 与未显式指定 cwd 的命令固定使用 `/workspace`。协议为 `dual-root-v2`。当 ChatsRoot 位于 Workspace 内时，Platform 下发 `/workspace/<ChatsRoot-relative>` mask，Hub 按 Workspace bind → mask tmpfs → current Chat bind 的顺序创建容器，确保 Chat 只从 `/chat` 可见。`/workspace`、`/chat`、mask 及其子路径是保留挂载目标，`runtimeConfig.sandboxMounts` 不能覆盖。session 复用身份包含 environment、canonical Workspace、canonical Chat、mask 和完整 mount fingerprint。

目录型 agent 可在源目录 `.config/` 保存专属覆盖，Skill `.config/` 提供可分发默认值；Platform 合并到 `ru-agents/<agentKey>/.config/`。平台冻结四个保留变量：`AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR`、`AP_ACCESS_TOKEN`。Host 注入前三者对应的生成配置目录、真实 Workspace（无 Workspace 时省略）和 Chat；Workspace Terminal 只注入 Agent 配置目录与真实 Workspace，不注入 `AP_CHAT_DIR`；Container 固定为 `/agent/.config`、`/workspace` 与 `/chat`。普通 Host Shell 不自动获得第四个变量；经验证的单条直接 oneid-token CLI 调用在独立子进程中使用该变量，已挂载 oneid-token stdio MCP 也从有效 identity 文件即时读取，默认文件为 `<有效 StateDir>/identity/access-token`，可由最高优先级的 `--identity-file <absolute-path>` 覆盖，不进入 Terminal 或 Container。agent `runtimeConfig.env`、skill `.runtime-env.json`、run dynamic env 与调用级 env 均不得覆盖。动态层只通过 `run_env` 的 `set/unset/update` 修改当前普通 native root run 的进程内 Scope；Host Bash、直接短进程和 Container 新 command 获取快照，子 Agent、Team、Terminal、MCP、ACP、Proxy、Channel、LSP、sidecar 和已启动进程不继承或更新，Platform 重启后的续接 run 从空动态层开始。HTTPX 的 chat state/secret 位于 `$AP_CHAT_DIR/.state/httpx` 与 `$AP_CHAT_DIR/.secret/httpx`，缺少合法 `AP_CHAT_DIR` 时不回退 global。完整组装、冲突和迁移规则见 [Agent 运行时组装](./docs/Agent运行时组装.md)。

`runtimeConfig.sandboxMounts` 会真实影响 Container Hub session mounts：

- `platform + mode`：恢复按需平台挂载，或覆盖默认 `/agent`、`/owner`、`/memory` 模式；`platform: skills-center` 会显式挂载 `/skills-center`
- `destination + mode`：覆盖非保留的默认基础挂载模式
- `source + destination + mode`：新增自定义挂载，不能拿来覆盖默认基础挂载路径

发布前运行 `make audit-workspace-chat`，只读列出旧 `working-directory`、无 Workspace 的 CODER/sandbox/KBASE、`workspaceRoot:@chat`、旧 `kbaseConfig.source`、非法 Workspace/Chats 关系和保留挂载冲突；对合法但无 Workspace 且挂载路径工具的普通 Agent，还会输出非阻断诊断，提示必须显式提供 Bash `cwd`、glob/grep `path` 和语义根路径。普通 Workspace 可以包含 ChatsRoot；KBASE Workspace 仍要求完全分离。

`configs/runtime.example.yml` 的 `container-hub` 节展开 `base-url`、默认 environment 和运行策略默认值；代码默认值仍作为未配置时的兜底。除 `AP_CONTAINER_HUB_BASE_URL` 外，Container Hub token、environment id、超时和 sandbox 策略统一写入 `container-hub.*`，用于对接 `agent-container-hub` 的 `AUTH_TOKEN` Bearer 鉴权。

`contextConfig.tags` 按 Agent 选择运行时上下文。旧 `agents` 标签和 `contextConfig.agents` 已忽略并显示一条非阻断管理告警，不再注入候选摘要。显式挂载 `builtin.platform-control` 的 Agent 可通过 `catalog_query.list` 按需发现 Agent；不改变 Chat 工具和子调用权限，`chat_start` 仍可使用当前 `Agent Identity.key` 创建自己的独立会话子任务。详见 [智能体配置](./docs/智能体配置说明.md#agent-发现与旧候选配置)。

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

产物写入 `dist/release/`，包含纯 Go runtime、配置模板、启停脚本、`bin/{rg,kbx,memx,pdftotext}`、`connectors/builtin.{dbx,httpx}/`（清单、bin/libs 与技能）、`libexec/poppler-pdftotext/`、builtins manifest、许可证 notice、压缩包 SHA-256 与大小报告。program manifest 声明 `desktop.runtimeResources: "v1"`；`deploy.sh` / `deploy.ps1` 将 Desktop 传入的 env.zip 与稳定设备标识交给统一的 `agent-platform runtime-resource-sync` 子命令，由 Platform 迁移已有 runtime 的 Agent、Skill、Tool、Team、Connector 与 Registry。包内五类一级资源及同路径 Registry 是发行方权威版本，同名目标会覆盖；新版包声明 `provider-register.json` 时，还会重新生成并注入 Provider API key。`release-program` 复验并复制 `build/builtins/<os>-<arch>/` 中的二进制与完整连接器包，不重写清单、技能或包哈希，不会构建 Rust sidecar，也不会读取相邻 `agent-platform-builtins`。KBX 尚无受支持的 Linux 发行目标，因此当前不能生成完整 Linux/Docker 包；同步脚本会提前拒绝该目标。Desktop 宿主集成时执行资源同步：

```bash
npm run sync:assets
```

完整打包细节见 [版本化打包方案](./docs/版本化打包方案.md)。

KBASE 是可组合的 Agent 公共能力：`mode: KBASE` 仍是强制启用知识库能力的专用预设，工具、技能、连接器和 memory 与其他内置类型一样完全取自 `agent.yml`，`GENERAL`、`PLAN-EXECUTE` 和原生非 ACP `CODER` 也可以通过 `kbaseConfig.enabled: true` 挂载同一套索引、watcher、检索和引用能力。所有 enabled KBASE 都以 `runtimeConfig.workspaceRoot` 为唯一内容根；旧 `kbaseConfig.source` 会硬失败。完整配置和兼容矩阵见 [智能体配置说明](./docs/智能体配置说明.md)。

KBASE 模式和普通 Agent 的知识库能力统一由受管 KBX CLI 实现，Platform 负责目录监听、异步刷新回执和重启对账。中立配置、DTO、工具处理器与引用发布位于 `internal/knowledge`；KBASE 工具、REST 和 `/healthz` 的 `kbase.sidecar` JSON 契约由该模块提供。索引范围由 scopeHash 隔离。Poppler 继续随包提供 KBX PDF 抽取及 Agent Bash 的 `pdftotext` 能力。详见 [KBX 接入](./docs/KBX接入.md)。当前检索基于抽取文本，不宣称支持图片、音频或视频语义检索。

`kbase_search` 支持混合 query、纯全文 search、向量 vsearch 和图关系 gsearch，提供复合过滤、词法排除、时效排名与图关系遍历参数，保留可核验证据。Agent 工具仍限定自身 Workspace 索引；向量/图检索需要相应索引，Platform 尚不自动构建图谱，重排模型、中心选库及表格专用工具尚未接入。

KBASE Editing 使用通用文本文件规则，不按索引格式硬编码扩展名或 UTF-8；删除、重命名、建目录、Bash 和二进制 Office/PDF 通用写入仍不开放。目录权限由 AccessPolicy/HITL 决定，Workspace 写入由 watcher 异步索引。完整约定见 [KBASE 编辑模式](./docs/KBASE编辑模式.md)。

## 5. 运维

### 查看日志

模型出现 `Model returned no assistant content.` 时，按 `runId` 检索服务日志中的 `llm_empty_response` 或 `llm_model_attempt_error`，区分模型空响应与传输/解析失败；`AP_DEBUG_LLM_CHAT_RECORD=true` 还保留逐次重试文件及有界 SSE 事件结构，字段与 trace 开关见 [模型空响应排查](./docs/配置化说明.md#模型空响应排查)。

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
- Memory 使用 OWNER.md、summary.md 与 daily 日期记录；Platform worker 默认每 300 秒整理已完成 Chat，通过按次 memx 提取事实到 Agent daily，逐 Agent summarize 后由 consolidate 归纳总体记忆。全局 memory.worker 控制频率、模型和批次限额，memory.summary.global/agent 控制 token 与行数预算；支持手工 HTTP 日期范围整理（含受管归档、进度、取消与重试）、管理端增量整理；Agent 通过 memx 获取记忆，通过 file_write/file_edit 修改 Markdown，受现有工具和路径权限约束；手工任务不改定时扫描起点。现有 kind=memory 保持兼容，旧 memory.md 复制一次并保留备份。详见 [记忆系统](./docs/记忆系统.md)。
- 上传后无法下载：确认文件已落到 `AP_RUNTIME_CHATS_DIR/<chatId>/`，并检查 `/api/resource?file=...` 是否使用响应中的 ChatScope `url`。

## 文档索引

参见 [完整文档索引](docs/README.md)，按配置、运行时、协议、权限、连接器、知识库、构建和验证分类。历史报告单列，不能作为当前能力或本轮测试通过的依据。

知识库中心：配置位于 `kbases/<id>/library.yml`，持久索引与状态位于 `ru-kbases/libraries/<id>/`；后者跨重启保留，不随 `ru-agents` 清空，也不可按 `ru-*` 批量清理。旧 `kbases-center` 不再读取，模板目录不进入库操作；来源离线且索引范围仍匹配时可读取完成索引，启动刷新后的失败仍须重新更新成功才能读取。支持部署级独立知识库、多 collection 建库、手动 KBX 索引更新，以及综合/全文/向量/图召回方式选择，图谱构建尚未接通，见 [知识库中心](docs/知识库中心.md)。

`builtin.task-control`（任务管理）独立提供五个 Chat 工具和两个 Automation 工具；`builtin.platform-control` 不再提供会话和自动化工具。任务管理不包含 Desktop 看板或网页控制；迁移与权限边界见 [连接器](docs/连接器.md#task-control-任务管理)。

平台管理与自动化的操作说明由相应内置连接器 Skill 承载，包含自动化时间/Cron 工作流、YAML 维护及 Provider/Model/外部连接器配置参考，不需要额外挂载外部 platform-admin 或 platform-automation Skill。自动化更新、时区和审批契约见 [自动化](docs/自动化.md#task-control-管理工具)。

`builtin.kanban-control`（看板控制）独立提供 `desktop_kanban` 的六个看板动作，依赖 Desktop；原 `builtin.platform-control` 挂载不再授予看板能力。需要看板的 Agent 应显式挂载新连接器，详见 [连接器](docs/连接器.md#kanban-control-看板控制)。
