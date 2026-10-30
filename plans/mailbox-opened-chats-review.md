# Mailbox 与工作会话查询：讨论汇总、质疑与代码现状

更新时间：2026-10-30（Asia/Shanghai）。

本文汇总本轮 Mailbox 工作、用户提出的问题、设计调整及当前代码证据，作为后续实施的讨论依据。**本文不是新能力已交付的声明，也不表示用户已经批准所有建议。**

相关文档：[后续总计划](mailbox-next.md)、[初始 Mailbox 方案](mailbox.md)、[主要聊天与派活探讨](primary-chat-dispatch.md)、[Mailbox 当前合同](../docs/Mailbox.md)。

## 1. 当前结论与状态

| 事项 | 当前状态 |
|---|---|
| Mailbox 表、字段、索引与 chats.db 命名风格统一 | 用户已确认，第一批已实现并提交 |
| reply / notify / ask 冻结收件 Agent 默认权限 | 第一批已实现并提交 |
| notify 先落 accepting，再复制附件 | 第一批已实现并提交 |
| 删除分身后，已受理回信及收件侧附件继续投递 | 第一批已实现并提交 |
| 默认 30 天终态正文清理，保留幂等及创建关系 | 第一批已实现并提交 |
| Mailbox 未上线时 user_version 保持 1 | 恢复 V1 的修复及回归纳入本次提交；保留严格结构与版本检查，不自动迁移或清库 |
| `/api/chats?openedBy=A` | 助手当前推荐复用现有接口，参数尚未实现 |
| 在 CHATS 保存创建关系，用索引直接筛选 | 为解决性能质疑提出的最新建议，尚未定稿和实施 |
| 按 running / awaiting / idle 筛选 | 讨论中的可选能力，尚未定稿和实施 |
| `/api/chats/opened` | 最初建议的独立接口，当前推荐方案不再采用；代码没有此路由 |
| `/api/send` | 原总计划中的拟新增入口，名称、协议和实施仍待确认；当前没有该接口 |
| 旧 `chat_start` 开出的会话兼容 | 用户明确表示即将下线，本次查询设计无需为其追加兼容；当前代码仍保留该回退 |

第一批提交为 `c092292d`（`fix(mailbox): 统一存储命名并完善邮件生命周期`）。其后的 `059960c0` 已调整权限选择器语义，后续实施应使用当前契约，不能把早期讨论中的 `default` 字样直接当作固定执行档位。

本文核对时 HEAD 为 `509b81a5`。查询设计没有对应实现提交；工作区还存在 Mailbox V1 修复及其他独立计划修改，不能把所有本地差异合并为本方案的实现。

## 2. 需求：谁使用“工作会话查询”

示例：用户在 A 的主要聊天中要求整理周报，A 通过 `chat_send {newChat:true}` 给自己或 B 创建独立工作会话 C。

- C 的所属 Agent 是实际执行者，可能是 A，也可能是 B。
- C 的发起 Agent 是 A。
- WebClient 需要展示 A 派出的会话，显示当前执行/等待状态，并允许用户进入详情、补充要求或处理审批。
- Agent 侧已有 `chat_query` 的 opened 动作，用于发现自己开出的会话。UI 查询应与它共用一致的创建关系和权限语义。
- 不要求多 Run 在同一 Chat 并发；继续保持一聊天一主 Run。

这项能力属于工作会话导航。收件箱主要呈现邮件，工作会话列表主要呈现 Chat；两者不能互相替代。达到跳数上限、请示没有成功上报时，用户仍应能在工作会话列表发现等待处理的 Chat。

需要进一步明确：mailbox 自动补建的目标 Agent 主要聊天，是否纳入“工作会话”。现有 `OPENED_CHAT_` 只表示实际建过 Chat，并不等价于 `newChat:true` 创建的独立工作；不能直接拿它当产品分类。

## 3. 用户质疑与设计调整

