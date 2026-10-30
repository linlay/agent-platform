# Mailbox 与主要聊天：后续计划

本文件是 Mailbox 与主要聊天的唯一后续计划。当前合同（含 chats.db 与 mail.db 的边界）见 [docs/Mailbox.md](../docs/Mailbox.md)。原 `plans/mailbox.md`、`plans/primary-chat-dispatch.md`、`plans/mailbox-opened-chats-review.md` 已并入这两份文档，讨论过程见 git 历史。

本计划只覆盖 Platform 与 WebClient；Desktop 在 WebClient 定稿后对齐。

## 现状（2026-10-30 核对代码）

已完成：

- 信、回信、即时消息、请示与 `chat_submit` 代答，跳数，重启挂起与人工重投（654a572c 起）。
- MAILS 命名与 chats.db 风格统一，schema 固定为 V1 基线（c092292d、a95be546）。
- 平台生成的 mail 冻结收件 Agent 默认档位；notify 先受理再复制附件；删除分身后已受理的回信照常投递；30 天终态正文清理、两端删除后清行、删除钩子对账。
- `mail.enabled` 默认关闭（a6f54762）。
- WebClient：收件箱（从设置菜单进入）、重投与丢弃、时间线来源徽标、`wait` 的 `mail` 唤醒原因。

未完成：

| 项 | 现在的行为 |
|---|---|
| `/api/mails` 摘要、详情、total | 列表直接返回正文和附件，没有 total，也没有详情接口；WebClient 读取 `{mails}` |
| 临时错误自动重试 | 留在 `queued`，只能等同一会话的下一次触发；一直没有触发就停到重启后挂起 |
| 归档时的状态转换 | 只有删除有钩子；已归档会话不再产生触发，归档时排队的 mail 停到重启 |
| 工作会话列表 | `chat_query opened` 先查 mail.db，再枚举全部 Chat、读 JSONL 回退、逐条查摘要和活动 Run；`/api/chats` 没有 openedBy |
| WebClient 展示 | 挂起角标、请示卡片、代答标记、工作会话导航、回放核对 |
| 后续阶段 | 统一用户发送入口、主视图直接审批、Automation/渠道改发信、删除 `chat_start`/`chat_steer` |

## 已确认的决定

- Run 不做地址，一切寄给会话。寄到已有会话是补充那件事，另一件事新开会话。
- 一个 Run 只正式回一次，内容是完整最终输出；不加 `final` 标志。
- 即时消息、请示与信分开；只有 letter 的 `started` 授予发信方 Run 控制。
- 代批上限：上级用自己当前的档位做这个操作不需要审批。
- 新工具统一用 `chat_` 前缀；`chat_start`、`chat_steer` 暂时保留，阶段 D 删除。
- MAILS 的表、字段、索引命名与 chats.db 一致；Mailbox 未上线，schema 以 V1 为基线，不迁移、不自动清库。
- 保持一个 Chat 同时只有一个主 Run；"同时派很多活"靠派给分身实现。
- 工作会话查询复用 `/api/chats` 并增加参数，不新开 `/api/chats/opened`：`agentKey` 表示所属 Agent，`openedBy` 表示发起 Agent，同时出现取交集。
- 工作会话列表不兼容旧 `chat_start` 开出的会话，也不扫描 JSONL。
- 分身审批时限保持 600 秒，超时按拒绝处理。

## 待拍板

| # | 问题 | 建议 |
|---|---|---|
| 1 | openedBy 的创建关系存在哪里 | 存 chats.db，见下文"openedBy 方案" |
| 2 | 默认开启 mail 的门槛 | 完成阶段 A 的 A1–A4 后再切换默认值 |
| 3 | 补建的主要对话算不算"开出" | 不算：主要对话属于 Agent 本身，不属于某次派活 |
| 4 | 工作会话列表本期是否支持状态筛选，以及排序、分页协议 | 先不做状态筛选，默认返回全部未归档的工作会话 |
| 5 | 统一发送入口的名称与协议 | 阶段 C 开始前再定 |
| 6 | 主要聊天 Q1–Q10 是否采纳下文的建议答案 | 采纳 |
| 7 | 文件名 `mail.db` 是否改为 `mails.db`，与 chats.db 对齐 | 未上线，可以直接改，同时改 runtime-recovery 与文档 |

## 阶段 A：可以默认开启

重启会把所有未完成的信挂起，必须由人处理。没有显眼的入口和自动恢复，默认开启就等于工作悄悄停住。Platform 与 WebClient 同批发布。

