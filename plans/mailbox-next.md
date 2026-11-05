# Mailbox 与主要聊天：后续计划

本文件是 Mailbox 与主要聊天唯一的后续计划，包含各阶段的实施方案。

- 当前合同见 [docs/Mailbox.md](../docs/Mailbox.md)。
- chats.db 的结构升级与 MAILS 并库暂缓，见 [chats-db-v2.md](chats-db-v2.md)。
- 主要聊天的长上下文、分页回放与切文件见 [primary-chat-long-context.md](primary-chat-long-context.md)。

原 `chat-send-unify.md`、`mailbox-main-chat.md` 已并入本文件，讨论过程见 git 历史。本计划只覆盖 Platform 与 WebClient；Desktop 在 WebClient 定稿后对齐。

## 现状（2026-11-05 核对代码）

已完成：

- 信、回信、即时消息、请示与 `chat_submit` 代答，跳数，重启挂起与人工重投（654a572c 起）。
- MAILS 命名与 chats.db 风格统一（c092292d、a95be546）。
- 平台生成的 mail 冻结收件 Agent 的默认档位；notify 先受理再复制附件；删除分身后，已受理的回信照常投递；30 天终态正文清理；两端删除后清行；删除钩子对账。
- `chat_send` 自动判断落地方式：空闲开 Run，在跑插入，不能插入时排队（`internal/mailbox/service.go` 的 `dispatch`）。
- WebClient：收件箱（从设置菜单进入）、重投与丢弃、时间线来源徽标、`wait` 的 `mail` 唤醒原因。

未完成：

| 项 | 现在的行为 |
|---|---|
| mail 开关 | `mail.enabled` 默认关闭；关闭时邮件 HTTP 接口返回 503，`chat_send` 等工具返回 `mail_unavailable` |
| 重启 | 启动时扫描收件 JSONL，把未完成的信和回信挂起，等人重投或丢弃（`mailbox.Service.Reconcile`） |
| 信封头 | 不落盘；每次组装模型输入时按 mailId 回查 mail.db（`QuerySession.MailHeaders`） |
| mail.db 保护 | 不在工具硬拒绝名单中，full_access 的 Agent 可以直接改写其中的权限冻结 |
| 临时错误自动重试 | 留在 `queued`，只能等同一会话的下一次触发 |
| 归档时关闭 mail | 只有删除有钩子 |
| `/api/mails` | 列表直接返回正文和附件，没有 total，也没有详情接口 |
| `chat_start`、`chat_steer` | 仍在 task-control 中，说明写"优先用 `chat_send`" |
| 工作会话列表 | `chat_query opened` 先查 mail.db，再枚举全部 Chat 读 JSONL；`/api/chats` 没有 openedBy |
| WebClient 展示 | 挂起角标、请示卡片、代答标记、工作会话导航、回放核对 |
| 用户消息 | 仍走 `/api/query`、`/api/steer`；会话忙或卡在等待项上时返回 409 |

## 已确认的决定

- Run 不做地址，一切寄给会话。寄到已有会话是补充那件事，另一件事新开会话。
- 一个 Run 只正式回一次，内容是完整最终输出；不加 `final` 标志。
- 即时消息、请示与信分开；只有 letter 的 `started` 授予发信方 Run 控制。
- 代批上限：上级用自己当前的档位做这个操作不需要审批。
- 新工具统一用 `chat_` 前缀。
- 保持一个 Chat 同时只有一个主 Run；"同时派很多活"靠派给分身实现。
- 工作会话查询复用 `/api/chats` 并增加参数，不新开 `/api/chats/opened`：`agentKey` 表示所属 Agent，`openedBy` 表示发起 Agent，同时出现取交集。
- 工作会话列表不兼容旧 `chat_start` 开出的会话，也不扫描 JSONL。
- 补建的主要对话不算"开出"：主要对话属于 Agent 本身，不属于某次派活。
- 分身审批时限保持 600 秒，超时按拒绝处理。
- 归档的 Chat 退出 mail，处理方式与删除相同；恢复归档不恢复 mail，也不重投。
- 主要聊天 Q1–Q13 采纳下文的建议答案。
- `agent_invoke` 保留为短时的内联子任务（同步执行，结果进入主上下文），不承担后台派活；后台派活统一使用 `chat_send`。