| 用户提出的问题或约束 | 助手最初的回答/方案 | 核对后的判断与当前建议 |
|---|---|---|
| mail.db 与 chats.db 命名是否应一致？ | mailbox 起初使用小写表名和字段 | 应一致，已改为 MAILS、大写下划线加尾部 `_` 的字段及 IDX_ 索引；HTTP JSON 名称不随之修改 |
| 未上线，可以不兼容升级 | 曾考虑已有库的升级方式 | 不要求加入通用迁移或启动时自动清库；开发环境重建必须显式操作，保留备份 |
| 为什么启动要求 user_version=2，明明未上线？ | 第一批错误地递增到 V2，旧 V1 库被拒绝 | Mailbox 当前结构回到 V1 基线；同版本号也仍严格核验结构，不能只改旧库标记绕过校验 |
| `/api/send` 是什么，为什么不知道这个接口？ | 原计划提出统一用户发送入口 | 它是拟议接口，不是现有能力，不能把它当成已确定或已实现的前提 |
| `/api/chats/opened` 有什么用、给谁用？ | 拟为 WebClient 增加独立工作会话列表入口 | 用途是用户导航和等待处理提示；这本身不证明需要新路由 |
| 与 `/api/chats?agentKey=A` 有什么不同，能否增加 opened 参数？ | 原先单列 `/api/chats/opened` | `agentKey` 表示所属 Agent，`openedBy` 表示发起 Agent；复用 `/api/chats` 更适合相同的 Chat 列表资源 |
| 新 API 也可以，但必须说明理由 | 讨论了独立接口 | 当前没有足够理由另开接口。若未来返回任务树、工作总览等独立结构，再单独讨论；不能以数据库不同作为拆 API 的依据 |
| 能否全走数据库，JSONL 查询太慢？ | 一度建议在 CHATS 存创建关系，又因可不兼容 chat_start 而提出两库查询复用现有数据 | 列表发现、权限筛选、排序和分页应不读 JSONL。当前 JSONL 回退只是已有实现，不能作为新列表的查询方案 |
| 旧 chat_start 马上下线，可以不用关心 | 现有 opened 查询兼容它，遍历候选 JSONL | 新工作会话查询不追加该兼容，也不为它扫描或补建 JSONL 索引；这不等于旧聊天历史要被删除 |
| 第一个 DB 查 ID，第二个 DB 查 Chat，再循环补状态，是否太慢？ | 两库方案原意是两次批量 SQL，但仍需要传递候选 ID | 不能未经测量就断言“必然严重变慢”，但全量 ID 搬运、大 IN 列表、逐条查询、状态集合重复扫描都存在规模风险。当前推荐把创建关系投影到 CHATS，索引查询列表，并批量读取 Runtime 状态 |
| 正在运行和已经结束的 Chat 是否都需要？ | 建议默认返回全部未归档工作会话，支持状态筛选 | Chat 可以反复执行多个 Run，应称运行中/等待处理/空闲；成功、失败、取消属于具体 Run 的结束结果 |

设计过程需要保留的区别：用户提出了接口合并、纯数据库查询和性能问题；用户没有明确批准最新的 CHATS 新增字段、具体状态参数或批量状态接口。当前应把这些写为待实施建议。

## 4. 现有代码实际做了什么

### 4.1 数据库与创建关系

- [mailbox/store.go](../internal/mailbox/store.go) 定义 `mail.db.MAILS`，保存 `FROM_AGENT_KEY_`、`SUBJECT_`、`TO_CHAT_ID_`、`OPENED_CHAT_` 及删除标记。
- `Store.OpenedChats` 根据发起 Agent、subject、是否建过 Chat、目标是否删除查询邮件行；它当前返回完整 Mail，而不是仅取 ID 的轻量索引结果。
- 第一批移除了“必须正文未清理”的筛选，保留期到期后创建关系仍在；这解决关系丢失，不等于列表查询已优化。
- [chat/store_schema.go](../internal/chat/store_schema.go) 中 `CHATS` 已有所属 `AGENT_KEY_`、名称、来源、时间、摘要和等待项等字段，**尚无 `OPENED_BY_AGENT_KEY_` / `OPENED_BY_CHAT_ID_`**。
- `mail.db` 与 `chats.db` 是独立数据库、独立连接。当前没有为 openedBy 配置跨库 JOIN 或单库创建关系索引。
- 归档另存于 `archive/archive.db` 的 `ARCHIVED_CHATS`、`ARCHIVED_RUNS`；邮件没有独立归档表。归档不是删除。

