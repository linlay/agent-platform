# AGENTS.md

## 1. 项目概览

`agent-platform` 是 `agent-platform` 的 Go 版运行时仓库，目标是在保持 Java runtime 接口风格与部署方式尽量一致的前提下，逐步形成可独立运行的 agent runtime。

当前仓库定位是“最小可运行闭环 + 特色能力持续补齐”：

- 已具备独立 HTTP 服务、统一 JSON 包裹与 `POST /api/query` 真流式 SSE。
- 已具备 chat 摘要、事件流、raw messages、上传资源落盘、归档与搜索。
- 已具备目录驱动的 agents / teams / skills / tools catalog，并在 Catalog 发布前将 Agent 定义、Agent 自有 Skill、技能中心 Skill 和 `.config` 组装到稳定的 `ru-agents/<agentKey>` 执行目录；query `mustUseSkills` 可在单次普通 Agent run 中强制使用额外技能中心 Skill，并以 Host 只读语义路径或 Container 整个技能中心只读挂载直接访问 `skills-center`，不复制、不生成 run-runtime。Admin Agent 支持安全校验完整 ZIP、以隐藏 staging/backup 原子导入或整目录覆盖；硬重载失败恢复旧来源，catalog 可发布但单个 Agent 无效时保留导入结果并返回诊断。
- 已具备 OpenAI / Anthropic 协议模型调用、统一 Tool、Container Hub sandbox 与 tools；`image_generate` 以统一参数支持文生图、最多四张本地/Chat 参考图的图生图，以及模型 YAML 显式声明的原生 mask/inpainting，生成和编辑请求分别由模型 YAML 的 `image.generation`、`image.edit` 协议块适配。
- 已具备由 `build/builtins/<os>-<arch>/` cache 固定、校验并随服务包分发的 Host builtins（rg/dbx/httpx/kbase-lance-engine/poppler-pdftotext）；`file_grep/file_glob` 稳定包装 rg，dbx/httpx 保持 CLI，KBASE PDF 默认调用 Poppler `pdftotext` launcher。
- 已具备 HITL question / approval / form、运行中 submit / steer / interrupt 协议入口，以及 question/planning 跨进程恢复和不可恢复等待项的幂等终态对账。
- 已具备固定 Schema 的 `platform_control` system control plane：Agent 显式挂载 Tool 即可调用全部注册 operation；`run.env.*` 仅保留当前普通 native root run 的 `set/unset`，使用进程内并发 Scope、operation-aware barrier 和 Host/Container 新 command snapshot，不修改 Platform 进程环境，也不跨 Platform 重启恢复。遗留 `runtimeConfig.runEnv` 静默忽略。
- 已具备 SQLite memory、FTS、可选 embedding、learn / consolidate / feedback 与 memory tools。
- 已具备可由普通 Agent 挂载、并保留专用 `mode: KBASE` 预设的 KBASE 文本知识库公共能力，包括 LanceDB generation 检索、加权 RRF、目录增量 watcher 与本地 Rust sidecar 管理；SQLite `control.db` 只负责 generation、文件状态与恢复日志。
- 已具备以 `runtimeConfig.workspaceRoot` 为唯一内容根的 KBASE 公共能力；专用 `mode: KBASE` 在 main/editing 两种 stage 使用相同的通用文本文件工具，当前 Chat 目录独立可写；单 run `editingMode` 只控制 KBASE Workspace mutation，写入与索引解耦，由 KBASE 目录 watcher 异步维护。
- 已具备 automation、`agent_invoke` 子智能体调度、`run_query` / `run_status` / `run_interrupt` 独立 Agent/Team 根 run 启动与控制、带隐藏协调器的 orchestrated Team、基于官方 Go SDK v1.6.1 的 MCP streamable HTTP/stdio session client 与 tool sync、WebSocket 控制面，以及 client/server channel 上按 Session 执行的 Agent 接出注册 v1（`agent.list/register/unregister`）；MCP 唯一稳定协议版本为 `2025-11-25`。Channel 注册当前不升级 Query Stream、Run TTL、`registrationId` 路由、HITL Schema 或控制协议。
尚未完全对齐 Java 版的部分能力包括 MCP 全量生产验证、automation 深度编排、热重载细节和更完整的客户端协议适配。未落地能力必须在专题文档中明确标注，不能写成已完成能力。

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统 + SQLite memory/control store + 本地 LanceDB KBASE generation
- 配置：环境变量 + `configs/*.yml`