2026-11-05 新增：

- **mail 常开。** 删除 `mail.enabled`，不再有关闭状态。
- **并库前，mail.db 每次启动都清空。** mail.db 保持独立，每次启动先删除再新建；重启前的 mail 不恢复、不挂起、不重投。这是临时做法：本期不改 chats.db，MAILS 并入 chats.db 暂缓；并库后 mail 持久保留，重启挂起、人工重投和保留期清理重新生效。所以这些代码都保留，不删除；启动清空的代码标 `TODO(chats-db-v2)`，并库时删除。
- **`chat_send` 不做 submit。** 作答只有两个入口：Agent 用 `chat_submit`，人用 `/api/submit`。`chat_send` 现有的自动判断不变（规则见 docs/Mailbox.md 的"投递规则"），调用方不需要在 start 和 steer 之间选择。
- **等待项上的会话照现行规则处理。** 停在提问、审批或表单上的会话收到信或用户消息时，先插入，在 Run 内排队，等待项解决后模型才读到，不会被当成作答。
- **删除 `chat_start`、`chat_steer` 提前。** 它们只依赖 mail 常开，不依赖用户消息改走 mailbox（阶段 3）。
- **保留期清理保留**（原待拍板 1）。并库后 mail 持久保留，`mail.retention-days` 与每日清理照旧。
- **工作会话列表暂时只覆盖本次进程**（原待拍板 2）。重启会取消所有 Run 并清空 mail，旧分身会话留在普通历史列表中。chats-db-v2 落地后改用 CHATS 的 `OPENED_BY_` 列，列表才能跨重启。
- **工作会话列表先不做状态筛选**（原待拍板 3），默认返回全部未归档的工作会话。
- **主要聊天开关决定出队策略**（原待拍板 5 的前半）。开启时主聊天按"主助理出队"处理，关闭时沿用现行投递规则，见阶段 6。
- **主要聊天开关放在 Platform 的 `runtime.yml`**（原待拍板 5a，即 P2）。WebClient 和 Desktop 从 Platform 读取（主 WS 的 `connected` 推送），不另存一份，两边始终一致，见阶段 6。
- **主要聊天开启时，主聊天在跑也一律排队，不提供插入**（原待拍板 5b，即 P3）。急改靠中断当前 Run。

## 待拍板

| # | 问题 | 建议 |
|---|---|---|
| 4 | 统一发送入口的名称与协议 | 阶段 5 开始前再定，见阶段 5 的说明 |

## 阶段 1：mail 常开，重启清空

目标：mail 不再是可选功能，mail.db 与进程同生同灭。Platform 与 WebClient 同批发布。

