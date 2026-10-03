# 平台控制连接器

`builtin.platform-control` 是 Platform 内嵌、只读、无需认证的 native 连接器。显式挂载后导入 12 个工具和 `platform-control` 技能，不自动授予 Bash。仅声明工具名不能获得连接器执行授权。`run_env` 保持独立。管理连接器主要由部署者为平台主智能体显式配置；源码不包含主智能体名单，不从业务类型或旧工具声明推导管理授权。

| 工具 | action | 执行环境 |
| --- | --- | --- |
| catalog_query | list/get/defaults/validate | 普通 native main root |
| catalog_manage | apply/delete | 普通 native main root |
| chat_query | current/list/search/read/artifacts | 普通 native main root |
| chat_manage | rename/setPinned/archive/restore/fork/export/delete | 普通 native main root |
| platform_inspect | runtimeStatus/securityExplain | native，支持受信任子任务/Team |
| desktop_shell | 9 个外壳动作 | Desktop |
| desktop_settings | 16 个设置动作 | Desktop |
| desktop_site | 6 个网站条目动作 | Desktop |
| desktop_webapp | 16 个应用动作 | Desktop |
| desktop_service | 12 个服务动作 | Desktop |
| desktop_market | 15 个市场动作 | Desktop |
| desktop_kanban | 6 个看板动作 | Desktop |

全部工具使用固定 `{action,args}`，action 枚举来自 `internal/connector/control_actions.go`；顶层未知字段拒绝。Catalog/Chat/Inspect 还严格检查动作参数类型与字段。Desktop 保留原有动作字段校验与确认流程，新适配器仅添加 `desktop.` 传输前缀。`agent.update`、`skill.update` 不再暴露给模型。不同工具的 action 不互相兼容。

Standalone 隐藏七个 Desktop 工具；Catalog/Chat 在子任务、Team、BTW/Explain 中隐藏并在执行时再次拒绝。ACP/Proxy/Channel 不经过 native 执行入口。planning/read-only 仅允许平台只读动作；七个 Desktop 管理工具全部禁止 planning，并按顺序屏障执行，包括其只读动作。未知动作按非只读处理。

## Catalog 源文件事务

查询支持 Agent、Team、Skill、Connector、Model、Tool；模型和工具仅查询。列表支持状态、1–100 条分页；get 返回脱敏 content、目录内容摘要 baseRevision、editable 和 redactedPaths。

可修改范围：

- Agent：agent.yml、SOUL.md、AGENTS.md。
- Team：team.yml / team.yaml；不能删除 Team。
- Skill：SKILL.md 和相对文本文件；package/member 与 package.json 成员列表一起发布，创建包仍使用现有管理入口。含 package.json 的包根不能作为普通 skill 操作，必须指定 package/member。
- 外部 Connector：已有包仅编辑 connector.json；新建 HTTP MCP 可额外传 mcpUrl，由平台生成 mcp.json，与清单一起审批、校验及原子发布。不提供 CLI 安装或通用文件写入。需要登录使用 auth_mode=mcp，复用现有 .well-known 发现、PKCE、回调和刷新；公开服务可显式 no_auth。创建候选在审批前复用登录的本地校验：MCP OAuth 地址要求 HTTPS，保留 localhost、127.0.0.1 和 ::1 的 HTTP 例外；no_auth 的 HTTP(S) 范围不变。保存候选不发起远端请求。

apply 提交完整 UTF-8 文本，最多 1 MiB；目录版本覆盖整个资源（最多 64 MiB），现有对象必须带 get 得到的版本，新建必须满足不存在。禁止绝对路径、点段、符号链接和非普通文件；内置对象、调用者自身以及受引用对象受保护。Agent env 查询脱敏，保留旧值必须使用 preservePaths；连接器行内凭据不通过此工具编辑。

prepare 生成脱敏前后内容和摘要；执行在 `adminsource` 共用 Agent/source mutation 锁内复验基准与授权，再隐藏 staging → backup → 原子发布，配合 catalog directory mutation 协调 watcher。硬重载失败恢复来源并重新加载。正常返回 applied、pending（执行目录租约待发布）或 invalid；不能把 pending/invalid 当作完全生效。确认来源及目录均恢复后返回工具错误 control_rolled_back（非零退出码），status 与 executionState 均为 rolled_back，表示操作失败、已恢复原状；解决失败原因后需重新提交并审批。恢复文件或重新加载失败时仍返回 unknown，不能声称恢复成功。其他执行错误按阶段区分 not_started、unknown 和 committed；已发布后的备份清理失败不能标成未执行。更新保留原文件权限，新建源文件默认 0644（仍受 umask 影响），私有状态另按 0600 写入。

## 强制一次性审批

所有 catalog_manage 与 chat_manage.delete 都必须人工批准，即使 full_access / auto_approve。审批绑定 subject、Agent、Run、tool invocation、目标、内容与基准版本；批准不赋予同轮规则授权，也不传给并发兄弟调用。提交必须匹配调用 ID，且只能 approve/reject。审批等待期间变更目标或候选内容会使授权失效。