### 4.2 已有 opened 工具链

当前调用链为：

```text
chat_query action=opened
  -> platformcontrol 的 opened 分支
  -> conversation.MailboxOpened
  -> mailbox.Service.OpenedChats
  -> mail.db 查创建邮件并按 Chat 去重
  -> runtime/query.Service.MailOpenedChats
      -> 枚举现有 Chat
      -> 兼容 run-query 来源，读取 JSONL 首条 query 的 origin
      -> 逐 Chat 查询 Summary
      -> 逐 Chat 调用 ActiveRunForChat
```

代码入口：

- [platformcontrol/control_handler.go](../internal/platformcontrol/control_handler.go)：opened 动作分支。
- [app/app.go](../internal/app/app.go)：装配 MailboxOpened 回调。
- [mailbox/interactions.go](../internal/mailbox/interactions.go)：邮件创建关系读取及去重。
- [runtime/query/mailbox.go](../internal/runtime/query/mailbox.go)：MailOpenedChats、旧来源和 JSONL 回退。
- [task-control 手册](../internal/resources/connectors/builtin.task-control/skills/task-control/references/chat.md)：现有工具说明。

因此，前面“直接查两张表即可”的描述是方案，不是目前工具链已经具备的性能保证。当前调用链确实包含用户担心的全量枚举、JSONL 读取和逐条补齐。

### 4.3 HTTP / WS 聊天列表

- [server/handler_chat.go](../internal/server/handler_chat.go)：`GET /api/chats` 支持现有 agentKey、mode、lastRunId、limit、pinned、primary、hasWorkspace 等条件，没有 openedBy 或 state 参数。
- [server/ws_routes.go](../internal/server/ws_routes.go)：主 WS 的 `/api/chats` payload 同样没有 openedBy 或 state。
- [chat/pinned_store.go](../internal/chat/pinned_store.go)：`ListOptions` 没有创建者筛选字段。
- [server/chat_read_state.go](../internal/server/chat_read_state.go)：`mapChatSummariesWithActiveRuns` 逐条补活动 Run。
- [runtime/runstate/manager.go](../internal/runtime/runstate/manager.go)：`ActiveRunForChat` 加锁后调用 `activeRunMatchLocked`，后者遍历 `m.runs`。

当前状态补齐不是每条都访问数据库，但它会多次遍历 Run 集合。如果返回 P 个 Chat，Run 集合有 R 项，这部分最坏可能产生约 P × R 的扫描工作量。应以基准测试核实实际代价，不能把它描述为已经有 Chat→Run 的常数时间索引。

### 4.4 WebClient

本次核对相邻 WebClient 源码可见：

- `src/app/layout/LeftSidebar.tsx` 仍通过设置菜单动作 open-mailbox 打开收件箱。
- `src/features/mailbox/components/MailboxPanel.tsx` 直接展示列表返回的正文、附件，支持筛选、重投、丢弃，并监听 mail.updated。
- `src/shared/data/api/requests/mailbox.ts` 读取 `{mails: MailRecord[]}`；尚未接入独立详情接口和 total。

以上是源码核对，不代表本次完成了浏览器 UI 联调。新的 openedBy 导航、请示卡片直接审批和统一发送队列均未在本次实现。

## 5. 设计方案对比

| 方案 | 查询路径 | 优点 | 局限/成本 | 当前建议 |
|---|---|---|---|---|---|
| 直接复用现有 opened 工具链 | Mail 查询 + Chat 枚举 + JSONL 回退 + 逐条状态 | 复用最直接 | 请求成本随历史量增长，包含不需要的旧入口兼容 | 不作为新列表路径 |
| 两库批量查询 | MAILS 取 Chat ID，再由 CHATS 批量筛选排序 | 可利用已存在的创建记录，无需复制元数据 | 候选 ID 大时有搬运、SQL 参数上限等成本，跨库一致性和稳定分页需处理；逐条 Summary 会退化 | 可作受控过渡，不作为当前长期推荐 |
| CHATS 保存创建关系 | chats.db 索引筛选、排序、限制数量，再批量补状态 | 列表数据集中，避免扫邮件/JSONL和搬运全部历史 ID，易于组合筛选 | 需要创建时写入、归档恢复及重建规则，已有 chats.db 的结构处理必须明确 | 最新推荐，未获实施确认 |