| # | 内容 | 做法 |
|---|---|---|
| 1.1 | 删除开关 | `internal/config`：`MailConfig` 去掉 `Enabled`。按平台配置的硬切惯例，`mail.enabled` 当作未知键，启动失败，不做兼容；已部署的 runtime.yml 需要手工删除该键。`max-hops` 保留。`internal/app/app.go` 无条件打开 mail.db、创建服务、绑定 Runtime、conversation 钩子与 runops，删除 `mailbox disabled` 分支。删除 `internal/server/handler_mailbox.go` 的 503 和 `internal/runops/mailbox.go` 的 `mail_unavailable` |
| 1.2 | 启动时删除 mail.db | `mailbox.Open` 打开前删除 `mail.db`、`mail.db-wal`、`mail.db-shm`：文件不存在则忽略；不是普通文件或删除失败时拒绝启动。然后按当前 DDL 新建。同一 runtime 本来就不支持两个 Platform 进程同时运行，不另加锁 |
| 1.3 | 重启恢复 | 保留 `mailbox.Service.Reconcile`、挂起证据（含 `consumed`）、人工重投和 WebClient 的对应文案：库是新建的，启动时无事可做，并库后重新生效。删除 `mailbox.ValidateRecovery` 和 `runtimerecovery` 中对 mail.db 的校验：mail.db 启动即被删除，结构又可以随时改，校验旧文件只会让恢复失败；并库后由 chats.db 的契约覆盖 |
| 1.4 | 信封头写入 JSONL | 重启删库后再回查 mail.db 就查不到，历史中的信和回信会被模型当成用户消息。改为投递时生成一次信封头，存入 `queryinput.MailInput` 的新字段 `Header`，即 JSONL query/steer 行顶层的 `mailHeader`（平台生成的字段，与 `mailId` 并列）。`internal/llm` 组装历史、steer 和压缩产物时，读行上的 `mailHeader`；压缩产物在 `_mailId` 旁边同时保存 `_mailHeader`。删除 `QuerySession.MailHeaders` 和按 ID 回查的 `mailbox.Header`，只保留渲染函数。回放事件不下发该字段。并库后同样保留这一做法：两端关闭后 MAILS 行会被删除，信封头写在 JSONL 中才不会丢。Mailbox 未上线，旧开发数据不补写 |
| 1.5 | 保护 mail.db | mail 常开后每个部署都有 mail.db，库中保存 subject、冻结档位和审批收据。把 `mail.db`、`-wal`、`-shm` 加入 `internal/accesspolicy/protected.go` 的硬拒绝名单。Bash 中的不透明命令仍无法拦截，与 `.state` 现状相同 |
| 1.6 | schema 规则 | 并库前 mail.db 每次都新建，结构调整直接改 DDL，不升版本号、不写迁移；`application_id` 和 `user_version` 只作身份标记 |
| 1.7 | 保留期 | 不变：`mail.retention-days` 与每日清理照旧，`StartRetention` 改为无条件启动 |
| 1.8 | 文档 | docs/Mailbox.md：删除"启用与关闭"一节；改写"chats.db 与 mail.db"（mail.db 始终存在，并库前启动时清空）、"重启与人工重投"（并库前重启丢弃全部 mail，启动挂起不触发；人工重投仍处理运行期挂起）和"模型输入：信封头"（改为落盘）。AGENTS.md：原地改写 §3 mailbox 模块说明和 §5 的 `mail.db` 条目。同步 `configs/runtime.example.yml` |

在 docs 中写明重启的后果：

- 在途派活不再回信。重启本来就会取消所有 Run，用户需要在主聊天重新发起。
- 发信 Agent 仍可寄往自己以前开出的会话：地址资格保留按 JSONL 首条 query 的 `origin` 判断的回退。

验收：

- 不写 mail 配置时，`chat_send`、`chat_notify`、`chat_submit` 都可用；配置了 `mail.enabled` 时启动失败。
- 启动后 mail.db 为空，残留的 `-wal`、`-shm` 已删除；删除失败时拒绝启动。
- 重启后，历史中的信和回信在模型输入中仍带信封头，文本与重启前相同，prompt 缓存前缀不变。
- 工具读写 mail.db 及其 `-wal`、`-shm` 时被硬拒绝。
- 非 mail 路径行为不变：普通 query、steer、submit，Chat 删除与归档，启动耗时。mail 钩子失败只记日志。`make test` 通过。

## 阶段 2：运行期补齐