| # | 内容 | 做法 |
|---|---|---|
| A1 | 临时错误自动重试 | 按会话退避：5 秒起，翻倍，最长 5 分钟；同一会话只有一个计时器，日志按会话限频。只重试确定未成功的临时失败，`unknown` 仍然挂起 |
| A2 | 归档时的状态转换 | 增加与删除钩子并列的归档钩子。寄往该会话的未完成信转为 `failed` 并寄失败回信；回信挂起，证据区分未消费、已消费和未知，恢复会话后可人工重投；即时消息和请示撤回。不清正文，恢复会话不自动重投 |
| A3 | `/api/mails` 摘要与详情 | 列表去掉 input、accessGrant 和正文全文，只保留正文前 200 字，增加 `total`；新增 `GET /api/mails/detail?mailId=`，返回全文、附件和等待项快照。WebClient 收件箱同批改为点开后再取详情 |
| A4 | 挂起提醒（WebClient） | 收件箱从设置菜单移到侧栏一级入口，显示挂起数量角标（`status=suspended` 的 `total`，收到 `mail.updated` 时刷新）；启动后若有挂起 mail，提示一次 |
| A5 | 切换默认值 | A1–A4 完成后，`mail.enabled` 默认改为 true，同步 `runtime.example.yml` 与文档 |

## 阶段 B：工作会话与请示处理

| # | 内容 | 做法 |
|---|---|---|
| B1 | 工作会话列表 | 见下文"openedBy 方案"；WebClient 在主聊天导航下列出，"等待处理"的高亮。跳数达到上限、请示不再上报时，用户也能在这里发现等待处理的会话 |
| B2 | 请示卡片 | 主聊天时间线中把请示显示为卡片：分身名称、问题、审批命令或表单字段，以及"去分身会话处理"。被答复、超时或撤回后按 `mail.updated` 变灰 |
| B3 | 代答展示 | 分身会话中由上级 `chat_submit` 作答的记录显示"由 X 代答"，审批另外显示依据的档位 |
| B4 | 主视图直接审批 | 在请示卡片内直接作答：以人的身份对分身的 `runId/awaitingId` 调用 `/api/submit`，不受代批上限约束。仍校验 Chat 归属、等待项和冻结调用，硬禁止与只读不放宽；两处同时作答时只有一次成功 |
| B5 | 回放核对 | 刷新和历史回放后，信封来源、回信 outcome、请示卡片状态与实时一致 |

### openedBy 方案