数据库分开属于内部实现条件，不是新 API 的充分理由。三种方案都可以放在同一个 `/api/chats` 后面；API 应按返回资源和业务语义划分。

## 6. 最新推荐方案（未实施）

### 6.1 API 复用

拟扩展现有接口，不新增 `/api/chats/opened`：

```http
GET /api/chats?openedBy=A
GET /api/chats?openedBy=A&agentKey=B
```

- agentKey：会话所属 Agent，沿用原语义。
- openedBy：创建该工作会话的 Agent。
- 两者同时出现时取交集，不因 openedBy 的存在重解释 agentKey。
- 当前用户身份由可信上下文取得，不允许通过参数冒用 subject。
- HTTP 与主 WS 共用同一应用服务和筛选规则。
- 保持现有聊天摘要响应形状，必要的元数据扩展另行定义。

以上请求是目标协议示例；代码当前未识别 openedBy 的筛选语义，不能将请求示例当作可用功能。

### 6.2 创建关系写入 Chat 索引

候选字段（不是已实现 DDL）：

```sql
OPENED_BY_AGENT_KEY_  TEXT NOT NULL DEFAULT ''
OPENED_BY_CHAT_ID_    TEXT NOT NULL DEFAULT ''
```

实现约束：

1. 由 Runtime / conversation / chat 存储边界在真正创建工作 Chat 时写入可信来源；Server 不拼装或修改业务来源。
2. 后续寄信、引用、插话不改写首次创建者。切换主要聊天指针也不改写原发起 Chat ID。
3. 以 Chat 生命周期为准；邮件正文保留期不影响查询。MAILS 继续保存投递与回信证据，CHATS 保存用于列表的创建元数据。
4. 用户归属必须可以直接用数据库字段验证；复用现有可信来源字段是否足够、是否需独立 owner subject 列，需要结合授权代码定稿。不能为了补权限又回到 JSONL 扫描。
5. 根据用户归属、openedBy、稳定排序字段设计复合索引，查询只取摘要所需列，并在数据库内完成筛选和数量限制。
6. 不追加旧 chat_start 的 JSONL 兼容。已有 mailbox 创建关系若需要补建，只能按明确流程读数据库，不在列表请求里做全量修复。
7. 明确 accepting 重放和并发创建的幂等规则；避免邮件已受理但 Chat 尚未创建时就暴露成真实工作会话。
8. 归档、恢复、派生、删除要明确保留/重置规则；尤其派生 Chat 不应无条件继承“由 A 派出的工作”的身份。

**重要边界：Mailbox 未上线，不代表现有 chats.db 和历史 Chat 可以清空。** 新增字段会触发严格 schema 校验；具体采用何种显式结构升级/索引重建流程尚待设计，不能把本次本地空 mail.db 的处理复制到真实聊天库，也不能默认通过扫描全部 JSONL 重建。

### 6.3 列表与实时状态

目标路径：

```text
HTTP / WS /api/chats
  -> conversation 应用服务
      -> chats.db：按权限、openedBy、其他条件和顺序查询摘要
      -> Runtime 窄接口：批量取得需要的活动/等待状态
  -> 组装本页响应
```

- 无状态过滤时：一次摘要 SQL，Runtime 按本页 Chat ID 一次批量获取状态；响应组装只遍历本页。
- 批量状态实现应单次遍历 Run 集合构建映射，或维护可靠的 Chat→Run 索引；避免每个 Chat 重扫全体 Run。
- 状态信息是查询时快照，不能承诺跨数据库与 Runtime 的分布式强一致；下一次查询或状态事件用于刷新。
- 状态要覆盖同 Chat 多活动 Run 冲突的既有错误语义，不能批量优化后静默选一个。
- 新列表发现和摘要路径不得打开 JSONL。用户主动进入历史详情仍按现有回放合同读取。

目标是查询次数和文件读取成本受控，不是声称组装 JSON 可以没有任何循环。目前没有此目标方案的性能实测，不能预先承诺具体延迟。

