# Mailbox 与主要聊天：后续计划

本文件是 Mailbox 与主要聊天唯一的后续计划，包含各阶段的实施方案。

- 当前合同见 [docs/Mailbox.md](../docs/Mailbox.md)。
- chats.db 的结构升级与 MAILS 并库暂缓，见 [chats-db-v2.md](chats-db-v2.md)。
- 主要聊天的长上下文、分页回放与切文件见 [primary-chat-long-context.md](primary-chat-long-context.md)。

原 `chat-send-unify.md`、`mailbox-main-chat.md` 已并入本文件，讨论过程见 git 历史。本计划只覆盖 Platform 与 WebClient；Desktop 在 WebClient 定稿后对齐。

## 执行记录（2026-11-06）

阶段 1–4 及阶段 5 的用户消息部分已完成本地实现与自动化验收，覆盖 Platform 和相邻 `agent-webclient`；Automation/渠道改为发信的方案已于 2026-11-07 确定，尚未实施。阶段 6 的 Platform 出队与主要聊天/分身分工已完成本地实现；6.7–6.8、长上下文分页及 Desktop 对齐尚未实施。2026-11-07 改为新增 `mode: BOT`，已实现的开关与 GENERAL 判断按 6.9 改造，导航按 6.10 改为三区；6.6 取消。

- 阶段 1：mail 常开、旧 enabled 键拒绝、启动清库、信封头随 JSONL 与压缩快照保留、数据库硬拒绝保护；恢复与保留期代码保留。
- 阶段 2：会话退避重试、归档关闭、列表摘要与 total、按需详情、侧栏入口与挂起角标。
- 阶段 3：移除两个旧工具及其生产 handler/幂等收据，后台派活统一 chat_send；保留信派生 Run 的控制限制与历史地址资格回退。
- 阶段 4：NEW_CHAT_ 创建关系、HTTP/WS openedBy 筛选、无 JSONL 列表查询、批量 Run 状态、工作会话导航、请示卡片、人工直接提交、代答来源与档位、实时与回放核对。
- 验证：`make test` 全部通过（含 memx 集成）；WebClient `npm test -- --runInBand` 502 个套件、4468 项测试通过，后续卡片/回放/请求相关回归通过；最终 `npm run build` 通过。新增用例覆盖数据库硬拒绝、重启清库、历史信封、归档钩子、工作列表零 JSONL 读取、批量状态、模型继承、详情隔离与卡片提交。
- 相邻 builtins 中未发现两个退役工具的引用，无需修改。未进行生产部署或真人联调。

部署时需删除既有 runtime.yml 的 mail.enabled；并库前每次启动会清空 mail.db，在途派活需要用户重新发起。

## 实施前现状（2026-11-05 核对代码）


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
- **主要聊天开关决定出队策略**（原待拍板 5 的前半）。开启时主聊天按"主助理出队"处理，关闭时沿用现行投递规则，见阶段 6。（2026-11-07 改为由 BOT mode 决定。）
- **主要聊天开关放在 Platform 的 `runtime.yml`**（原待拍板 5a，即 P2）。WebClient 和 Desktop 从 Platform 读取（主 WS 的 `connected` 推送），不另存一份，两边始终一致，见阶段 6。（2026-11-07 取消，开关删除。）
- **主要聊天开启时，主聊天在跑也一律排队，不提供插入**（原待拍板 5b，即 P3）。急改靠中断当前 Run。（2026-11-07 起适用于 BOT 主聊天。）

2026-11-06 新增：

- **主要聊天只调度，分身干活不调度。** 同一个 Agent 按会话分两个角色：主要聊天只有平台固定的五个调度工具和 action_announce 进度工具，agent.yml 只描述分身；分身没有调度工具，靠自动回信和 `chat_notify` 回复。调度提示词放全局配置，不新增 `PRIMARY.md`。（"不新增 agent.yml mode"已于 2026-11-07 改为新增 BOT mode，见下。）
- **分身省略 `accessLevel` 时继承主要聊天受理时的当前档位**，不再用 Agent 默认值。

2026-11-07 补充确认：`mail.max-hops` 先保留 4，当前跳数含义不变；接受一次用户输入后典型顺序派活只能往返两轮的限制，暂不调整默认值或计数语义（原待拍板第 7 项）。

2026-11-07 剩余任务确认：

- **放弃 gateway。** agent-gateway 目前不使用，不转发 `/api/send`、`/api/mails*` 和 `mail.updated`。主要聊天、用户消息发送和收件箱只支持直连 Platform 与 Desktop。
- **mail.db 继续独立，暂不并入 chats.db。** [chats-db-v2.md](chats-db-v2.md) 继续暂缓。这些现状因此长期保留：每次启动清空 mail.db，排队中的 mail 不跨重启，openedBy 工作会话列表重启后为空。（BOT 的分身身份与"进行中"列表按 Agent 判断，不受影响，见阶段 6。）
- **Automation 与渠道统一改为发信**，见阶段 5 末尾。原待拍板第 6 项已定。
- 以下各项按建议确认（用户未提异议）：分身身份短读、默认调度提示词只留 YAML、主要聊天拒绝 `/api/steer`（阶段 6.6–6.8），长上下文首版分页（见 [primary-chat-long-context.md](primary-chat-long-context.md)），关闭 Q13，Q12 维持现状。

2026-11-07 BOT mode：

- **新增 `mode: BOT`。** 调度角色归 BOT 这个 mode，取代 `runtime.yml` 的 `primary-chat.enabled` 与 Native GENERAL 限制。理由：BOT 不会是 CODER，规划对 BOT 没用，独立 mode 便于管理、更清晰。BOT 与 TEAM 同为带运行逻辑的 mode。见阶段 6 的"BOT mode"。
- **BOT 除主聊天外的聊天一律是分身**，用户不能给 BOT 新建普通对话。
- **GENERAL 的主要聊天变回普通聊天**，不迁移；需要机器人时新建 BOT Agent。
- **BOT 可以有 Workspace，没有规划。**
- **导航三区并存**：机器人、对话、项目，用户自行隐藏，属于客户端显示偏好，后端不记录（6.10）。
- **所有消息都走 mailbox，`/api/execute` 也是**，见阶段 5 末尾；gateway 不管。
- 原待拍板第 8 项关闭；6.6 分身身份短读取消。