| # | 内容 | 做法 |
|---|---|---|
| 2.1 | 临时错误自动重试 | 按会话退避：5 秒起，每次翻倍，最长 5 分钟；同一会话只有一个计时器，日志按会话限频。只重试确定未成功的临时失败，`unknown` 仍然挂起 |
| 2.2 | 归档时关闭 mail | conversation 归档成功后，调用与删除相同的钩子（`MailboxPurge`），失败只记日志。`FROM_CHAT_DELETED_AT_`、`TO_CHAT_DELETED_AT_` 改名为 `FROM_CHAT_CLOSED_AT_`、`TO_CHAT_CLOSED_AT_`（mail.db 每次新建，直接改 DDL）。寄往该会话的：未完成的信转为 `failed` 并寄失败回信，未消费的回信丢弃，即时消息和请示撤回。该会话发出的：已受理的回信照常投递。恢复归档不恢复 mail |
| 2.3 | `/api/mails` 摘要与详情 | 列表去掉 input、accessGrant 和正文全文，只保留正文前 200 字，增加 `total`；新增 `GET /api/mails/detail?mailId=`，返回全文、附件和等待项快照。WebClient 收件箱同批改为点开后再取详情 |
| 2.4 | 收件箱入口（WebClient） | 收件箱从设置菜单移到侧栏一级入口，显示挂起数量角标（`status=suspended` 的 `total`，收到 `mail.updated` 时刷新） |

## 阶段 3：删除 `chat_start`、`chat_steer`

`chat_send` 已覆盖这两个工具的用途：

- 用 `newChat` 开分身。目标是自己时，继承当前模型和思考强度，与 `chat_start` 共用 `startRequest`。
- 寄往已有会话：在跑时插入，空闲时开 Run。

做法：

- 删除 `internal/resources/tools/chat_start.yml`、`chat_steer.yml`，以及 task-control 连接器中的这两个工具。runops 中只服务这两个工具的 handler、幂等收据和一层限制一并删除。保留由信开出的 Run 调用 `chat_get_status`、`chat_interrupt` 或使用 `wait` 的 `run.terminal` 时返回 `run_chaining_not_allowed` 的规则。
- 跳数规则删除"`chat_start` 开出的 Run 为父 Run 当前跳数 + 1"。
- 已有的 `run-query:` 来源 Chat 属于历史数据，归属判断（`mailChatOwned`）继续读取。地址资格的 JSONL 首条 query 回退保留，阶段 1 之后它也服务重启前由 mail 开出的会话。
- 清理以下位置对这两个工具的引用：
  - task-control 手册、`wait.yml`、`chat_interrupt.yml`；
  - docs/子智能体调度.md、docs/Mailbox.md 的工具表；
  - AGENTS.md：原地改写 task-control 工具数量、"新开会话"用语和 `chat_start` 准入规则这几句；
  - 相邻仓库：WebClient 测试中的工具名，以及 builtins 中引用它们的技能或 Agent。

验收：

- 工具表和手册中不再出现这两个工具。
- 派活、补充、更正都用 `chat_send` 完成。
- `make test` 通过。

## 阶段 4：工作会话与请示处理

| # | 内容 | 做法 |
|---|---|---|
| B1 | 工作会话列表 | 见下文。WebClient 在主聊天导航下列出，"等待处理"的高亮。跳数达到上限、请示不再上报时，用户也能在这里发现等待处理的会话 |
| B2 | 请示卡片 | 在主聊天时间线中把请示显示为卡片：分身名称、问题、审批命令或表单字段，以及"去分身会话处理"。被答复、超时或撤回后，按 `mail.updated` 变灰 |
| B3 | 代答展示 | 分身会话中由上级 `chat_submit` 作答的记录显示"由 X 代答"，审批另外显示依据的档位 |
| B4 | 主视图直接审批 | 在请示卡片内直接作答：以人的身份对分身的 `runId/awaitingId` 调用 `/api/submit`，不受代批上限约束。仍校验 Chat 归属、等待项和冻结的调用，硬禁止与只读不放宽；两处同时作答时只有一次成功 |
| B5 | 回放核对 | 刷新和历史回放后，信封来源、回信 outcome、请示卡片状态与实时一致 |

