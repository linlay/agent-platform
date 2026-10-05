# AGENTS.md

## 1. 项目概览

本仓库是 Agent Platform 的 Go 运行时，提供 HTTP/SSE 与 WebSocket 接口、目录驱动的 Agent/Team/Skill/Connector、工具执行、会话持久化、Markdown Memory 和 KBASE 检索。

普通内置类型为 GENERAL、CODER、KBASE；`engine` 缺省 native，显式 acp 使用外部 bridge，不由 acpBridgeId 推断。TEAM 仅用于内部隐藏协调器。历史 REACT 只兼容读取，新配置与 API 输入使用 GENERAL。

本文保留开发入口、模块边界和必须遵守的约束；功能与接口细节以文末专题索引为入口。未实现或未经目标环境验证的能力不得写成已交付。

Memory 由 Platform worker 调用 memx 维护 summary 与 daily；知识库读取使用受管 KBX CLI，索引 update/refresh 尚未接通。当前范围见 [记忆系统](docs/记忆系统.md) 与 [KBX 接入](docs/KBX接入.md)。

## 2. 技术栈

- 语言：Go
- HTTP：标准库 `net/http`
- 序列化：标准库 `encoding/json`
- 存储：本地文件系统 + Markdown memory + SQLite control store + 本地 LanceDB KBASE generation
- 配置：环境变量 + `configs/*.yml`