按 [chats.db 与 mail.db](../docs/Mailbox.md#chatsdb-与-maildb) 的边界，"哪个 Agent 开出了这个 Chat"是 Chat 创建时就确定、之后不变的 Chat 属性，应存在 chats.db 中。目前它只在 mail.db 里，而且 `OPENED_CHAT_` 的含义也不一样。

**接口**

```http
GET /api/chats?openedBy=A
GET /api/chats?openedBy=A&agentKey=B
```

HTTP 与主 WS 共用同一应用服务和筛选规则；用户身份取自可信上下文，不允许通过参数指定 subject；摘要响应的形状保持不变。

**存储（推荐）**

CHATS 增加 `OPENED_BY_AGENT_KEY_`、`OPENED_BY_CHAT_ID_`，并建立 `(OPENED_BY_AGENT_KEY_, UPDATED_AT_ DESC, CHAT_ID_ DESC)` 复合索引。

- mailbox 建 Chat 时（`EnsureMailChat` → `EnsureChatWithInitialName`），由 Runtime 写入这两个字段；mailbox 仍不直接写 chats.db。
- 只在 `newChat` 建会话时写入，补建的主要对话不写（待拍板 3）。
- 写入后不再改变：之后的寄信、引用、插话和主要对话指针切换都不改写。归档与恢复时保留，派生出的新 Chat 不继承。
- 归属仍按 `SOURCE_` 判断，需要确认能否直接在 SQL 中按用户筛选。
- `chat_query opened` 改走同一查询，删除 JSONL 回退和旧 `chat_start` 兼容。

代价：这是 chats.db 第一次改结构。sqlitecontract 在运行期只校验、不迁移，runtime-recovery 也没有格式转换器，所以要先设计 chat-v2 的显式升级路径：不能靠扫描全部 JSONL 重建，也不能清空历史 Chat。archive.db 的 `ARCHIVED_CHATS` 是否同步加列，一并确定。

**备选**

MAILS 增加 `(SUBJECT_, FROM_AGENT_KEY_, OPENED_CHAT_)` 索引，先取出 chatId，再批量查询 CHATS。mail.db 尚未上线，改索引成本低。但这样一来，Chat 列表就依赖 mail.db，违反"chats.db 不依赖 mail.db"的边界：mail 关闭时功能会消失，排序和分页也要跨库处理。只适合作过渡方案。

**状态**

- 默认返回所有未归档的工作会话，状态分为运行中、等待处理、空闲。Chat 可以多次运行，成功或失败是某个 Run 的结果，不是 Chat 的状态。
- 由 Runtime 按本页的 chatId 批量提供状态，只遍历一次 Run 集合。现有 `mapChatSummariesWithActiveRuns` 对每个 Chat 都扫描一遍 Run 集合，不能沿用。
- 如果开放状态筛选，筛选必须在分页截断之前完成。普通 `wait` 不算等待处理；排队或挂起的 mail 不混入这三种状态。

**验收**

- openedBy 与 agentKey 组合的结果与权限一致，HTTP 与 WS 相同。
- 列表路径打开 JSONL 的次数为 0。
- 筛选在数据库内完成并先于 limit；用 EXPLAIN QUERY PLAN 确认走复合索引。
- 补齐状态的开销不随"本页 Chat 数 × Run 数"增长。
- 在不同的历史 Chat 数、活跃 Run 数和页大小下，测量延迟与内存。
- 归档恢复、正文保留期到期、删除发起 Chat、切换主要对话指针之后，创建关系仍符合定义。

## 阶段 C：用户消息走 mailbox

目标：用户随时都能发消息，由平台决定开 Run、插入还是排队；客户端不再区分 query 和 steer，也不再因会话忙收到 409。阶段 C 之后，用户输入依赖 mail.db，mail 不再是可选功能，因此必须先完成阶段 A。

开始前需要定下：

- 入口的名称与协议（原拟 `POST /api/send`，尚未确认），HTTP 与主 WS 使用同一套逻辑。
- 用户消息在 mail.db 中的形态。候选是 letter + `ORIGIN_TYPE_ = user`、hop 为 0、不生成回信；需要保留真正的用户输入语义、跳数归零和权限。
- 运行选项（`planningMode`、`editingMode`、`model`、`accessLevel`）只在开新 Run 时生效。与当前 Run 设置不同的消息进入排队，形成顺序屏障，后面的消息不能越过它插入旧 Run。
- 请求幂等，以及取消排队（`/api/mails/action` 增加 `cancel`，只对 `queued` 有效）与投递之间的竞争。
- 重启后未投递的用户消息挂起，由用户重发或丢弃。
- 订阅：先沿用 `run.started` 推送加自动挂接；仍有遗漏时再增加 `chat.subscribe {chatId}`。
- `/api/query`、`/api/steer` 保留给 Desktop、gateway、Automation、execute 和渠道使用。何时删除另行决定，与删除 `chat_start`/`chat_steer` 工具是两件事。

WebClient：输入框一律调用新入口。消息显示为待发气泡，状态为排队中、已插入或已开始，排队中的可以取消。去掉"运行中改走 steer"的分支和 `active_run_conflict` 的提示路径。

## 阶段 D：收敛与主要聊天形态

| # | 内容 | 做法 |
|---|---|---|
| D1 | Automation 与渠道改为发信 | 走阶段 C 的入口（origin 为 automation 或 channel），目标会话忙时排队，不再拒绝。Automation 的初始权限和执行历史合同沿用现有源码，不套用 Agent 发信规则 |
| D2 | 删除 `chat_start`、`chat_steer` | 阶段 C 稳定后硬切删除，连同 runops 的一层限制、工具 YAML、task-control 手册和文档 |
| D3 | 主要聊天形态定稿 | 把下表的答案写入 docs |
| D4 | Desktop 对齐 | WebClient 定稿后进行 |

### 主要聊天形态

WebClient 的主要聊天模式把用户收拢到一个主窗口，但执行仍然是一个 Chat 只有一个 Run：主聊天跑长任务时，用户只能插话，卡在审批上时还会被 409 拒绝。目标是主 Agent 一边派活、一边接用户指令，让用户感觉可以同时派出很多件事。mailbox 已经解决了派活的结果回传和多层派活；剩下的问题是主窗口怎样呈现派出去的工作（阶段 B），以及用户输入怎样不被挡住（阶段 C）。

| # | 问题 | 建议答案 |
|---|---|---|
| Q1 | 主聊天只做调度，还是也跑长任务 | 都做：短活自己做；长任务或另一件事用 `chat_send {newChat:true}` 派给分身。规则写进 task-control 手册 |
| Q2 | 派出去的活放在哪里 | 挂在主聊天下的工作会话列表中（B1），不进普通历史列表的顶层 |
| Q3 | 结果怎么回来 | 已由 mailbox 解决：回信自动开 Run |
| Q4 | 派给自己的分身是否作为默认方式 | 不设默认，由模型按 Q1 的规则选择 |
| Q5 | 能否多层派活 | 已由 mailbox 解决：可以，受跳数限制 |
| Q6 | `primary_chat_busy` 是否保留 | 保留，只用于切换指针；阶段 C 之后用户消息不再因会话忙被拒 |
| Q7 | 审批在哪里处理 | 两处都可以：主视图卡片（B4）或分身会话 |
| Q8 | 任务表、Kanban、云端 Job、mail 是否合成一套 | 工作会话列表就是任务托盘；Kanban 和云端 Job 本期不合并 |
| Q9 | 主聊天忙时，Automation 或渠道指向它怎么办 | 见 D1：排队 |
| Q10 | Desktop 与 WebClient 的呈现是否一致 | WebClient 定稿后 Desktop 对齐 |

派活入口：`agent_invoke` 保留为短时的内联子任务（同步执行，结果进入主上下文），不承担后台派活；后台派活统一使用 `chat_send`。

## 需要同步修改

`docs/Mailbox.md`、`docs/API与协议.md`、`docs/HITL协议.md`、`docs/子智能体调度.md`、`AGENTS.md`、`configs/runtime.example.yml`、task-control 手册；WebClient 的接口类型、文案和测试。