### B1 工作会话列表

**数据源：mail.db。** MAILS 增加 `NEW_CHAT_` 列，只有 `chat_send {newChat:true}` 建的会话记为 1。补建的主要对话仍记 `OPENED_CHAT_ = 1`（这一列用于受理重放），但 `NEW_CHAT_` 为 0，不算开出。

**接口**

```http
GET /api/chats?openedBy=A
GET /api/chats?openedBy=A&agentKey=B
```

- HTTP 与主 WS 共用同一应用服务和筛选规则。用户身份取自可信上下文，不允许通过参数指定 subject。摘要响应的形状不变。
- conversation 先从 mailbox 取出由 A 开出、属于当前 subject 的 chatId 集合（只含本进程内的记录，数量有限），再在 chats.db 中按 `CHAT_ID_ IN (...)` 与其他条件一起筛选和截断。
- `chat_query opened` 改走同一查询，删除 `MailOpenedChats` 中枚举全部 Chat、读 JSONL 的部分。
- 重启后列表为空（已暂时接受）。chats-db-v2 落地后，数据源改为 CHATS 的 `OPENED_BY_` 列，接口不变。

**状态**

- 默认返回所有未归档的工作会话，状态分为运行中、等待处理、空闲。Chat 可以多次运行，成功或失败是某个 Run 的结果，不是 Chat 的状态。
- 由 Runtime 按本页的 chatId 批量提供状态，只遍历一次 Run 集合。现有 `mapChatSummariesWithActiveRuns` 对每个 Chat 都扫描一遍 Run 集合，不能沿用。
- 普通 `wait` 不算等待处理；排队或挂起的 mail 不混入这三种状态。

**验收**

- openedBy 与 agentKey 组合的结果与权限一致，HTTP 与 WS 相同。
- 列表路径打开 JSONL 的次数为 0。
- 补齐状态的开销不随"本页 Chat 数 × Run 数"增长。
- 归档后恢复、删除发起会话、切换主要对话指针之后，创建关系仍符合定义。

## 阶段 5：用户消息走 mailbox

目标：用户随时都能发消息，由平台决定开 Run、插入还是排队；客户端不再区分 query 和 steer，也不再因会话忙收到 409。主聊天由此不再有绕过 mailbox 的输入路径。

**为什么需要新的发送入口。** 现在 WebClient 输入框的发送方式：

- 会话空闲时调用 `/api/query`；
- 会话在跑时调用 `/api/steer`；
- 卡在等待项上或发生冲突时收到 409。

`/api/query` 的响应就是这次 Run 的 SSE 事件流，而排队中的消息还没有 Run，没有流可以返回。所以用户消息需要一个只负责"投进 mailbox"的入口：它立即返回受理结果，包括 `mailId`、`delivery: started|inserted|queued`，开了 Run 时还有 `runId`；事件仍通过现有的推送与挂接获得。这个入口原拟名 `POST /api/send`，主 WS 上对应同名的帧类型。

开始前需要定下：

- 入口的名称与协议（待拍板 4），HTTP 与主 WS 使用同一套逻辑：
  - 请求字段：目标（chatId 或 agentKey）、message、references、上传文件、运行选项、幂等键；
  - 响应形状；
  - 与 `/api/query` 的新建会话、`primary:true` 的对应关系。
- 用户消息在 MAILS 中的形态：letter，`ORIGIN_TYPE_ = user`，hop 为 0，不生成回信，不加信封头。保留用户输入的语义（chat 引用资格、跳数归零）和权限。
- 落地沿用"投递规则"：
  - 空闲时开 Run，在跑时插入。
  - 规划 Run、收尾或压缩中、有未决等待项时排队。
  - 停在提问、审批或表单上时插入，等待项解决后才被读到，不代替作答。作答仍走 `/api/submit`。