当前没有引入 Web 框架、第三方路由库、外部数据库或消息队列。Go 主程序仍以 `CGO_ENABLED=0` 构建；知识库通过受管 KBX CLI 读取，旧 `kbase-lance-engine` 执行链路已断开；KBX 已接入正式分发链路，索引维护协议尚待接通。配置默认值以 `internal/config/config.go` 与 `configs/*.example.yml` 为事实源。

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
- `internal/kbx`：当前知识库执行门面，使用受管 CLI 及 chunk/evidence 协议；维护连接基础已实现，但未来 update 协议尚未接到生产。
- `internal/kbase`：保留的旧引擎与共享配置/DTO/工具权限门面，生产 app 不再构造其 Manager；旧实现说明：`Manager` 只作为公开门面和组件装配点，内部由 capability resolver/state、storage validator/auditor、watch/lifecycle supervisor、refresh coordinator、generation service、query/status/files service 与 Lance runtime 分别维护配置解析、存储契约、调度、索引/恢复、检索和 sidecar 生命周期。app adapter 只向 Manager 暴露 enabled capability，`AgentSpec.WorkspaceRoot` 是唯一内容根事实；未启用与不存在统一按 not found 处理。该包同时维护公共 prompt、HTTP 业务错误与五个工具 handler；不得 import `internal/agent` 或 `internal/catalog`。
- `internal/agent/team`：内部 TEAM profile、硬编码调度规则、成员 roster prompt、session-local 隐藏工具与调度状态机；TEAM 不能配置成普通 agent。
- `internal/runtime`：HTTP/WS 无关的 Query 与 Run 应用运行时；`types` 保存内部命令和结果，`query` 实现普通/旁聊 Query 准入、根 Run 注册/控制、Native 阻塞与异步启动、continuation 仲裁及重启 awaiting 对账，`session` 统一构造根/子 Agent/Team 的执行上下文和 system-init，`catalogview/reference` 承接租约快照与引用物化；`runstate` 持有活动 Run、observer、compact 协调与恢复等待项的唯一内存存储实现，`runexec` 执行 Native 生命周期、usage/终态落盘和 freeze 收尾，`orchestration` 执行子 Agent/Team 调度与结果回注。App 直接组装以上组件，不再反向注入 Server Native 方法。`adapter` 仅适配旧执行器/catalog DTO；受管根 Proxy 的上游 SSE/WS/channel 驱动归 `proxy.Driver`，公共收尾及 recorder/usage 归 `runexec`；异步与阻塞调用统一注册并后台执行，阻塞结果直接来自完成记录，前台观察者不控制上游接收。Server 经显式 ProxyPort 保留响应与 channel 适配、路由配置和控制转发；旧阻塞 SSE 入口已移除，ProxyPort 仍保留路由与控制适配。Runtime 不得依赖 `internal/server`；边界与集成注意见 [Runtime模块边界](docs/Runtime模块边界.md)。
- `internal/runops`：显式挂载的 `run_query` / `run_status` / `run_interrupt` named handler、调用方/subject 所有权、父 run/tool ID 幂等与禁止链式调用；直接依赖 `internal/runtime` 的窄接口，不经过 Server。
- `internal/platformcontrol` 维护平台控制操作；`internal/runenvops` 维护独立 run_env handler；`internal/runenv` 保存进程内 Scope、revision、限额及摘要幂等收据；`internal/toolpolicy` 提供中立操作调度属性。
- `internal/server`：HTTP/WS 解码、鉴权、响应映射、SSE flush 和迁移期薄适配；不得直接依赖 `llm`、`tools` 或具体 Agent mode。
- `internal/conversation`、`internal/adminsource`、`internal/chatresource`：分别承接会话/归档编排、管理端源码 mutation 并发事务、Chat 资源解析与 mutation 边界。
- `internal/llm`：prompt 构建、run stream、HITL、planning、tool loop；Provider HTTP 打开、首响应超时和响应分类由 `internal/modelclient` 承接。
- `internal/tools`：通用 tool registry/router、Bash、FileTools、memory、desktop、MCP tool 调用；mode 工具通过命名 handler 接入，不在 executor 中增加 mode switch。
- `internal/chat`：chat 摘要、事件、StepLine、raw messages、资源文件、归档、回放。
- `internal/memory`：个人 Markdown 文件、版本冲突、日期文字查询与上下文记录原则；不持有数据库或知识索引。
- `internal/memoryworker`：已完成 Chat 的增量调度、模型连接配置同步、memx 子进程协议和手工触发；不自行生成记忆文件，不依赖 Server。
- `internal/view`：VIEW 展示定义、声明资源、远端模板获取和 Chat 内容寻址快照；无 Tool 执行或 HITL 决策职责。VIEW 与 MCP/CLI 组件可组合，纯 VIEW 不授予 Bash/PATH。
- `internal/connector`：中立连接器包/JSON/技能与 assets 图标结构校验、ZIP 原子导入、Agent PATH 合并与定义编辑；旧包/凭据/MCP 目录迁移代码及 connector-migrate 命令已移除；`internal/connectormigrate` 仅保留独立 Desktop 工具声明调整。MCP 通过统一 Sources 读取 Platform 内置包和 runtime/connectors-center 外部原包，执行读取 Agent 挂载引用指向的 ru-connectors/<id>/<contentDigest>，MCP 按 Agent/连接器/组件/内容版本建立独立实例，旧 registries/mcp-servers 目录直接忽略；`internal/connectorauth` 负责部署级 token 保存/退出、null 模式显式受管 CLI 准备/扫码、普通 OAuth 授权码与 MCP OAuth 发现、PKCE、loopback 回调和持久化刷新；oneid-token 复用 Desktop identity-file，按调用环境注入 AP_ACCESS_TOKEN，HTTP MCP 按同一来源生成 Bearer Header，不复制 SSO 凭据到连接器状态目录。auth_bindings 声明包外凭证的 HTTP/Host CLI/stdio 消费映射；MCP 支持多资源 grant、客户端注册信息、元数据回退及追加授权。认证状态按秒驱动 MCP Registry 更新，不触碰包文件或重建 Agent。通用执行按连接器/adapter 授权，直接传 argv 或 MCP 原生工具参数，不注册业务 operation/profile；通用执行与 Agent/管理接口共用当前部署的连接器凭据，不提供多租户凭据隔离；通用 runtime 安装尚未实现，见连接器专题。
- `internal/catalog`：agent / team / skill / tool 目录装载与定义解析；Team 只接受目录式 orchestrated 定义，并以原子快照冻结成员、协调器配置和 prompt。
- `internal/config`：环境变量、YAML、默认值。
- `internal/httpclient`：Platform 出站 HTTP 客户端工厂、显式/环境/系统固定代理解析与缓存刷新；内部服务使用直连客户端，不修改标准库全局 Transport 或进程环境；默认 `auto` 在 Windows/macOS 上跳过 PAC/WPAD 并在无适用固定代理时直连；显式 `pac_auto` 启用 Windows WinHTTP PAC/WPAD（企业网络待目标系统验证），仅无显式 PAC 的 WPAD 发现返回 12180 时按无代理直连；解析失败标记 `proxy=unresolved`，DNS 与 Windows 网络错误提供脱敏分类。macOS PAC/WPAD 尚未实现，`pac_auto` 命中不支持的自动代理时报错。
- `internal/stream`：统一事件、dispatcher、assembler、normalizer 与 EventBus；SSE writer 属于 `internal/server` 传输层。
- `internal/sandbox`：Container Hub client、mounts、sandbox 执行。
- `internal/automation`：automation 注册、调度、执行记录；`query.accessLevel` 保存每次触发的初始权限，省略为 default，定时与手动触发一致，不继承 Chat 历史权限。
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