WebClient 展示目标、版本、完整脱敏变更前后内容与权限字段提醒。模型提供的“已确认”字段无效。Desktop 业务动作继续由 Desktop 原有确认处理，Platform 不重复添加审批。

已知 agent/team/skill/connector 的模型候选 content 在事件、历史和 trace 中保留原文，避免下一轮丢失编辑内容。读取的既有 env 值和审批安全副本仍按字段脱敏，服务端 preservePaths 回填值不注入模型参数；未知类型和未完成参数保守脱敏。run_env 参数依旧可观测，不用于 Secret。

## 会话

全部操作通过 conversation 服务和原 Chat 存储实现。匿名只能访问当前 Chat；query:subject 的 Chat 验证所有者；历史读取不依赖 Agent 当前有效性。read/search 和 Markdown 导出只包含用户与助手可见正文。snapshot 导出复用现有 Snapshot V1 完整时间线，包括思考、工具及已发布产物元数据；不是原始存储备份，不新增导出系统提示词或私有状态。

list/search 按 Chat ID 的稳定顺序使用存储层 keyset 分页，游标不再包含全部 ID，使用进程内随机密钥认证加密以隐藏扫描到的其他用户 ID，重启后需重新查询；不跟随 UI 的置顶/手工排序。单页至多扫描 100 个列表候选，权限过滤后可能为空但仍有 nextCursor。search 每次至多扫描 50 个会话、16 MiB 历史，在会话之间检查 2 秒期限；超过 8 MiB 的单会话显式列在 skippedChatIds，返回 incomplete，不能宣称无结果。命中片段至多 500 Unicode 字符，并围绕命中位置。read 使用 continuation cursor；read 在 16,000 字符预算内切分长消息，返回 offset/continued，冻结已读消息边界的内容摘要，历史被改动后要求重新开始。artifacts 复用已认证 Chat 资源接口。归档列表分页覆盖全部记录。

rename/setPinned 可以操作当前 Chat，置顶复用现有服务与 chats.order.changed。archive/delete/fork 拒绝当前调用 Chat、活动 Run 和 pending HITL。删除审批同时绑定摘要和历史内容版本。fork/export 使用 Run/tool ID 派生结果路径和持久收据，同一次调用不会产生多个副本，参数变更冲突。export 输出当前 Chat 下的正文 Markdown 或标准完整 snapshot JSON。未完成的 fork 收据无法证明已存在目标来源时返回 idempotency_conflict，不认领现有目标、不覆盖或删除。HTTP 与工具的归档、恢复、置顶、删除、改名、复制共用修改锁；Service 依赖视图显式共享锁，不复制 sync.Mutex。

## 诊断与边界

runtimeStatus 返回配置启用状态、目录数量、运行模式/uptime、MCP 的已有同步快照、连接器本地 configured 状态与 KBASE sidecar 快照，不触发联网探测。暂未注入的组件明确返回 unavailable；Container Hub 的 enabled 不是健康探测。securityExplain 解释本 Run 挂载、动作与文件访问策略，不授予权限。

本次不改变 Run 准入与 Chat mutation 之间的既有并发边界，不宣称已解决所有跨入口的准入竞态。`@chat/` 在 Workspace 外的 Desktop 预览仍受原协议限制。进程崩溃的任意多文件事务没有新增全局恢复日志；来源发布和 fork/export 通过已有原子落盘及收据恢复边界处理。真实 Desktop、Windows 与 Container Hub 仍需目标环境联调。

## 迁移

旧 platform_control、desktop_action 与 builtin.desktop 退出新调用目录，旧历史名称保留回放。`configs/tools.yml` 出现 platform-control（含 enabled:false）即报错；删除旧段，保留独立 run-env 段。创建模板移除 platform-admin 能力组及应用/技能制作组隐含管理权限。

`cmd/migrate-desktop` 提供预览、离线应用、逐项 pending 报告、备份和回滚。旧 platform_control 仅移除；旧 desktop_cdp/desktop-cdp/builtin.desktop-web 迁往 builtin.web-control。desktop_action/desktop-action 未有显式新管理挂载时，以及旧 builtin.desktop 的历史能力无法判定时，列为 pending，保留该 Agent 源文，不能通过 --allow-expansion 跳过。其余明确条目可继续迁移；有 pending 时不清理旧共享技能。管理员逐条明确新连接器配置并移除旧声明后重新预览。只迁移源文件，不编辑运行目录或 Chat 历史。

工具具体参数见 [Catalog reference](../internal/resources/connectors/builtin.platform-control/skills/platform-control/references/catalog.md) 与 [Chat reference](../internal/resources/connectors/builtin.platform-control/skills/platform-control/references/chat.md)。

## 初次实现验证记录（修复前）