- 运行选项（`planningMode`、`editingMode`、`model`、`accessLevel`）只在开新 Run 时生效。与当前 Run 设置不同的消息进入排队，形成顺序屏障，后面的消息不能越过它插入旧 Run。
- 请求幂等，以及取消排队（`/api/mails/action` 增加 `cancel`，只对 `queued` 有效）与投递之间的竞争：两者只有一方成功。
- 重启：并库前 mail.db 被清空，服务端不再有未投递的用户消息。WebClient 在本地保留尚未确认消费的待发气泡；重连后服务端查不到该 mailId 时，标为"未送达"，用户可以重发（作为新消息）或删除。
- 订阅：先沿用 `run.started` 推送加自动挂接；仍有遗漏时再增加 `chat.subscribe {chatId}`。
- `/api/query`、`/api/steer` 保留给 Desktop、gateway、Automation、execute 和渠道使用，何时删除另行决定。

WebClient：

- 输入框一律调用新入口。
- 消息显示为待发气泡，状态为排队中、已插入或已开始，排队中的可以取消。
- 去掉"运行中改走 steer"的分支和 `active_run_conflict` 的提示路径。

随后，Automation 与渠道改为发信：

- 走同一入口（origin 为 automation 或 channel），目标会话忙时排队，不再拒绝。
- Automation 的初始权限和执行历史合同沿用现有源码，不套用 Agent 发信规则。
- 开始前定下：重启时仍在排队的 Automation 消息，在执行历史中记为什么结果。

## 阶段 6：主要聊天形态与 Desktop

| # | 内容 | 做法 |
|---|---|---|
| 6.1 | 主要聊天形态定稿 | 把下表的答案写入 docs |
| 6.2 | 主要聊天出队策略 | 见下文；开关位置与插入规则待拍板 |
| 6.3 | 长上下文 | 见 [primary-chat-long-context.md](primary-chat-long-context.md)。约束：切换聊天文件必须保持同一个 chatId。mail 的地址在发信时确定且不再改变，换 chatId 会让分身的回信、即时消息和请示落回旧会话 |
| 6.4 | Desktop 对齐 | WebClient 定稿后进行 |

### 主要聊天形态

WebClient 的主要聊天模式把用户收拢到一个主窗口，但执行仍然是一个 Chat 只有一个 Run。目标是主 Agent 一边派活、一边接用户指令，让用户感觉可以同时派出很多件事。

mailbox 已经解决了派活结果的回传和多层派活。剩下两个问题：主窗口怎样呈现派出去的工作（阶段 4），以及用户输入怎样不被挡住（阶段 5）。

| # | 问题 | 建议答案 |
|---|---|---|
| Q1 | 主聊天只做调度，还是也跑长任务 | 都做：短活自己做；长任务或另一件事用 `chat_send {newChat:true}` 派给分身。规则写进 task-control 手册 |
| Q2 | 派出去的活放在哪里 | 挂在主聊天下的工作会话列表中（B1），不进普通历史列表的顶层 |
| Q3 | 结果怎么回来 | 已由 mailbox 解决：回信自动开 Run |
| Q4 | 派给自己的分身是否作为默认方式 | 不设默认，由模型按 Q1 的规则选择 |
| Q5 | 能否多层派活 | 已由 mailbox 解决：可以，受跳数限制 |
| Q6 | `primary_chat_busy` 是否保留 | 保留，只用于切换指针；阶段 5 之后用户消息不再因会话忙被拒 |
| Q7 | 审批在哪里处理 | 两处都可以：主视图卡片（B4）或分身会话 |
| Q8 | 任务表、Kanban、云端 Job、mail 是否合成一套 | 工作会话列表就是任务托盘；Kanban 和云端 Job 本期不合并 |
| Q9 | 主聊天忙时，Automation 或渠道指向它怎么办 | 见阶段 5：排队 |
| Q10 | Desktop 与 WebClient 的呈现是否一致 | WebClient 定稿后 Desktop 对齐 |
| Q11 | 主聊天与分身用什么模型 | 主聊天用最强模型、低思考；分身的思考强度由主聊天在 `newChat` 时指定。思考强度跟随 Chat，只有 `newChat` 能带新的值。语音暂不纳入主要聊天 |
| Q12 | 主聊天上的确认能等多久 | 可以一直等。依赖等待项过期时间的改造（见 chats-db-v2 的"另做"） |
| Q13 | 主聊天怎样了解分身的进度 | 只读探测，不借用 steer，也不用 notify 把进度塞进主时间线。先确认 `chat_get_status {mailId}` 是否够用，不够再加最小的只读接口 |