Memory 默认由 `AP_RUNTIME_MEMORY_DIR` 控制，以 summary.md 和 daily/YYYY-MM-DD.md 为内容源；用户资料位于 owner/OWNER.md。memx 管理收据及恢复日志，Platform worker 进度位于 `.state/memory-worker`。旧 memory.md 仅在 summary 缺失时复制并保留备份；旧数据库不迁移。

KBX 新索引使用 `AP_RUNTIME_KBASE_DIR/<agentKey>/kbx/<scopeHash>/index.sqlite`（及 KBX 配套存储）；workspace 模式用 `.kbx-platform/<agentKey>/<scopeHash>/`。以下旧 KBASE 数据仅保留，不再由运行入口访问：

- `control.db`：schema v4 控制面，记录 generation、文件状态、file operation、增量 refresh 指标和 index run；不保存 chunk、FTS 或 embedding。control 与 Lance schema 版本独立；SQLite 控制面只接受当前 schema，绝不原地迁移。
- `generations/<generationId>/lance/`：LanceDB chunks table 及索引；同级 `manifest.json` 保存 generation 元数据。

核心 DTO 位于 `internal/api`，包括 query、submit、steer、interrupt、chat、upload、automation、Markdown memory 等请求和响应类型。

## 6. API 定义

非 SSE JSON 统一使用 `{code,msg,data}` 包裹，成功 `code:0`。HTTP/WS 参数、路由、流事件与错误语义统一查阅 [API 与协议](docs/API与协议.md)；人工交互查阅 [HITL 协议](docs/HITL协议.md)。不要在此重复维护接口清单。

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
- AWCP 遵循网站手册渐进披露：固定工具 `awcp_manual` 返回目录/章节说明，章节请求携带 `section` 与 `revision`，页面通过 `surfaceId` 在 Run grant 内选择，通用 `awcp_invoke` 接收内联 `revision/action/args` 或互斥的 `paramsFile`；文件仅含这三个字段，复用 CDP 文件权限、审批和大小限制，wire 只发送解析后的 JSON。网站说明只作为工具结果，`internal/llm` 不得加入 AWCP 专属状态、动态 Schema、纠错预算或调度分支；授权页面与业务校验留在工具/Desktop/网站边界。详见 [MCP与工具交互](docs/MCP与工具交互.md)。
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