## 待拍板

| # | 问题 | 建议 |
|---|---|---|
| 9 | 语音定稿（`internal/voice/session.go`）与 WebApp 助手（`internal/webappbridge/assistant.go`）仍直接开 Query，按"所有消息都走 mailbox"也要改为发信 | 都作为用户消息发信：会话忙时排队，不再返回 `chat_busy`；语音发给 BOT 时进入主聊天，照常朗读主聊天的回复。实施前再核对语音的打断、等待项提示和朗读时机 |

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

**为什么需要新的发送入口。** 现在 WebClient 输入框在会话空闲时调用 `/api/query`，在跑时调用 `/api/steer`，卡在等待项上或发生冲突时收到 409。`/api/query` 的响应就是这次 Run 的 SSE 事件流，而排队中的消息还没有 Run，没有流可以返回。所以用户消息需要一个只负责"投进 mailbox"的入口，立即返回受理结果，事件仍通过现有的推送与挂接获得。

### 已确认的设计（2026-11-06）

- 入口为 `POST /api/send`，主 WS 用同名帧 `/api/send`。
- **人与 Agent 一视同仁。** 用户消息和 Agent 的信是同一张表、同一套投递规则、同一个收件箱。差别只来自发件人就是收件会话本身：不生成回信，不加信封头，不产生请示，跳数为 0。
- **运行选项只在开新 Run 时生效。** 插入正在运行的 Run 时直接忽略，不排队，在响应的 `ignoredOptions` 中说明。`accessLevel` 不在其中：运行中 Run 的档位由 `/api/access-level` 调整。
- **不提供取消。** 投进 mailbox 即不可撤销；会话消费很快，几乎没有可撤销的窗口。需要停下时中断 Run。
- **收件箱分三箱**：待收取、已收件、垃圾箱，见下文。角标为待收取的总数。

### 发送协议

**入口。** `POST /api/send` 与主 WS 帧 `/api/send` 共用同一应用服务。只用于主 lane；以下仍只走 `/api/query`：
- BTW 和解读（`lane`、`sideId`）；
- `hidden`、`params`、`scene`、`stream`；
- 空消息续跑（`canContinue`）。

**请求**

```json
{
  "requestId": "req_xxx",
  "chatId": "chat_xxx",
  "agentKey": "assistant",
  "primary": true,
  "message": "……",
  "references": [],
  "planningMode": false,
  "editingMode": true,
  "model": {"key": "…", "reasoningEffort": "LOW"},
  "accessLevel": "default"
}
```

- `requestId`：必填，由客户端生成（WebClient 现在已经为每次发送生成），兼作幂等键。
- `message` 与有效引用至少有一项；只带有效引用的消息沿用 `/api/query` 的历史与引用准入，首次会话仍要求正文。正文和引用都为空的续跑继续走 `/api/query`。
- `chatId`：已有会话，或客户端预分配、上传时创建的空会话；省略时新建会话。空会话与新建会话的处理同 `/api/query`。
- `agentKey`：新建会话时必填；会话已存在时必须与会话的 Agent 一致。
- `primary`：只在新建会话时有效，含义同 `/api/query`。
- `references`：同 `/api/query`。文件先经 `/api/upload` 上传到目标会话，再以引用传入，不走 mail 的附件复制。
- 运行选项 `planningMode`、`editingMode`、`model`、`accessLevel`：校验规则同 `/api/query`，只在这条消息开出新 Run 时生效。`model` 使用与 Query 一致的 `key`；仅保存请求显式给出的模型选项，省略时在开 Run 时按 Agent 配置解析，避免把默认模型当作用户覆盖值。
- `detail` 沿用现有位置（HTTP 用 URL 参数，WS 用帧顶层字段），只用于这条消息开出的 Run。
- 公开请求拒绝未知字段，与 `/api/query` 一致。

**响应**（`{code,msg,data}` 包裹）

```json
{"mailId": "mail_xxx", "chatId": "chat_xxx", "delivery": "inserted", "runId": "run_xxx", "ignoredOptions": ["model"]}
```

| delivery | 含义 | 客户端 |
|---|---|---|
| `started` | 已开出 `runId` | 按现有方式挂接该 Run |
| `inserted` | 已插入 `runId` 所指的当前 Run | 继续看当前 Run |
| `queued` | 排队中，没有 `runId` | 等 `mail.updated` 变为 inserted 或 started |

- `ignoredOptions`：插入时，请求中与当前 Run 不同的 `planningMode`、`editingMode`、`model` 被忽略，在这里列出；没有时省略。排队的消息之后被插入时，`mail.updated` 带同一字段。
- 只有确定性的错误才拒绝：参数无效；会话不存在、已归档或不属于本人；Agent 不可用；引用不可用；运行选项不合法；`idempotency_conflict`。会话忙不再返回 409。
- 同一 subject 下 requestId 重复时，不再创建 mail，返回同一 mail 的当前投递收据；requestId 相同而内容不同，返回 `idempotency_conflict`。进程内仅合并并发请求，受理完成后由 MAILS 响应重放。

**落地规则**沿用 docs/Mailbox.md 的"投递规则"：
- 空闲时开 Run，在跑时插入；
- 规划 Run、收尾或压缩中、有未决等待项时排队；
- 停在提问、审批或表单上时插入，等待项解决后才被读到，不代替作答；作答仍走 `/api/submit`；
- BOT 主聊天在跑时一律排队（阶段 6 的 P3）。

**事件**

- 消息被消费时，`request.query` 或 `request.steer` 事件带 `requestId` 和 `mailId`，客户端据此把待发气泡替换为正式消息。
- `mail.updated` 增加 `requestId` 与 `ignoredOptions`，推送到本人的连接。

### 收件箱：待收取、已收件、垃圾箱

收件箱按 mail 的去向分三箱，像事务中心的待办、已办与垃圾箱，不按发件人区分：

| 箱 | `box` | 状态 | 说明 |
|---|---|---|---|
| 待收取（默认） | `pending` | `accepting`、`queued`、`inflight`、`suspended` | 还没被读到。正常情况下很快清空；留在这里的是排在长 Run 后面的，或需要人处理的挂起 mail |
| 已收件 | `received` | `delivered`、`done` | 已被收件会话读到 |
| 垃圾箱 | `trash` | `failed`、`discarded`、`withdrawn` | 没有被读到，也不会再投递 |

