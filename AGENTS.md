# AGENTS.md

## 1. 项目概览

本仓库是 Agent Platform 的 Go 运行时，提供 HTTP/SSE 与 WebSocket 接口、目录驱动的 Agent/Team/Skill/Connector、工具执行、会话持久化、Markdown Memory 和 KBASE 检索。

普通内置类型为 GENERAL、CODER、KBASE、TEAM、EXECUTOR；`engine` 缺省 native，显式 acp 使用外部 bridge，不由 acpBridgeId 推断。TEAM 是带成员委派能力的公开 Agent。历史 REACT 只兼容读取，新配置与 API 输入使用 GENERAL。

设计约束：GENERAL/CODER/KBASE/TEAM 的 mode 写在 agent.yml 中，只在加载时提供默认值，不提供运行时功能、目录授权、必填校验或专属逻辑。mode 不要求 Workspace 或知识库绑定；项目规则文件、expectedBranch、文件变更 hook、子 Agent 调用、阶段与预算键对四种 mode 一致。editingMode 只是 Run 的编辑状态，不影响 stage、system-init cache key、fingerprint 或 system prompt；规划才是独立的运行形态。尚存的 mode 分支（ACP 内部落为 CODER、TEAM 编排）见 docs/mode-capability-audit.html，不能因已有实现而扩展。

本文保留开发入口、模块边界和必须遵守的约束；功能与接口细节以文末专题索引为入口。未实现或未经目标环境验证的能力不得写成已交付。

Memory 由 Platform worker 调用 memx 维护分层 Agent daily/summary 与总体 summary（agents→summarize→consolidate），预算使用 memory.summary.global/agent；旧 worker.summary-max-chars 拒绝，分层管理 API 尚未完成；上下文由 contextConfig.tags 的 memory-global/memory-agent 独立选择，memoryConfig.enabled 仅控制采集，支持定时增量与手工日期范围任务（独立进度、配置模型）；知识库通过受管 KBX CLI 读取和维护，Platform 管理目录监听、库级自动维护与重启对账。当前范围见 [记忆系统](docs/记忆系统.md) 与 [KBX 接入](docs/KBX接入.md)。

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统 + Markdown memory + SQLite runtime store + 受管 KBX 知识库
- 配置：环境变量 + `configs/*.yml`

当前没有引入 Web 框架、第三方路由库、外部数据库或消息队列。Go 主程序仍以 `CGO_ENABLED=0` 构建；知识库通过受管 KBX CLI 读取和维护；Poppler 继续用于 KBX PDF 抽取和 Agent Bash；KBX 已接入正式分发链路；Agent 索引维护要求维护 JSON v1 能力探测。配置默认值以 `internal/config/config.go` 与 `configs/*.example.yml` 为事实源。

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

核心模块边界（详细调用规则见 [Runtime 模块边界](docs/Runtime模块边界.md)）：