首次本地运行、更新相邻 builtin 项目或执行 `make release` 前，先执行 `./scripts/sync-local-builtins.sh`；它每次在隔离工作目录中重新构建本机 `dbx`、`httpx`、`kbx`、`memx` 和 `poppler-pdftotext` launcher/archive，并原子更新 `build/builtins/<host>/`。Poppler native runtime 是校验后重新打包的预编译 payload，不在 platform 中编译；`rg` 是唯一只校验复制的 vendor artifact。同步按各本地项目的 `VERSION` 生成临时 lock；Shell 与 PowerShell 在 cache 激活后使用同一正式 lock 状态机。schema v2 的组件 `version/commit/source` 是全平台目标 release，target 同名字段与 `path/sha256` 是该平台实际 release。精确 native host 上严格更高的干净版本经一次精确 `yes` 可抢占为新目标；其他平台的本地 VERSION/Git HEAD 匹配目标并验证成功后自动更新自己的 target。交叉构建只更新 cache，任何 runner 都不得写其他平台 SHA；同版本不同 commit/SHA、dirty、降级、checkout 不匹配或非交互 leader 均不回写。正式写 lock 前必须先将验证 archive 原子固化到相邻项目的稳定 `dist/<version>/`，同路径不同 SHA 必须拒绝；并发 lock 变化同样放弃写入。`--all` 仅为 canonical lock 声明的 Poppler 目标构建，当前为 darwin-arm64 与 windows-amd64，且正式 lock 仍只允许精确 host target 跟随。同步脚本不写 `release-local/`；`make run` / `make build-local` / `make release` 不得重新引入 builtin 或 Rust 构建步骤；运行和 release 从本机 build cache 使用 builtin 二进制；release 原样复制已校验的完整连接器包，保持资源及树哈希不变，不从 Platform 源码补写清单或技能。

涉及文档、配置或目录规范调整时，同步检查 `README.md`、`AGENTS.md`、`docs/` 与 `.gitignore`。

## 9. 已知约束与注意事项