- 接口：`GET /api/mails` 增加 `box`，默认 `pending`；其余筛选参数不变，`total` 按所选的箱计算。`box` 与 `status` 同时给出时取交集。
- 角标：待收取的 `total`，取代阶段 2.4 的挂起计数。消费很快，平时为 0；不为 0 说明有 mail 卡住。
- 垃圾箱只读，不提供清空或永久删除；正文按保留期清理，两端会话关闭后删行，与其他 mail 相同。
- WebClient：收件箱分三页；用户消息在会话时间线中仍以待发气泡展示状态。

### Platform 实施

| # | 内容 | 做法 |
|---|---|---|
| 5.1 | MAILS 结构 | 增加 `REQUEST_ID_`，以及冻结的 `PLANNING_MODE_`、`EDITING_MODE_`、`DETAIL_`、`IGNORED_OPTIONS_`；唯一索引 `(SUBJECT_, REQUEST_ID_) WHERE REQUEST_ID_ <> ''`。用户消息 `ORIGIN_TYPE_ = user`，`FROM_AGENT_KEY_`、`FROM_CHAT_ID_`、`FROM_RUN_ID_` 为空，因此不触发自寄检查和发件侧关闭处理。mail.db 每次新建，直接改 DDL |
| 5.2 | 受理服务 | 在 `internal/runtime/query` 新增发送服务，HTTP 与 WS 共用：按 `/api/query` 的准入规则校验目标、归属、引用、interactionConfig、运行选项和权限档位（`default` 按 Agent 默认解析）；新建会话与 `primary:true` 的处理同 `/api/query`，不取会话 mutation 锁；然后写入 MAILS 并按投递规则处理。不经过 Agent 发信的地址资格检查（已打开、被引用、主要对话三类） |
| 5.3 | 投递 | `DeliverMail` 区分用户消息：开 Run 时使用保存的运行选项、`detail`、`requestId`，未显式选择的模型按届时 Agent 配置解析，origin 为 user；插入时走普通用户 steer（带 `requestId`，没有 Agent origin）。`renderHeader` 对用户消息返回空。插入时比较冻结选项与当前 Run，把不同的项写入 `IGNORED_OPTIONS_` |
| 5.4 | Run 绑定 | `bindMailboxSession` 把用户消息当作用户直接输入：不设 RunOrigin（不受 `run_chaining_not_allowed` 限制），chat 引用资格取自该消息，跳数归 0。由用户消息开出的 Run 不产生请示，`chat_notify` 不可用（没有发信会话） |
| 5.5 | 结算 | Run 结束时，用户消息转为 `done`，不生成回信。"为信开出的 Run 没读到信就结束时转为 failed"同样适用 |
| 5.6 | 收件箱接口 | `mailbox.Filter` 增加 `Box`，`List` 与 `Count` 按箱筛选；`mail.updated` 增加 `requestId`、`ignoredOptions` |
| 5.7 | 路由 | `server_routes.go` 注册 `POST /api/send`，`ws_routes.go` 注册同名帧；server 只做解码、鉴权与响应映射 |
| 5.8 | 非 Native 后端 | 目标范围与 `/api/query` 相同。实施时核对 ACP 与 Proxy 的插入路径；不支持插入的后端，在跑时按排队处理 |
| 5.9 | 文档 | `docs/API与协议.md` 新增发送入口一节；`docs/Mailbox.md` 增加用户消息与三箱；AGENTS.md 的 mailbox 一句与 §6 API 说明原地改写；`README.md` |

本阶段 `/api/query`、`/api/steer` 不变，继续给 Desktop、gateway、Automation、execute 和渠道使用；Automation、渠道与 execute 随后改为发信，见本阶段末尾。

### WebClient 实施

- 直连 Platform/Desktop 时，正文或只带有效引用的输入一律调用 `/api/send`（连接主 WS 时用 WS 帧）；BTW、解读和空消息续跑仍走 `/api/query`。Gateway 尚未转发 `/api/send`，网关模式暂时保留原 Query/steer 路径。去掉"运行中改走 steer"的分支，以及 `active_run_conflict`、`awaiting_pending` 的 409 提示路径。
- 待发气泡：发送时按 `requestId` 立即显示，状态取自响应的 `delivery` 和之后的 `mail.updated`（排队中、已插入、已开始）。收到同一 `requestId` 的 `request.query` 或 `request.steer` 后替换为正式消息。有 `ignoredOptions` 时在气泡旁提示"这条按当前任务的设置处理，新设置从下一个 Run 开始生效"。
- `started` 时按现有方式挂接 `runId`；订阅先沿用 `run.started` 推送加自动挂接，仍有遗漏时再增加 `chat.subscribe {chatId}`。
- 刷新恢复：打开会话时用 `GET /api/mails?box=pending&chatId=` 取回仍在排队的用户消息，恢复待发气泡。
- 重启：并库前 mail.db 会被清空。重连后查不到某个待发气泡的 mailId 时，标为"未送达"，用户可以重发（作为新消息，使用新的 requestId）或删除。
- 收件箱：分"待收取""已收件""垃圾箱"三页；角标改为待收取的 `total`。

### 验收

- 同一会话：空闲时 `started`；在跑时 `inserted`；规划 Run、压缩中或有未决等待项时 `queued`，之后按规则开 Run 或插入。客户端不再收到会话忙的 409。
- 停在等待项上的会话收到用户消息时，等待项不被当作已作答。
- 插入时模型不同：响应 `ignoredOptions: ["model"]`，当前 Run 不变；下一条开出新 Run 的消息使用新模型。
- 同一 requestId 重复发送只受理一次；内容不同返回 `idempotency_conflict`。
- 由用户消息开出的 Run：没有 RunOrigin 限制，跳数为 0，不生成回信、请示和信封头，chat 引用按用户输入授权。
- 三箱的筛选与 `total` 正确，`box` 与 `status` 取交集；角标等于待收取总数。
- 刷新后排队中的气泡能恢复；重启后显示"未送达"。
- `/api/query`、`/api/steer`、Desktop、gateway、Automation、execute 和渠道的行为不变；`make test` 与 WebClient 测试通过。