当前没有引入 Web 框架、第三方路由库、外部数据库或消息队列。Go 主程序仍以 `CGO_ENABLED=0` 构建；KBASE 通过随包分发的 `kbase-lance-engine` Rust 伴随进程使用锁定的 LanceDB Rust SDK。配置默认值以 `internal/config/config.go` 与 `configs/*.example.yml` 为事实源。

## 3. 架构设计

启动装配主链路：

```text
cmd/agent-platform/main.go
  -> app.New()
  -> config.Load()
  -> chat store / memory store / catalog registry
  -> model registry / MCP registry / gateway registry
  -> sandbox service / runtime tool executor
  -> LLM agent engine / automation orchestrator
  -> server.New()
```

核心模块边界：

- `internal/agent`：中立 mode 契约、公共 prompt 模板变量与 system-init spec；`internal/agent/builtin` 是 CODER/KBASE/TEAM 的静态分派点。
- `internal/agent/coder`：CODER profile、prompt、planning、ACP/workspace 策略与创建默认策略。
- `internal/agent/kbase`：专用 `mode: KBASE` 的 profile、prompt、system-init、创建默认值与严格工具/memory 边界。
- `internal/kbase`：mode 中立的 KBASE 公共能力；`Manager` 只作为公开门面和组件装配点，内部由 capability resolver/state、storage validator/auditor、watch/lifecycle supervisor、refresh coordinator、generation service、query/status/files service 与 Lance runtime 分别维护配置解析、存储契约、调度、索引/恢复、检索和 sidecar 生命周期。app adapter 只向 Manager 暴露 enabled capability，`AgentSpec.WorkspaceRoot` 是唯一内容根事实；未启用与不存在统一按 not found 处理。该包同时维护公共 prompt、HTTP 业务错误与五个工具 handler；不得 import `internal/agent` 或 `internal/catalog`。
- `internal/agent/team`：内部 TEAM profile、硬编码调度规则、成员 roster prompt、session-local 隐藏工具与调度状态机；TEAM 不能配置成普通 agent。
- `internal/runops`：显式挂载的 `run_query` / `run_status` / `run_interrupt` named handler、调用方/subject 所有权、父 run/tool ID 幂等与禁止链式调用；实际 query admission、detached executor 和 Proxy 控制复用 `internal/server` facade。
- `internal/platformcontrol` 与 `internal/runenv`：统一 system control operation registry/handler，以及当前普通 native root run 的进程内并发 Scope、revision、limits 与幂等状态。
- `internal/server`：HTTP 路由、请求校验、响应包裹、SSE / WebSocket 协调。
- `internal/llm`：模型协议、prompt 构建、run stream、HITL、planning、tool loop。
- `internal/tools`：通用 tool registry/router、Bash、FileTools、memory、desktop、MCP tool 调用；mode 工具通过命名 handler 接入，不在 executor 中增加 mode switch。
- `internal/chat`：chat 摘要、事件、StepLine、raw messages、资源文件、归档、回放。
- `internal/memory`：SQLite memory、FTS、embedding、生命周期整理、反馈循环。
- `internal/catalog`：agent / team / skill / tool 目录装载与定义解析；Team 只接受目录式 orchestrated 定义，并以原子快照冻结成员、协调器配置和 prompt。
- `internal/config`：环境变量、YAML、默认值。
- `internal/stream`：统一事件、dispatcher、SSE writer、事件归一化。
- `internal/sandbox`：Container Hub client、mounts、sandbox 执行。
- `internal/automation`：automation 注册、调度、执行记录。
- `internal/ws` 与 `internal/gateway`：WebSocket 控制面与反向 gateway 连接。