- `configs/` 下配置启动时读取，运行中修改需要重启 runtime。
- `agents/`、`skills-center/` 与 runtime 的外部 `connectors-center/` 是可编辑事实源；Platform 内置连接器及其技能随包只读，`builtin.*` 为平台保留命名空间；Agent 配置内普通 Skill、Terminal 与常规 Skill runtime 使用 Platform 生成的 `ru-agents/`；连接器完整包、技能和 bin 使用本 Agent 的 `ru-connectors/<id>/<contentDigest>`，共享包位于独立 `ru-connectors/`，Agent 目录只保存挂载引用。运行目录不提交、不打包、不允许人工编辑；Platform 启动清空并完整重建 `ru-agents`。热重载先组装候选，普通 Agent 内容整目录发布，连接器版本独立发布；活动 Run、子调用、Team 成员和 Terminal 的租约阻止本 Agent 普通文件替换；仅待发布变更、删除/失效、Agent 加载或绑定阶段失败、旧连接器版本待回收时在最后一个使用者结束后重载；前置校验失败仅保留原待处理标记，无关重载不标记待处理，其他 Agent 独立发布；本地 MCP 绑定完成后才准入新租约。凭证与受管 CLI 状态留在 `.state/connectors`，不随 Agent 重建或删除。唯一的 query 运行时例外是普通 Agent 的非空 `mustUseSkills`：所有选中 Skill 的 canonical 目录获得本 run trusted read + readonly roots；未配置 Skill 还必须从当前有效 skills-center catalog 重新验证，并按需暴露 `@skills-center`。Container 去重后挂载整个 `/skills-center` 为只读，但未选中兄弟目录不获得免审读授权。该例外不合并额外 Skill 的 `.config`、`.runtime-env.json`、`.bash-hooks`，不增加 Tool/MCP/Agent hostAccess/accessLevel；Team 明确拒绝。
- `POST /api/query` 默认逐事件 flush；启用 `configs/runtime.yml -> h2a.render.*` 缓冲后，客户端看到的输出可能不再逐事件抵达。
- WebSocket 是控制面，浏览器/普通客户端文件字节仍走 `POST /api/upload` 和隐藏的 `GET /api/resource` 数据面。新 Markdown 的 Chat 文件只使用相对于当前 Chat 的 `<relativePath>`，也可引用普通 Agent Workspace 或冻结临时根内的实际 Host 绝对路径与 HTTP(S)/data/blob；Markdown 不使用 `@temp`。真实 `/api/resource` 请求地址和 `<currentChatId>/<relativePath>` 都不是 Markdown 协议，历史 endpoint Markdown 不迁移且不再预览。
- `runtimeConfig.env` 不会通过 catalog API 回显，避免泄露代理、凭据或私有 endpoint。
- 平台控制工具使用固定 action/args Schema 并要求受信任连接器挂载；run_env 使用独立 operation/params Schema，普通 Native GENERAL/CODER/KBASE 默认挂载，可通过 excludeTools 排除。旧 platform-control 配置整段硬失败，run-env 只保留 deny-keys 与三个限额。
- Markdown Memory 全局默认开启，Agent 显式启用；旧 SQLite/管理工具配置明确拒绝，不提供历史迁移。Owner 与长期记忆只在新 Native Run 读取；文件编辑无需 catalog 重载。
- 文件工具权限独立于 Bash 权限，普通越权路径通过 HITL approval 兜底；readonly、临时根逃逸与其他 hard block 不产生可放宽的 HITL。
- `AP_AGENT_CONFIG_HOME`、`AP_WORKSPACE_DIR`、`AP_CHAT_DIR` 与 `AP_ACCESS_TOKEN` 为 Platform 保留变量。Agent/Skill/run.env/调用配置共用 `shellenv.UnsafeOverride`；技能 `.runtime-env.json` 的 PATH 只追加额外目录。Host 工具环境按 `bash.inherit-env` 名单继承，`SSH_AUTH_SOCK` 仅给 Git 网络操作，默认 Bash 无登录 profile。AP_ACCESS_TOKEN 仅在验证的 oneid-token 直接 CLI 和对应 MCP 身份链路即时注入，不进入普通 Host Shell。有效 StateDir 与 identity 文件三档均拒绝普通工具读写；完整隔离与敏感读取例外尚未落地，见 [AccessPolicy 与 HITL 边界](docs/AccessPolicy与HITL改造.md)。
- 专用 KBASE 未开启 editing 时 Workspace 可读但不可 mutation，当前 Chat 目录仍按 `@chat` 可读写；开启后 Workspace mutation 在 shipped default policy 下免逐次 HITL。external 和其他 chatId 默认进入 HITL，`writeRoots`、hostAccess、`full_access` 或 approval 可按通用策略放宽；这些授权不能放宽非 editing KBASE Workspace，管理员显式 block 仍优先。Workspace mutation 不触发同步索引 hook，KBASE watcher 按 debounce 与 change set 异步刷新。
- MCP registry 同时支持 `streamable-http` 与 `stdio`，版本兼容范围由锁定的官方 SDK 校验：优先请求 `2025-11-25`，接受 `2025-06-18`、`2025-03-26` 和 `2024-11-05`，缺失、无效及未知版本仍拒绝并关闭连接。必须保留 SDK 原始 Connection，使协商版本、HTTP 协议头和 SSE 状态更新生效；日志记录实际协商版本。本地 YAML/重复 Key/transport 契约错误仍使启动或热重载硬失败；合法配置发布后，远端初始化、`tools/list` 与 availability 重试由单 worker 后台执行，`pending/syncing/unavailable` 不影响 Platform 基础健康。旧 external stdio 私有协议没有兼容期；`service.yml`、`type: external`、`external:` 或 `kind: external-service` 会使启动/热重载硬失败。平台、新版 stdio server 二进制和 registry 配置必须同批发布。
- `agent_invoke` 只允许显式配置的普通主 agent 使用，当前禁止嵌套；orchestrated Team 自动注入 session-local embedded builtin `agent_delegate` 和三个 plan tools。普通 Agent 配置、session 与执行入口均拒绝 `agent_delegate`，该工具也不进入公开工具 catalog。
- flat plan task 按数组顺序执行且同时最多一个 `in_progress`；最前面的非终态 task 可由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，`in_progress` 可进入任一终态，终态重试必须追加新 task。TEAM 的 plan task 表示顺序阶段，但当前阶段内部仍可通过单次 `agent_delegate` 按 `maxParallel` 并行执行成员。
- `run_query` / `run_status` / `run_interrupt` 只允许分别显式配置的普通主 Agent 根 run 使用，query 按精确 catalog `agentKey/teamId` 启动独立根 run，省略 accessLevel 时继承父 Run 调用当时的当前档位（不受显式覆盖开关限制，后续不联动）；不设目标白名单、深度/并发配置或 maxActiveRuns。status/interrupt 只接受同一调用 Agent 与 subject 创建的 run，目标 run 禁止再次调用任一 run 工具。旧 `agent_run_query`、`agent_run_status`、`agent_run_interrupt` 已删除且配置引用会硬失败。
- chat 创建后 `teamId` 固定。Team 以 `teamId` 为公开 owner，`agentKey` 不得与 Team 请求或控制请求同时出现；隐藏协调器 key 只用于进程内执行，不得作为公共 Agent 身份回显。
- Team 成员、成员定义、协调器配置与 prompt 在 run 开始时解析为快照，运行中 catalog 热重载不改变该 run；下一次 run 才读取新快照。
- KBASE Lance sidecar 只监听 loopback，由 Go 生成一次性 Bearer token 并监督生命周期。存在 enabled KBASE capability 时会启动并探测 sidecar；`mode: KBASE` 将其标为 required，故障使健康检查失败，普通 Agent 附加能力将其标为 optional，故障只在 `/healthz` 和 capability 状态中报告 degraded。无 active generation 时 search 返回 stale 并触发冷建，sidecar 故障显式返回 unavailable。
- 当前 KBASE 只对文本抽取结果做 embedding/FTS；PDF/DOCX/PPTX/HTML 均是先抽取文本，不得宣称支持图片、音频或视频语义检索。
- SQLite runtime store 使用 `application_id`（库类型）和 `user_version`（schema 版本）作为身份契约。仅在 `app.New` 启动装配期，`chats.db`、`archive.db`、KBASE `control.db` 的标记恰为 `0/0`，且表、列语义、约束、索引、触发器和 FTS 对象完整匹配当前 DDL 时，服务才会在事务中写入当前标记；列物理顺序不影响比较。运行期仅验证，绝不认领、迁移、删除或修复。其他标记组合、结构差异或残留旧数据均拒绝；chat/archive 会阻止启动，required KBASE capability 会隔离对应 Agent 并保留管理端诊断，引用它的 Team 同样不可运行；optional capability 保留普通 Agent 可运行并报告 degraded/unavailable。

