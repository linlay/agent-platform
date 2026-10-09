# 平台控制连接器

`builtin.platform-control` 是 Platform 内嵌、只读、无需认证的 native 连接器。显式挂载后导入 9 个工具和 `platform-control` 技能，不自动授予 Bash。仅声明工具名不能获得连接器执行授权。`run_env` 保持独立。管理连接器主要由部署者为平台主智能体显式配置；源码不包含主智能体名单，不从业务类型或旧工具声明推导管理授权。

| 工具 | action | 执行环境 |
| --- | --- | --- |
| catalog_query | resourceTypes/list/get/defaults/validate | 普通 native main root |
| catalog_manage | apply/delete | 普通 native main root |
| platform_inspect | runtimeStatus/securityExplain | native，支持受信任子任务/Team |
| desktop_shell | 9 个外壳动作 | Desktop |
| desktop_settings | 16 个设置动作 | Desktop |
| desktop_site | 6 个网站条目动作 | Desktop |
| desktop_webapp | 16 个应用动作 | Desktop |
| desktop_service | 12 个服务动作 | Desktop |
| desktop_market | 15 个市场动作 | Desktop |

全部工具使用固定 `{action,args}`，action 枚举来自 `internal/connector/control_actions.go`；顶层未知字段拒绝。Catalog/Chat/Automation/Inspect 还严格检查动作参数类型与字段。Desktop 保留动作字段校验；日常管理的确认由 Platform 接管，重影响动作继续使用 Desktop 确认，适配器添加 `desktop.` 传输前缀。`agent.update`、`skill.update` 不再暴露给模型。不同工具的 action 不互相兼容。

平台、任务与看板连接器的 `action/args` 管理工具，`description` 只说明能力和对应 Skill 入口，不重复动作清单、参数表、示例或业务约束；`args.description` 只指向对应 Skill 手册。动作参数、类型、默认值、审批边界和重试规则由 Skill 及其 references 说明，模型应先读技能入口，再读本次操作所需章节。静态 Schema 保留现有枚举与类型校验，服务端继续负责执行准入。

Standalone 隐藏七个 Desktop 工具；Catalog/Chat/Automation 在子任务、Team、BTW/Explain 中隐藏并在执行时再次拒绝。ACP/Proxy/Channel 不经过 native 执行入口。planning/read-only 仅允许平台只读动作；七个 Desktop 管理工具全部禁止 planning，并按顺序屏障执行，包括其只读动作。未知动作按非只读处理。

`configs/agent-settings.yml` 支持全局和 mode `preset-connectors`，示例预置 `builtin.web-control` 与 `builtin.task-control`。普通 native GENERAL/CODER/KBASE 合并整包挂载，去重且不回写 Agent 源码；ACP/隐藏 Team 协调器不注入。连接器列表返回 `presetConnectorIds`、`declaredConnectorIds` 和包含两者的 `connectorIds`；预置项不可从单个 Agent 取消。删除连接器也检查全局及所有 mode 预置引用。平台管理连接器仍不进入默认预置。

看板工具 `desktop_kanban` 已迁入独立的 `builtin.kanban-control`。Chat 与 Automation 已迁入独立的 `builtin.task-control`，原有服务和审批实现继续复用；本文相关章节保留协议说明，不代表它们仍由 platform-control 挂载。