这里没有类继承：`internal/agent` 是中立契约层，`internal/agent/coder`、`internal/agent/kbase` 与 `internal/agent/team` 是该契约下的三个内置 mode 实现，`internal/agent/builtin` 只负责静态分派。`internal/kbase` 是可由多种普通 mode 组合的公共能力，不属于 mode 分派层。TEAM 是仅由 orchestrated Team 在 run 内合成的内部 mode；隐藏协调器不进入普通 agent catalog，也不能通过普通 Agent YAML 或管理接口创建。

## 4. 目录结构

```text
.
├── cmd/agent-platform/          # 进程入口
├── configs/                     # 配置模板与本地覆写入口
├── docs/                        # 中文专题文档
├── internal/                    # Go runtime 实现
│   ├── agent/                   # 中立 mode 契约及 CODER/KBASE/TEAM 特有实现
│   ├── runops/                  # 独立 run 工具组 handler、所有权与幂等
│   └── kbase/                   # mode 中立的知识库公共能力
├── build/                       # 忽略的多平台 builtin 本地装配缓存
├── scripts/                     # 审计和辅助脚本
├── Dockerfile
├── Makefile
├── compose.yml
├── README.md
└── VERSION
```

`docs/` 是特色能力的主说明区；当前项目事实文件 `AGENTS.md` 只保留事实总览、开发入口和专题索引。

## 5. 数据结构

Chat 默认由 `AP_RUNTIME_CHATS_DIR` 控制，主要包含：

- `chats.db`：chat 摘要索引。
- `<chatId>.jsonl`：运行事件、StepLine、system init 与 raw messages。
- `<chatId>/<uploaded-or-generated-file>`：上传与图片生成资源；工具返回内部绝对 `path` 和相对于当前 Chat 的稳定 `url`（不含 `chatId`），用户可见内容只使用 `url`。
- `<chatId>/artifacts/<runId>/<filename>`：`artifact_publish` 的发布副本；发布结果 URL 必须指向该副本。

Memory 默认由 `AP_RUNTIME_MEMORY_DIR` 控制，当前以 SQLite store 为主，支持 FTS、可选 embedding、observation / fact 生命周期、`/api/learn` 与 memory tools。

KBASE 默认由 `AP_RUNTIME_KBASE_DIR` 控制，每个 agent storageDir 可包含：

- `control.db`：schema v4 控制面，记录 generation、文件状态、file operation、增量 refresh 指标和 index run；不保存 chunk、FTS 或 embedding。control 与 Lance schema 版本独立；SQLite 控制面只接受当前 schema，绝不原地迁移。
- `generations/<generationId>/lance/`：LanceDB chunks table 及索引；同级 `manifest.json` 保存 generation 元数据。

核心 DTO 位于 `internal/api`，包括 query、submit、steer、interrupt、learn、chat、upload、automation、memory console 等请求和响应类型。

## 6. API 定义

所有非 SSE JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

主要接口分组：

- Catalog：`/api/agents`、`/api/agent`、`/api/skills`、`/api/teams`、`/api/admin/skills`、`/api/admin/tools`；`/api/skills` 同时支持 HTTP 与 WebSocket，按 `agentKey` 返回有效技能中心 Skill 和该 Agent 已配置 Skill 的并集，并用 `agentHasSkill` 标识 Agent 当前是否已有。
- Chat：`/api/chats`、`/api/chat`、`/api/chats/search`、`/api/read`、`/api/chat/export`。
- Archive：`/api/archives`、`/api/archive`、`/api/archives/search`。
- Run：`/api/query`、`/api/attach`、`/api/submit`、`/api/steer`、`/api/interrupt`。
- Memory：`/api/learn`、memory console 相关接口。
- KBASE：`/api/kbase/{agentKey}/status`、`/api/kbase/{agentKey}/refresh` 以及五个 KBASE tools。
- Project / Resource：`/api/project/tree`、`/api/project/changes`、`/api/project/diff`、`/api/upload`、`/api/resource`。Project 只读接口只接受 CODER/KBASE 的 Workspace 相对 POSIX 路径，复用 file-history 作为 Run Diff 基线。
- Viewport / WebSocket：`/api/viewport`、`/ws`。

