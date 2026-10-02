# AGENTS.md

## 1. 项目概览

`agent-platform` 是 `agent-platform` 的 Go 版运行时仓库，目标是在保持 Java runtime 接口风格与部署方式尽量一致的前提下，逐步形成可独立运行的 agent runtime。

当前仓库定位是“最小可运行闭环 + 特色能力持续补齐”：

- 已具备独立 HTTP 服务、统一 JSON 包裹与 `POST /api/query` 真流式 SSE。
- Agent 类型由 `mode` 与顶层 `engine` 共同表达：`GENERAL`（通用；YAML 与 API 输入不再接受 `REACT`，仅历史 Chat/Run 数据兼容读取）、`CODER`、`KBASE` 由内置引擎执行；`engine: acp` 配合 `runtimeConfig.acpBridgeId` 交给外部 ACP bridge，可以不写 mode。`engine` 缺省为 `native`，不由 `acpBridgeId` 推断。接口统一返回 `GENERAL` 和 `engine`；历史 Chat 的 `REACT` 不改写，筛选与响应按别名处理。`configs/general-settings.yml` 提供 GENERAL 创建默认值，并可选让项目型 GENERAL 读取 Workspace 下的 AGENTS.md（默认关闭），见 [智能体配置说明](docs/智能体配置说明.md#类型与执行引擎)。
- 新建项目型 Agent 通过 `configs/agent-creation.yml` 的能力模板组合技能、工具和连接器：`/api/admin/agents/creation-options` 返回四种创建类型（通用、编程、知识库、外部引擎）、各能力组的成员与实时可用性、默认模型和 ACP 引擎；`/api/admin/agents/create` 的 `capabilityGroups` 按组 key 展开并落盘为具体配置。模板只在创建时展开，不是持续绑定；字段缺省时不展开，出现时（含空数组）要求具体项目目录和可用模型，成员缺失、连接器互斥或对外部引擎提交能力组都在落盘前报错。`/api/admin/host/directories` 让远端客户端选择 Platform 宿主上的目录。见 [智能体配置说明](docs/智能体配置说明.md#创建模板)。
- Agent `interactionConfig` 统一控制模型、权限级别、必用技能、连接器、本地文件和聊天记录输入；GENERAL 默认全开，CODER（含 ACP）默认关闭聊天记录，KBASE 默认关闭模型/权限级别/连接器/聊天记录。仅 `/api/agent` 返回解析值，Run 恢复快照私存于 `.state/run-interactions`，不进入公开 query；普通 Agent query 与活动 Run 控制执行准入校验，详见 [智能体配置说明](docs/智能体配置说明.md#对话输入能力-interactionconfig)。
- 产物发布的 `artifact.published` push 按单个产物发送，只投递给已认证 Desktop Main；BTW、Explain 与其他 WS 不接收，attach/回放不重发。普通本地与代理实时发布统一转换，独立于网关与当前 Chat；`resource.pushed` 仅在实际上传网关成功后发送。
- 已有 Chat 的发现与回放按持久化记录读取，不以当前 Agent 有效为前提；无效或删除 Agent 仍不能续聊。`/api/agents?includeChats=...` 仅为有效目录预览，完整历史使用 `/api/chats`，WebClient 独立加载和 composer 禁用的配套边界见 [会话存储与回放](docs/会话存储与回放.md#历史读取与当前-agent-可用性)。
- 已具备 chat 摘要、事件流、raw messages、上传资源落盘、归档与搜索；自动上下文压缩在完整输入估算达到 90% 时执行不调用模型的 L1 `l1_tools`，统一处理 reasoning 与完整工具组，按模型窗口保护最近 5/7/10 轮完整模型调用（模型 YAML `l1KeepRecentRounds` 可覆盖为 5～10），未完成交互额外保护；L1 仅给原 JSON 行增加 `_compact:{level,id,keep?}`，keep 只允许 content/reasoning/tool，无 keep 整行退出上下文，不复制原文或新增 L1 行。L1 后仍达到 90% 才执行单次 LLM L2 `summary`，独立摘要插入保留记录之前，标记与插入原子提交。旧字符串标记与旧 checkpoint 兼容读取；L1 不使用 60% 目标，L2 的 60% 只是选材目标而非成功硬门槛。
- 已具备目录驱动的 agents / teams / skills / tools catalog，并在 Catalog 发布前将 Agent 定义、Agent 自有 Skill、技能中心 Skill 和 `.config` 组装到稳定的 `ru-agents/<agentKey>` 执行目录；query `mustUseSkills` 可在单次普通 Agent run 中强制使用额外技能中心 Skill，并为每个选中 Skill 目录建立 trusted read + readonly roots；Container 仍只读挂载整个技能中心，但 AccessPolicy 只免审读取选中目录，不复制、不生成 run-runtime。Admin Agent 支持安全校验完整 ZIP、以隐藏 staging/backup 原子导入或整目录覆盖；硬重载失败恢复旧来源，catalog 可发布但单个 Agent 无效时保留导入结果并返回诊断。Market 技能包由 Platform 原子安装到 `skills-center/<package>`，package.json 必填 name 与 skills 对象数组（成员只存 id），成员展示信息读取各自 SKILL.md；包信息编辑统一使用 /api/admin/source 的 skill-package target。包内技能 ID 为 package/skill，和顶层同名独立技能互不覆盖，临时 ZIP 不持久化。
- 已具备独立 `OPENAI_RESPONSES` 原生模型协议，使用本地有效历史与 `store:false`；有 Chat 上下文时自动发送由 Chat ID 的 SHA-256 前缀派生的 `prompt_cache_key`，跨 Run/恢复保持一致，不在 JSONL 另存。每次接受的模型调用在 react 行保存可选 `responseId`，加密推理保存在 assistant `reasoning_content`，react-tool 不重复 ID。工具/HITL、压缩、文本与视觉辅助调用复用原链路，未接入上游托管工具，见 [Responses协议](docs/Responses协议.md)。
- 已具备 OpenAI / Anthropic 协议模型调用、统一 Tool、Container Hub sandbox 与 tools；`image_generate` 以统一参数支持文生图、最多四张本地/Chat 参考图的图生图，以及模型 YAML 显式声明的原生 mask/inpainting，生成和编辑请求分别由模型 YAML 的 `image.generation`、`image.edit` 协议块适配。两个协议块可独立配置 `omitResponseFormat`（默认 false）以省略 Images 请求的 `response_format`，成功结果报告实际返回格式。
- 连接器配置完成状态保存在 `.state/connectors/<id>/connection.json` 的 `configured`，没有独立启用开关，缺文件默认未配置；内置仅表示随包分发；显式声明 `auth_mode: "no_auth"` 的连接器无需配置，不读取或写入 connection.json，Agent 挂载后即可调用。配置列表读取本地授权快照，Token 提交与显式 `/api/connectors/check` 执行验证；暂时无法验证的 Token 保存到私有待验证文件，保留已有凭据，检查成功后替换。退出只清理本地授权，保留 CLI 私有设置/数据，取消登录和凭据检查，已开始业务调用允许结束；Host Bash 原有 Agent 范围凭据注入、Terminal 和连接器技能禁止 mustUseSkills 保持不变。详见 [连接器安装与授权](docs/连接器安装与授权.md)。
- CLI 连接器导入后独立异步准备：包有 bin 时跳过 init 并只校验包内入口；无 bin 时原样执行对应 OS 的 init，再检查 versionCheck。Host 自动补充 npm 全局命令 PATH，不改 npm prefix 或安装版本；准备状态在 `.state/connectors/<id>/preparation.json`，登录不再隐式安装，configEnv 继续为挂载 Agent/Terminal 注入独立凭据目录。准备与来源 mutation 使用跨进程锁互斥；旧启动器包需覆盖升级，Container npm 自动探测未实现。见 [连接器安装与授权](docs/连接器安装与授权.md)。
- 模型 HTTP 与流内错误按结构化 code/type 精确映射统一平台错误码，不按上游 message 英文关键词分类；公开 status 表达平台错误语义，diagnostics.upstreamStatus 保留实际 HTTP 状态，限流、额度与鉴权等复用对应重试属性，见 [Responses协议](docs/Responses协议.md#流错误诊断)。
- 模型空响应与传输/解析失败分别输出常开结构化诊断；可选 LLM trace 按每次尝试独立留档，原路径保留最近尝试，并保存前 16/后 48 帧的有界事件结构、处理结果、结束方式、响应元数据与正文/思考计数，空响应标记 `empty_response`；输出受限、过滤/拒答与其他空响应分别返回明确中文提示和非重试错误终态，保留已生成内容，输出受限不执行本轮工具，见 [模型空响应排查](docs/配置化说明.md#模型空响应排查)。
- Native 模型流式正文与推理各自达到 4,000 Unicode 字符后启用精确尾部复读检测；命中取消当前请求，以 `model_output_repetition` 非重试错误收口并丢弃未提交轮次，已执行工具不回滚。阈值与范围见 [配置化说明](docs/配置化说明.md#流式复读取消)。
- 统一工具生命周期支持 live-only `tool.output`（每次调用 `0..N` 条，最终仍由唯一 `tool.result` 收口）；当前仅 Native Host Bash 通过普通 pipe 接收 stdout/stderr，以 tee 临时文件作为最终事实源并按 50ms 周期发送原始控制流，Container Hub Bash、Chat JSONL 与冷回放不保存过程输出。活动异步工具的取消先在整批共享 2 秒期限内收集真实返回；超时或仅返回取消错误时显式记录副作用未知，只有确定未启动的调用才标记 `executed:false`，工具结果持久化先于 Run 终态。
- 已具备由 `build/builtins/<os>-<arch>/` cache 固定、校验并随服务包分发的 Host builtins（rg/dbx/httpx/kbase-lance-engine/poppler-pdftotext）；`file_grep/file_glob` 稳定包装 rg，dbx/httpx 已成为 `builtin.dbx/builtin.httpx`，dbx/httpx 清单与完整技能源码位于相邻 `agent-platform-connectors/{dbx,httpx}/connector/`，完整包（清单、bin/libs、skills）随 Platform 分发，按 Agent 挂载组装到 ru-connectors/<id>/<contentDigest>，不可修改或删除；`runtime/connectors-center` 只提供外部原包，遗留 builtin 副本忽略，Agent 挂载后增加本 Agent 运行包 PATH 并导入技能元数据，skillId 保留原始技能名、不加连接器前缀，同一 Agent 内重名报冲突，技能正文随完整包复制，不重复放入 Agent 的普通 skills 目录；通用持久化运行状态根为 `.state/`，连接器授权与受管 CLI 状态归入 `.state/connectors/<id>`，连接器技能禁止 mustUseSkills，KBASE PDF 默认调用 Poppler `pdftotext` launcher。
- steer 支持 `selection.text` 纯文本选区（不要求视觉模型），以及通过 `/api/upload` 上传后以 `references` 注入当前 native Agent/Team 协调器的图片与普通文件（含 HTML/MD），允许纯附件及混合附件；视觉模型准入冻结图片输入，非视觉模型的图片和普通文件为经校验的工具读取引用，公开事件仅带引用，JSONL 仅保存 message/references，续聊按当前模型能力重建附件输入（图片不保留历史版本，失效附件提示不可用），旧 messages 快照兼容读取。同一主 Chat 的首次 query 要求非空正文，后续 query 可只带有效文件或选区引用，仅最后一次主 Run 明确异常结束或取消后允许完全空白表示继续；空 query 的 message 保持为空，仅模型输入补充英文继续指令，steer 同样接受完全空白，表示继续（公开 message 为空，模型输入补充独立的“立即进行下一步”指令）；远端附件路径尚不支持。
- 活动 native CODER planning 的阶段切换保留 steer；生成候选计划或等待确认期间的新指令使旧计划失效，并在同一 Run 中重新规划。旧确认与 steer 原子仲裁，已失效计划不能被迟到的 approve 执行；失效事件和工具结果进入回放，跨进程 suspended 等待仍通过 submit 恢复。见 [HITL协议](docs/HITL协议.md)。
- 已具备 HITL question / approval / form、运行中 submit / steer / interrupt 协议入口，以及 question/planning 跨进程恢复和不可恢复等待项的幂等终态对账；活动 Run 保留收尾权，恢复 claim 失败不退回补写，无执行者的补写按等待项串行并重新读取持久化状态。Host Bash builtin 审批准备与启动分离，单次/本轮人工批准后均可恢复符合条件的并发；一次性授权绑定 toolID，不随执行上下文复制给兄弟调用，写入与控制操作屏障仍保持顺序。同一 Host Bash 的 access/security 与 builtin 技能 hook 在首次审批合并冻结，批准后新增未授权 hook 按要求变化收口，不复用已回答的等待项，见 [Bash审批卡住排查](docs/Bash审批卡住排查.md)。
- 已具备固定 Schema 的 `platform_control` system control plane：Agent 显式挂载 Tool 即可调用全部注册 operation；`run.env.*` 仅保留当前普通 native root run 的 `set/unset`，使用进程内并发 Scope、operation-aware barrier 和 Host/Container 新 command snapshot，不修改 Platform 进程环境，也不跨 Platform 重启恢复。遗留 `runtimeConfig.runEnv` 静默忽略。 `chat.set_pinned` 为普通 native root Run 提供当前/指定 Chat 的实例级持久置顶，复用 conversation 服务及 `chats.order.changed` 广播；planning、子任务与 Team 不开放。
- 已具备默认关闭的 SQLite memory、FTS 文本检索、显式记录与手工 consolidate。Memory embedding、learn/自动反馈和上下文预览已退役；KBASE 能力独立保留。
- 已具备可由普通 Agent 挂载、并保留专用 `mode: KBASE` 预设的 KBASE 文本知识库公共能力，包括 LanceDB generation 检索、加权 RRF、目录增量 watcher 与本地 Rust sidecar 管理；SQLite `control.db` 只负责 generation、文件状态与恢复日志。
- 已具备以 `runtimeConfig.workspaceRoot` 为唯一内容根的 KBASE 公共能力；专用 `mode: KBASE` 与其他内置类型一样合并 Platform 预置工具与 `agent.yml` 声明的工具，并使用声明的技能、连接器和 memory（没有固定工具集，新建时仅写入未被 Platform 预置覆盖的文件工具），main/editing 两种 stage 工具相同，当前 Chat 目录独立可写；单 run `editingMode` 只控制 KBASE Workspace mutation，写入与索引解耦，由 KBASE 目录 watcher 异步维护。
- 已具备 automation、`agent_invoke` 子智能体调度、`run_query` / `run_status` / `run_interrupt` 独立 Agent/Team 根 run 启动与控制、带隐藏协调器的 orchestrated Team、基于官方 Go SDK v1.6.1 的 MCP streamable HTTP/stdio session client 与后台 tool sync、WebSocket 控制面，以及 client/server channel 上按 Session 执行的 Agent 接出注册 v1（`agent.list/register/unregister`）；MCP 本地 Registry 同步校验，远端初始化/发现/重试不进入启动、保存或 watcher 关键路径，优先请求 `2025-11-25`，兼容 SDK 支持的 `2025-06-18`、`2025-03-26` 和 `2024-11-05`。Channel 注册当前不升级 Query Stream、Run TTL、`registrationId` 路由、HITL Schema 或控制协议。
尚未完全对齐 Java 版的部分能力包括 MCP 全量生产验证、automation 深度编排、热重载细节和更完整的客户端协议适配。未落地能力必须在专题文档中明确标注，不能写成已完成能力。

技能展示元数据已支持顶层 `displayName`（非空时优先）及 `metadata.displayName/i18n/version/revision`，API 名称字段仅返回 `id/displayName`，按请求语言解析显示名称与描述，无显示名称时回退 SKILL.md 的 name，不返回 name 或翻译表；id/name 不一致只告警不阻断；版本保留顶层优先兼容规则，名称与版本诊断不改写技能，客户端刷新接入见 [技能展示元数据](docs/技能展示元数据.md)。

`contextConfig.agents` 为非授权的候选摘要引用：不可用项跳过，正常主 Agent 保持可用，管理接口与运行日志提供有界 `context_agents_unavailable` 警告，实际调用与必要执行依赖仍严格校验，见 [智能体配置说明](docs/智能体配置说明.md#context-tags)。Agent 加载不再由 Desktop 工具名强制推导 `builtin.desktop` 声明；执行时的受信任挂载与权限检查保留，见 [Desktop 连接器](docs/连接器共享包与Desktop迁移.md#builtindesktop)。

Native Agent 通过 preset-tools 或显式声明挂载 `wait`：offset 优先于 base，时间为必填上限，可选多事件 any/all，同 Run steer 唤醒，空白 steer 表示继续并可提前结束等待（按普通 steer 入队与持久化，正文为空，无专用 skip 接口）；tool.wait/update 支持 WebClient 倒计时及 attach 快照。等待暂停执行预算、受 lifetimeTimeout 约束，普通根 Run 使用 awaiting 恢复，子任务/Team/非空 run.env 不跨重启恢复。见 [原生等待工具](docs/原生等待工具.md)。

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
  -> runtime/runstate + runtime/query + runtime/proxy
  -> server.New()
```

核心模块边界：

- `internal/connectorops` 提供调用方中立的 CLI/MCP 执行、连接器/adapter 短期授权和可选持久幂等收据；不持有 WebApp、appId、Chat 或页面生命周期模型，不注册业务 operation/profile，复用 `internal/connectorauth` 的部署级凭据。可信本地身份签发执行授权，HTTP 接入见 [连接器执行协议](docs/连接器执行协议.md)。`internal/chatresource` 通过独立 Chat API 读取发布产物，按已有 principal/Chat 权限校验，不借用连接器授权。

- `internal/agent`：中立 mode 契约、公共 prompt 模板变量与 system-init spec；`internal/agent/builtin` 是 CODER/KBASE/TEAM 的静态分派点。
- `internal/agent/general`：GENERAL 类型的创建默认值与项目规则文件读取策略；没有固定工具集、prompt 或 stage，不进入 builtin descriptor。
- `internal/agent/coder`：CODER profile、prompt、planning、ACP/workspace 策略与创建默认策略。
- `internal/agent/kbase`：专用 `mode: KBASE` 的 profile、prompt、system-init 与创建默认值（含创建时写入的工具清单）；没有固定工具或 memory 边界。
- `internal/agentcreation`：mode 中立的创建模板展开，按 `configs/agent-creation.yml` 把所选能力组合并去重为具体工具、技能和连接器，并校验成员存在与连接器互斥；不依赖 catalog 或 server。
- `internal/kbase`：mode 中立的 KBASE 公共能力；`Manager` 只作为公开门面和组件装配点，内部由 capability resolver/state、storage validator/auditor、watch/lifecycle supervisor、refresh coordinator、generation service、query/status/files service 与 Lance runtime 分别维护配置解析、存储契约、调度、索引/恢复、检索和 sidecar 生命周期。app adapter 只向 Manager 暴露 enabled capability，`AgentSpec.WorkspaceRoot` 是唯一内容根事实；未启用与不存在统一按 not found 处理。该包同时维护公共 prompt、HTTP 业务错误与五个工具 handler；不得 import `internal/agent` 或 `internal/catalog`。
- `internal/agent/team`：内部 TEAM profile、硬编码调度规则、成员 roster prompt、session-local 隐藏工具与调度状态机；TEAM 不能配置成普通 agent。
- `internal/runtime`：HTTP/WS 无关的 Query 与 Run 应用运行时；`types` 保存内部命令和结果，`query` 实现普通/旁聊 Query 准入、根 Run 注册/控制、Native 阻塞与异步启动、continuation 仲裁及重启 awaiting 对账，`session` 统一构造根/子 Agent/Team 的执行上下文和 system-init，`catalogview/reference` 承接租约快照与引用物化；`runstate` 持有活动 Run、observer、compact 协调与恢复等待项的唯一内存存储实现，`runexec` 执行 Native 生命周期、usage/终态落盘和 freeze 收尾，`orchestration` 执行子 Agent/Team 调度与结果回注。App 直接组装以上组件，不再反向注入 Server Native 方法。`adapter` 仅适配旧执行器/catalog DTO；根 Proxy 的 SSE/WS/channel 驱动仍通过显式 ProxyPort 保留在 Server，生命周期全面统一归 R18，不能写成已完成。Runtime 不得依赖 `internal/server`；边界与集成注意见 [Runtime模块边界](docs/Runtime模块边界.md)。
- `internal/runops`：显式挂载的 `run_query` / `run_status` / `run_interrupt` named handler、调用方/subject 所有权、父 run/tool ID 幂等与禁止链式调用；直接依赖 `internal/runtime` 的窄接口，不经过 Server。
- `internal/platformcontrol` 与 `internal/runenv`：统一 system control operation registry/handler，以及当前普通 native root run 的进程内并发 Scope、revision、limits 与幂等状态。
- `internal/server`：HTTP/WS 解码、鉴权、响应映射、SSE flush 和迁移期薄适配；不得直接依赖 `llm`、`tools` 或具体 Agent mode。
- `internal/conversation`、`internal/adminsource`、`internal/chatresource`：分别承接会话/归档编排、管理端源码 mutation 并发事务、Chat 资源解析与 mutation 边界。
- `internal/llm`：prompt 构建、run stream、HITL、planning、tool loop；Provider HTTP 打开、首响应超时和响应分类由 `internal/modelclient` 承接。
- `internal/tools`：通用 tool registry/router、Bash、FileTools、memory、desktop、MCP tool 调用；mode 工具通过命名 handler 接入，不在 executor 中增加 mode switch。
- `internal/chat`：chat 摘要、事件、StepLine、raw messages、资源文件、归档、回放。
- `internal/memory`：SQLite memory、FTS 文本检索、上下文召回与显式生命周期整理。
- `internal/view`：VIEW 展示定义、声明资源、远端模板获取和 Chat 内容寻址快照；无 Tool 执行或 HITL 决策职责。VIEW 与 MCP/CLI 组件可组合，纯 VIEW 不授予 Bash/PATH。
- `internal/connector`：中立连接器包/JSON/技能与 assets 图标结构校验、ZIP 原子导入、Agent PATH 合并与定义编辑；`internal/connectormigrate` 是旧 MCP 目录和 Agent 引用的显式离线迁移入口。MCP 通过统一 Sources 读取 Platform 内置包和 runtime/connectors-center 外部原包，执行读取 Agent 挂载引用指向的 ru-connectors/<id>/<contentDigest>，MCP 按 Agent/连接器/组件/内容版本建立独立实例，旧 registries/mcp-servers 目录直接忽略；`internal/connectorauth` 负责部署级 token 保存/退出、null 模式显式受管 CLI 准备/扫码、普通 OAuth 授权码与 MCP OAuth 发现、PKCE、loopback 回调和持久化刷新；oneid-token 复用 Desktop identity-file，按调用环境注入 AP_ACCESS_TOKEN，HTTP MCP 按同一来源生成 Bearer Header，不复制 SSO 凭据到连接器状态目录。auth_bindings 声明包外凭证的 HTTP/Host CLI/stdio 消费映射；MCP 支持多资源 grant、客户端注册信息、元数据回退及追加授权。认证状态按秒驱动 MCP Registry 更新，不触碰包文件或重建 Agent。通用执行按连接器/adapter 授权，直接传 argv 或 MCP 原生工具参数，不注册业务 operation/profile；通用执行与 Agent/管理接口共用当前部署的连接器凭据，不提供多租户凭据隔离；通用 runtime 安装尚未实现，见连接器专题。
- `internal/catalog`：agent / team / skill / tool 目录装载与定义解析；Team 只接受目录式 orchestrated 定义，并以原子快照冻结成员、协调器配置和 prompt。
- `internal/config`：环境变量、YAML、默认值。
- `internal/httpclient`：Platform 出站 HTTP 客户端工厂、显式/环境/系统固定代理解析与缓存刷新；内部服务使用直连客户端，不修改标准库全局 Transport 或进程环境；默认 `auto` 在 Windows/macOS 上跳过 PAC/WPAD 并在无适用固定代理时直连；显式 `pac_auto` 启用 Windows WinHTTP PAC/WPAD（企业网络待目标系统验证），仅无显式 PAC 的 WPAD 发现返回 12180 时按无代理直连；解析失败标记 `proxy=unresolved`，DNS 与 Windows 网络错误提供脱敏分类。macOS PAC/WPAD 尚未实现，`pac_auto` 命中不支持的自动代理时报错。
- `internal/stream`：统一事件、dispatcher、assembler、normalizer 与 EventBus；SSE writer 属于 `internal/server` 传输层。
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
│   ├── runtime/                 # Query/Run 门面、状态、执行、编排与 Proxy
│   ├── runops/                  # 独立 run 工具组 handler、所有权与幂等
│   ├── conversation/            # Chat/Archive/Compact 应用服务
│   ├── adminsource/             # Admin source mutation 事务边界
│   ├── chatresource/            # Chat 资源应用服务
│   ├── modelclient/             # Provider 协议 HTTP 客户端
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
- `chat-order.json` / `chat-pinned.json`：实例级 recent/manual 展示排序与独立跨 mode 置顶顺序，不修改 Chat 内容时间或数据库 schema。
- `<chatId>.jsonl`：运行事件、StepLine、system init 与 raw messages。
- `<chatId>/<uploaded-or-generated-file>`：上传与图片生成资源；工具返回内部绝对 `path` 和相对于当前 Chat 的稳定 `url`（不含 `chatId`），用户可见内容只使用 `url`。
- `<chatId>/artifacts/<runId>/<filename>`：`artifact_publish` 的发布副本；发布结果 URL 必须指向该副本。

Automation 定义目录中的 `executions.db` 是 schema V2 的旁路执行历史库。已知旧版在后台创建一致性备份后重建为空 V2，不迁移旧行；History 初始化、备份和写入失败不得阻止 Platform、Automation 调度或 Query/Run。`AUTOMATION_EXECUTIONS` 保存触发快照、`chatId/runId`、真实 `finishReason` 和完整助手结果，列表只读取摘要，详情按需读取全文。

Memory 默认由 `AP_RUNTIME_MEMORY_DIR` 控制，当前固定使用 SQLite store，支持 FTS 文本检索、observation / fact 显式生命周期治理与 memory tools。旧 embedding 列和历史记录保留，运行时不再使用向量、learn 或自动反馈。

KBASE 默认由 `AP_RUNTIME_KBASE_DIR` 控制，每个 agent storageDir 可包含：

- `control.db`：schema v4 控制面，记录 generation、文件状态、file operation、增量 refresh 指标和 index run；不保存 chunk、FTS 或 embedding。control 与 Lance schema 版本独立；SQLite 控制面只接受当前 schema，绝不原地迁移。
- `generations/<generationId>/lance/`：LanceDB chunks table 及索引；同级 `manifest.json` 保存 generation 元数据。

核心 DTO 位于 `internal/api`，包括 query、submit、steer、interrupt、chat、upload、automation、memory console 等请求和响应类型。

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

- 用户目录置顶：`/api/skills` 与 `/api/connectors/order` 支持 HTTP GET/PUT 和 WebSocket；共用 `internal/catalogorder` 用户隔离与原子落盘，分别保存到 `skills-center/order.json`、`connectors-center/order.json`。同一用户全部 Agent 共用各自有序置顶列表，更新单个 `{id,pinned}`，不触发 catalog/runtime 重载，不修改连接器配置或授权状态。
- Catalog：`/api/agents`、HTTP-only `/api/agents/order`、`/api/agent`、`/api/skills`、`/api/teams`、`/api/admin/agents/creation-options`、`/api/admin/host/directories`、`/api/admin/skills`、`/api/admin/skill-packages/*`、`/api/admin/tools`、`/api/connectors`、`/api/admin/connectors`、`/api/admin/connectors/detail`；`/api/skills` 同时支持 HTTP 与 WebSocket，返回全局有效技能中心目录和用户级 `pinned`；可选 `agentKey` 仅用于计算 `configured`，不筛选目录，不传时均为 false。
- 外部连接器删除：`DELETE /api/admin/connectors/detail?id=<id>` 与 Agent mutation 及连接器导入/编辑串行，检查当前源码引用和保留的运行挂载；占用返回 409 和 `data.agentKeys`，内置包 403。删除以隐藏 staging 支持重载失败回滚；授权与 CLI 状态保留在 `.state/connectors/<id>`。
- 标准连接器执行：`/api/connectors/execution/grants`、`/api/connectors/execution/{list,describe,invoke}` 与独立凭据入口 `/api/connectors/auth`；旧专用传输返回 410，协议升级和原收据命名空间保留要求见 [连接器执行协议](docs/连接器执行协议.md)。
- Chat：`/api/chats`、`/api/chats/order`、`/api/chat`、`/api/chats/search`、`/api/read`、`/api/chat/export`、`/api/chat/artifacts/{list,get,read}`。产物接口始终要求 JWT 并检查现有 Chat 引用权限。Chat order 支持 HTTP/WS `set_mode/move/set_pinned`，同实例跨 mode 共用置顶组；列表 `pinned` 与 catalog 附带 Chat 的 `chatsPinned` 在 limit/includeChats 之前筛选，归档/删除清理置顶，恢复不继承。
- Archive：`/api/archives`、`/api/archive`、`/api/archives/search`。
- Run：`/api/query`、`/api/btw`、`/api/attach`、`/api/submit`、`/api/steer`、`/api/interrupt`。Desktop 的普通 `/ws` 同时支持`main`、`btw`、`explain` 三条 lane（source 分别为 `desktop-main`、`desktop-btw`、`desktop-explain`）；三者统一用 WS `/api/query`，按已认证连接身份分流为普通或隐藏旁聊执行。main 是默认 Desktop target 并接收全局 Push，btw/explain 不更新主 Chat 历史、摘要或未读；HTTP 统一使用 `/api/query`，body `lane` 默认 `main`、支持 `btw`，`explain` 仅限 Desktop WS；旧 HTTP `/api/btw` 保留兼容，网页仍使用 SSE。Run 网络来源为 HTTP 或 WS，无网络来源时 transport 为空；run_query 继承可信父 Run 的连接归属，runOrigin 单独表达派生关系。除 Submit 外，Run 控制仍校验 transport 及 WS 身份/device/lane；Submit 支持跨设备和 HTTP/WS 提交，保留既有认证、owner 和审批校验。归属保存在 `.state/run-controls`，恢复与 planning 执行续跑继承，旧 Run 缺归属拒绝。每条 WS 只允许一个 Run stream，切换前 detach，三条 lane 可并行。
- Memory：memory console 的记录、scope 与历史接口；`/api/learn` 和 `/api/memory/context-preview` 已删除。
- KBASE：`/api/kbase/{agentKey}/status`、`/api/kbase/{agentKey}/refresh` 以及五个 KBASE tools。
- Project Git：独立 HTTP `GET /api/project/git?agentKey=...` 按实际 Workspace 读取分支/游离 HEAD/非 Git/无目录/不可用状态，不按 mode 筛选；依赖宿主 Git，只读且限时，不进入 `/api/agents` 或 `/api/agent`，不改变 `expectedBranch` 约束。
- Project Git 分支操作：HTTP `GET/POST /api/project/git/branches` 按需列本地分支、切换或新建并切换，校验 revision；只允许完整且不包含 ChatsRoot 的 worktree，保留 Git 改动保护，不 force/stash/clean、不执行 hooks，不修改 `expectedBranch` 配置。
- Project / Resource：`/api/project/tree`、`/api/project/changes`、`/api/project/diff`、`/api/upload`、`/api/resource`、`/api/resource/image/commit`。Project 只读接口只接受 CODER/KBASE 的 Workspace 相对 POSIX 路径，复用 file-history 作为 Run Diff 基线；图片 commit 只修改 active Chat 的 Artifact/Reference 资源域。
- View / WebSocket：`/api/view`、旧兼容 `/api/viewport`、`/ws`。

详细协议拆分到专题文档：REST / SSE / WebSocket 见 [API与协议](docs/API与协议.md)，真流式与 attach 见 [真流式和H2A](docs/真流式和H2A.md)，HITL 见 [HITL协议](docs/HITL协议.md)。

## 7. 开发要点

- 通用运行时配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准；KBASE capability 的配置、索引/检索默认值和工具名以 `internal/kbase` 为准，专用 KBASE mode 的 profile、prompt、创建策略和边界以 `internal/agent/kbase` 为准；CODER/TEAM 规则分别以 `internal/agent/coder`、`internal/agent/team` 为准，文档只解释和引用。
- Runtime 目录只接受 `.env` allowlist：`AP_RUNTIME_DIR` 与 `AP_RUNTIME_REGISTRIES_DIR`、`AP_RUNTIME_CHATS_DIR`、`AP_RUNTIME_MEMORY_DIR`、`AP_RUNTIME_KBASE_DIR`、`AP_RUNTIME_PAN_DIR`、`AP_RUNTIME_STATE_DIR`。`AP_RUNTIME_STATE_DIR` 为空时使用 `<AP_RUNTIME_DIR>/.state`；其他子目录固定从 runtime 根派生，`configs/runtime.yml` 的整个 `paths` 节（包括旧迁移来源键）出现即报错。连接器管理命令仅用 `--runtime-dir` 选择部署，状态目录复用 `AP_RUNTIME_STATE_DIR`，不提供其他目录参数。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem`、真实 token 和私钥不得提交。
- 工具运行时配置以 `configs/tools.yml` 为外部事实源，包含 access policy、bash 和 file tools。全平台发布共用 `configs/tools.example.yml`；工具权限支持 `@root`（Unix/macOS 根目录、Windows 当前驱动器根），full_access 默认引用它。未显式配置 Bash 命令列表时使用平台默认值。
- `configs/tools.yml` 中的旧 YAML 路径策略键（如 `bash.allowed-paths`、`file-tools.allowed-read-paths`）会在启动阶段硬失败；Go 配置结构中的旧路径字段也已删除，目录权限统一走 `tools.access-policy`。
- `@temp` 仍为进程启动时冻结的通用临时读写根，不豁免脚本执行。本 Run 经 `file_write/file_edit` 写入当前 Chat 目录、由系统解释器执行且内容未变的脚本三档免执行审批；其他脚本 default 按调用、内容版本和环境审批，auto_approve 自动审计，full_access 允许。每 Run 私有临时目录尚未实现。Workspace 写入由解析后的 editing 能力决定（GENERAL/CODER 默认可编辑、KBASE 默认只读），editing=false 时 Workspace 写入与在 Workspace 内运行无法分析的程序均硬拒绝。readonly、临时根逃逸检查优先，详见 [工具目录权限](docs/工具目录权限.md)。
- 连接器挂载授予冻结入口的 CLI 执行权，仍核验 canonical 路径与 SHA-256；连接器业务 hook 不豁免。普通 Host Shell 不注入身份/连接器凭据，已验证的单条直接 CLI 以独立子进程接收本连接器凭据和配置目录；需要凭据的复合、包装或重定向调用须拆分。外围 Shell 和其他资源要求独立审查。
- AccessPolicy 写判定必须在 writeRoots、hostAccess 与 HITL 之前检查当前 level readonly roots 和 trusted run readonly roots；命中 readonly 后直接 block，exact/rule approval 不得放宽。`mustUseSkills` 的 run roots 只覆盖本次选中 Skill 的 canonical 目录，未选中兄弟目录不继承。
- `internal/skillsexec` 为 Agent YAML 已配置普通 Skill 和本次 `mustUseSkills` 选中 Skill 的 `scripts/**` 保存独立的本 Run 内存执行凭据，绑定 Agent/Run/执行环境、严格现存 canonical 路径和实际字节 SHA-256。复用通用解释器与脚本入口识别，仅免对应入口 opaque 审批（`bash-access:skill-script`）；Host 启动前复验，Container 使用选中技能 Host–guest 映射与容器摘要。内容不匹配撤销，不落盘、不复制技能、不跨 Run/子调用继承，同 Run 压缩保留，恢复同 Run 不重建凭据。外围 Shell、hard block、readonly 与 KBASE mutation gate 不变；Host 不隔离脚本内部访问，入口摘要不锁定完整依赖图。
- 脚本入口策略对所有 Agent 相同，bootstrap 没有特权。`scriptstate` 自写证明只在当前 Chat 目录内取消执行审批；可信技能入口仍按 `skillsexec` 核验。命令语义统一由 `internal/shellanalysis` 描述（选项、操作数角色、递归、删除、远端修改、Git 子命令与配置依赖），`bash -c`/`env -S` 递归分析；破坏性操作、Git 可执行配置写入与远端修改分别由 `approvals.destructive/executable-config/remote-mutation` 决定。批准绑定精确调用、canonical cwd、环境、access level 和内容 SHA-256；普通路径规则不扩到父目录。复杂 Shell 与远端 mutation 仅单次批准。Host 启动前重新核对，Container 使用实际 guest 摘要；不隔离任意代码内部访问。
- Catalog 按资源根保留独立 watcher（重叠根合并），事件统一分类排队并串行 reload。技能/连接器 ZIP 在发布保护区外解压校验，区内重新检查当前状态；快照和普通保存不暂停监听，目录 mutation 只暂停对应根。API 与 watcher 通过加载前后一致的内容指纹去重，恢复监听只做对应类别差异检查，不无条件全量 reload；失败及加载期间变化不确认新状态。现有 skills→agents/ru-agents 组装和活动租约保护保留。管理入口保护不覆盖 Bash/外部编辑器直接写盘，详见 [Agent运行时组装](docs/Agent运行时组装.md#技能包事务与目录监听)。
- 新增能力优先放进对应 `internal/*` 模块，不在 server 层堆业务逻辑。
- TEAM 是内部专用 mode：公共机制进入 `internal/agent`，调度规则进入 `internal/agent/team`。普通 `AgentDefinition` 必须拒绝 `mode: TEAM`，隐藏协调器不得注册到 `/api/agents`、`/api/agent` 或普通 `agent_invoke` 目标中。
- 新增 API 保持统一 JSON 包裹、字段命名和错误语义。
- AWCP 遵循网站手册渐进披露：固定 `desktop_cdp` 方法 `AWCP.getManual` 返回目录/章节说明，章节请求携带 `{section,revision}`，页面通过 `surfaceId` 在 Run grant 内选择，通用 `AWCP.invoke` 接收 `{revision,action,args}`。网站说明只作为工具结果，`internal/llm` 不得加入 AWCP 专属状态、动态 Schema、纠错预算或调度分支；授权页面与业务校验留在工具/Desktop/网站边界。详见 [MCP与工具交互](docs/MCP与工具交互.md)。
- Desktop 普通 Action 白名单跟随 `desktop/src/shared/desktop-actions.ts`，排除仅限 WebApp page 的动作；相邻仓库存在时工具测试直接核对上游定义，CI 可通过 `DESKTOP_SOURCE` 指定 checkout，见 [MCP与工具交互](docs/MCP与工具交互.md)。
- 连接器包版本由资源发布方维护；Platform 不根据来源市场或重新打包动作推断版本，不用 CLI 或 Skill 版本替代连接器版本。具体服务适配应留在连接器资源包，项目文档只描述通用契约。
- KBASE 对外 tool/REST/`source.publish` 契约以 LanceDB 路径回归；只有 `indexHash` 变化可触发新 generation，`queryHash` 中的 topK/RRF/权重/候选池调整不得引发全量重建。
- KBASE watcher 对所有 `kbaseConfig.enabled: true` 的 capability 使用路径级 change set 更新 active generation；启动、手工普通 refresh 与周期 reconcile 才做全目录对账，`force=true`、首次索引和 `indexHash` 变化才创建新 generation。
- 专用 KBASE 的 Workspace 始终是最终 canonical `runtimeConfig.workspaceRoot`，当前 Chat 目录只保存在 `ChatDir`；main/editing 两种 stage 使用 `agent.yml` 声明的同一组工具，没有固定工具集。KBASE editing 是 Workspace mutation 的 run 授权，不是 Agent 配置。它复用通用 `AccessPolicy -> AccessPlan -> HITL -> FileTools` 主链路；session 冻结的 `ScopedFilePolicy` 只负责会话工具准入、Workspace 识别、`WorkspaceMutationEnabled`、Workspace 已有文件先读后写和新文件父目录已存在，不覆盖 AccessPlan，也不限制文本扩展名或编码。`accessLevel`、hostAccess 与 HITL 按通用规则作用于 external，但不能替代 `editingMode:true`；工具集由 `agent.yml` 决定，声明了 Bash 也不能绕过未开启 editing 时的 Workspace 只读。
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

首次本地运行、更新相邻 builtin 项目或执行 `make release` 前，先执行 `./scripts/sync-local-builtins.sh`；它每次在隔离工作目录中重新构建本机 `dbx`、`httpx`、Rust sidecar 和 `poppler-pdftotext` launcher/archive，并原子更新 `build/builtins/<host>/`。Poppler native runtime 是校验后重新打包的预编译 payload，不在 platform 中编译；`rg` 是唯一只校验复制的 vendor artifact。同步按各本地项目的 `VERSION` 生成临时 lock；Shell 与 PowerShell 在 cache 激活后使用同一正式 lock 状态机。schema v2 的组件 `version/commit/source` 是全平台目标 release，target 同名字段与 `path/sha256` 是该平台实际 release。精确 native host 上严格更高的干净版本经一次精确 `yes` 可抢占为新目标；其他平台的本地 VERSION/Git HEAD 匹配目标并验证成功后自动更新自己的 target。交叉构建只更新 cache，任何 runner 都不得写其他平台 SHA；同版本不同 commit/SHA、dirty、降级、checkout 不匹配或非交互 leader 均不回写。正式写 lock 前必须先将验证 archive 原子固化到相邻项目的稳定 `dist/<version>/`，同路径不同 SHA 必须拒绝；并发 lock 变化同样放弃写入。`--all` 仅为 canonical lock 声明的 Poppler 目标构建，当前为 darwin-arm64 与 windows-amd64，且正式 lock 仍只允许精确 host target 跟随。同步脚本不写 `release-local/`；`make run` / `make build-local` / `make release` 不得重新引入 builtin 或 Rust 构建步骤；运行和 release 从本机 build cache 使用 builtin 二进制；release 原样复制已校验的完整连接器包，保持资源及树哈希不变，不从 Platform 源码补写清单或技能。

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`AGENTS.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `agents/`、`skills-center/` 与 runtime 的外部 `connectors-center/` 是可编辑事实源；Platform 内置连接器及其技能随包只读，`builtin.*` 为平台保留命名空间；Agent 配置内普通 Skill、Terminal 与常规 Skill runtime 使用 Platform 生成的 `ru-agents/`；连接器完整包、技能和 bin 使用本 Agent 的 `ru-connectors/<id>/<contentDigest>`，共享包位于独立 `ru-connectors/`，Agent 目录只保存挂载引用。运行目录不提交、不打包、不允许人工编辑；Platform 启动清空并完整重建 `ru-agents`。热重载先组装候选，普通 Agent 内容整目录发布，连接器版本独立发布；活动 Run、子调用、Team 成员和 Terminal 的租约阻止本 Agent 普通文件替换；仅待发布变更、删除/失效、Agent 加载或绑定阶段失败、旧连接器版本待回收时在最后一个使用者结束后重载；前置校验失败仅保留原待处理标记，无关重载不标记待处理，其他 Agent 独立发布；本地 MCP 绑定完成后才准入新租约。凭证与受管 CLI 状态留在 `.state/connectors`，不随 Agent 重建或删除。唯一的 query 运行时例外是普通 Agent 的非空 `mustUseSkills`：所有选中 Skill 的 canonical 目录获得本 run trusted read + readonly roots；未配置 Skill 还必须从当前有效 skills-center catalog 重新验证，并按需暴露 `@skills-center`。Container 去重后挂载整个 `/skills-center` 为只读，但未选中兄弟目录不获得免审读授权。该例外不合并额外 Skill 的 `.config`、`.runtime-env.json`、`.bash-hooks`，不增加 Tool/MCP/Agent hostAccess/accessLevel；Team 明确拒绝。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和隐藏的 `GET /api/resource` 数据面。新 Markdown 的 Chat 文件只使用相对于当前 Chat 的 `<relativePath>`，也可引用普通 Agent Workspace 或冻结临时根内的实际 Host 绝对路径与 HTTP(S)/data/blob；Markdown 不使用 `@temp`。真实 `/api/resource` 请求地址和 `<currentChatId>/<relativePath>` 都不是 Markdown 协议，历史 endpoint Markdown 不迁移且不再预览。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- `platform_control` 对所有 Agent 使用同一固定 Schema；Agent 显式挂载 Tool 即获得全部注册 operation，Skill 与 `mustUseSkills` 不会替 Agent 挂载 Tool。动态 key 无需预声明，只有当前普通 native root run 成功 set 的 key 才能 unset；set value 是会进入会话、trace 与导出的普通 Tool 参数，不得承载 Secret。子 Agent、Team、ACP、Proxy、Channel、Terminal、MCP、LSP、sidecar 和已启动进程不继承。旧 `platform_config` 以及 `platform-control.profiles/bindings` 配置硬失败，遗留 `runtimeConfig.runEnv` 静默忽略。
- Memory 全局默认关闭；Agent `memoryConfig.embedding/autoRemember`、Provider `memory` 已退役，出现即报错。runtime memory 的两个 hybrid weight 和 prompts 的 `memory` 节已退役，加载时静默忽略，不阻止启动，也不恢复旧能力。旧数据库及 schema 保留；静态 memory.md、Chat 摘要/压缩和 KBASE 不属于该退役范围。
- 文件工具权限独立于 Bash 权限，普通越权路径通过 HITL approval 兜底；readonly、临时根逃逸与其他 hard block 不产生可放宽的 HITL。
- `AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR` 与 `AP_ACCESS_TOKEN` 为 Platform 保留变量。Agent/Skill/run.env/调用配置共用 `shellenv.UnsafeOverride`；技能 `.runtime-env.json` 的 PATH 只追加额外目录。Host 工具环境按 `bash.inherit-env` 名单继承，`SSH_AUTH_SOCK` 仅给 Git 网络操作，默认 Bash 无登录 profile。AP_ACCESS_TOKEN 仅在验证的 oneid-token 直接 CLI 和对应 MCP 身份链路即时注入，不进入普通 Host Shell。有效 StateDir 与 identity 文件三档均拒绝普通工具读写；完整隔离与敏感读取例外尚未落地，见 [改造进度](docs/AccessPolicy与HITL改造.md)。
- 专用 KBASE 未开启 editing 时 Workspace 可读但不可 mutation，当前 Chat 目录仍按 `@chat` 可读写；开启后 Workspace mutation 在 shipped default policy 下免逐次 HITL。external 和其他 chatId 默认进入 HITL，`writeRoots`、hostAccess、`full_access` 或 approval 可按通用策略放宽；这些授权不能放宽非 editing KBASE Workspace，管理员显式 block 仍优先。Workspace mutation 不触发同步索引 hook，KBASE watcher 按 debounce 与 change set 异步刷新。
- MCP registry 同时支持 `streamable-http` 与 `stdio`，版本兼容范围由锁定的官方 SDK 校验：优先请求 `2025-11-25`，接受 `2025-06-18`、`2025-03-26` 和 `2024-11-05`，缺失、无效及未知版本仍拒绝并关闭连接。必须保留 SDK 原始 Connection，使协商版本、HTTP 协议头和 SSE 状态更新生效；日志记录实际协商版本。本地 YAML/重复 Key/transport 契约错误仍使启动或热重载硬失败；合法配置发布后，远端初始化、`tools/list` 与 availability 重试由单 worker 后台执行，`pending/syncing/unavailable` 不影响 Platform 基础健康。旧 external stdio 私有协议没有兼容期；`service.yml`、`type: external`、`external:` 或 `kind: external-service` 会使启动/热重载硬失败。平台、新版 stdio server 二进制和 registry 配置必须同批发布。
- `agent_invoke` 只允许显式配置的普通主 agent 使用，当前禁止嵌套；orchestrated Team 自动注入 session-local embedded builtin `agent_delegate` 和三个 plan tools。普通 Agent 配置、session 与执行入口均拒绝 `agent_delegate`，该工具也不进入公开工具 catalog。
- flat plan task 按数组顺序执行且同时最多一个 `in_progress`；最前面的非终态 task 可由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，`in_progress` 可进入任一终态，终态重试必须追加新 task。TEAM 的 plan task 表示顺序阶段，但当前阶段内部仍可通过单次 `agent_delegate` 按 `maxParallel` 并行执行成员。
- `run_query` / `run_status` / `run_interrupt` 只允许分别显式配置的普通主 Agent 根 run 使用，query 按精确 catalog `agentKey/teamId` 启动独立根 run；不设目标白名单、深度/并发配置或 maxActiveRuns。status/interrupt 只接受同一调用 Agent 与 subject 创建的 run，目标 run 禁止再次调用任一 run 工具。旧 `agent_run_query`、`agent_run_status`、`agent_run_interrupt` 已删除且配置引用会硬失败。
- chat 创建后 `teamId` 固定。Team 以 `teamId` 为公开 owner，`agentKey` 不得与 Team 请求或控制请求同时出现；隐藏协调器 key 只用于进程内执行，不得作为公共 Agent 身份回显。
- Team 成员、成员定义、协调器配置与 prompt 在 run 开始时解析为快照，运行中 catalog 热重载不改变该 run；下一次 run 才读取新快照。
- KBASE Lance sidecar 只监听 loopback，由 Go 生成一次性 Bearer token 并监督生命周期。存在 enabled KBASE capability 时会启动并探测 sidecar；`mode: KBASE` 将其标为 required，故障使健康检查失败，普通 Agent 附加能力将其标为 optional，故障只在 `/healthz` 和 capability 状态中报告 degraded。无 active generation 时 search 返回 stale 并触发冷建，sidecar 故障显式返回 unavailable。
- 当前 KBASE 只对文本抽取结果做 embedding/FTS；PDF/DOCX/PPTX/HTML 均是先抽取文本，不得宣称支持图片、音频或视频语义检索。
- SQLite runtime store 使用 `application_id`（库类型）和 `user_version`（schema 版本）作为身份契约。仅在 `app.New` 启动装配期，`chats.db`、`archive.db`、Memory SQLite 与 KBASE `control.db` 的标记恰为 `0/0`，且表、列语义、约束、索引、触发器和 FTS 对象完整匹配当前 DDL 时，服务才会在事务中写入当前标记；列物理顺序不影响比较。运行期仅验证，绝不认领、迁移、删除或修复。其他标记组合、结构差异或残留旧数据均拒绝；chat/archive/memory 会阻止启动，required KBASE capability 会隔离对应 Agent 并保留管理端诊断，引用它的 Team 同样不可运行；optional capability 保留普通 Agent 可运行并报告 degraded/unavailable。

## 特色功能文档索引

- [Runtime模块边界](docs/Runtime模块边界.md)：Runtime 门面、子包职责、调用依赖规则、Run 订阅时序和迁移期约束。
- [智能体配置说明](docs/智能体配置说明.md)：agent / team / skill 定义、CODER、KBASE、目录式 Team、prompt files、memoryConfig、runtimeConfig。
- [Agent运行时组装](docs/Agent运行时组装.md)：`ru-agents`、Agent 自有/技能中心 Skill 选择、`.config` 冲突与热重载。
- [配置化说明](docs/配置化说明.md)：环境变量、`configs/*.yml`、默认值、优先级、废弃变量。
- [HTTP客户端与系统代理](docs/HTTP客户端与系统代理.md)：默认 `auto` 跳过 PAC/WPAD、显式覆盖、macOS/Windows 固定代理、`pac_auto` 启用 Windows PAC/WPAD、绕过、刷新与平台限制。
- [工具目录权限](docs/工具目录权限.md)：Bash、FileTools、allowed paths、越权审批、读后写闭环。
- [真流式和H2A](docs/真流式和H2A.md)：SSE、heartbeat、`[DONE]`、attach、backlog、H2A 缓冲。
- [记忆系统](docs/记忆系统.md)：SQLite 手工记录、FTS 文本检索、consolidate、memory tools 与已退役能力。
- [运行时和沙箱](docs/运行时和沙箱.md)：runtime 目录、Container Hub、mounts、host / sandbox 工具边界。
- [Platform控制工具设计](docs/Platform控制工具设计.md)：`platform_control` operation、显式工具挂载、run-local 环境快照、并发、脱敏、恢复与执行通道边界。
- [KBASE LanceDB 检索与控制面](docs/KBASE-LanceDB检索与控制面.md)：LanceDB sidecar、control.db、generation、加权 RRF、恢复、回滚与分发边界。
- [KBASE 编辑模式](docs/KBASE编辑模式.md)：`editingMode`、通用文本文件、AccessPolicy/HITL、watcher 异步索引和 KBASE Workspace/Chats 分离。
- [KBASE 编辑模式越权对抗测试报告](docs/KBASE编辑模式越权对抗测试报告.md)：准入、固定工具集（历史结论，见报告内适用范围说明）、HITL、approval replay、路径逃逸、chat 隔离和索引 hook 的红队验证记录。
- [API与协议](docs/API与协议.md)：HTTP API 参数、SSE、WebSocket、HTTP 文件数据面、resource ticket。
- [HITL协议](docs/HITL协议.md)：question / approval / form、submit、awaiting 事件。
- [自动化](docs/自动化.md)：automation registry、orchestrator、dispatch、执行记录。
- [子智能体调度](docs/子智能体调度.md)：`agent_invoke`、TEAM 隐藏调度与 `run_query` / `run_status` / `run_interrupt` 独立根 run 控制。
- [连接器](docs/连接器.md)：统一 MCP/CLI/VIEW、builtin 包、Agent 挂载、自动技能、mustUse 边界、管理接口与旧目录迁移。
- [VIEW连接器](docs/VIEW连接器.md)：VIEW 包、Agent 作用域、快照、HTTP/WS 和 WebClient 表单桥接、旧 viewport 迁移。
- [MCP与工具交互](docs/MCP与工具交互.md)：统一 Tool、MCP registry、tool sync 与可选 viewport 交互元数据。
- [会话存储与回放](docs/会话存储与回放.md)：chat store、StepLine、raw messages、archive、search、resource。
- [鉴权与安全边界](docs/鉴权与安全边界.md)：JWT、JWKS、本地公钥、resource ticket、CORS、敏感配置。
- [版本化打包方案](docs/版本化打包方案.md)：README 索引的交付专题文档。
- [手工测试用例](docs/手工测试用例.md)：curl 回归用例。

- `builtin.desktop` 是受信任内置 native 连接器，自动挂载 desktop_action/desktop_cdp 与对应技能，不自动授予 Bash；声明 `auth_mode: "no_auth"`，无需连接配置；挂载授权与客户端在线状态分离。共享包、运行快照及显式离线迁移见 [连接器共享包与Desktop迁移](docs/连接器共享包与Desktop迁移.md)。


### Desktop 内嵌连接器来源

`builtin.desktop`（桌面端）与 `builtin.desktop-web`（桌面端（网页））随 Platform Go 程序编译分发，共用 native handler 和工具，按完整功能/WorkPanel 与网页功能装配技能；公共 CDP 与网页参考资料只维护一份。同一 Agent 二选一，旧 `builtin.desktop` 保持完整功能。目录名称和描述按请求语言解析，互斥关系由包清单 `mutuallyExclusiveWith` 声明，服务端通用校验，WebClient 显示具体冲突并保留原选择。启动原子发布到各自 `ru-connectors/<id>/<contentDigest>/`，已有相同内容的运行包校验复用；进程持有两版共享包租约，各 Agent 仅持挂载引用。网页版是技能引导范围，不新增执行权限层，详见 [连接器共享包与Desktop迁移](docs/连接器共享包与Desktop迁移.md#builtindesktop)。

Desktop 不属于外部 builtin 构建缓存，不要求 `sync-local-builtins`，修改其源码资源后正常 `make run-local` 即可生效。`builtin.httpx`、`builtin.dbx` 和其他外部可执行组件仍按既有流程准备、校验缓存。旧缓存中的 Desktop 条目仍接受完整性校验，但应用装配始终选择当前程序内嵌版本；发布阶段从已校验的输出副本移除该旧条目，不改原缓存。运行时资源导入校验复用相同内嵌装配流程。此调整不改变连接器配置状态、Agent 挂载、工具权限或历史 Chat。

- 连接器锁统一放入 runtime `.lock/`：`shared-connector-layout.lock` 保护共享目录初始化，`connectors/assembly.lock` 协调装配与回收，`connectors/install/<id>.lock`、`connectors/operations/<id>.lock`、`connectors/leases/<id>/<digest>.lock` 分别保护共享包安装、来源/准备/授权操作与版本租约。路径直接切换，不兼容旧锁路径；更新前停掉同一 runtime 的旧进程。锁释放后保留，不在运行中删除。`ru-connectors/.shared-v1` 仅作布局标记，不再执行旧 ru-connectors 的备份、退役或兼容迁移。

- Native 未提交模型尝试失败时以 `reasoning.end/content.end/tool.end` 的 `status:"failed"` 与公共 error 收尾，正常 end 不增加状态字段；失败输出作为独立展示 event 保存，不进入模型上下文，`run.activity` 仅作辅助提示。详见 [API与协议](docs/API与协议.md)。

技能管理 HTTP：`GET /api/admin/skills` 返回 `{skills,packages,pinned}`，`PUT /api/admin/skills/pin {id,pinned}` 返回 `{pinned}`，复用用户级 skillOrder 存储但不注册 WS。旧包列表 GET 保留供 Desktop；管理页不再消费使用端 `/api/skills`。新列表对象结构需与 WebClient 同批发布，详见 API与协议。

Platform 在启动时读取 `configs/tools.yml` 顶层 `preset-tools`，自动挂载到普通 Native GENERAL/CODER/KBASE（含子智能体和 Team 成员），不写入 agent.yml；缺省或空数组不预置，未知名称启动失败。`toolConfig.excludeTools` 排除预置与自身工具，不影响连接器和运行时自动依赖；ACP 不接受非空工具及排除配置，隐藏 Team 协调器仍使用固定集合。详情 `toolBindings` 提供来源、锁定、排除和实际生效状态；结构化保存保留源码排除项和旧有显式声明，创建时动态去除预置重复项。见 [智能体配置说明](docs/智能体配置说明.md#platform-预置工具)。

内置工具名保持小写及下划线，平台自有输入字段统一 camelCase；旧参数名在模型准备与工具调用边界明确拒绝，不做别名转换。图片来源使用 sourceType: referenceName/filePath。内嵌及 agent-local 工具定义只接受 inputSchema，parameters 硬失败。协议透传、上游 Images response_format 与存储列名保持原契约；历史不改写，拒绝的旧文件写入参数仍需脱敏。详见 [工具输入命名](docs/MCP与工具交互.md#工具输入命名)。


`run_query` 可选顶层 `accessLevel/mustUseSkills/chatName`；权限缺省 default、不继承父 Run（续聊也一样），新 Chat 名称与 chatId 互斥。`configs/tools.yml` 的 `runQuery.allowAccessLevelOverride` 默认关闭，非默认档位明确拒绝；开启是对所有有资格调用该工具的 Agent 的权限委派信任决定，仍经目标准入。技能选择不受开关控制，Team/连接器限制沿用。未知字段（含旧调用中被忽略的 taskName）和错误类型严格拒绝。query/status 返回当前 accessLevel；status 覆盖所有 HITL 等待类型及有界审批摘要，父 Agent 不能代 submit。详见 [子智能体调度](docs/子智能体调度.md)。