### 随后：Automation、渠道与 execute 改为发信（2026-11-07 确认）

- **统一入口。** Automation 与渠道消息走和用户消息相同的受理、排队与投递流程；目标会话忙时排队，不再返回 409。来源由平台的可信入口确定，公共输入不能伪造。
- **来源。** Automation 的 origin 为 `automation`。渠道消息仍记为 `user`（是人发的），跳数为 0，没有信封头、回信和请示；渠道只记在来源信息里，不新增 `channel` 这种 origin，与现状一致。渠道身份与授权规则不变。
- **权限。** Automation 的初始档位仍来自它自己 query 的 `accessLevel`，省略时为 `ask_for_approval`，不继承聊天上一次的档位，也不套用 Agent 发信规则。
- **执行历史跟随投递。** 收到收据只记"已触发、排队中"，不记为成功；开出 Run 后关联 `chatId/runId`，Run 结束后记真实结果。插入正在运行的 Run 时，关联该 Run，以该 Run 的结束为结果。
- **重启（原待拍板第 6 项）。** mail.db 每次启动都会清空，重启前还在排队、未开出 Run 的 Automation 消息，在执行历史中记为失败，原因为 `platform restarted before execution started`，与现有的 `platform restarted before execution completed`（`internal/automation/history_service.go`）同一写法；不伪造 Run 或结果，不自动重发。
- **渠道回复。** 渠道仍按现有方式回复；排队的渠道消息在开出或插入 Run 后照常回复。重启丢失的排队消息不回复，也不补发。
- **execute。** `/api/execute` 同样受理为 mail，来源与用户消息相同（`user`）：目标会话忙时排队，不再返回 409。调用方在排队期间保持连接；断开时，未投递的 mail 撤回（`withdrawn`），已开出的 Run 中断。等待 `result_submit`、打回与返回形状不变。BOT 不接受 execute：不能为 BOT 新建会话，主聊天也没有 `result_submit`。
- **划线。** 发给某个 Chat 的消息一律走 mailbox：用户输入、Automation、渠道、execute，以及待拍板第 9 项的语音与 WebApp 助手。不算消息、保留原入口的：BTW/解读旁路、`/api/submit` 作答与续跑、"继续"这类对已有 Run 的控制、`agent_invoke`/`agent_delegate` 子任务、计划批准后的执行 Run。

## 阶段 6：BOT mode、导航与 Desktop

2026-11-07 改为新增 `mode: BOT`：调度角色不再是 `runtime.yml` 全局开关加在 Native GENERAL 上的会话身份，而是 BOT 这个 mode 本身。下文已按新决定改写；末尾"阶段 6 Platform 本轮实施记录"描述的是旧开关实现，按 6.9 改造。

| # | 内容 | 做法 |
|---|---|---|
| 6.1 | 形态定稿 | 把下表的答案写入 docs |
| 6.2 | BOT 主聊天出队策略 | 见下文 |
| 6.3 | 长上下文 | 见 [primary-chat-long-context.md](primary-chat-long-context.md)，适用于 BOT 主聊天。顺序：先分页回放，再观察压缩，最后分卷。约束：分卷必须保持同一个 chatId。mail 的地址在发信时确定且不再改变，换 chatId 会让分身的回信、即时消息和请示落回旧会话 |
| 6.4 | Desktop 对齐 | WebClient 完成 6.10 后按同一协议进行：删除设置中的"主要对话模式（实验）"，侧栏排序与显示设置增加 `group:bots` |
| 6.5 | 主聊天与分身的分工 | 见下文"BOT mode" |
| 6.6 | ~~分身身份短读~~ | 取消（2026-11-07）：BOT 的分身按"BOT 且不是主聊天"判断，不再读 JSONL 首条 query |
| 6.7 | 默认调度提示词只留一份 | 删除 `internal/config/config_defaults.go` 中硬编码的调度文案，只保留 `configs/agent-prompt.example.yml` 的 `bot.system-prompt`。部署配置缺这一项时为空、不报错，平台内置的调度协议照常注入 |
| 6.8 | BOT 主聊天不接受 steer | BOT 主聊天的主 lane 拒绝 `/api/steer`（HTTP 与 WS），错误码 `bot_chat_queue_only`，提示改用 `/api/send`，保证没有入口绕过 P3 的排队规则。BTW、解读与分身不受影响 |
| 6.9 | Platform 改造 | 见下文"Platform 改造" |
| 6.10 | 导航三区 | 见下文"导航：机器人、对话、项目" |

### BOT 形态

BOT 把用户收拢到一个主窗口，但执行仍然是一个 Chat 只有一个 Run。目标是 BOT 一边派活、一边接用户指令，让用户感觉可以同时派出很多件事。

mailbox 已经解决了派活结果的回传和多层派活。剩下两个问题：主窗口怎样呈现派出去的工作（阶段 4、6.10），以及用户输入怎样不被挡住（阶段 5）。

| # | 问题 | 答案 |
|---|---|---|
| Q1 | 主聊天只做调度，还是也跑长任务 | 只调度（2026-11-06 改）：主聊天没有执行工具，需要动手的活都用 `chat_send` 派出，新的事 `newChat:true`，补充用 `chatId`。见"BOT mode" |
| Q2 | 派出去的活放在哪里 | BOT 除主聊天外的聊天都是分身，只出现在 BOT 页头的"进行中"和历史中，不进对话区、项目区（6.10） |
| Q3 | 结果怎么回来 | 已由 mailbox 解决：回信自动开 Run |
| Q4 | 派给自己的分身是否作为默认方式 | 是（2026-11-06 改）：派给自己的分身是主聊天干活的唯一方式；也可以寄给其他 Agent |
| Q5 | 能否多层派活 | BOT 的自分身派活只有一层：分身没有 `chat_send`。其他 mode 及既有内联 `agent_invoke` 规则不变；寄往其他 BOT 主聊天仍可能继续调度，受跳数限制 |
| Q6 | `primary_chat_busy` 是否保留 | 保留，只用于切换指针；阶段 5 之后用户消息不再因会话忙被拒 |
| Q7 | 审批在哪里处理 | 两处都可以：主视图卡片（B4）或分身会话 |
| Q8 | 任务表、Kanban、云端 Job、mail 是否合成一套 | "进行中"就是任务托盘；Kanban 和云端 Job 本期不合并 |
| Q9 | 主聊天忙时，Automation 或渠道指向它怎么办 | 见阶段 5：排队 |
| Q10 | Desktop 与 WebClient 的呈现是否一致 | 一致；WebClient 先做，Desktop 按同一协议对齐（6.4） |
| Q11 | 主聊天与分身用什么模型 | 主聊天用 agent-settings.yml `bot.dispatch` 的模型（建议最强模型、低思考）；分身默认用 agent.yml 的模型，主聊天可在 `newChat` 时指定模型和思考强度。思考强度跟随 Chat，只有 `newChat` 能带新的值。语音见待拍板第 9 项 |
| Q12 | 主聊天上的确认能等多久 | 维持现状（2026-11-07）：审批仍按 600 秒超时，排队中的请示可在卡片上直接作答。等待项过期合同随 chats-db-v2 的"另做"再处理，本期不动 |
| Q13 | 主聊天怎样了解分身的进度 | 已关闭（2026-11-07）：主聊天有 `chat_get_status {mailId}` 与 `chat_query`，够用，不新增接口 |