### 6.4 是否查询“运行完”的 Chat

建议默认包含未归档的运行中、等待处理和空闲会话，不让一件工作结束后立刻从导航消失。Chat 可以多次执行 Run，因此“任务成功/失败”与“Chat 当前空闲”要分别表达。

如增加 state 参数，候选值为：

| 候选状态 | 建议语义 |
|---|---|
| running | 有活动主 Run，且不处于需要提交的问题/审批/表单等待 |
| awaiting | 有待用户或上级处理的等待项；包括没有活动 Run 但仍待确认的计划 |
| idle | 没有活动主 Run，也没有未决等待项 |

普通工具 wait 不自动等同于“等待人处理”。排队邮件、挂起邮件不应被模糊塞进上述状态；是否增加独立标记另定。

**状态过滤必须发生在最终分页/数量截断之前。** 需要运行状态筛选时，可以先批量取得 Runtime 的目标状态集合，再作为 SQL 条件；必须评估集合大小和参数上限。若要求连状态过滤也完全在数据库完成，则需额外设计持久状态投影和重启/故障对账，当前尚无该能力。

是否本期开放 state、具体排序和 limit/游标协议，以及等待状态与活动状态的优先级，均待确认。

## 7. 第一批已做工作与启动故障记录

### 7.1 第一批

已提交的内容包括统一存储命名、关系字段明确、生成邮件默认权限冻结、notify 先受理后复制、删除分身后保留回信、30 天终态正文清理、两端删除清行、归档删除钩子、迟到完成回调校验及配套测试/文档。

关键代码：[MAILS 结构与收据事务](../internal/mailbox/store.go)、[生成邮件与回信](../internal/mailbox/service.go)、[附件与删除处理](../internal/mailbox/interactions.go)、[保留期](../internal/mailbox/retention.go)、[配置](../configs/runtime.example.yml)、[生命周期回归](../internal/mailbox/lifecycle_test.go)。

### 7.2 V2 引起的启动失败与修复

用户在 2026-10-30 07:16:30 报告本地启动失败：代码要求 application_id=1095781708、user_version=2，现有 mail.db 为相同 application_id、user_version=1。

原因：第一批把尚未上线的结构规范调整当作已发布版本升级，递增为 V2，同时保持严格校验，导致旧库拒绝打开。旧库还有小写旧列结构，只把库内版本号改成 2 也不能解决问题。

用户明确质疑版本提前递增。随后代码改回 `mail-v1` / `user_version=1`，补充当前 V1 可重新打开、结构不匹配的旧 V1 保持拒绝且不改数据的回归；同步文档。该修复与本文纳入本次提交。

本地核实旧 mail.db 为 0 封邮件，已显式备份并按当前结构重建。备份文件包括 `mail.db.before-naming-20261030-072333`。没有在程序中加入自动迁移、自动删库或放宽结构校验。

本次修复验证：Mailbox、App、SQLite contract 测试通过；Server 全量测试一次失败后复跑通过，首次失败原因未定位，不能写为已证明无关；Mailbox 竞态检查通过。实际 make run-local 成功启动并处理 WebClient 请求，验证进程已停止。本地启动验证不等于远端环境发布验证。

### 7.3 本次提交评估（2026-10-30）

- 在 Mailbox 尚未上线、现阶段不承诺旧开发库兼容的前提下，将当前结构固定为 V1 合理。本次仅修改 mail.db 的 Spec 名称和 user_version，产品 VERSION 仍为 v0.5.30，表结构和既有生命周期实现没有回滚。
- 当前 V1 必须正常重开；旧结构 V1 继续被严格结构校验拒绝。补充相同 DDL、标为 V2 的开发库回归，确认拒绝打开后 user_version 仍为 2，邮件数据保持原样。
- 不操作本机真实运行库，不修改其他 SQLite 数据库，不加入自动降级、迁移或清库逻辑。7.2 的备份与启动记录是此前工作记录，本次不据此声称重新验证过该实例或发布环境。
- 本文的 openedBy、CHATS 创建关系投影、状态筛选和统一发送入口均作为讨论材料提交，没有对应 API、数据库列或 WebClient 功能交付。
- 本次提交前评估基线为 `5554545f`；`go test ./internal/mailbox ./internal/sqlitecontract ./internal/app` 与 `git diff --check` 均通过，文档本地链接目标校验通过。未修改 VERSION，未操作或重建真实运行库。