- Go 全量回归：`DESKTOP_SOURCE=../zenmind-desktop go test -p 2 ./...`，124 个包通过或无测试；后续目录成员与诊断收尾另行通过 adminsource/platformcontrol/app 定向回归。
- Desktop 动作契约：与本地 Desktop 源码交叉校验通过，不等于真实客户端联调。
- WebClient：TypeScript、模块边界检查、6 个相关测试套件（51 项）通过。
- WebClient 全量存在 19 个失败套件 / 74 项失败；未修改 HEAD 在独立临时目录复测得到相同失败套件与数量，属于现有基线。
- zenmind-env：本地 8 个、云端模板 4 个 Agent 源定义通过暂存区离线迁移，写回前复验原文摘要；未改动运行包或 Chat 历史。迁移备份位于临时目录 `/tmp/env-control-source-migration/.desktop-migration-1586485693`；云端备份位置见 `/tmp/env-cloud-control-migration.json`。
- Windows、真实 Desktop 和 Container Hub 仍待目标环境联调；本次没有执行部署或提交 Git commit。

## 2026-10-03 审核修复与未完成项

已修复候选正文回归、包根误删、资源大小写保护、锁复制、自动迁移扩权、Desktop 阶段策略、固定大小查询游标、搜索预算与片段、完整 snapshot、HTTP MCP 最小包创建及明确执行状态。zenmind-env 已更新退役技能/工具引用和点击脚本参数；依据旧迁移摘要撤回 7 处自动管理挂载，未内置主智能体名称。

本轮补充：`configs/tools.yml` 支持 `preset-connectors`，示例与本地配置预置 `builtin.web-control`。普通 native GENERAL/CODER/KBASE 合并整包挂载，去重且不回写 Agent 源码；ACP/隐藏 Team 协调器不注入。连接器列表返回 `presetConnectorIds`、`declaredConnectorIds` 和包含两者的 `connectorIds`；预置项不可从单个 Agent 取消。删除连接器也检查全局预置引用。平台管理连接器仍不进入默认预置。

`catalog_query.get` 的 agent.yml 环境变量脱敏及 `catalog_manage.apply` 的 preservePaths 补回通过原文位置编辑，保留未改动的注释、引号、字段顺序、换行和多行值；环境变量表达式按原文补回，不落盘为进程环境的值。重复键等无法明确定位的文本拒绝处理；原本的块状多行值不能补回到候选行内 flow env，需要保留块状 env。此保证针对控制工具源码链路，不等于所有既有结构化表单都支持无损 YAML 往返。

archive/restore 同时支持单个 `chatId` 或 `chatIds`（1–100 个互异 ID，两者互斥）。请求格式先整体验证，再逐项检查权限并执行；失败继续，返回 total/succeeded/failed 和含错误、executionState 的逐项结果，不回滚已成功项。单项响应兼容原契约。

以下事项尚未完成，不能作为已交付能力：

- 按用户本轮决定保留现有搜索保护：单会话内部仍会构建快照，8 MiB 以上会话明确跳过并报告 incomplete/skippedChatIds。向量化摘要留作后续，不在本次实现流式分段搜索、全文索引和单会话中途取消。固定 ID 分页没有冻结全库快照，新增 ID 在游标之前时需重新查询。
- fork 崩溃后若仅有 started 收据，现有目标安全拒绝恢复；未新增可原子恢复的来源证明日志。
- 完整 snapshot 使用既有时间线契约。归档附件元数据尚未单独恢复，产物文件字节不打包进 JSON；不是原始 JSONL/凭据的备份。
- 真实 Desktop 网页技能、Windows 权限与大小写、真实第三方 MCP OAuth 仍需目标环境联调；本地测试不能代替这些验证。
- WebClient 导出资源已可本地构建；相邻 tunnel-hub-server 仓库缺失，仅分享页面展示资源未同步；不影响控制工具、snapshot 生成或本地导出，不作为本次核心改造阻塞项。i18n 原有 55 项违规仍保留，新增 12 项已消除。

### 修复验证

- 全量 Go 回归最终 124 个包通过或无测试。首轮 TestQuerySSEPersistsChatHistory 出现一次终态事件未落盘的时序失败；该用例连续 10 次复测通过，随后全量重跑通过，未据此认定其根因已修复。
- 全量之后追加的游标认证加密通过 conversation/adminsource 的 race 回归，以及 platformcontrol/credentialview/toolpolicy/connectormigrate 定向回归；最终 go vet ./... 通过。
- WebClient TypeScript、模块边界、3 个相关 Jest 套件 17 项通过；i18n 恢复到既有 55 项，仍不是零违规。导出模板构建及不可变资源校验通过；服务器同步因目标仓库缺失未执行。
- 环境检查覆盖 34 份 Agent YAML、2 份 include 清单和修改后的网页点击脚本语法；指定范围内无退役工具/技能引用。此检查不等于真实业务页面操作验收。


### preset-connectors 与原文编辑补充验证

- `DESKTOP_SOURCE=/Users/linlay/Project/zenmind/zenmind-desktop go test -p 2 ./...`：124 个包通过或无测试，包括 Desktop 源码契约。
- 最后补充的无效 Agent 配置展示、预置开关、YAML 空多行值和批量归档/恢复边界，catalog/adminsource/conversation/server 定向测试通过。
- adminsource/conversation 的 race 回归通过；WebClient TypeScript、模块边界及连接器相关 3 个 Jest 套件 35 项通过。
- i18n 保持原有 55 项违规，没有新增硬编码。未改动本轮已确认保留的大会话搜索限制；未执行部署或重启。