- `internal/agent` 保存中立 mode 契约；`builtin` 静态分派 CODER/KBASE/TEAM。EXECUTOR 只有模板默认值（`internal/agent/executor`），不注册 descriptor，LLM 沿用 GENERAL 路径。GENERAL 没有固定工具集、prompt 或 stage；CODER、KBASE、TEAM 的特有规则分别归各自子包。TEAM 使用普通 AgentDefinition 与 agentKey，成员配置归 Agent、编排状态归 Run。
- `internal/catalog` 装载目录定义并冻结 Team 成员、协调器与 prompt 快照。参见 [智能体配置](docs/智能体配置说明.md) 和 [运行时组装](docs/Agent运行时组装.md)。
- `internal/runtime` 负责 Query 准入、Session、Run 状态、执行、恢复和子任务编排，不得依赖 Server。App 直接组装各组件；`adapter` 只转换旧执行器/catalog DTO。受管根 Proxy 驱动归 `proxy.Driver`，公共收尾与 recorder/usage 归 `runexec`；Server 经 ProxyPort 保留路由、响应、channel 与控制适配，不能宣称 ProxyPort 已移除。
- `internal/server` 只做 HTTP/WS 解码、鉴权、响应映射、SSE flush 和薄适配，不得直接依赖 llm、tools 或具体 Agent mode。
- `internal/runops` 负责 Chat 会话工具、所有权、幂等及旧工具的一层调用限制，直接依赖 Runtime 窄接口；`internal/automation` 负责注册、调度与执行记录。自动化初始权限来自 `query.accessLevel`，省略为 default，不继承 Chat 历史权限。 `internal/automation.Service` 共用于 HTTP 和 Task Control 的独立 Automation 工具，版本校验、审批及调用收据见 [自动化](docs/自动化.md#task-control-管理工具)。
- `internal/mailbox` 保存独立 mail.db，负责 letter/reply/notify/ask 的受理、在途收据、回信幂等、附件复制与重启人工重投。Runtime 通过窄接口提供准入、消费、等待项及续接后的空闲钩子；工具授权在发信 Run 的权限临界区冻结，投递不依赖发信 Run 存活。仅 letter 的 started 记录授予发信方 Run 控制，inserted 只能查 mail，回信/即时消息/请示开出的 Run 不授予对方控制；请示代批在分身冻结环境下按上级当前档位重评，强制人工、只读与硬禁止不可放宽。详见 [Mailbox](docs/Mailbox.md)。
- `internal/llm` 负责 prompt、模型流、HITL、planning 与工具循环；`internal/modelclient` 承接 Provider HTTP、首响应超时和错误分类；`internal/tools` 是通用工具 registry/router，mode 工具通过 named handler 接入，不增加 mode switch。
- `internal/conversation`、`adminsource`、`chatresource` 分别负责会话/归档编排、源码 mutation 事务、Chat 资源解析和 mutation；Chat 资源沿用 principal/Chat 权限，不借用连接器授权。`internal/chat` 保存会话与回放数据。
- `internal/connector` 校验、导入和编辑中立连接器包；`connectorauth` 负责部署级授权与 CLI 准备；`connectorops` 提供调用方中立的 CLI/MCP 执行与短期授权，不持有 WebApp、appId、Chat 或页面生命周期，不注册业务 operation/profile。旧包/凭据/MCP 目录迁移和 connector-migrate 命令已移除，`connectormigrate` 仅保留独立 Desktop 工具声明调整。包、挂载与凭据边界见 [连接器](docs/连接器.md)、[安装与授权](docs/连接器安装与授权.md) 和 [执行协议](docs/连接器执行协议.md)。
- `internal/view` 负责内置与连接器两种来源的 VIEW 定义、声明资源、模板获取、Chat 快照、Markdown VIEW 块语法与 prompt 目录渲染，无工具执行或 HITL 决策职责；纯 VIEW 不授予 Bash/PATH。助手正文的 ` ```view key=<connectorId>/<viewKey> ` 块正文只放符合 VIEW `inputSchema` 的视图输入（不放查询结果，其他属性忽略），模板自行执行声明的查询，由 stream dispatcher 在块闭合时用产生内容的执行者冻结挂载绑定快照（`content.view`、`content.snapshot.views`），`/api/view` 对连接器只读已发布 hash 的快照；display VIEW 目录与规则只来自 `agent-prompt.yml` 的 `shared.view`，目录每个 VIEW 一行（key 与用途），inputSchema 与块模板由普通只读工具 `view_describe` 按需返回（不披露查询）（分发配置仅 general/coder 段 preset），见 [VIEW连接器](docs/VIEW连接器.md#markdown-view-块)。`internal/viewquery` 执行 VIEW 快照声明的只读查询（`/api/view/query`），按查看者的 Chat 权限授权而非 Agent 工具准入、不复查挂载；内置与外部连接器共用同一查询形状，`native` 只调用 `internal/connector/native.go` 登记给声明连接器的原生操作，`cli`/`mcp` 只调用声明包自身组件，经 `connectorops` 执行，快照记录的连接器 version 与当前一致才执行、否则 `view_version_outdated`，查询之间不互斥、只租用所用 CLI 安装（argv 结构化拼接、不经 Shell，CLI 输出须为 JSON）；runId 解析出的 agentKey 进入查询上下文，当前不裁剪。内嵌原生连接器可携带 view.json。
- `internal/viewaction` 承接用户在 display VIEW 中声明的 cli/mcp 写操作（`/api/view/invoke`），与只读查询隔离；宿主确认后提交，按在线 Chat 所有权、发布 Run、当前有效及源码挂载、安装版本与动作声明复核，通过 connectorops 的精确授权、JSON stdin、持久化收据与审计执行。HTML 保持禁止原生表单及网络直连，不携带凭据；未知写入不自动重发。
- `internal/platformcontrol` 维护平台控制操作，知识库管理工具直接调用 kbases.Service，来源审批、配置修订与幂等收据归库服务；libraryId 绑定对所有 mode 可选，配置后的存在性校验归管理入口，Run 启动按绑定而非 mode 校验。`runenvops` 是独立 run_env handler，`runenv` 保存进程内 Scope、revision、限额与幂等收据；`toolpolicy` 提供中立调度属性。
- `internal/memory` 负责 Markdown 文件、revision 与查询，不持有知识索引；`memoryworker` 调度已完成 Chat、同步模型配置并调用 memx，不自行生成记忆文件，不依赖 Server。参见 [记忆系统](docs/记忆系统.md)。
- `internal/kbases` 负责知识库定义、collection 来源、持久状态、目录监听和维护调度。`internal/kbx` 是知识库执行门面，使用受管 CLI 与 chunk/evidence 协议，Platform worker 按库 ID 管理监听、update/embed 和重启对账；不运行 kbx watch。`internal/knowledge` 保存中立的 capability 配置、DTO、工具处理器、路径过滤和引用发布契约，不持有索引、存储或进程管理，不得 import agent 或 catalog。`internal/agent/kbase` 保存专用 KBASE mode 规则。对外 KBASE mode、工具、REST 和 sidecar JSON 字段由这些模块提供。参见 [KBX 接入](docs/KBX接入.md)。
- `internal/config` 负责配置装载；`httpclient` 统一出站客户端与代理，不修改全局 Transport 或进程环境，内部服务直连；代理平台限制见 [HTTP 客户端与系统代理](docs/HTTP客户端与系统代理.md)。
- `internal/stream` 负责中立事件、dispatcher、assembler、normalizer 与 EventBus，SSE writer 归 Server；`sandbox` 负责 Container Hub 执行与挂载；`ws`、`gateway` 负责 WebSocket 控制面与反向 gateway。

## 4. 目录结构

以下为关键目录节选，完整模块职责见上文与专题文档。

```text
.
├── cmd/agent-platform/          # 进程入口
├── configs/                     # 配置模板与本地覆写入口
├── docs/                        # 中文专题文档
├── internal/                    # Go runtime 实现
│   ├── agent/                   # 中立 mode 契约及 CODER/KBASE/TEAM 特有实现
│   ├── runtime/                 # Query/Run 门面、状态、执行、编排与 Proxy
│   ├── runops/                  # Chat 会话工具组 handler、所有权与幂等
│   ├── conversation/            # Chat/Archive/Compact 应用服务
│   ├── adminsource/             # Admin source mutation 事务边界
│   ├── chatresource/            # Chat 资源应用服务
│   ├── modelclient/             # Provider 协议 HTTP 客户端
│   ├── knowledge/               # mode 中立的知识库契约与工具处理器
│   ├── kbases/                  # 知识库定义、状态、监听与维护调度
│   └── kbx/                     # 受管 KBX 执行与维护
├── build/                       # 忽略的多平台 builtin 本地装配缓存
├── scripts/                     # 构建、发布、builtin 同步与审计脚本
├── Dockerfile
├── Makefile
├── compose.yml
├── README.md
└── VERSION
```

`docs/` 是特色能力的主说明区；当前项目事实文件 `AGENTS.md` 只保留事实总览、开发入口和专题索引。

知识库以 `<AP_RUNTIME_DIR>/kbases/<id>/library.yml` 保存来源配置，以 `ru-kbases/<id>/` 保存 KBX 数据和状态。`ru-kbases` 跨重启保留，不能按 `ru-*` 清理。任何 Native Agent 可通过一个可选的 `kbaseConfig.libraryId` 固定绑定库（KBASE 不强制）；普通 Native Agent 挂载 `builtin.kbase-control` 后可按 `libraryId` 检索任意可读库，非空 `references` 中的 kbase 引用 固化本 Run 必须使用的库（最多 8 个），不授予额外工具或写权限，Workspace 与来源解耦；知识库服务按库自动监听、500ms 合并、五分钟及重启对账。普通内容刷新保持已提交内容可读，全文完成而 embedding 失败时 degraded；范围变化、未知 partial 或中断禁读并重试。collection 的 include/exclude、库级与集合逐字段合并的 chunk、库级 textEncoding 纳入来源指纹；description 不计入，models.embedding 从共享 registry 选择模型并覆盖 runtime 默认，模型合同变化仅重建全库向量、全文仍可读；retrieval 按库默认→匹配固定绑定库的 Agent 显式覆盖→单次调用合并，models.reranker/queryExpansion 和 collection.defaultQuery 下次查询生效、不计入指纹；默认排除不禁止显式选择或回读；库删除有引用时 409。Agent 的 kbaseConfig 只接受 libraryId 与 retrieval。见 [知识库](docs/知识库.md) 与 [KBX 接入](docs/KBX接入.md)。

## 5. 数据结构

Chat 默认由 `AP_RUNTIME_CHATS_DIR` 控制，主要包含：

- `chats.db`：chat 摘要索引。
- `chat-order.json` / `chat-pinned.json`：Platform 管理的实例级 recent/manual 展示排序，缺省 recent（更新时间倒序），以及独立跨 mode 置顶顺序，不修改 Chat 内容时间或数据库 schema。
- `chat-primary.json`：每个 Agent 的主要聊天指针（agentKey → chatId），其他 active Chat 为历史聊天；由创建 Chat 的 query `primary:true` 或 `PUT /api/chats/primary` 切换；Mailbox 仅在指针缺失时补建并设定主要聊天，当前主要聊天有活动 Run 时 409 拒绝，Automation/chat_start/execute 等入口不切换，见 [API 与协议](docs/API与协议.md#主要聊天)。
- `<chatId>.jsonl`：运行事件、StepLine、system init 与 raw messages。
- JSONL query 行的 `query` 只保存调用方原样输入（HTTP/WS 请求体、chat_start 参数、子任务条目、Automation 配置的 query），平台不往里加字段；平台决定与平台算出的有效值写在行顶层（requestId、agentKey、lane、origin、accessGrant、planRunId、accessLevel、hidden、planningMode、editingMode、model、references，任务行另有 taskId/taskName/taskToolId/taskAgentKey），缺省值写入时存下、读取时不重算，无输入的行不写 query；请求字段名大小写变体拒绝。`origin` 由平台判定（user/agent/automation）；HTTP/WS 请求及 Automation API/YAML query 中的 `role` 均接收、保留但不参与执行；`agent.yml` 的描述字段 `role` 继续保留；读取不分新旧布局，旧记录缺少的顶层决定视为未设置，显示字段从 query 读取。btw/explain 分支分别存于 `<chatId>/.btw/`、`.explain/`，协议用 `sideId`。详见 [API 与协议](docs/API与协议.md)。
- `<chatId>/<uploaded-or-generated-file>`：上传与图片生成资源；工具返回内部绝对 `path` 和相对于当前 Chat 的稳定 `url`（不含 `chatId`），用户可见内容只使用 `url`。
- `<chatId>/artifacts/<runId>/<filename>`：`artifact_publish` 的发布副本；发布结果 URL 必须指向该副本，写作 `@chat/artifacts/<runId>/<filename>`（字面路径），读取方同时接受裸相对引用。

Automation 定义目录中的 `executions.db` 是 schema V2 的旁路执行历史库。已知旧版在后台创建一致性备份后重建为空 V2，不迁移旧行；History 初始化、备份和写入失败不得阻止 Platform、Automation 调度或 Query/Run。`AUTOMATION_EXECUTIONS` 保存触发快照、`chatId/runId`、真实 `finishReason` 和完整助手结果，列表只读取摘要，详情按需读取全文。

Memory 默认由 `AP_RUNTIME_MEMORY_DIR` 控制，以总体 summary.md、agents/<agentKey>/summary.md 和 agents/<agentKey>/daily/YYYY-MM-DD.md 为当前 memx 内容源（管理端 daily/search 仍为旧布局，分层接入待完成）；用户资料位于 owner/OWNER.md。memx 管理收据及恢复日志，Platform worker 进度位于记忆目录的 `.worker/`。旧 memory.md 仅在 summary 缺失时复制并保留备份；旧数据库不迁移。

KBX 索引固定使用 `ru-kbases/<libraryId>/index.sqlite` 及配套存储；Agent 不拥有索引。知识工具路径为 collection/relativePath，引用 ID 包含 libraryId。原文通过已发布来源的专用 Chat API 回读，不扩大 Workspace 文件权限。

核心 DTO 位于 `internal/api`，包括 query、submit、steer、interrupt、chat、upload、automation、Markdown memory 等请求和响应类型。

## 6. API 定义

非 SSE JSON 统一使用 `{code,msg,data}` 包裹，成功 `code:0`。HTTP/WS 参数、路由、流事件与错误语义统一查阅 [API 与协议](docs/API与协议.md)；人工交互查阅 [HITL 协议](docs/HITL协议.md)。不要在此重复维护接口清单。

## 7. 开发要点

- 配置按 agent-settings（全局 + mode preset、创建默认值、file 声明式 Workspace 规则、顶层 ACP）、agent-prompt（shared 含 skill/view/reference 等，coder/kbase）、tools（含 AI profiles）、runtime（KBX 和 memory/memx）归属；旧分散配置拒绝加载，迁移入口为 `config-migrate`。KBX embedding 由 library.models.embedding 覆盖 runtime.kbx 默认并使用共享模型 registry，Agent 创建默认值来自对应 mode 的 default-agent。见 [Agent 配置合并](docs/Agent配置合并.md)。
- `planningMode` 是原生 GENERAL/CODER/KBASE/TEAM 的通用能力，实现位于 `internal/agent/planmode`，不属于 CODER，也不是 Run 内的阶段切换：规划 Run 只产出计划并等待确认，批准后由 Runtime 启动同一 Agent 的一次普通 Run 来执行，规划 Run 不在自身内执行计划。两者的工具均为 Agent 有效工具减去 `agent-settings.yml` 的 `planning-mode.exclude-tools` / `execute-exclude-tools`，`finalize_planning` 由 Platform 追加与去除；代码不内置只读工具清单，阶段级 `toolConfig.tools` 硬失败；规划与执行都不允许换模型，原生 Agent 的 `stageSettings.planning/execute` 声明 `modelKey` 同样硬失败。ACP 交给 bridge；KBASE 规划 Run 不开启 editing。新增规则不得再以 `mode == CODER` 判断规划能力。见 [Agent 配置合并](docs/Agent配置合并.md#planning-mode-工具排除)。

- 通用运行时配置事实源以 `internal/config/config.go` 和 `configs/*.example.yml` 为准；KBASE capability 的配置、索引/检索默认值和工具名以 `internal/knowledge` 为准，专用 KBASE mode 的 profile、prompt、创建策略和边界以 `internal/agent/kbase` 为准；CODER/TEAM 规则分别以 `internal/agent/coder`、`internal/agent/team` 为准，文档只解释和引用。
- Agent 候选不再注入 prompt；旧 `contextConfig.agents` 与 `agents` context 标签忽略并合并为一条非阻断管理 warning（其他旧配置拒绝规则不变）。显式挂载 builtin.platform-control 后通过 catalog_query.list 发现全部公开 Agent 摘要；invocable 仅描述 agent_invoke 静态目标资格，不改变 chat_start、Automation 或 Team 授权。详见 [智能体配置](docs/智能体配置说明.md#agent-发现与旧候选配置)。
- 共享 Skill 调度提示按用户目标和技能触发条件判断适用性；用户指令优先，技能工作流不扩大任务范围或改变交付形态。该提示约束不替代工具权限和 HITL。
- Runtime 目录只接受 `.env` allowlist：`AP_RUNTIME_DIR` 与 `AP_RUNTIME_REGISTRIES_DIR`、`AP_RUNTIME_CHATS_DIR`、`AP_RUNTIME_MEMORY_DIR`、`AP_RUNTIME_PAN_DIR`、`AP_RUNTIME_STATE_DIR`。`AP_RUNTIME_STATE_DIR` 为空时使用 `<AP_RUNTIME_DIR>/.state`；其他子目录固定从 runtime 根派生，`configs/runtime.yml` 的整个 `paths` 节（包括旧迁移来源键）出现即报错。连接器管理命令仅用 `--runtime-dir` 选择部署，状态目录复用 `AP_RUNTIME_STATE_DIR`，不提供其他目录参数。
- `.env`、真实 `configs/*.yml`、真实 `configs/*.pem`、真实 token 和私钥不得提交。
- 工具运行时配置以 `configs/tools.yml` 为外部事实源，包含 access policy、bash 和 file tools。全平台发布共用 `configs/tools.example.yml`；工具权限支持 `@root`（Unix/macOS 根目录、Windows 当前驱动器根），full_access 默认引用它。未显式配置 Bash 命令列表时使用平台默认值。
- `configs/tools.yml` 中的旧 YAML 路径策略键（如 `bash.allowed-paths`、`file-tools.allowed-read-paths`）会在启动阶段硬失败；Go 配置结构中的旧路径字段也已删除，目录权限统一走 `tools.access-policy`。
- `@temp` 仍为进程启动时冻结的通用临时读写根，不豁免脚本执行。本 Run 经 `file_write/file_edit` 写入当前 Chat 目录、由系统解释器执行且内容未变的脚本三档免执行审批；其他脚本 default 按调用、内容版本和环境审批，auto_approve 自动审计，full_access 允许。每 Run 私有临时目录尚未实现。Workspace 写入由解析后的 editing 能力决定（GENERAL/CODER 默认可编辑、KBASE 默认只读），editing=false 时 Workspace 写入与在 Workspace 内运行无法分析的程序均硬拒绝。readonly、临时根逃逸检查优先，详见 [工具目录权限](docs/工具目录权限.md)。
- 连接器挂载授予冻结入口（包内 bin 与活动安装 bin）的 CLI 执行权，仍核验 canonical 路径与 SHA-256；`view.json` 中 `intercept: true` 的 cli action 不豁免：Agent 直接调用该命令时按模板反解参数进 VIEW form 人工审阅，提交后按模板重建单条直接调用执行，无法反解或非单条直接调用以 `view_intercept_rejected` 拒绝；技能 `.bash-hooks` 已删除，出现即加载失败，见 [VIEW连接器](docs/VIEW连接器.md#cli-拦截审阅)。普通 Host Shell 不注入身份/连接器凭据，但注入已挂载连接器 `cli.json.env` 的公开值（含配置目录），复合命令可直接使用；已验证的单条直接 CLI 以独立子进程额外接收本连接器凭据；需要凭据的复合、包装或重定向调用须拆分。外围 Shell 和其他资源要求独立审查。
- AccessPolicy 写判定必须在 writeRoots、hostAccess 与 HITL 之前检查当前 level readonly roots 和 trusted run readonly roots；命中 readonly 后直接 block，exact/rule approval 不得放宽。`references` 中的 skill 引用 的 run roots 只覆盖本次选中 Skill 的 canonical 目录，未选中兄弟目录不继承。
- `internal/skillsexec` 为 Agent YAML 已配置普通 Skill 和本次 `references` 中的 skill 引用 选中 Skill 的 `scripts/**` 保存独立的本 Run 内存执行凭据，绑定 Agent/Run/执行环境、严格现存 canonical 路径和实际字节 SHA-256。复用通用解释器与脚本入口识别，仅免对应入口 opaque 审批（`bash-access:skill-script`）；Host 启动前复验，Container 使用选中技能 Host–guest 映射与容器摘要。内容不匹配撤销，不落盘、不复制技能、不跨 Run/子调用继承，同 Run 压缩保留，恢复同 Run 不重建凭据。外围 Shell、hard block、readonly 与 KBASE mutation gate 不变；Host 不隔离脚本内部访问，入口摘要不锁定完整依赖图。
- 脚本入口策略对所有 Agent 相同，bootstrap 没有特权。`scriptstate` 自写证明只在当前 Chat 目录内取消执行审批；可信技能入口仍按 `skillsexec` 核验。命令语义统一由 `internal/shellanalysis` 描述（选项、操作数角色、递归、删除、远端修改、Git 子命令与配置依赖），`bash -c`/`env -S` 递归分析；破坏性操作、Git 可执行配置写入与远端修改分别由 `approvals.destructive/executable-config/remote-mutation` 决定。批准绑定精确调用、canonical cwd、环境、access level 和内容 SHA-256；普通路径规则不扩到父目录。复杂 Shell 与远端 mutation 仅单次批准。Host 启动前重新核对，Container 使用实际 guest 摘要；不隔离任意代码内部访问。
- Catalog 按资源根保留独立 watcher（重叠根合并），事件统一分类排队并串行 reload。技能/连接器 ZIP 在发布保护区外解压校验，区内重新检查当前状态；快照和普通保存不暂停监听，目录 mutation 只暂停对应根。API 与 watcher 通过加载前后一致的内容指纹去重，恢复监听只做对应类别差异检查，不无条件全量 reload；失败及加载期间变化不确认新状态。现有 skills→agents/ru-agents 组装和活动租约保护保留。管理入口保护不覆盖 Bash/外部编辑器直接写盘，详见 [Agent运行时组装](docs/Agent运行时组装.md#技能包事务与目录监听)。
- 普通 Native Agent 工具只来自全局/mode preset、Agent 显式声明与连接器自身工具；Skills、运行环境、Memory、KBASE capability 和 CODER 阶段不得隐式补工具或恢复被排除工具。专用 memory_read/memory_search/memory_write/memory_update 已下线；Agent 通过 memx 获取记忆、file_write/file_edit 修改 Markdown，仍受现有工具和路径权限约束，不自动授予工具；四个知识检索工具归 `builtin.kbase-control`，分发示例经 kbase.preset-connectors 挂载，GENERAL/CODER 显式挂载；单独声明工具名硬失败，`toolConfig.excludeTools` 对原生连接器工具同样生效。内部 planning 与 TEAM 协议工具保留，参见 [智能体配置](docs/智能体配置说明.md)。
- `POST /api/execute` 在新 Chat 中执行一次 Run，等待 `result_submit`（`status` success+output / failure+reason）及 Run 正常结束后返回；提交通过后按普通工具循环继续，不强制结束、不追加专用收尾轮或限制后续工具；准入只看有效工具是否含 `result_submit`，不判断 mode。`outputSchema` 由调用方按次给出，交卷校验、按 Run 生成工具定义归 `internal/resultsubmit`；打回 3 次 `output_invalid`，未交卷提醒一次后 `output_missing`，需要审批直接 `approval_required`，提问/表单照常等待，调用方断开即中断。输入文件按上传规则存入 Chat；`output` 是任意结构，只按 `outputSchema` 校验，不解释其中的 `@chat/` 路径。JSONL 格式不变。全局 preset 只放所有 Native Agent 都需要的能力，`ask_user_question`、`artifact_publish`、`view_describe`（仅 general/coder）与 web/task-control 列在各 mode 段，executor 段预置 `result_submit`。见 [智能体执行接口](docs/智能体执行接口.md)。
- 事件下发按 `detail` 分 none/summary/full 三档（HTTP 用 URL 参数，WS 用帧顶层字段，不进 query 请求体），缺省按 Agent interactionConfig.defaultDetail > agent-settings.yml 对应 mode 的 interaction-config.default-detail > summary 解析。档位属于 Run：`/api/query` 受理时冻结，所有观察者同档，`/api/attach` 带 detail 拒绝，`/api/chat` 在有进行中 Run 时忽略请求档位，只有无进行中 Run 的历史 Chat 可按请求档位读取；档位不落盘、不进入 system prompt；/api/chat 与流式 run.start 的 display 元数据区分 Agent 默认值与实际下发值，元数据不落盘。summary 只放行白名单事件和进度 `run.activity`（一句话、`seq` 为 0、不落盘、attach 时补发当前条），不含重试；none 与 summary 事件相同，进度活动只放行公示（`action_announce`，`kind:"announce"`）及 idle 清除；公示在服务端按 agent-settings.yml `announce-hold-seconds`（默认 60）至少压住同 scope 其他进度活动，idle 与终止提前结束；工具活动每个模型回合只在参数完整时发一条；full 是全集（含进度与带 `retry` 的重试活动），供共享上游 stream 的中间层使用，详细视图忽略进度活动。落盘始终完整，Proxy 上游固定请求 full；Desktop broker 不改写 detail，窗口跟随 Agent，图片工作台等读取工具事件的主进程 Run 显式请求 full。推导与过滤归 `internal/stream`（activity/detail），文案在输出边界按查看者语言渲染，工具说明取工具 YAML 的 `activityArgs`，见 [细节等级与 run.activity](docs/API与协议.md#细节等级与-runactivity)。
- 新增能力优先放进对应 `internal/*` 模块，不在 server 层堆业务逻辑。
- Native `ANTHROPIC` 的显式思考配置使用 `thinking.type: adaptive`、`thinking.display: summarized` 与 `output_config.effort`，由有效 stage 的 reasoning 设置控制，不按模型名分支；请求构造在合并 compat 后统一设置，累计 token 用量由协议适配映射到现有 usage 事件；模型级 `maxOutputTokens` 作为 Anthropic 默认请求预算与输出能力上限，不按模型名推断，见 [Anthropic 自适应思考](docs/配置化说明.md#anthropic-自适应思考)。
- Agent 的 `modelConfig.maxInputTokens` 设定上下文窗口，Run 取它与所用模型上限的较小值，取值函数为 `models.EffectiveContextWindow`（Run 与手动压缩共用）；仅管理入口保存时校验不超过模型上限，非 native Agent 拒绝，见 [智能体配置](docs/智能体配置说明.md)。
- 会话亲和键由 `internal/modelrequest.AffinityKey` 从 chatId（无 Chat 时 runId）派生，无开关：Responses 固定发送 `prompt_cache_key`，Anthropic 固定发送 `metadata.user_id`，辅助工具调用沿用调用方 Chat；两字段归平台所有，compat 中出现即加载失败。provider `requestIdHeader` 声明中转请求 ID 头（BabelArk 必配 `X-Babelark-Request-Id`），成功调用写入 usage `upstreamRequestId` 并持久化，失败进入错误诊断，见 [会话亲和与上游请求 ID](docs/配置化说明.md#会话亲和与上游请求-id)。
- 工具内部的模型调用（当前 image_generate、vision_recognize、speech_generate）经 `ToolExecutionResult.ModelUsage` 上报上游实际返回的 usage，归一化在 `internal/modelusage`；未返回按 0、不推算。语音合成只上报上游计费字符数（不计入 token），模型 `pricing.unit: per_10k_characters` 按 `characters` 单价只记总费用；`type: tts` 必须配 `MINIMAX_TTS`/`DASHSCOPE_TTS` 协议，请求与响应按协议在源码实现，模型 YAML 不声明请求格式。Native 运行流把它计入 Run 累计并发出 `usage.current.source:"tool"` 快照，按工具模型自身 pricing 计价，不改变上下文窗口与 Run 主模型；不得在 Processor 另行累加，否则会被主模型的 run 快照覆盖。步骤行以 `toolUsage` 持久化并参与回放，见 [配置化说明](docs/配置化说明.md#工具内模型调用的用量与费用)。
- TEAM 是公开 mode：通用机制归 `internal/agent`，调度归 `internal/agent/team`。总控与成员一起冻结版本和租约；成员不可为 TEAM/ACP、不可带 agent_invoke。规划 Run 禁用 agent_delegate，执行 Run 可委派。详见 [智能体配置](docs/智能体配置说明.md#team-agent-配置)。
- 删除外部连接器时自动移除 Agent 源码挂载；活动 Run／子任务／TEAM 成员／Terminal 或保留的运行 pin 占用则硬拒绝，不主动终止。预置引用仍受保护，HTTP 与 catalog_manage 共用删除事务，见 [连接器](docs/连接器.md)。
- 新增 API 保持统一 JSON 包裹、字段命名和错误语义。
- 内置工具的显式 boolean 字段兼容精确字符串 `"true"` / `"false"`，公共方法归 `internal/toolinput`，在相关校验和审批前归一化；不转换普通文本或 MCP/开放参数，不放宽 HTTP/WS API 类型，详见 [MCP与工具交互](docs/MCP与工具交互.md#内置工具布尔参数兼容)。
- Agent 创建复用 `/api/admin/agents/create`；所有项目创建（包括知识库项目）必须写入 `runtimeConfig.workspaceRoot` 并传请求级 `isProject:true`，强制具体、现存且非 canonical 文件系统根的 Workspace，拒绝 `@root`。模型的 catalog validate/apply 使用同名 boolean，审批绑定并在发布前复验；知识来源与 Workspace 独立不免除项目目录要求。标志不写入 `agent.yml`，Projects 仍按公开 `workspaceDir` 判断。省略或 false 保留普通 Agent 契约，详见 [智能体创建](docs/智能体配置说明.md#智能体创建)。
- AWCP 遵循网站手册渐进披露：固定工具 `awcp_manual` 返回目录/章节说明，章节请求携带 `section` 与 `revision`，页面通过 `surfaceId` 在 Run grant 内选择，通用 `awcp_invoke` 接收内联 `revision/action/args` 或互斥的 `paramsFile`；文件仅含这三个字段，复用 CDP 文件权限、审批和大小限制，wire 只发送解析后的 JSON。网站说明只作为工具结果，`internal/llm` 不得加入 AWCP 专属状态、动态 Schema、纠错预算或调度分支；授权页面与业务校验留在工具/Desktop/网站边界。详见 [MCP与工具交互](docs/MCP与工具交互.md)。
- Desktop 普通 Action 白名单跟随 `desktop/src/shared/desktop-actions.ts`，排除仅限 WebApp page 的动作；相邻仓库存在时工具测试直接核对上游定义，CI 可通过 `DESKTOP_SOURCE` 指定 checkout，见 [MCP与工具交互](docs/MCP与工具交互.md)。
- 连接器包版本由资源发布方维护；Platform 不根据来源市场或重新打包动作推断版本，不用 CLI 或 Skill 版本替代连接器版本。具体服务适配应留在连接器资源包，项目文档只描述通用契约。
- 启动、热重载与目录枚举按连接器隔离无效包，记录 warning 后继续；MCP 本地定义失败仅跳过对应包或挂载的路由，同包组件不得部分发布。目录根遍历失败仍返回错误，管理目录保留无效包的 invalid 状态及错误原因；单包读取、定义保存与本次导入目标保持严格校验，ZIP/MCP 地址导入隔离其他无效已安装包，仅与健康包检查冲突，见 [连接器](docs/连接器.md)。
- `connector.json.authMode` 取值为 none/self/token/mcp/oneid，`oauth`（即 mcp）与 `oneid-token` 是长期别名，读取时归一；独立 OAuth 模式已删除，`oauth` 块只承载 MCP 授权设置，显式授权/Token 端点报错，旧独立 OAuth grant 不复用。MCP 授权发现以未认证 initialize 的 Bearer 挑战为准、well-known 兜底，不用 GET 探测；`/api/connectors/check` 与 `checkConnector` 对未授权 mcp 组件执行不登录、不注册的发现。见 [连接器安装与授权](docs/连接器安装与授权.md#mcp-标准授权)。
- 本版本临时兼容改造前 `connector.json` 的蛇形字段、认证值、内联 Token 表单与 auth_bindings，读取时在 `internal/connector/manifest_compat.go` 统一转换，不改写原包，管理摘要使用当前格式；旧 `auth_mode:none` 仍表示 self。下个版本按统一标记 `TODO(connector-compat)` 删除兼容层及逐组件 OAuth/CLI grant 适配，并清理对应测试与文档，规则及移除清单见 [清单临时兼容](docs/连接器.md#清单临时兼容下个版本移除)。不恢复旧目录迁移或旧 API。
- kbase-control 提供 list/search/files/read/status，四个检索工具均必填 libraryId；索引由知识库服务自动维护，Agent 侧没有刷新工具或接口。查询预算不改变库索引范围。
- `kbase_search` 支持 query/search/vsearch/gsearch，复合过滤与 Agent 范围取交集；纯全文不解析模型配置，严格向量/图检索不静默回退。图关系和边证据保留并检查来源范围；Platform 尚不自动建图，共享库绑定已接入，重排和查询扩展通过库 models.reranker/queryExpansion 接入；reranker 引用 registry type: reranker（必填 reranker.endpointPath），扩展引用 OPENAI Chat Completions。参数和验证边界见 [KBX 接入](docs/KBX接入.md)。
- editingMode 是四种 Native Agent 的通用 Workspace 编辑开关；mode 只能提供模板默认值，显式 true/false 不按 mode 限制。省略时 KBASE 默认 false，GENERAL/CODER/TEAM 默认 true，规划 Run 始终只读。Workspace 始终是最终 canonical runtimeConfig.workspaceRoot，当前 Chat 目录独立；library.collections[].editable 与 collection Run 授权链路已删除，旧字段在 YAML/API 输入拒绝。来源目录的文件/Bash 权限统一由 Workspace、hostAccess 与 AccessPolicy/HITL 决定，绑定/选库不增加读写根，容器挂载须显式配置。KBASE 专属的结构化文件附加保护已删除，文件工具对四种 mode 行为一致，先读后写只跟随 file-tools 全局配置。
- 测试以 `make test` / `go test ./...` 为主，协议变更优先覆盖 `internal/server`、`internal/stream`、`internal/llm`、`internal/tools`。

## 8. 开发流程

`catalog_manage.import` 为普通 Native root 提供 Skill/Skill 包与 Connector ZIP 导入；输入仅资源类型、ZIP 路径与覆盖意图，包身份、摘要和安装基准由后端识别并绑定审批。Handler 经中立 ToolApprovalInputPlanner 声明审阅输入，LLM 不识别 catalog 参数形状；来源读取审批先于包审阅，正式目标固定为 skills-center 或 connectors-center，连接器技能留在包内；复用 Catalog 目录事务、回滚与独立 CLI 准备，进程内收据随调用 Run 结束清理，不恢复未完成调用。契约见 [Platform 控制工具](docs/Platform控制工具设计.md#zip-包导入)。

本地开发：

```bash
cp .env.example .env
./scripts/sync-local-builtins.sh
make audit-workspace-chat
make run
make test
```

首次运行、更新相邻 builtin 或 release 前先同步本机 `build/builtins/<host>/` cache。linux/amd64 cache 由 `make sync-builtins-linux` 在固定构建容器内生成，容器内即原生 linux/amd64 主机，不放宽正式 lock 规则；linux/arm64 仍不支持。`make release-image` 从该 cache 构建 linux/amd64 镜像并产出离线部署包；`.dockerignore` 是白名单，新增需要进入镜像的路径必须显式放行。`make run`、`make build-local`、`make release` 只消费已校验的 cache，不得重新引入 builtin/Rust 构建。release 原样复制完整连接器包，不从 Platform 源码补写清单或技能；同步脚本不写 `release-local/`。

正式 lock 的 host 限制、精确 `yes`、干净版本、稳定归档固化与并发校验必须保留，交叉构建不得写其他平台 SHA。完整同步、发布命令与目标平台限制见 [版本化打包方案](docs/版本化打包方案.md#2-发布命令)；不要在此另行维护组件版本或平台清单。

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`AGENTS.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `agents/`、`skills-center/` 与 runtime 的外部 `connectors-center/` 是可编辑事实源；Platform 内置连接器及其技能随包只读，`builtin.*` 为平台保留命名空间；Agent 使用 `ru-agents/<key>/<revision>` 的进程内版本目录，普通 Skill 按内容摘要共享于只读 `ru-skills/<digest>`，Agent 的 skills 目录只保存引用；连接器继续使用既有 `ru-connectors/<id>/<contentDigest>` 机制。运行目录不提交、不打包、不允许人工编辑；启动清空重建 ru-agents 与 ru-skills，不跨重启保留历史或新增持久 pin。热重载发布新版本不等待旧 Run，租约绑定具体版本，旧版本无引用后回收；无效来源继续使用最近成功版本并报告未同步，来源删除拒绝新准入。Team 子任务与仍持有租约的执行器内续接继承冻结版本；规划 Run 已结束、通过延迟提交批准启动执行时使用当前发布版本；Platform 重启统一取消所有未完成 Run，清除等待项，保留聊天历史；下一次 Query 使用当前定义，不恢复历史执行或动态环境。Agent 版本整树与共享 Skill 全树复验、去除写权限、损坏拒绝新租约，仍在使用时不原地修复；静态 .config 只读，状态不得写入。Host 任意脚本仍不具备完整隔离。凭证与 CLI 配置目录留在 `.state/connectors`，init 安装的程序、准备记录和当前可用安装在持久的 `connector-installs/<id>`（不按 ru-* 清理），二者都不随 Agent 重建或删除。Query/steer 的目录授权例外是 skill 引用：所有选中 Skill 的 canonical 目录获得本 run trusted read + readonly roots；未配置 Skill 还必须从当前有效 skills-center catalog 重新验证；attachment.skills 开启时固定暴露 `@skills-center`。Container 去重后挂载整个 `/skills-center` 为只读，但未选中兄弟目录不获得免审读授权。该例外不合并额外 Skill 的 `.config`、`.runtime-env.json`，不增加 Tool/MCP/Agent hostAccess/accessLevel；TEAM 总控同样支持。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和隐藏的 `GET /api/resource` 数据面。新 Markdown 的 Chat 文件使用 `@chat/<relativePath>`，裸 `<relativePath>` 与之等价；Workspace 文件使用 `@workspace/<relativePath>`，也可引用普通 Agent Workspace 或冻结临时根内的实际 Host 绝对路径与 HTTP(S)/data/blob；别名由 WebClient 渲染时解析，Markdown 不使用 `@temp`、`@runtime`。真实 `/api/resource` 请求地址和 `<currentChatId>/<relativePath>` 都不是 Markdown 协议，历史 endpoint Markdown 不迁移且不再预览。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- 平台控制工具使用固定 action/args Schema 并要求受信任连接器挂载；run_env 使用独立 operation/params Schema，通过 preset 或 Agent 显式声明挂载，分发示例列入全局 preset-tools，可通过 excludeTools 排除。旧 platform-control 配置整段硬失败，run-env 只保留 deny-keys 与三个限额。
- HTTP MCP 的 mcpUrl 创建入口仅在 authMode 键缺失时写入 mcp，审批/摘要/落盘使用同一内容；管理端 import-mcp 同样缺省 mcp，允许显式 none，按主机名生成本地配置包并复用 ZIP 发布事务，同主机不覆盖；其他包必须显式声明 authMode。platform_inspect.checkConnector 仅普通 native 主 Run 显式调用，以独立短期 HTTP MCP 会话校验包、握手及工具发现，无需目标挂载、不注册运行时工具、不调用业务工具、不启动 stdio/CLI。连接器 apply/get 的授权快照保持本地读取，创建成功不受快照失败影响；需要登录时结束本轮，用户登录后回读及继续检查。详见 [平台控制连接器](docs/Platform控制工具设计.md)。
- Markdown Memory 代码缺省开启，分发 runtime.example.yml 显式关闭，部署需在 runtime.yml 开启，Agent 采集须显式启用 memoryConfig.enabled，上下文独立通过 contextConfig.tags 的 memory-global/memory-agent 选择；旧 SQLite/管理工具配置明确拒绝，不提供历史迁移。Owner 与长期记忆只在新 Native Run 读取；文件编辑无需 catalog 重载。
- 文件工具权限独立于 Bash 权限，普通越权路径通过 HITL approval 兜底；readonly、临时根逃逸与其他 hard block 不产生可放宽的 HITL。
- `AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR` 与 `AP_ACCESS_TOKEN` 为 Platform 保留变量。Agent/Skill/run.env/调用配置共用 `shellenv.UnsafeOverride`；技能 `.runtime-env.json` 的 PATH 只追加额外目录。Host 工具环境按 `bash.inherit-env` 名单继承，`SSH_AUTH_SOCK` 仅给 Git 网络操作，默认 Bash 无登录 profile。AP_ACCESS_TOKEN 仅在验证的 oneid 直接 CLI 和对应 MCP 身份链路即时注入，不进入普通 Host Shell。有效 StateDir 与 identity 文件三档均拒绝普通工具读写；完整隔离与敏感读取例外尚未落地，见 [AccessPolicy 与 HITL 边界](docs/AccessPolicy与HITL改造.md)。
- 四种 Native Agent 的 Workspace mutation 均受 editingMode 控制：显式 false 硬拒绝 Workspace mutation，当前 Chat 目录按 `@chat` 可读写；显式 true 时 Workspace mutation 在 shipped default policy 下免逐次 HITL。external 和其他 chatId 默认进入 HITL，`writeRoots`、hostAccess、`full_access` 或 approval 按通用策略作用于 external，不能放宽 editing=false 的 Workspace；管理员显式 block 仍优先。文件 mutation 不触发同步知识索引 hook，库 watcher 按 debounce 与 change set 异步刷新。
- MCP registry 同时支持 `streamable-http` 与 `stdio`，版本兼容范围由锁定的官方 SDK 校验：优先请求 `2025-11-25`，接受 `2025-06-18`、`2025-03-26` 和 `2024-11-05`，缺失、无效及未知版本仍拒绝并关闭连接。必须保留 SDK 原始 Connection，使协商版本、HTTP 协议头和 SSE 状态更新生效；日志记录实际协商版本。本地 YAML/重复 Key/transport 契约错误仍使启动或热重载硬失败；合法配置发布后，远端初始化、`tools/list` 与 availability 重试由单 worker 后台执行，`pending/syncing/unavailable` 不影响 Platform 基础健康。旧 external stdio 私有协议没有兼容期；`service.yml`、`type: external`、`external:` 或 `kind: external-service` 会使启动/热重载硬失败。平台、新版 stdio server 二进制和 registry 配置必须同批发布。
- `configs/tools.yml` 的 `mcp.tool-mode` 默认 false，已挂载 MCP 工具逐个进入模型工具列表；开启后改为平台加入的 `mcp_search`/`mcp_call`（仅在挂载了 MCP 组件时），工具 ID 为 `连接器/MCP 服务/工具名`，调用按 Run 冻结的挂载与当前 schema 复核。两个工具不能在 Agent 或 preset 中声明，只读门禁与 VIEW 跟随真实目标，见 [MCP 工具模式](docs/MCP与工具交互.md#mcp-工具模式)。
- `agent_invoke` 只允许显式配置的普通主 agent 使用，当前禁止嵌套；TEAM 的执行 Run 只自动注入 session-local embedded builtin `agent_delegate`，plan 工具来自普通配置或 preset。普通 Agent 配置、session 与执行入口均拒绝 `agent_delegate`，该工具也不进入公开工具 catalog。
- flat plan task 按数组顺序执行且同时最多一个 `in_progress`；最前面的非终态 task 可由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，`in_progress` 可进入任一终态，终态重试必须追加新 task。TEAM 的 plan task 表示顺序阶段，但当前阶段内部仍可通过单次 `agent_delegate` 按 `maxParallel` 并行执行成员。
- 中文“会话”和“对话”均指 Chat；“新开会话／对话”优先使用 `chat_send {newChat:true}`；旧 `chat_start` 保留并省略 `chatId`，未指定目标时可省略 `agentKey`，服务端从可信调用上下文补齐当前 Agent；只有继续用户指定的已有 Chat 才传 `chatId`。查询与中断仍按 `runId` 定位一次执行，不关闭或删除 Chat。
- `chat_start` / `chat_get_status` / `chat_interrupt` / `chat_steer` 只允许挂载 `builtin.task-control` 的主 Agent 根 run（含 TEAM） 使用，chat_start 仅按精确 catalog `agentKey` 启动独立根 run，省略时使用可信调用方 Agent，省略 accessLevel 时继承父 Run 受理当时的当前档位（后续不联动），显式同级或降级直接启动，显式高于父档位必须经父 Chat 的强制人工审批，并由 Runtime 在授权受理点按冻结基线消费一次性收据后才创建 Chat；`runQuery.allowAccessLevelOverride` 已移除，见 [子智能体调度](docs/子智能体调度.md)；不设目标白名单、深度/并发配置或 maxActiveRuns。status/interrupt/steer 只接受同一调用 Agent 与 subject 创建的 run，这些旧执行工具对派生根 Run 继续限制一层，新 mailbox 工具按跳数限制。 `chat_start` 只需 message；省略 agentKey 使用当前 Agent；目标为当前 Agent 时省略的 modelKey/reasoningEffort 逐字段继承调用方当前 Run 的有效值（关闭思考为 NONE，续聊同样适用），目标为其他 Agent 时不继承、使用目标自己的模型与默认思考强度，显式值仅覆盖对应字段；生效值纳入幂等与审批摘要并接受目标准入校验；目标 `interactionConfig` 只约束用户入口，不约束 `chat_start`/`chat_steer`，豁免仅限本次准入、不改 Run 冻结快照，审批、模型有效性与引用授权不变；`chat_query models` 只读列出本地 chat 模型，ACP 选项仍由 bridge 校验。
- Chat 创建后根 `agentKey` 固定。事件携带实际执行者 key，控制只认根 Run owner。成员历史隐藏其他执行者工具结果；总控保留自己的完整工具历史和成员最终结果。
- Team 成员、成员定义、协调器配置与 prompt 在 run 开始时解析为快照，运行中 catalog 热重载不改变该 run；下一次 run 才读取新快照。
- `/healthz` 报告 KBX 维护能力；单个库或 Agent 绑定失效不使平台返回 503，Run 与工具独立检查库是否就绪。
- 当前 KBASE 只对文本抽取结果做 embedding/FTS；PDF/DOCX/PPTX/HTML 均是先抽取文本，不得宣称支持图片、音频或视频语义检索。
- SQLite runtime store 使用 `application_id`（库类型）和 `user_version`（schema 版本）作为身份契约。仅在 `app.New` 启动装配期，`chats.db`、`archive.db` 的标记恰为 `0/0`，且表、列语义、约束、索引、触发器和 FTS 对象完整匹配当前 DDL 时，服务才会在事务中写入当前标记；列物理顺序不影响比较。运行期仅验证，绝不认领、迁移、删除或修复。其他标记组合或结构差异拒绝并阻止启动。KBX 数据合同由受管 CLI 校验。

客户端入口由后端决定：`interactionConfig.showPlanningMode/showEditingMode/showTerminal` 是 mode 提供默认值、agent.yml 可覆盖的展示开关，不做请求准入；默认规划入口四种 Native 都展示，编辑入口只有 KBASE 展示，终端入口只有 CODER 展示。编辑开关在客户端初始为关闭，展示时显式发送、隐藏时省略并由后端取 Agent 默认。WebClient 不得按 mode 或 engine 判断入口，项目入口按 `workspaceDir` 判断。`activeRun` 返回本次 Run 的 planningMode 与 editingMode 供刷新后还原，`request.query` 中的 editingMode 供同进程续接与计划批准后的执行使用；Platform 重启不恢复未完成 Run。

`/api/agent` 的 tools、skills、connectors 均为 ID 数组：tools/skills 反映已发布运行时，connectors 反映已保存的非预置挂载；不返回技能展示对象或 toolBindings，后者仅由管理详情与保存响应返回。连接器使用与管理接口分离：使用接口仅返回前端展示和选择所需字段，并过滤平台预置；管理接口保留完整信息，预置挂载只读。隐藏目录不改变运行时自动挂载，非预置 builtin 仍可选择。接口和错误契约见 [连接器](docs/连接器.md#使用目录与管理目录)。

连接器使用目录、Agent 关联读取与单项切换共用 HTTP/主 WS 业务逻辑和精简 DTO；`/api/connectors` 提供本地 readiness、MCP 同步快照与 Agent 范围的 reloadPending，读取不执行 CLI 或主动探测上游。旧挂载读取保留兼容，WebClient 复用 `/api/agent.connectors`；WS 展示使用连接语言，写入字段出现即按 mutation 校验，不将 false/null 或不完整写入当成读取。管理和认证接口保持 HTTP。

原生连接器仅通过 `connector.json` 的 `type: native` 与已注册 ID 识别，工具归属与 VIEW 查询操作归属以 `internal/connector/native.go` 为唯一事实源；不再维护 `native.json` capabilities 声明，旧文件明确拒绝。

五个内嵌原生连接器 platform-control/task-control/kanban-control/kbase-control/web-control 从 `0.4.0` 开始使用 Platform 版本系列，Platform 发布时统一维护其源码 `connector.json` 版本；不进入外部 builtin/connector lock。dbx/httpx 保持独立项目版本。

Desktop 原生连接器不属于外部 builtin 构建缓存，不要求 `sync-local-builtins`，修改其源码资源后正常 `make run-local` 即可生效。`builtin.httpx`、`builtin.dbx` 和其他外部可执行组件仍按既有流程准备、校验缓存。旧缓存中的 Desktop 条目仍接受完整性校验，但应用装配始终选择当前程序内嵌版本；发布阶段从已校验的输出副本移除该旧条目，不改原缓存。运行时资源导入校验复用相同内嵌装配流程。

- 连接器锁统一放入 runtime `.lock/`：`shared-connector-layout.lock` 保护共享目录初始化，`connectors/assembly.lock` 协调装配与回收，`connectors/install/<id>.lock`、`connectors/operations/<id>.lock`、`connectors/leases/<id>/<digest>.lock` 分别保护共享包安装、来源/准备/授权操作与版本租约。路径直接切换，不兼容旧锁路径；更新前停掉同一 runtime 的旧进程。锁释放后保留，不在运行中删除。`ru-connectors/.shared-v1` 仅作布局标记，不再执行旧 ru-connectors 的备份、退役或兼容迁移。

内置工具名保持小写及下划线，平台自有输入字段统一 camelCase；旧参数名在模型准备与工具调用边界明确拒绝，不做别名转换。图片来源使用 sourceType: referenceName/filePath。内嵌及 agent-local 工具定义只接受 inputSchema，parameters 硬失败。协议透传、上游 Images response_format 与存储列名保持原契约；历史不改写，拒绝的旧文件写入参数仍需脱敏。详见 [工具输入命名](docs/MCP与工具交互.md#工具输入命名)。

工具 YAML 不再接受顶层 label，仅支持展示用 `i18n.{en,zh-CN}.{label,description}`；内嵌工具仅配置 label 翻译，不配置 i18n.description，原始及 Schema description 保持英文。模型工具定义不带 label/翻译表。Native tool.start/snapshot/result 冻结内部展示快照，HTTP/WS/回放/导出按查看者语言解析并移除翻译表；旧历史不迁移。Desktop 在建立连接时同步全局语言，设置切换后通过 /api/locale 更新已连接通道；WS 响应与流展示统一读取当前连接语言，不冻结展示语言。Native 模型提示词单独冻结 Run 启动时的语言，配置与恢复规则见 [Agent 配置合并](docs/Agent配置合并.md#runtime-context-语言与模板)。见 [工具展示多语言](docs/MCP与工具交互.md#工具展示多语言)。


`ask_user_form` 是按需显式声明的独立 HTML 输入表单工具，不进入全局预设；固定使用 builtin/ask_user_form 的 form 协议，无授权语义、不支持重启恢复，BTW 禁用，planning/execute 默认排除。HTML/CSS/SVG 共享策略归 `internal/formhtml`，由内置 VIEW 注入；工具交互归 `internal/toolinteraction`，提交在唤醒前校验类型与大小，见 [MCP与工具交互](docs/MCP与工具交互.md#独立输入表单-ask_user_form)。

## 特色功能文档索引

完整分类见 [文档索引](docs/README.md)，统一维护当前规范、未完成能力、验证记录和历史归档的入口。

开发常用：[智能体执行接口](docs/智能体执行接口.md)、[Runtime 模块边界](docs/Runtime模块边界.md)、[智能体配置](docs/智能体配置说明.md)、[工具目录权限](docs/工具目录权限.md)、[API 与协议](docs/API与协议.md)、[版本化打包](docs/版本化打包方案.md)。

待实施设计：[云端部署与持续任务架构设计](docs/云端部署与持续任务架构设计.md)，覆盖完整 Linux 镜像、用户实例、身份与 HTTPS、云端 Job 和 Desktop 离线续跑；不改变当前重启取消 Run 等事实与约束。

`builtin.task-control`（任务管理）独立提供九个 Chat 工具和两个 Automation 工具；`chat_steer` 不审批、不比较双方档位，同进程同调用冻结首次结果（含未受理与 unknown），收据随调用方 Run 结束清理（在途调用完成后释放）；确定未发送/明确拒绝为 not_started，SSE 传输或响应不确定为 unknown；由调用身份派生 steerId；Runtime 写入可选 request.steer.origin，旧 steer 来源不进入模型输入、不传上游；mail 的信封头只在模型输入边界生成。`builtin.platform-control` 不再提供会话和自动化工具。任务管理不包含 Desktop 看板或网页控制；迁移与权限边界见 [连接器](docs/连接器.md#task-control-任务管理)。

自动化行为、自然语言时间/Cron 与 YAML 契约归 task-control 的按需 references；Provider/Model 和外部连接器组件配置维护参考归 platform-control。Skill 不增加工具或文件权限，不应恢复外部 platform-admin/platform-automation 的重复路由。具体自动化更新与时区语义见 [自动化](docs/自动化.md#task-control-管理工具)。

`builtin.kbase-control`（知识库检索）独立提供 `kbase_search/kbase_files/kbase_read/kbase_status` 四个只读工具及配套 Skill，不依赖 Desktop；仍读取 Agent 的固定 `kbaseConfig.libraryId` 绑定，库管理工具留在 `builtin.platform-control`，详见 [连接器](docs/连接器.md#kbase-control-知识库检索)。

`builtin.kanban-control`（看板控制）独立提供 `desktop_kanban` 的六个看板动作，依赖 Desktop；原 `builtin.platform-control` 挂载不再授予看板能力。需要看板的 Agent 应显式挂载新连接器，详见 [连接器](docs/连接器.md#kanban-control-看板控制)。

管理工具归属：`/api/admin/tools` 与 Agent 管理详情的 `toolBindings` 仅展示独立工具；原生连接器工具及 MCP 工具由 `/api/admin/connectors` 每项的 `tools` 提供名称、说明与路由标识。`catalogVisible:false` 仍限制独立目录，不阻止在所属连接器中查看详情；不改变原始 definition、运行时工具集合及挂载权限。通用 bash/file_read 依赖不视作连接器所属工具。

管理详情及管理端创建、修改、改名响应不返回顶层 tools，独立工具信息统一由 toolBindings 表达；definition.toolConfig 保持原始编辑语义。使用端 /api/agent.tools 继续返回运行时工具 ID 数组。

- Query/steer、chat_start、agent_invoke 与 Automation 使用统一 references。定位、授权、累计限额、压缩和恢复边界以 [引用协议](docs/引用协议.md) 为准；选中资源不注入 system prompt，旧独立选择字段出现即拒绝。

非 Native 执行后端（ACP/PROXY/CHANNEL）不支持本地 skill/kbase/chat 定位引用，query 与 steer 均拒绝；attachment 的对应入口始终关闭，显式配置不扩大后端能力。公开请求 DTO 与工具/Automation query 拒绝未知字段；历史事件按内部快照读取，不套用外部请求校验。