### BOT 主聊天出队策略

出队方式由 mode 决定：只有 BOT 的主聊天按"主助理出队"处理，其他会话（含其他 mode 的主要聊天和 BOT 的分身）始终按现行投递规则。依赖阶段 5：用户消息要先进 mailbox，才谈得上排队。

| 会话状态 | 其他会话：现行投递规则 | BOT 主聊天：主助理出队 |
|---|---|---|
| 空闲 | 用最早一条开 Run，其余插入这个 Run | 用最早一条开 Run，其余留在队列 |
| 在跑 | 插入当前 Run | 留在队列，当前 Run 结束后按顺序逐条开 Run，不提供插入（P3） |
| 停在提问、审批或表单上 | 插入，等待项解决后才被读到 | 留在队列；作答仍走 `/api/submit` |
| 收尾或压缩中、有未决等待项 | 排队 | 排队 |

- 用户消息与分身的回信、即时消息、请示在同一队列里，按到达顺序处理。Automation 和渠道发往主聊天的消息同样排队（Q9）。
- 请示排在队列中时，用户仍可以从请示卡片（B2、B4）直接作答，不必等主聊天读到；否则一个长 Run 之后，请示可能已经超过 600 秒的时限。
- 主聊天读到一件事后怎样处理，由主聊天的模型决定：不需要动手的直接回复用户，用 `chat_send {chatId}` 补充给已有的分身，或用 `chat_send {newChat:true}` 新开分身（Q1、Q4）。出队策略只决定主聊天什么时候读到这件事。

P1（是否需要单独的出队策略）已确认：需要。

**P2 开关（2026-11-07 取消）。** 出队方式与调度角色都由 BOT mode 决定，不再有开关：删除 `runtime.yml` 的 `primary-chat` 段（出现即启动失败）、`GET /api/runtime/capabilities` 与主 WS `connected.data.primaryChat`。导航不需要从服务端读取开关，见 6.10。

**P3 BOT 主聊天在跑时，用户的新消息一律排队（已确认）**

这里只管"主聊天正在跑一个 Run 时，用户又发来一句"。例如主聊天正在写一段回复，用户补一句"用英文写"：排队时，这句话要等当前 Run 结束才被读到。

一律排队，不提供插入。理由：
- 按 Q1，主聊天的 Run 本来就短，排队等待的时间不长；
- 需要立刻更正时，用户可以中断当前 Run，队列随即出队；
- 不必在发送协议中增加插入标记，也不用区分哪些消息算更正。

分身寄来的 mail 和模型在任何情况下都不能插队。

### BOT mode（2026-11-07 确认）

- **组成。** 一个 BOT Agent 由一条主聊天和若干分身组成。主聊天是 `chat-primary.json` 指向的会话，只调度。**除主聊天外，BOT 的所有聊天都是分身**：不看是谁开的（BOT 自己、其他 Agent 或用户直接输入之后），也不读 JSONL 首条 query。
- **不能新建普通对话。** `/api/send` 发给 BOT 且不带 chatId 时进入主聊天，没有则新建并设为主聊天，不需要 `primary:true`。`/api/query` 主 lane 和 `/api/execute` 不能为 BOT 新建会话。新分身只由 `chat_send {newChat:true}` 开出。
- **可以有 Workspace。** 分身在 Workspace 中干活；主聊天不读 `AGENTS.md` 与 Workspace 提示文件。
- **没有规划。** 主聊天与分身都拒绝 `planningMode`；BOT 的 interactionConfig 默认值关闭规划和终端入口。
- **只支持 Native。** `engine: acp`、Proxy 或渠道 Agent 配置 `mode: BOT` 时加载失败。
- **其他 mode 不变。** GENERAL 等 Agent 的主要聊天是普通聊天，没有调度角色、不逐条出队；主要聊天指针仍是 `chat_send` 不带 chatId 时的收信地址。已有的 GENERAL 主要聊天不迁移，需要机器人时新建 BOT Agent。
- **BOT 是带运行逻辑的 mode。** 与 TEAM 一样，规则归 `internal/agent/bot`。这是"mode 只提供默认值"约定的明确例外，不作为其他 mode 增加专属逻辑的先例。

上述角色限制只作用于主 lane。BTW 与解读（explain）明确保留既有只读旁路合同：继续使用 Agent 的模型、AGENTS.md/Workspace 等上下文及工具定义，执行时只读，最多 3 轮、4 次工具调用，不派活、不代批、不参与主队列，也不改变主聊天角色。