Desktop 原生连接器不属于外部 builtin 构建缓存，不要求 `sync-local-builtins`，修改其源码资源后正常 `make run-local` 即可生效。`builtin.httpx`、`builtin.dbx` 和其他外部可执行组件仍按既有流程准备、校验缓存。旧缓存中的 Desktop 条目仍接受完整性校验，但应用装配始终选择当前程序内嵌版本；发布阶段从已校验的输出副本移除该旧条目，不改原缓存。运行时资源导入校验复用相同内嵌装配流程。

- 连接器锁统一放入 runtime `.lock/`：`shared-connector-layout.lock` 保护共享目录初始化，`connectors/assembly.lock` 协调装配与回收，`connectors/install/<id>.lock`、`connectors/operations/<id>.lock`、`connectors/leases/<id>/<digest>.lock` 分别保护共享包安装、来源/准备/授权操作与版本租约。路径直接切换，不兼容旧锁路径；更新前停掉同一 runtime 的旧进程。锁释放后保留，不在运行中删除。`ru-connectors/.shared-v1` 仅作布局标记，不再执行旧 ru-connectors 的备份、退役或兼容迁移。

内置工具名保持小写及下划线，平台自有输入字段统一 camelCase；旧参数名在模型准备与工具调用边界明确拒绝，不做别名转换。图片来源使用 sourceType: referenceName/filePath。内嵌及 agent-local 工具定义只接受 inputSchema，parameters 硬失败。协议透传、上游 Images response_format 与存储列名保持原契约；历史不改写，拒绝的旧文件写入参数仍需脱敏。详见 [工具输入命名](docs/MCP与工具交互.md#工具输入命名)。

工具 YAML 不再接受顶层 label，仅支持展示用 `i18n.{en,zh-CN}.{label,description}`；内嵌工具仅配置 label 翻译，不配置 i18n.description，原始及 Schema description 保持英文。模型工具定义不带 label/翻译表。Native tool.start/snapshot 冻结内部展示快照，HTTP/WS/回放/导出按查看者语言解析并移除翻译表；旧历史不迁移。Desktop 在建立连接时同步全局语言，设置切换后通过 /api/locale 更新已连接通道；WS 请求与流统一读取当前连接语言，不保存请求或 Run 级语言。见 [工具展示多语言](docs/MCP与工具交互.md#工具展示多语言)。


## 特色功能文档索引

| 主题 | 文档 |
| --- | --- |
| 配置与 Agent | [配置化说明](docs/配置化说明.md)、[智能体配置说明](docs/智能体配置说明.md)、[技能展示元数据](docs/技能展示元数据.md) |
| Runtime 与执行目录 | [Runtime 模块边界](docs/Runtime模块边界.md)、[Agent 运行时组装](docs/Agent运行时组装.md)、[运行时和沙箱](docs/运行时和沙箱.md) |
| 权限与审批 | [工具目录权限](docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](docs/AccessPolicy与HITL改造.md)、[Bash 审批排查](docs/Bash审批卡住排查.md)、[鉴权与安全边界](docs/鉴权与安全边界.md) |
| 接口与流 | [API 与协议](docs/API与协议.md)、[HITL 协议](docs/HITL协议.md)、[真流式和 H2A](docs/真流式和H2A.md)、[Gateway 接出注册](docs/Gateway-Agent注册与调用协议.md) |
| 模型与网络 | [Responses 协议](docs/Responses协议.md)、[HTTP 客户端与系统代理](docs/HTTP客户端与系统代理.md) |
| 连接器 | [连接器](docs/连接器.md)、[安装与授权](docs/连接器安装与授权.md)、[执行协议](docs/连接器执行协议.md)、[共享包与 Desktop 迁移](docs/连接器共享包与Desktop迁移.md)、[VIEW](docs/VIEW连接器.md) |
| 工具与调度 | [MCP 与工具交互](docs/MCP与工具交互.md)、[平台控制连接器](docs/Platform控制工具设计.md)、[参数错误与恢复提示](docs/工具参数错误审计.md)、[Run 环境工具](docs/Run环境工具.md)、[原生等待工具](docs/原生等待工具.md)、[子智能体调度](docs/子智能体调度.md)、[自动化](docs/自动化.md) |
| 会话与记忆 | [会话存储与回放](docs/会话存储与回放.md)、[记忆系统](docs/记忆系统.md) |
| KBASE | [KBX 接入](docs/KBX接入.md)、 [检索与控制面](docs/KBASE-LanceDB检索与控制面.md)、[编辑模式](docs/KBASE编辑模式.md) |
| 构建与运维 | [版本化打包](docs/版本化打包方案.md)、[运行时资源迁移](docs/运行时资源迁移.md)、[Windows Git Bash](docs/WindowsGitBash实施进度.md)、[手工测试用例](docs/手工测试用例.md) |