Agent 发现复用 `catalog_query.list`（resourceType=agent），条目增加 key/name/role/description/mode/invocable，包含当前 Agent，排除隐藏 TEAM 协调器；不按旧候选配置过滤。invocable 复用 agent_invoke 的静态目标校验，不代表调用者权限，chat_start 的目标契约不变。旧候选字段与标签忽略并提供一条非阻断管理 warning，不再常驻注入 prompt。分页默认 20，公共入口校验 limit 为 1–100 整数，内部切片前另做 1–100 防御夹取。管理诊断沿用现有 SourcePath/错误详情契约，可能暴露 Host 源路径，不应宣称为路径脱敏接口；新弃用告警不携带路径或候选值。完整说明见 [Agent 发现](智能体配置说明.md#agent-发现与旧候选配置)。

## Catalog 源文件事务

查询支持 Agent、Team、Skill、Connector、Model、Provider、Tool、MCP 组件；`resourceTypes {}` 返回类型与操作能力矩阵。列表默认 20 条，支持 1–100 条分页，返回 `items/nextCursor/total/hasMore`；total 为当前请求按 status 筛选后的数量，不冻结跨页快照。必须用同一 resourceType/status 跟进 nextCursor 到空才能报告完整清单。可编辑源 get 返回脱敏 content、目录内容摘要 baseRevision、editable 和 redactedPaths；只读资源返回白名单 definition。

| resourceType | list/get | validate/apply | delete | 边界 |
| --- | --- | --- | --- | --- |
| agent | 是 | 是 | 是 | 调用者自身、引用与版本保护 |
| team | 是 | 是 | 否 | 协调器不单独进入目录 |
| skill | 是 | 是 | 是 | 技能中心独立技能及 package/member；不含包元数据或 Agent 自有技能 |
| connector | 是 | 外部包 | 外部包 | 内置不可变；已有包仅 connector.json，新 HTTP MCP 可附 mcpUrl |
| provider | 是 | 否 | 否 | 包括没有模型的已加载供应商 |
| model | 是 | 否 | 否 | 已加载模型，含无 provider 的 ACP 模型 |
| tool | 是 | 否 | 否 | Catalog 工具定义，不等于某 Agent 可调用集合 |
| mcp | 是 | 否 | 否 | connectorId/component，本地声明，不是运行时实例 |

以上可写操作仍受挂载、调用者、审批、引用和版本校验限制。Provider 仅返回 key、protocols、credentialConfigured、defaultModel、modelKeys/modelCount；不返回 API Key、URL、endpoint、headers 或完整配置。credentialConfigured 只表示本地非空值，不表示授权验证成功。provider/model 的 valid 表示当前 registry 已加载，未新增坏源文件枚举。

Connector 列表补充 hasMcp/hasCli/hasView/hasNative、mcpKeys 与 editable。一个包可以包含多个 MCP 组件，也可能只有 CLI/VIEW/native，不能以 Connector 数量代替 MCP 数量。MCP list/get 只给出父连接器、组件名、transport、enabled、builtin 和 `scope:connector-declaration/availability:not_checked`；valid 仅表示包可加载，不证明 transport/URL 等通过运行时校验。不输出 URL、command、args、env 或 headers。损坏包会使 MCP 枚举失败，避免静默报告不完整清单。

现有 `platform_inspect runtimeStatus {component:"mcp"}` 才是 Agent/内容版本作用域的已缓存同步状态入口，最多返回 100 项，count 表示实际总数；它不触发联网探测。未挂载组件可能无实例，一个组件也可能对应多个 Agent/版本实例。MCP 远端 tools/resources/prompts 的完整查询及统一会话分页尚不支持。

其他实际资源的取舍：Skill package 元数据已有 `/api/admin/skill-packages/*` 与 admin source 管理，尚无独立 Catalog target；Agent 自有/连接器技能需按所属定义读取。Chat/Archive/Artifact 使用 chat_query 与 Chat API；活动 Run 用 chat_get_status；Automation 使用专用 automation 工具/API；Memory/Owner 使用其专用文件与权限协议；KBASE 文档/索引使用专用 KBASE 能力。ACP bridge、Gateway/Channel、创建默认值和部署配置属于执行/配置域，使用 defaults/runtimeStatus 与既有管理入口，不把运行状态或凭据目录当作通用源码资源。未开放 provider/model/MCP 写入、任意文件写入、凭据编辑或全平台资源 CRUD。


可修改范围：

- Agent：agent.yml、SOUL.md、AGENTS.md。
- Team：team.yml / team.yaml；不能删除 Team。
- Skill：SKILL.md 和相对文本文件；package/member 与 package.json 成员列表一起发布，创建包仍使用现有管理入口。含 package.json 的包根不能作为普通 skill 操作，必须指定 package/member。
- 外部 Connector：已有包仅编辑 connector.json；新建 HTTP MCP 可额外传 mcpUrl，由平台生成 mcp.json，与清单一起审批、校验及原子发布。不提供 CLI 安装或通用文件写入。需要登录使用 auth_mode=mcp，复用现有 .well-known 发现、PKCE、回调和刷新；公开服务可显式 no_auth。创建候选在审批前复用登录的本地校验：MCP OAuth 地址要求 HTTPS，保留 localhost、127.0.0.1 和 ::1 的 HTTP 例外；no_auth 的 HTTP(S) 范围不变。保存候选不发起远端请求。

apply 提交完整 UTF-8 文本，最多 1 MiB；目录版本覆盖整个资源（最多 64 MiB），现有对象必须带 get 得到的版本，新建必须满足不存在。禁止绝对路径、点段、符号链接和非普通文件；内置对象、调用者自身以及受引用对象受保护。Agent env 查询脱敏，保留旧值必须使用 preservePaths；连接器行内凭据不通过此工具编辑。

prepare 生成脱敏前后内容和摘要；执行在 `adminsource` 共用 Agent/source mutation 锁内复验基准与授权，再隐藏 staging → backup → 原子发布，配合 catalog directory mutation 协调 watcher。硬重载失败恢复来源并重新加载。正常返回 applied、pending（执行目录租约待发布）或 invalid；不能把 pending/invalid 当作完全生效。确认来源及目录均恢复后返回工具错误 control_rolled_back（非零退出码），status 与 executionState 均为 rolled_back，表示操作失败、已恢复原状；解决失败原因后需重新提交并审批。恢复文件或重新加载失败时仍返回 unknown，不能声称恢复成功。其他执行错误按阶段区分 not_started、unknown 和 committed；已发布后的备份清理失败不能标成未执行。更新保留原文件权限，新建源文件默认 0644（仍受 umask 影响），私有状态另按 0600 写入。

`catalog_query.get` 的 agent.yml 环境变量脱敏及 `catalog_manage.apply` 的 preservePaths 补回通过原文位置编辑，保留未改动的注释、引号、字段顺序、换行和多行值；环境变量表达式按原文补回，不落盘为进程环境的值。重复键等无法明确定位的文本拒绝处理；原本的块状多行值不能补回到候选行内 flow env，需要保留块状 env。此保证针对控制工具源码链路，不等于所有既有结构化表单都支持无损 YAML 往返。

## 一次性审批与权限档位

`catalog_manage.apply` 在 default 下人工审阅，在 auto_approve / full_access 下由服务端自动批准并记录 auto_approved 决策；`catalog_manage.delete` 与 `chat_manage.delete` 仍必须人工批准，即使 full_access / auto_approve。审批绑定 subject、Agent、Run、tool invocation、目标、内容与基准版本；批准不赋予同轮规则授权，也不传给并发兄弟调用。提交必须匹配调用 ID，且只能 approve/reject。审批等待期间变更目标或候选内容会使授权失效。

平台控制通过工具 YML 的 `confirmationRules` 选择界面：`catalog_manage` 的 `/action: delete` 使用 `resource_delete_review`，其默认规则保留 `platform_control_review`；`chat_manage.delete` 使用独立的 `chat_delete_review` 模板，展示会话标识、归档位置和基准版本；Go Handler 不再指定模板。匹配后发出 `awaiting.ask(mode: form, view: {source: builtin, key: platform_control_review, renderer: html})`；`forms[].id` 绑定工具调用，`forms[].form` 保存业务审阅数据。HTML 随 Platform 编译内置，经 `/api/view?source=builtin&key=platform_control_review` 返回，固定 key 不接受本地或远端覆盖。通用 approval 不再扩展 review/before/after/fingerprint，WebClient 不解释平台控制业务字段。

模板只读展示创建、修改、资源删除或 Chat 删除；修改默认展示有界文本差异，完整脱敏内容可折叠查看。Agent 主定义 apply 才附带权限字段提醒。模板通过 awaiting_init/update 接收数据，仅响应宿主 awaiting_collect；同意、拒绝、理由和倒计时由宿主承担。容器限制整体高度，HTML 内部滚动。

服务端以内部冻结的调用上下文接受 approve/reject，严格校验工具 ID，不从公开表单数据推导授权；表单返回值不能改写工具参数，也不进入 Bash 命令重建路径。批准后仍按内容摘要和基准版本复验、消费一次性授权；拒绝反馈回到 Agent 重新生成候选。超时不自动批准，form 仍不跨进程恢复。模型提供的“已确认”字段无效。Desktop 日常管理按业务使用六个专用审阅页：外观/皮肤/桌宠/Copilot 偏好、网站条目增改删、看板写操作、单个 WebApp 启停/重启/打开/偏好/撤销发布、产物导出及敏感诊断读取。default 用业务名称、具体字段和操作影响展示审阅，原始 JSON 默认折叠在技术详情；auto_approve / full_access 自动批准并记录决策；每次仍消费绑定 Run、调用 ID 与原始参数的一次性授权，不发送 permissionMode 提权字段。诊断审批只展示数据类别，批准前不采集敏感值。审阅前通过固定只读动作，在总计 3 秒预算内尽力读取所选对象的名称和当前值；仅保留相关字段与所选名称，不回传整个列表或 WebApp 配置。修改页可显示“当前 → 改为”，读取失败明确标注当前值不可用，不伪造前值。该快照仅用于展示，不是版本锁或新增授权依据；Desktop 执行时继续校验实际状态。auto_approve / full_access 不做展示预读取。

| view key | 业务展示 |
| --- | --- |
| `desktop_appearance_review` | 主题、语言、皮肤、桌宠和页面助理偏好，显示名称和选项变化 |
| `desktop_website_review` | 网站名称、网址和助理的前后对比；移除入口的实际影响 |
| `desktop_kanban_review` | 任务标题/内容、状态、优先级及执行安排；删除关联定时任务和待办自动执行提醒 |
| `desktop_webapp_review` | 应用名称、名称/打开方式变更，启停/重启/撤销发布的具体影响 |
| `desktop_export_review` | 页面名称、文件格式、下载目录；文件名由实际导出生成 |
| `desktop_diagnostics_review` | 将读取的数据类别和提供给当前智能体的范围，不提前读取诊断值 |

六页共用内置 CSS 和宿主 collect 消息协议，view 服务将共享资源内联，保持离线 HTML 与原 CSP。对象名称和业务内容只通过 textContent 显示，不执行 HTML 或 Markdown。未知附加字段会提示展开技术详情核对。

Desktop 对这些动作仅在可信内部 agentPlatform 调用上下文下豁免自身确认；公开请求自报 source 不获得豁免。原 action confirmation 定义、全局开关与 dialog 保留，将动作移出白名单并撤销对应 Platform 审阅可恢复原确认。导航、打开详情/日志、市场刷新等轻量动作不新增 Platform 审阅。市场资源管理（含安装/升级/卸载与镜像导入导出/删除）、基础服务变更、WebApp 安装/卸载/公开发布继续由 Desktop 确认，不叠加 Platform view；原市场及 WebApp 安装前置审批已移除。Platform/Desktop 需配套更新，旧 Desktop 对新 Platform 审阅动作仍可能重复确认。

已知 agent/team/skill/connector 的模型候选 content 在事件、历史和 trace 中保留原文，避免下一轮丢失编辑内容。读取的既有 env 值和审批安全副本仍按字段脱敏，服务端 preservePaths 回填值不注入模型参数；未知类型和未完成参数保守脱敏。run_env 参数依旧可观测，不用于 Secret。

## 会话（已迁入 Task Control）

全部操作通过 conversation 服务和原 Chat 存储实现。匿名只能访问当前 Chat；query:subject 的 Chat 验证所有者；历史读取不依赖 Agent 当前有效性。read/search 和 Markdown 导出只包含用户与助手可见正文。snapshot 导出复用现有 Snapshot V1 完整时间线，包括思考、工具及已发布产物元数据；不是原始存储备份，不新增导出系统提示词或私有状态。

list/search 按 Chat ID 的稳定顺序使用存储层 keyset 分页，游标不再包含全部 ID，使用进程内随机密钥认证加密以隐藏扫描到的其他用户 ID，重启后需重新查询；不跟随 UI 的置顶/手工排序。单页至多扫描 100 个列表候选，权限过滤后可能为空但仍有 nextCursor。search 每次至多扫描 50 个会话、16 MiB 历史，在会话之间检查 2 秒期限；超过 8 MiB 的单会话显式列在 skippedChatIds，返回 incomplete，不能宣称无结果。命中片段至多 500 Unicode 字符，并围绕命中位置。read 使用 continuation cursor；read 在 16,000 字符预算内切分长消息，返回 offset/continued，冻结已读消息边界的内容摘要，历史被改动后要求重新开始。artifacts 复用已认证 Chat 资源接口。归档列表分页覆盖全部记录。

rename/setPinned 可以操作当前 Chat，置顶复用现有服务与 chats.order.changed。archive/delete/fork 拒绝当前调用 Chat、活动 Run 和 pending HITL。删除审批同时绑定摘要和历史内容版本。fork/export 使用 Run/tool ID 派生结果路径和持久收据，同一次调用不会产生多个副本，参数变更冲突。export 输出当前 Chat 下的正文 Markdown 或标准完整 snapshot JSON。未完成的 fork 收据无法证明已存在目标来源时返回 idempotency_conflict，不认领现有目标、不覆盖或删除。HTTP 与工具的归档、恢复、置顶、删除、改名、复制共用修改锁；Service 依赖视图显式共享锁，不复制 sync.Mutex。

archive/restore 同时支持单个 `chatId` 或 `chatIds`（1–100 个互异 ID，两者互斥）。请求格式先整体验证，再逐项检查权限并执行；失败继续，返回 total/succeeded/failed 和含错误、executionState 的逐项结果，不回滚已成功项。单项响应兼容原契约。

## 诊断与边界

runtimeStatus 返回配置启用状态、目录数量、运行模式/uptime、MCP 的已有同步快照、连接器本地 configured 状态与 KBASE sidecar 快照，不触发联网探测。暂未注入的组件明确返回 unavailable；Container Hub 的 enabled 不是健康探测。securityExplain 解释本 Run 挂载、动作与文件访问策略，不授予权限。

Run 准入与 Chat mutation 之间尚无覆盖所有入口的统一并发事务。`@chat/` 在 Workspace 外的 Desktop 预览仍受原协议限制。任意多文件事务没有全局崩溃恢复日志；来源发布和 fork/export 通过已有原子落盘及收据恢复边界处理。真实 Desktop、Windows、Container Hub 与第三方 MCP OAuth 仍需目标环境联调。

搜索不提供向量化摘要、流式分段全文索引或单会话中途取消；分页不冻结全库快照，新增 ID 位于游标之前时需重新查询。snapshot 不打包产物字节，归档附件元数据尚未单独恢复。

控制工具准入返回 `field/expected/actual/recovery`，未知键名和值不回显；审批准备保留结构化错误。格式与覆盖范围见 [工具参数错误与恢复提示](工具参数错误审计.md)。

## 迁移

旧 platform_control、desktop_action 与 builtin.desktop 退出新调用目录，旧历史名称保留回放。`configs/tools.yml` 出现 platform-control（含 enabled:false）即报错；删除旧段，保留独立 run-env 段。客户端明确提交所需的连接器挂载；创建接口不隐含授予管理权限。

`cmd/migrate-desktop` 提供预览、离线应用、逐项 pending 报告、备份和回滚。旧 platform_control 仅移除；旧 desktop_cdp/desktop-cdp/builtin.desktop-web 迁往 builtin.web-control。desktop_action/desktop-action 未有显式新管理挂载时，以及旧 builtin.desktop 的历史能力无法判定时，列为 pending，保留该 Agent 源文，不能通过 --allow-expansion 跳过。其余明确条目可继续迁移；有 pending 时不清理旧共享技能。管理员逐条明确新连接器配置并移除旧声明后重新预览。只迁移源文件，不编辑运行目录或 Chat 历史。

工具具体参数见 [Catalog reference](../internal/resources/connectors/builtin.platform-control/skills/platform-control/references/catalog.md) 与 [Chat reference](../internal/resources/connectors/builtin.task-control/skills/task-control/references/chat.md)。

## Automation 独立工具组

新增 `automation_query`（list/get/executions/execution/validate）与 `automation_manage`（create/update/setEnabled/delete/trigger），使用独立 `builtin.task-control` 挂载和 Native 根 Run 准入。它们共用 `internal/automation.Service`，不调用 Server handler 或 Desktop action，也不扩展 Catalog 的 resourceType。三类业务 view、版本校验、调用收据与手动执行语义见 [自动化](自动化.md#task-control-管理工具)。