| | 主聊天（调度） | 分身（执行） |
|---|---|---|
| 哪些会话 | BOT 的主要聊天（`chat-primary.json` 指向） | BOT 的其他所有聊天 |
| 工具 | 平台固定提供 `chat_send`、`chat_submit`、`chat_get_status`、`chat_interrupt`、`chat_query`、`action_announce`，不读 agent.yml 的工具、连接器、技能和 MCP，也不能通过配置增减 | agent.yml 的有效工具（bot 段 preset、显式声明与连接器），去掉 `chat_send`、`chat_submit`、`chat_get_status`、`chat_interrupt`（即使挂了 task-control）；保留 `chat_notify` |
| 提示词 | configs/agent-prompt.yml 的 `bot.system-prompt`、平台直接注入的必要调度协议、`SOUL.md`、Agent 名称与描述、分身能力清单（只列工具名和技能名）。不读取 Agent 的 `AGENTS.md`、Workspace 提示文件或配置技能的说明；必要协议取自随包内置资源 | agent.yml 与 bot 段模板 |
| 模型 | agent-settings.yml 的 `bot.dispatch.modelKey`、`bot.dispatch.reasoningEffort`；留空时用 agent.yml 的模型 | agent.yml 的模型；`chat_send` 显式给出时用给出的值 |
| 权限档位 | 用户在输入框选的档位，作为派活上限和代批依据 | 见下方"档位继承" |
| 回复 | 回复用户 | 最终回答由平台自动寄回；`chat_notify` 发阶段结果；卡在等待项上自动转请示 |
| 输入框入口 | 不显示规划、编辑、终端和模型选择；档位选择保留，文字为"派给分身的档位" | 不显示规划、终端；编辑按 Agent 配置 |

```yaml
# agents/assistant/agent.yml
key: assistant
name: 小助理
mode: BOT
runtimeConfig:
  workspaceRoot: ~/notes   # 可选；分身在这里干活
modelConfig: ...           # 分身的模型
toolConfig: ...            # 分身的工具

# configs/agent-settings.yml：删除顶层 primary 段；新增 bot 段，与 general/coder/kbase/team 并列
bot:
  dispatch:                # 主聊天的模型；留空用 agent.yml 的模型
    modelKey:
    reasoningEffort:
  interaction-config: ...
  preset-tools: [...]      # 分身的默认工具
  preset-connectors: [...]
  default-agent: {...}

# configs/agent-prompt.yml：primary 段改为 bot 段
bot:
  system-prompt: |
    ……调度规则：什么开新分身、什么补充给已有分身、怎样写派活的信……

# configs/runtime.yml：删除 primary-chat 段，出现即启动失败
```

不新增环境变量。

**档位继承（必须遵守）。** BOT 主聊天派给自己开分身，省略 `accessLevel` 或显式传入 `default` 时，分身的档位均等于主聊天**受理这封信那一刻的当前档位**：不是 Agent 的 `accessConfig.default`，也不是主聊天 Run 启动时的档位。`PrepareRunStart` 的 `plan.ParentAccessLevel` 只是预检查快照；最终必须在 `AcceptMail` 的 `AcceptAtRunAccessLevel` 回调中使用实时 `parent` 解析并冻结，不能复用预检查档位。显式具体 `accessLevel` 仍不得高于该档位。冻结规则不变：受理后用户再调整主聊天的档位，已受理的信不跟着变。寄往其他 Agent 的规则不变。

其他规则：

- 名称、`SOUL.md`、记忆（按 agent.yml 的 contextConfig）、主要聊天指针两边相同，所以仍是同一个 Agent。不新增 `PRIMARY.md`。
- 用户在主聊天中上传的文件和选中的引用，由主聊天通过 `chat_send` 的 `files`、`references` 转给分身。
- 模型继承：现在派给自己时子 Chat 继承调用方模型（`internal/runops/tool_handler.go` 的 `sameAgent`）。调用方是 BOT 主聊天时改为用 agent.yml 的模型，其他情况不变。
- 由信开出的 Run 的 `run_chaining_not_allowed` 保留；分身的工具列表里本来就没有这些工具。
- 主聊天的六个工具由平台提供，与 `finalize_planning`、`agent_delegate` 同类。实施时原地改写 AGENTS.md "普通 Native Agent 工具只来自……"一句，并同步 docs/Mailbox.md 的"调用方限制"和 task-control 手册。

验收：

- BOT 主聊天的工具恰好是这六个，提示词不含 `AGENTS.md`；BOT 的任一其他聊天（BOT 自己开、其他 Agent 开、用户直接输入之后、重启之后）的工具是 agent.yml 去掉四个调度工具。
- 主聊天档位为 A，不写 `accessLevel` 开分身，分身档位为 A；受理前把档位改为 B，分身为 B；受理后再改，已受理的分身不变。显式值高于当前档位时拒绝。
- 主聊天派活不写模型时，分身用 agent.yml 的模型。
- BOT 拒绝 `planningMode`；`/api/send` 不带 chatId 进入主聊天，没有时新建并设为主聊天；`/api/query` 主 lane 与 `/api/execute` 为 BOT 新建会话时拒绝；非 Native 配置 `mode: BOT` 时加载失败。
- 配置了 `runtime.yml` 的 `primary-chat`，或 agent-settings.yml、agent-prompt.yml 的顶层 `primary` 段时，启动失败。
- 非 BOT Agent（含 GENERAL 的主要聊天）的工具、模型、档位和出队策略与旧开关关闭时相同。

### Platform 改造（6.9）

| 位置 | 改动 |
|---|---|
| mode | 内置类型增加 BOT。`internal/agent/bot` 注册 descriptor：模板默认值、interactionConfig 默认值（规划、终端关闭）、规划不可用；非 Native 拒绝 |
| 角色判断 | `internal/runtime/session/primary_chat.go` 的 `PrimaryChatEligible` 改为 mode 为 BOT 且 Native；`internal/runtime/query/primary_chat_role.go` 去掉 `PrimaryChat.Enabled`。分身判断改为"BOT 且不是主聊天"，`mailNewChat` 与 origin 不再用于身份 |
| 配置 | 删除 `PrimaryChatConfig.Enabled` 与 runtime.yml 的解析，键出现即启动失败；`agent-settings.primary` 改为 `bot.dispatch`，`agent-prompt.primary` 改为 `bot.system-prompt`，旧顶层 `primary` 段拒绝加载 |
| 入口 | `/api/send` 发给 BOT 且不带 chatId 时路由到主聊天，没有则新建并设指针；`/api/query` 主 lane 与 `/api/execute` 拒绝为 BOT 新建会话；BOT 的规划请求拒绝 |
| 接口 | 删除 `GET /api/runtime/capabilities`（`internal/server/handler_runtime_capabilities.go`）与 WS `connected.data.primaryChat`；mode 取值增加 BOT。`/api/chats/primary`、`primaryChat` 摘要、`chat.primary.changed` 不变 |
| 文档 | AGENTS.md 原地改写：§1 内置类型与"mode 只提供默认值"一句（BOT、TEAM 是带运行逻辑的例外）、§7 主要聊天一段改为 BOT、planningMode 与 editingMode 的适用 mode、"WebClient 不得按 mode 判断入口"改为导航可按 BOT 分区。同步 `docs/mode-capability-audit.html`、docs/Mailbox.md、docs/API与协议.md 的主要聊天一节、docs/智能体配置说明.md |