## 8. 后续工作与此前 D1—D9 的处理

| 编号 | 当前处理 |
|---|---|
| D1 保留期 | 第一批已实现默认 30 天；按首次终态时间计算，清正文/快照及附件元数据，保留关系；挂起不清；物理附件跟随收件 Chat 生命周期 |
| D2 临时错误 | 自动退避重试未实现；建议 5 秒起、间隔至多 5 分钟，仅确定未成功投递的临时失败可重试，unknown 保持挂起 |
| D3 分身删除后的回信 | 第一批已实现已受理回信继续投递，附件使用收件侧副本 |
| D4 归档 | 归档时的投递状态转换仍待实现；要区分未消费、已消费和未知证据；恢复后人工重投；归档本身不触发正文删除 |
| D5 审批时限 | 保持现有默认 600 秒及 HITL 配置覆盖；建议展示上区分超时未处理和人工拒绝 |
| D6 用户消息入 mailbox | 尚未实现；letter + user 来源、无回信只是候选形态，需要保留真正的用户输入语义、跳数和权限 |
| D7 不同 Run 设置 | 尚未实现；建议比较有效设置并冻结，排队形成顺序屏障，后续消息不能越过它插入旧 Run |
| D8 旧 HTTP 入口 | `/api/query`、`/api/steer` 仍保留；与删除 chat_start/chat_steer 工具是两件事，不应混为一次删除 |
| D9 主要聊天形态 | 短活自己做、长活派出、工作列表、两处审批属于后续产品方向；导航分组、指针切换和创建关系的具体规则未全部定稿 |

建议的实施顺序（未授权直接执行）：

1. 定稿 openedBy 的创建关系存储、旧聊天库结构处理、单库索引与批量状态方案，明确无需旧 chat_start 兼容。
2. Platform 收尾：归档状态转换、受控临时重试、邮件摘要/详情与 total；列表协议变化必须与 WebClient 同批交付。
3. 工作会话查询与 WebClient 导航联动；收件箱一级入口和挂起角标、请示卡片、代答标记、主视图人工提交与回放一致性。
4. 再讨论统一用户发送入口，包括是否叫 `/api/send`、请求幂等、取消竞争、设置顺序屏障、待发恢复和订阅补偿。不能把尚未确认的新接口当现有依赖。
5. Automation / 外部渠道迁移、旧工具收敛、主要聊天规则定稿，再由 Desktop 对齐。Automation 的初始权限和执行历史合同继续按当前源码处理，不直接套用 Agent 发信规则。

主视图人工提交复用人的 submit 授权，不能受主 Agent 代批档位上限约束；仍须验证目标 Chat 所有权、等待项和冻结调用，硬禁止与只读约束不能放宽。两处同时作答必须只有一次成功。

## 9. 后续实施的验收条件

- openedBy 表示发起者，agentKey 表示所属者，组合结果与权限一致；HTTP/WS 相同。
- 新列表路径的 JSONL 打开次数为 0，去掉旧 chat_start 回退后不再扫描历史正文。
- 证明摘要查询不会先取全部候选 ID 再逐条查库；证明筛选先于最终 limit/游标截断。
- 状态只补需要的结果，避免 P 次扫描全体 Run；保留等待项和多活动 Run 冲突语义。
- 数据库复合索引需用 EXPLAIN QUERY PLAN 核验；记录数据库调用次数、Runtime 扫描次数和文件打开次数。
- 在不同历史 Chat 数、活跃 Run 数和页大小下测量延迟及内存，不能只凭小样本断言快或慢。
- 归档恢复、邮件保留期、删除发起 Chat、主聊天指针切换与创建关系保持规则符合定义。
- 新字段的数据库处理有明确方案，不自动清空历史聊天，不把版本号调整当作结构修复。
- 本文设计需经下一次明确实施指令再修改代码；记录文档本身不代表 API 或数据库字段已上线。