### 主要聊天出队策略

主要聊天开关只决定主聊天（Agent 的主要对话）的出队方式，不决定是否进 mailbox。其他会话始终按现行投递规则处理。依赖阶段 5：用户消息要先进 mailbox，才谈得上排队。

| 主聊天状态 | 关闭：现行投递规则 | 开启：主助理出队 |
|---|---|---|
| 空闲 | 用最早一条开 Run，其余插入这个 Run | 用最早一条开 Run，其余留在队列 |
| 在跑 | 插入当前 Run | 留在队列，当前 Run 结束后按顺序逐条开 Run，不提供插入（P3） |
| 停在提问、审批或表单上 | 插入，等待项解决后才被读到 | 留在队列；作答仍走 `/api/submit` |
| 规划 Run、收尾或压缩中、有未决等待项 | 排队 | 排队 |

- 用户消息与分身的回信、即时消息、请示在同一队列里，按到达顺序处理。Automation 和渠道发往主聊天的消息同样排队（Q9）。
- 请示排在队列中时，用户仍可以从请示卡片（B2、B4）直接作答，不必等主聊天读到；否则一个长 Run 之后，请示可能已经超过 600 秒的时限。
- 主聊天读到一件事后怎样处理，由主聊天的模型决定：自己做，用 `chat_send {chatId}` 补充给已有的分身，或用 `chat_send {newChat:true}` 新开分身（Q1、Q4）。出队策略只决定主聊天什么时候读到这件事。

P1（是否需要单独的出队策略）已确认：需要。

**P2 开关（已确认）**

- 出队发生在服务端，分身回信到达时没有客户端在场，所以开关由 Platform 保存：

  ```yaml
  # configs/runtime.yml
  primary-chat:
    enabled: false   # 默认关闭，即现行投递规则；启动时读取，修改后重启生效
  ```

- 作用于所有 Agent 的主要对话（`chat-primary.json` 指向的 Chat），其他会话不受影响。
- 客户端从 Platform 读取这个值，不另存一份，所以两边始终一致：
  - 主 WS 的 `connected` 推送增加 `primaryChat: {enabled}`；
  - 另在一个 HTTP 读取入口提供，实施时选现有接口。
- WebClient 现在的"主要对话模式"只是本地导航状态（`primaryNavigation`），Platform 并不知道。改为跟随 Platform 的值：开启时使用主要对话导航，关闭时使用普通导航，去掉本地切换按钮。否则用户在普通导航下发往主聊天的消息仍会被排队，两边对不上。Desktop 同样跟随。
- 开关切换后，已经插入的消息不受影响，只改变之后的出队。

**P3 主聊天在跑时，用户的新消息一律排队（已确认）**

这里只管"主聊天正在跑一个 Run 时，用户又发来一句"。例如主聊天正在写一段回复，用户补一句"用英文写"：如果排队，这句话要等当前 Run 结束才被读到。

开启时一律排队，不提供插入。理由：
- 按 Q1，主聊天的 Run 本来就短，排队等待的时间不长；
- 需要立刻更正时，用户可以中断当前 Run，队列随即出队；
- 不必在发送协议中增加插入标记，也不用区分哪些消息算更正。

分身寄来的 mail 和模型在任何情况下都不能插队。