### 导航：机器人、对话、项目（6.10）

三个区同时存在，按 Agent 分区：

| 区 | 内容 |
|---|---|
| 机器人 | `mode: BOT` 的 Agent，一行一个：头像、名称、主聊天最后一句、运行中、等你确认或未读。带 Workspace 的加 📁 角标。点击打开主聊天；还没有主聊天时打开空白主聊天页，第一句经 `/api/send` 新建。可排序 |
| 对话 | 非 BOT、没有 Workspace 的 Agent 的聊天 |
| 项目 | 非 BOT、有 Workspace 的 Agent，按项目展开聊天 |

- 每个区都可以隐藏，只是客户端显示偏好：WebClient 存 localStorage，Desktop 并入现有侧栏排序与显示设置。隐藏不影响服务端：BOT 照常收信干活，历史与搜索仍能找到。
- 删除 WebClient 侧栏顶部的 `primaryNavigation` 按钮和 `supportsPrimaryChat`（`mode === GENERAL`）判断，以及 Desktop 设置中的"主要对话模式（实验）"。
- BOT 的聊天（主聊天和分身）不进对话区、项目区和置顶区。
- BOT 主聊天页：页头"进行中 N"列出该 BOT 正在运行或等待处理的分身；菜单有"历史""重新开始"（归档当前主聊天，下一句新建）"编辑智能体"。数据用现有的 `/api/chats?agentKey=<bot>&primary=false` 加活动 Run 投影，不依赖 mail.db 的 openedBy，重启后仍完整。分身页页头显示返回 BOT 主聊天的链接。
- 前端只用 `mode`、`workspaceDir` 和已有的 `primary` 判断分区与页面，不新增 `chatRole` 等字段。输入框入口仍取自后端给的 interactionConfig；BOT 主聊天额外隐藏模型选择。
- 先完成 WebClient，再按同一协议对齐 Desktop（6.4）。

2026-11-06 审核修订：阶段 5 入口名称确认使用 HTTP POST /api/send 与主 WS 同名帧。本轮先修复阶段 1–4 审核问题，不提前接入用户消息发送路径。


### 阶段 5 用户消息实施记录（2026-11-06）

- 已实现 HTTP/主 WS `/api/send`、subject/requestId 幂等、与 Query 共享的用户准入、运行选项/身份冻结（detail 不由请求指定，开 Run 时按 Agent 配置定档）、开 Run/插入/排队和用户消息结算；用户消息没有 Agent 信封、回信、请示或派生 Run 控制限制。
- 已实现 pending/received/trash 三箱与交集筛选、待收取总数角标、待发气泡、本地按身份保存、分页全文恢复、WS 重连对账、启动清库后的未送达提示与新 requestId 重发。消费先于收据不复活气泡；网络结果未知保留正文并等待对账，不当作明确拒绝重发。
- ACP/Proxy/渠道的活动 Run 保守排队；Proxy 空闲启动、运行中排队、空闲后投递和无回信结算已补集成测试。agent-gateway 当前没有 `/api/send` 转发；为保留既有网关行为，WebClient 网关模式继续走旧 Query/steer，直连 Platform/Desktop 走统一发送。
- 验证：Platform `make test` 通过（Server 的 SSE 历史落盘时序用例首次失败，按 Make 既有规则重试通过）；全仓 `go build` 通过，新发送入口 `-race` 通过。WebClient 全量 507 个套件、4508 项通过，相关发送/恢复回归及 typecheck、边界检查、生产 build 通过；构建只有现有包体积告警。
- 本阶段未改 Automation、渠道原入口或主要聊天 P1–P3；待拍板第 6 项仍保留。

审核修复（2026-11-06）：
- 模型只冻结请求显式给出的值，省略时开 Run 按 Agent 配置解析。此前受理时把 Agent 默认模型写成显式选择，关闭模型选择的 Agent 每条用户消息都投递失败，并会覆盖各阶段的思考设置。
- 收据 `status` 为 failed 时，WebClient 直接标未送达，不挂接收据里的 runId。
- 进程内幂等表在受理完成后释放；用户消息的投递上下文在消费或进入终态时释放（mailbox 终态钩子 `MailReleased`），并库后需持久化，标记 TODO(chats-db-v2)。
- `/api/send` 的 `model` 字段改为与 `/api/query` 相同的 `key`。
- 只带有效引用的消息也走 `/api/send`，规则同 `/api/query`；网关模式不变。
- 文档：发送一节移入 API 文档 Run 章节并补表格行；Mailbox 的 `mail.updated` 字段与推送范围更正。

### 阶段 6 Platform 本轮实施记录

- 开关默认关闭；开启后仅 Native GENERAL 的当前主要聊天使用平台固定六工具并逐条出队。mode 始终保留 GENERAL，其他 mode 与后端不变。主要聊天不装配执行连接器、技能、Workspace 规则或 AGENTS.md；分身能力只作为名称清单进入调度提示词。
- 调度模型归 `agent-settings.primary`，提示词归 `agent-prompt.primary`；分身省略模型时使用 Agent 配置。自分身按首条主 query 的 `mailNewChat` 与 origin 判断，用户直接输入及重启后仍排除四个调度工具；显式 false 区分补建主要聊天，旧记录保留同 Agent origin 的回退。
- 分身省略档位或显式 default 时均在 `AcceptAtRunAccessLevel` 临界区冻结父实时档位；预检查后的修改生效、受理后的修改不追溯。显式具体档位校验实时上限，Agent 默认档位不能通过 default 绕过实时继承。
- HTTP `/api/runtime/capabilities` 与 WS connected 的开关声明已提供。相邻 WebClient 的并行导航改动保持原样；客户端开关跟随、主要聊天输入框入口隐藏、长上下文和 Desktop 尚未在本轮完成验收。`mail.max-hops` 保留 4，原待拍板第 7 项已确认暂不调整。
- 已验证 `make test` 全量通过（无重试）、相关包回归、权限/身份 race 检查、实际 HTTP→LLM 五工具装配及全仓构建。未部署。