详细协议拆分到专题文档：REST / SSE / WebSocket 见 [API与协议](docs/API与协议.md)，真流式与 attach 见 [真流式和H2A](docs/真流式和H2A.md)，HITL 见 [HITL协议](docs/HITL协议.md)。

## 7. 开发要点

- 通用运行时配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准；KBASE capability 的配置、索引/检索默认值和工具名以 `internal/kbase` 为准，专用 KBASE mode 的 profile、prompt、创建策略和边界以 `internal/agent/kbase` 为准；CODER/TEAM 规则分别以 `internal/agent/coder`、`internal/agent/team` 为准，文档只解释和引用。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem`、真实 token 和私钥不得提交。
- 工具运行时配置以 `configs/tools.yml` 为外部事实源，包含 access policy、bash 和 file tools。
- `configs/tools.yml` 中的旧 YAML 路径策略键（如 `bash.allowed-paths`、`file-tools.allowed-read-paths`）会在启动阶段硬失败；Go 配置结构中的旧路径字段也已删除，目录权限统一走 `tools.access-policy`。
- `@temp` 是进程启动时冻结的通用临时根：Unix/macOS 使用 `os.TempDir()` 与 canonical `/tmp`，Windows 只使用 `os.TempDir()`；effective default read/write roots 无条件包含它。临时根内 FileTools 跳过路径、LLM 预审批和通用写审批，但保留写前读、并发校验、大小与 history。单条 `python`/`python3` 直接执行临时 `.py`，或单条 `node` 直接执行临时 `.js/.mjs/.cjs` 时，各 accessLevel 均按普通 allow 执行；内联代码、重定向、复合命令和其他 opaque command 不适用。bashsec hard block、readonly、KBASE mutation gate 与临时根 symlink/junction 逃逸仍优先。
- 新增能力优先放进对应 `internal/*` 模块，不在 server 层堆业务逻辑。
- TEAM 是内部专用 mode：公共机制进入 `internal/agent`，调度规则进入 `internal/agent/team`。普通 `AgentDefinition` 必须拒绝 `mode: TEAM`，隐藏协调器不得注册到 `/api/agents`、`/api/agent` 或普通 `agent_invoke` 目标中。
- 新增 API 保持统一 JSON 包裹、字段命名和错误语义。
- KBASE 对外 tool/REST/`source.publish` 契约以 LanceDB 路径回归；只有 `indexHash` 变化可触发新 generation，`queryHash` 中的 topK/RRF/权重/候选池调整不得引发全量重建。
- KBASE watcher 对所有 `kbaseConfig.enabled: true` 的 capability 使用路径级 change set 更新 active generation；启动、手工普通 refresh 与周期 reconcile 才做全目录对账，`force=true`、首次索引和 `indexHash` 变化才创建新 generation。
- 专用 KBASE 的 Workspace 始终是最终 canonical `runtimeConfig.workspaceRoot`，当前 Chat 目录只保存在 `ChatDir`；main/editing 两种 stage 固定提供相同的五个文件工具。KBASE editing 是 Workspace mutation 的 run 授权，不是 Agent 配置。它复用通用 `AccessPolicy -> AccessPlan -> HITL -> FileTools` 主链路；session 冻结的 `ScopedFilePolicy` 只负责固定工具准入、Workspace 识别、`WorkspaceMutationEnabled`、Workspace 已有文件先读后写和新文件父目录已存在，不覆盖 AccessPlan，也不限制文本扩展名或编码。`accessLevel`、hostAccess 与 HITL 按通用规则作用于 external，但不能替代 `editingMode:true`；固定工具集仍不可扩大。
- 测试以 `make test` / `go test ./...` 为主，协议变更优先覆盖 `internal/server`、`internal/stream`、`internal/llm`、`internal/tools`。

## 8. 开发流程

本地开发：

```bash
cp .env.example .env
./scripts/sync-local-builtins.sh
make audit-workspace-chat
make run
make test
```

首次本地运行、更新相邻 builtin 项目或执行 `make release` 前，先执行 `./scripts/sync-local-builtins.sh`；它每次在隔离工作目录中重新构建本机 `dbx`、`httpx`、Rust sidecar 和 `poppler-pdftotext` launcher/archive，并原子更新 `build/builtins/<host>/`。Poppler native runtime 是校验后重新打包的预编译 payload，不在 platform 中编译；`rg` 是唯一只校验复制的 vendor artifact。同步按各本地项目的 `VERSION` 生成临时 lock；Shell 与 PowerShell 在 cache 激活后使用同一正式 lock 状态机。schema v2 的组件 `version/commit/source` 是全平台目标 release，target 同名字段与 `path/sha256` 是该平台实际 release。精确 native host 上严格更高的干净版本经一次精确 `yes` 可抢占为新目标；其他平台的本地 VERSION/Git HEAD 匹配目标并验证成功后自动更新自己的 target。交叉构建只更新 cache，任何 runner 都不得写其他平台 SHA；同版本不同 commit/SHA、dirty、降级、checkout 不匹配或非交互 leader 均不回写。正式写 lock 前必须先将验证 archive 原子固化到相邻项目的稳定 `dist/<version>/`，同路径不同 SHA 必须拒绝；并发 lock 变化同样放弃写入。`--all` 仅为 canonical lock 声明的 Poppler 目标构建，当前为 darwin-arm64 与 windows-amd64，且正式 lock 仍只允许精确 host target 跟随。同步脚本不写 `release-local/`；`make run` / `make build-local` / `make release` 不得重新引入 builtin 或 Rust 构建步骤；运行和 release 只从本机 build cache 使用 builtin，并在 release 时重新校验 manifest 中所有 payload。

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`AGENTS.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `agents/` 与 `skills-center/` 是可编辑事实源；Agent 配置内 Skill、Terminal 与常规 Skill runtime 只使用 Platform 生成的 `ru-agents/`。该目录不提交、不打包、不允许人工编辑；Platform 启动时无条件清空并完整重建，Agent/Skill 热重载只更新稳定目录而不清空整个根。唯一的 query 运行时例外是普通 Agent 的非空 `mustUseSkills` 含未配置 Skill：必须从当前有效 skills-center catalog 重新验证；Host 直接开放真实 `@skills-center` 只读根，Container 去重后挂载整个 `/skills-center` 为只读。该例外不合并额外 Skill 的 `.config`、`.runtime-env.json`、`.bash-hooks`，不增加 Tool/MCP/access 权限；Team 明确拒绝。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和隐藏的 `GET /api/resource` 数据面。新 Markdown 的 Chat 文件只使用相对于当前 Chat 的 `<relativePath>`，也可引用普通 Agent Workspace 或冻结临时根内的实际 Host 绝对路径与 HTTP(S)/data/blob；Markdown 不使用 `@temp`。真实 `/api/resource` 请求地址和 `<currentChatId>/<relativePath>` 都不是 Markdown 协议，历史 endpoint Markdown 不迁移且不再预览。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- `platform_control` 对所有 Agent 使用同一固定 Schema；Agent 显式挂载 Tool 即获得全部注册 operation，Skill 与 `mustUseSkills` 不会替 Agent 挂载 Tool。动态 key 无需预声明，只有当前普通 native root run 成功 set 的 key 才能 unset；set value 是会进入会话、trace 与导出的普通 Tool 参数，不得承载 Secret。子 Agent、Team、ACP、Proxy、Channel、Terminal、MCP、LSP、sidecar 和已启动进程不继承。旧 `platform_config` 以及 `platform-control.profiles/bindings` 配置硬失败，遗留 `runtimeConfig.runEnv` 静默忽略。
- 文件工具权限独立于 Bash 权限，越权路径通过 HITL approval 兜底。
- `AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR` 与 `AP_ACCESS_TOKEN` 是 Platform 保留变量，agent、skill 和调用级 env 均不得覆盖。host bash/tool 与 Container Hub 使用前三者的 canonical 路径；Workspace Terminal 只使用 canonical `AP_AGENT_CONFIG_HOME` 与 `AP_WORKSPACE_DIR`，不注入 `AP_CHAT_DIR`。`AP_ACCESS_TOKEN` 仅在普通 Agent Host Bash 创建前从有效 identity 单行文件即时读取并注入，默认文件为 `<AP_RUNTIME_DIR>/identity/access-token`，显式 `--identity-file <absolute-path>` 优先，不进入 Terminal、Container、Proxy、ACP、MCP、LSP 或 sidecar。文件缺失、不可读、为空或非法时省略该变量，不缓存也不中断 Bash。
- 专用 KBASE 未开启 editing 时 Workspace 可读但不可 mutation，当前 Chat 目录仍按 `@chat` 可读写；开启后 Workspace mutation 在 shipped default policy 下免逐次 HITL。external 和其他 chatId 默认进入 HITL，`writeRoots`、hostAccess、`full_access` 或 approval 可按通用策略放宽；这些授权不能放宽非 editing KBASE Workspace，管理员显式 block 仍优先。Workspace mutation 不触发同步索引 hook，KBASE watcher 按 debounce 与 change set 异步刷新。
- MCP registry 同时支持 `streamable-http` 与 `stdio`，严格要求协商版本 `2025-11-25`。旧 external stdio 私有协议没有兼容期；`service.yml`、`type: external`、`external:` 或 `kind: external-service` 会使启动/热重载硬失败。平台、新版 stdio server 二进制和 registry 配置必须同批发布。
- `agent_invoke` 只允许显式配置的普通主 agent 使用，当前禁止嵌套；orchestrated Team 自动注入 session-local embedded builtin `agent_delegate` 和三个 plan tools。普通 Agent 配置、session 与执行入口均拒绝 `agent_delegate`，该工具也不进入公开工具 catalog。
- flat plan task 按数组顺序执行且同时最多一个 `in_progress`；最前面的非终态 task 可由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，`in_progress` 可进入任一终态，终态重试必须追加新 task。TEAM 的 plan task 表示顺序阶段，但当前阶段内部仍可通过单次 `agent_delegate` 按 `maxParallel` 并行执行成员。
- `run_query` / `run_status` / `run_interrupt` 只允许分别显式配置的普通主 Agent 根 run 使用，query 按精确 catalog `agentKey/teamId` 启动独立根 run；不设目标白名单、深度/并发配置或 maxActiveRuns。status/interrupt 只接受同一调用 Agent 与 subject 创建的 run，目标 run 禁止再次调用任一 run 工具。旧 `agent_run_query`、`agent_run_status`、`agent_run_interrupt` 已删除且配置引用会硬失败。
- chat 创建后 `teamId` 固定。Team 以 `teamId` 为公开 owner，`agentKey` 不得与 Team 请求或控制请求同时出现；隐藏协调器 key 只用于进程内执行，不得作为公共 Agent 身份回显。
- Team 成员、成员定义、协调器配置与 prompt 在 run 开始时解析为快照，运行中 catalog 热重载不改变该 run；下一次 run 才读取新快照。
- KBASE Lance sidecar 只监听 loopback，由 Go 生成一次性 Bearer token 并监督生命周期。存在 enabled KBASE capability 时会启动并探测 sidecar；`mode: KBASE` 将其标为 required，故障使健康检查失败，普通 Agent 附加能力将其标为 optional，故障只在 `/healthz` 和 capability 状态中报告 degraded。无 active generation 时 search 返回 stale 并触发冷建，sidecar 故障显式返回 unavailable，绝不回退旧 SQLite 文件。
- 当前 KBASE 只对文本抽取结果做 embedding/FTS；PDF/DOCX/PPTX/HTML 均是先抽取文本，不得宣称支持图片、音频或视频语义检索。
- SQLite runtime store 使用 `application_id`（库类型）和 `user_version`（schema 版本）作为身份契约。仅在 `app.New` 启动装配期，`chats.db`、`archive.db`、Memory SQLite 与 KBASE `control.db` 的标记恰为 `0/0`，且表、列语义、约束、索引、触发器和 FTS 对象完整匹配当前 DDL 时，服务才会在事务中写入当前标记；列物理顺序不影响比较。运行期仅验证，绝不认领、迁移、删除或修复。其他标记组合、结构差异或残留旧数据均拒绝；chat/archive/memory 会阻止启动，required KBASE capability 会隔离对应 Agent 并保留管理端诊断，引用它的 Team 同样不可运行；optional capability 保留普通 Agent 可运行并报告 degraded/unavailable。

## 特色功能文档索引

- [智能体配置说明](docs/智能体配置说明.md)：agent / team / skill 定义、CODER、KBASE、目录式 Team、prompt files、memoryConfig、runtimeConfig。
- [Agent运行时组装](docs/Agent运行时组装.md)：`ru-agents`、Agent 自有/技能中心 Skill 选择、`.config` 冲突与热重载。
- [配置化说明](docs/配置化说明.md)：环境变量、`configs/*.yml`、默认值、优先级、废弃变量。
- [工具目录权限](docs/工具目录权限.md)：Bash、FileTools、allowed paths、越权审批、读后写闭环。
- [真流式和H2A](docs/真流式和H2A.md)：SSE、heartbeat、`[DONE]`、attach、backlog、H2A 缓冲。
- [记忆系统](docs/记忆系统.md)：remember、SQLite memory、FTS、embedding、learn、consolidate、memory tools。
- [运行时和沙箱](docs/运行时和沙箱.md)：runtime 目录、Container Hub、mounts、host / sandbox 工具边界。
- [Platform控制工具设计](docs/Platform控制工具设计.md)：`platform_control` operation、显式工具挂载、run-local 环境快照、并发、脱敏、恢复与执行通道边界。
- [KBASE LanceDB 迁移](docs/KBASE-LanceDB迁移.md)：LanceDB sidecar、control.db、generation、加权 RRF、迁移验证、恢复、回滚与分发边界。
- [KBASE 编辑模式](docs/KBASE编辑模式.md)：`editingMode`、通用文本文件、AccessPolicy/HITL、watcher 异步索引和 KBASE Workspace/Chats 分离。
- [KBASE 编辑模式越权对抗测试报告](docs/KBASE编辑模式越权对抗测试报告.md)：准入、固定工具集、HITL、approval replay、路径逃逸、chat 隔离和索引 hook 的红队验证记录。
- [API与协议](docs/API与协议.md)：HTTP API 参数、SSE、WebSocket、HTTP 文件数据面、resource ticket。
- [HITL协议](docs/HITL协议.md)：question / approval / form、submit、awaiting 事件。
- [自动化](docs/自动化.md)：automation registry、orchestrator、dispatch、执行记录。
- [子智能体调度](docs/子智能体调度.md)：`agent_invoke`、TEAM 隐藏调度与 `run_query` / `run_status` / `run_interrupt` 独立根 run 控制。
- [MCP与工具交互](docs/MCP与工具交互.md)：统一 Tool、MCP registry、tool sync 与可选 viewport 交互元数据。
- [会话存储与回放](docs/会话存储与回放.md)：chat store、StepLine、raw messages、archive、search、resource。
- [鉴权与安全边界](docs/鉴权与安全边界.md)：JWT、JWKS、本地公钥、resource ticket、CORS、敏感配置。
- [版本化打包方案](docs/版本化打包方案.md)：README 索引的交付专题文档。
- [手工测试用例](docs/手工测试用例.md)：curl 回归用例。