### 已确认的评审修复（2026-11-07）

- 主要聊天开自分身时，省略和显式 default 统一继承受理时父实时档位；补充 Agent 默认 full_access、父为 ask_for_approval 的防提权回归，以及受理前后修改档位的用例。
- 必要调度协议作为随包内置资源直接注入主要聊天 system prompt，覆盖发信、文件转交、回执与 Run 控制资格、代答审批、查询参数及跳数边界；五工具的会话专用说明不再要求读取不可用的 Skill，并更正档位、模型和思考默认值。普通会话的工具说明与 schema 保持原样。
- 部署侧提示词位置仍为 configs/agent-prompt.yml 的 primary.system-prompt；必要协议独立保留，不随自定义提示词覆盖而丢失。BTW/explain 明确保留原有 Agent 配置和只读旁路限制，仅主 lane 采用固定调度角色。
- 已通过相关完整包回归、实际 HTTP→LLM 协议与五工具说明检查、BTW/解读回归、针对性 race 检查和全仓构建。身份读取的全文件/全局锁问题尚未修改，待用示例解释短读方案后确认；未部署。

### 阶段 6 BOT Platform 改造实施记录（2026-11-07）

本节替代上面的旧开关实现说明；旧记录保留作历史，不代表当前合同。

- 已实现 6.7–6.9 的 Platform 改造：新增 Native BOT descriptor、独立 system-init profile、bot preset/default-agent/Workspace 规则配置与创建入口；拒绝 ACP 与 proxyConfig。GENERAL 主要聊天恢复普通聊天，不迁移。
- BOT 主聊天固定六工具并逐条出队，其余 BOT 聊天全部按分身装配，不读取 JSONL 判断身份；保留 BTW/explain 的旁路合同、分身模型与受理时实时档位冻结。
- `/api/send` 不带 chatId 时解析或创建 BOT 主聊天；解析、创建和指针发布与现有 mutation 锁串行，并发首条消息只创建一条主聊天。BOT 拒绝规划、execute 与主 lane Query 新建；用户指定新 chatId 不能绕过主聊天入口。
- 6.8：BOT 主聊天主 lane 的 HTTP/WS steer 在共享 Runtime 入口拒绝，错误码 `bot_chat_queue_only`；分身、BTW/explain 和其他 mode 保留原行为。
- 删除 runtime primary-chat 配置、capabilities HTTP 接口及 connected.data.primaryChat；agent-settings/prompt 顶层 primary 拒绝加载。调度配置改为 bot.dispatch 与 bot.system-prompt；默认调度文案只留示例 YAML，缺省为空，内置必要协议照常注入。没有新增环境变量。
- 已同步配置示例、AGENTS/README、Mailbox/API/Agent 配置文档及 task-control 协议。计划引用的 docs/mode-capability-audit.html 在当前工作树中不存在，未把它记为已同步。
- 上述首批实施时尚未覆盖的 Automation/渠道/execute 与 WebClient，见下方第二批记录；Desktop、长上下文及第 9 项语音/WebApp 仍待核对。当前改动未提交、未部署。
- 验证：`CGO_ENABLED=0 go test ./...` 全量通过（设置 MEMX_TEST_BINARY，含 memx 集成）；`CGO_ENABLED=0 go build ./...` 通过；BOT HTTP→LLM 六工具、并发主聊天创建、入口拒绝、分身权限冻结、角色与旁路相关 race 检查通过。首次全量的 npm discovery 超时、独立 Server 回归的既有 run.complete 落盘时序失败，分别复跑后通过；最终全量无失败。

### BOT 与消息入口第二批实施记录（2026-11-07）

- BOT 不作为 TEAM 成员或 agent_invoke 目标；外部 Agent 只寻址 BOT 主聊天，不能创建、发现或读取分身。BOT 主聊天查询分身改从 chats.db 的 agentKey/primary:false 列表读取并复核权限，不新增 chatRole 或来源短读。
- BOT 已有分身不能切成主聊天，保留清空指针后重新创建的入口；L2/L3 不换 ChatID，L3 实施暂缓。取消 JSONL mailNewChat 的持久化、投影及身份用途。
- 主 lane 普通 Query 与正文/引用 steer 保留外部协议，在 Runtime 门面转为 mailbox；包含 Desktop 普通输入、Automation、渠道。Automation 保留自身 origin，默认档位不继承 Chat 历史，不生成 Agent 信封、回信或派生控制权。
- execute 经过 mailbox，忙时排队，断开撤回未投递信件或只中断实际开出的 Run。执行前的应用归属登记仍在模型启动前完成，失败不启动执行。BOT 一律拒绝 execute。
- WebClient：BOT 创建/编辑、机器人导航与客户端隐藏偏好、分身与普通对话/项目隔离、主聊天首发、上传暂存、主/分身页头、返回主聊天及按 primary:false 分页的分身历史；BOT 禁用规划和终端，主聊天不显示模型/编辑；重新开始有影响提示与取消按钮。
- 本批明确搁置：BOT 对外派活的跨 Run 最终结果关联、进行中筛选及数量、L3、Desktop 导航和设置、语音/WebApp Query、BTW/explain、Submit/Interrupt/空白继续/计划批准的 mailbox 控制合同、内部子任务。Gateway 独立协议不改。不宣称“所有消息已统一完成”。
- 未提交、未部署。验证结果以本轮最终报告为准；上面的“全量通过”只描述首批当时的工作区。
- 第二批最终验证：`CGO_ENABLED=0 go test ./... -p 2`（设置 MEMX_TEST_BINARY）与 `go build ./...` 通过；BOT、外部寻址、execute 取消与 WS 终态顺序的针对性 race 检查通过，并发 BOT 首次投递连续 20 轮 race 通过。修正受理已有 Chat 时重复改写身份导致的 SQLite 锁冲突，排队阶段仅冻结消息，实际开 Run 时更新 Chat。WebClient 的 515 套、4578 项测试及生产构建通过；只补齐既有 ConnectorPicker 测试的 transport mock，连接器 hook 的原有未提交实现保留。
