# Mailbox

Mailbox 负责会话之间的往来。每条记录叫 mail，按 `kind` 分四种：

| kind | 名称 | 性质 | 产生方式 |
|---|---|---|---|
| `letter` | 信 | 正式：派活、补充 | `chat_send` |
| `reply` | 回信 | 平台对信的答复 | Run 结束时由平台生成 |
| `notify` | 即时消息 | 非正式：阶段结果、提醒，不需要回复 | `chat_notify` |
| `ask` | 请示 | 分身卡在等待项上，转给上级处理 | 由平台根据等待项生成 |

mail 都寄给 Chat，不寄给 Run；寄给 Agent 就是寄给它的主要对话。工具参数见 task-control 的 [Mailbox 手册](../internal/resources/connectors/builtin.task-control/skills/task-control/references/mailbox.md)，后续计划见 [plans/mailbox-next.md](../plans/mailbox-next.md)。

## 启用与关闭

Mailbox 默认关闭，在 `configs/runtime.yml` 中显式启用，重启后生效：

```yaml
mail:
  enabled: true
```

设置 `mail.enabled: false` 或省略该项即可关闭。关闭时不打开或创建 `mail.db`，不绑定 Mailbox 运行时钩子，不执行邮件恢复或保留期清理；已有数据库保持原样。邮件 HTTP 接口返回 503，`chat_send`、`chat_notify`、`chat_submit` 及邮件状态查询返回 `mail_unavailable`；普通 `chat_start`、`chat_steer` 等会话控制继续可用。启用后才校验已有数据库的 schema。

## chats.db 与 mail.db

两个库都在 `AP_RUNTIME_CHATS_DIR` 下，职责不同：chats.db 记录 Chat 本身，mail.db 记录 mail 的投递过程。

| | chats.db | mail.db |
|---|---|---|
| 表 | `CHATS`（每个 Chat 一行摘要）、`RUNS`（已结束的 Run） | `MAILS`（每条 mail 一行） |
| 事实源 | Chat 内容在 `<chatId>.jsonl`，chats.db 是它的摘要索引 | mail 正文、附件清单、等待项快照、投递状态、回信关系 |
| 存在条件 | 始终存在 | 仅在 `mail.enabled: true` 时存在 |
| 身份 | `APCH`，chat-v1，`user_version=1` | `APML`，mail-v1，`user_version=1`（未上线基线） |
| 归档 | Chat 移入 `archive/archive.db` | 不移动；归档不算删除 |

**引用是单向的。** mail.db 通过 ID 指向 Chat 和 Run（`FROM_CHAT_ID_`、`TO_CHAT_ID_`、`FROM_RUN_ID_`、`RUN_ID_`、`REPLY_OF_RUN_ID_`），chats.db 不保存任何 mailId。mail 在收件 Chat 中留下两样东西，二者都属于 Chat：

- JSONL 的 query/steer 行：顶层带 `mailId`、`mailKind`、`mailAttempt`、`mailHop`（回信另有 `outcome`），正文存在该行的 `query` 或 `steer` 中。这一行就是"已消费"的证据。
- 附件副本 `<chatId>/mail/<mailId>/`，随 Chat 的生命周期保留。

**谁写谁。** mailbox 不直接写 chats.db 或 JSONL：建 Chat、开 Run、插入都通过 Runtime 的入口完成，由 Runtime 落盘。Chat 一侧不写 mail.db，只通过钩子通知 mailbox：Runtime 的 Run 结束、消费、会话空闲、等待项产生与解决，以及 conversation 的删除钩子。

**没有跨库事务，以 Chat 为准对账。**

- 先写 mail.db，再改动 Chat。受理时先写 `accepting` 并预留 chatId，再建 Chat，然后转为 `queued`；投递时先在事务中占位 `inflight`，再调用 Runtime，回调按 attempt 核对。
- 重启时扫描收件 JSONL，出现过该 mailId 的补记为已消费。
- 保留期任务逐个检查 mail.db 引用的 chatId 是否仍在 chats.db 或 archive.db 中，不在就补做删除处理。
- Chat 删除会驱动 mail 清理，反之不成立：mail 从不删除或修改 Chat。

**归属与"谁开出的"。**

- Chat 属于哪个用户只看 chats.db。mailbox 建的 Chat 与用户自己建的一样，都是 `SOURCE_ = query:<subject>`；旧 `chat_start` 建的 Chat 来源为 `run-query:`，沿 JSONL 首条 query 的 `origin` 追溯到父 Chat。判断归属不读 mail.db。
- "哪个 Agent 开出了这个 Chat"目前不在 chats.db 中。mailbox 开出的 Chat 只能从 mail.db 中 `OPENED_CHAT_ = 1` 那封信的 `FROM_AGENT_KEY_`、`FROM_CHAT_ID_` 得知；旧 `chat_start` 开出的记录在 JSONL 首条 query 的 `origin` 中。仅看 chats.db，mailbox 开出的 Chat 与用户自己建的无法区分。
- `OPENED_CHAT_` 的本义是"这封信受理时需要建 Chat"，用于受理重放。补建的主要对话也记为 1，所以它不等于"派出去的工作会话"。

**规则。**

- chats.db 不依赖 mail.db。mail 关闭时，Chat 的创建、列表、归属、归档和删除照常工作。
- Chat 回放和 `/api/chats` 不读 mail.db。只有组装模型输入时，才按行上的 mailId 读 mail.db 生成信封头；mail 关闭时不加信封头。
- Chat 自身的属性放 chats.db 与 JSONL；投递过程、回信关系和证据放 mail.db。两库只用 ID 关联，不做跨库事务或 JOIN；需要同时用到两边的数据时，由应用层分别读取。

## 地址

发信时一次性解析出 `toChatId`，之后不再改变；主要对话指针后来切换，已发出的信仍留在原会话。

| 写法 | 解析结果 |
|---|---|
| `chatId` | 指定的已有会话：补充那件事 |
| `newChat: true` | 为目标 Agent 新开会话（分身）：另一件事 |
| 都不写 | 目标 Agent 的主要对话；没有时新建并设为主要对话（只补缺，不切换） |

`agentKey` 省略时为发信 Agent 自己；`chatId` 与 `newChat` 互斥。`chatId` 只允许以下三类会话，且必须属于目标 Agent 和同一用户：

1. 发信 Agent 开出的会话（mail.db 中的创建记录，或旧 `chat_start` 留下的 query `origin`）；
2. 用户最近一次直接输入中引用的会话（chat 类 reference）；
3. 目标 Agent 的主要对话。

寄给发信 Run 自己所在的会话、已归档或已删除的会话一律拒绝。可寄不代表可读或可中断，`chat_query` 与 `chat_interrupt` 另行授权。

## 投递规则

四类 mail 共用同一规则：对收件会话中 `queued` 的 mail 按时间顺序处理，视会话状态而定。

| 会话状态 | 处理 |
|---|---|
| 没有 Run，也没有未决等待项 | 用最早的一条开 Run（`delivery = started`），其余插进这个 Run |
| 有 Run，正常运行或停在普通 `wait` 上 | 经 steer 通道插入（`delivery = inserted`），`wait` 以唤醒原因 `mail` 被唤醒 |
| 有 Run，停在提问、审批或表单等待上 | 插入；内容在 Run 内排队，等待项解决后才被模型读到，不会代替作答 |
| 有 Run，且是规划 Run | 排队（插入会作废候选计划） |
| 有 Run，正在收尾或压缩 | 排队 |
| 没有 Run，但有未决等待项（如待确认的计划） | 排队，等等待项清除或续接仲裁完成 |

触发投递的时机：发出时、Run 结束时、会话维护结束时、等待项清除时。重启时不投递。计划被批准时，先注册执行 Run，再把排队内容插入其中；计划被拒绝、超时或执行准入失败时，会话空闲，按普通规则投递。`chat_send` 没有 `planningMode`，信不会开出规划 Run。

在途记录：投递前在一个事务中写入 `inflight`、`attempts + 1`、`steer_id = <mailId>:<attempts>`、`delivery` 和目标 `run_id`，再调用 Runtime；所有回调都核对 attempts，旧尝试迟到的回调直接丢弃。

- 已消费：转为 `delivered`，写入 `consumed_at`。
- 插入后 Run 已结束仍未消费，或确定未发出：退回 `queued`。为信开出的 Run 没读到信就结束时，信转为 `failed`，避免反复开 Run。
- 结果未知：信和回信转为 `suspended`（证据 `unknown`），即时消息和请示转为 `withdrawn`。
- 投递复核得到确定结论（目标 Agent、会话、归属、引用、模型或附件不再成立）时，信转为 `failed` 并寄回失败回信，回信转为挂起，即时消息和请示撤回。
- 读盘、关停等临时错误保持 `queued`，等待同一会话的下一次触发；没有自动重试，一直没有触发就等到重启后挂起。

## 信与回信

- 每封信都会有回信，没有 `reply` 参数；不需要回复的内容用即时消息。
- 一个 Run 只正式回一次。Run 结束时，对该 Run 消费过的信，每个发信会话各寄一封回信。正文是该 Run 的 `RunCompletion.AssistantText`（完整版的 lastRunContent），附带本 Run 通过 `artifact_publish` 发布的文件；`outcome` 为 `completed|failed|canceled`。
- 补充：信插入仍在运行的会话时，与原来的信合并，只回一封；会话已空闲时，补充信开出新 Run，新 Run 结束后再回一封。
- 回信本身不再要求回信。
- 生成回信、写 `ANSWERED_BY_MAIL_ID_`、原信转为 `done` 在同一事务中完成。回信按 `REPLY_KEY_` 唯一：正常回信为 `run:<runId>:<toChatId>`；失败回信（投递复核失败或收件会话被删除，此时还没有 Run）为 `letter:<letterId>`。

| 时间 | 发生了什么 |
|---|---|
| 10:00 | 前台寄出 M1"整理本周周报"，开出分身 R2 |
| 10:05 | R2 还在跑，前台寄补充信 M4"把上周的也补上"，M4 插进 R2 |
| 10:08 | R2 发即时消息"先给你本周的初稿"，前台无需回复 |
| 10:20 | R2 结束，前台收到一封回信，同时回答 M1 和 M4 |
| 10:30 | 前台再补充一点；R2 已结束，补充信开出 R6，R6 结束后再回一封 |

## 即时消息

- `chat_notify {message, files?}` 发往开出当前 Run 的那封信的发信会话；当前 Run 不是由信开出的，则不可用。
- 用于发信方需要尽早知道的事。纯进度不发，走 `run.activity` 和 `chat_get_status`。
- 不产生回信；收到即时消息而开出的 Run 结束时也不回信。

## 请示与代答

- 由信开出的 Run 进入提问、审批或表单等待时，分身照常停在等待项上，平台同时生成一条请示，发往开出该 Run 的那封信的发信会话。用户直接开出的 Run 进入等待项时，照旧由人处理，不产生请示。
- 请示与等待项同生同灭：等待项被人答复、超时或 Run 被取消后，未送达的请示转为 `withdrawn`，已送达的请示调用 `chat_submit` 返回 `already_resolved`。审批默认等待 600 秒，超时按拒绝处理。
- 请示正文按等待项渲染为可读文本（不含表单 HTML），等待项快照保存在 mail.db 中。
- `chat_submit {runId, awaitingId, param | params}` 的形状与 `/api/submit` 一致，`param` 与 `params` 互斥；只能答复寄到本会话的请示。

按等待项来源 `AWAITING_SOURCE_` 处理，不按 mode 判断（带审阅页的工具审批也以 `mode = form` 呈现）：

| 来源 | 上级能做什么 | 提交形状 |
|---|---|---|
| `ask_user_question` | 回答；拿不准时先在自己的会话里问用户 | `params`，每题一项 `answer` 或 `answers` |
| `ask_user_form` | 填写，按现有表单规则校验类型与大小 | `param`：`{decision: "approve", data}`，或 `reject`、`dismiss` |
| `tool_approval` | 随时可以拒绝；批准受代批规则约束 | 与原等待项相同 |

代批规则（仅 `tool_approval`）：

- 平台用上级 Run 当前的档位，在分身冻结的环境中把这次调用重新走一遍审批判定（Bash 和文件访问走 AccessPolicy，工具审阅走该工具的审批规划），不继承上级的路径、工具、凭据或规则。判定结果仍需审批时返回 `approval_requires_human`。不能用档位高低代替判定，因为 `auto_approve` 下破坏性操作仍要人批。
- 批准只对应原等待项中那次调用的指纹。
- 硬禁止与只读根不可批准（人也不能）；强制人工审批（如提升权限档位的审阅）永不代批。
- 代答的 submit 记录中写入代答者的 `origin`，审批另记依据的 `accessGrant`。

## 跳数

跳数表示这件事离用户最近一次亲口说话隔了多远，用于防止 Agent 之间无休止地派活、通知和请示。

1. 用户、Automation、`/api/execute` 的输入为 0 跳；`chat_start` 开出的 Run 为父 Run 当前跳数 + 1。
2. Run 发出的 mail（包括平台代它生成的回信）记为 Run 当前跳数 + 1，存于 `HOP_`。
3. Run 的当前跳数：收到用户直接输入时归 0；收到 mail 时取 `max(当前, mail.hop)`；被上级 `chat_submit` 答复时取 `max(当前, 上级当前 + 1)`。只在发出时加一，接收时不加。
4. 达到上限后，`chat_send`、`chat_notify` 返回 `mail_hop_limit`，请示不再上报、只等用户；回信照常寄出，`chat_submit` 不受限制。
5. 上限为 `mail.max-hops`，默认 4。

| 步骤 | 产生 | 跳数 |
|---|---|---|
| 用户："整理周报" | 前台 R1 | 0 |
| R1 派活 | 信，开出分身 R2 | 1 |
| R2 结束 | 回信，开出 R3 | 2 |
| R3 再派 | 信，开出分身 R4 | 3 |
| R4 结束 | 回信，开出 R5 | 4 |
| R5 还想派 | 被拒绝，R5 把结果交给用户 | — |

## 授权与控制

**受理时冻结。** `chat_send` 在发信 Run 的权限临界区内判断，写入 mail.db 即为受理。冻结的内容包括：发件身份（agent、subject、chat、run、tool）、生效档位与审批收据的消费结果（`accessGrant`，收据在受理时消费）、模型、规范化后的引用、跳数和目标会话。

**投递时复核。** 检查目标 Agent 仍存在且可准入；目标会话存在、未归档，归属一致；引用仍可用；模型仍在注册表中；附件摘要一致。不检查发信 Run 是否仍在运行，也不与它当前的档位比较。

**权限档位。**

- `accessLevel`、`modelKey`、`reasoningEffort`、`chatName` 只能与 `newChat` 同用。档位省略时按收件 Agent 的 `accessConfig.default` 解析，未配置为 `ask_for_approval`；显式档位不得高于发信 Run 受理时的当前档位。
- 寄往已有会话的信，冻结受理时收件 Agent 的默认档位；开新 Run 时使用该档位和目标 Agent 的默认模型，插入时不改变正在运行的 Run 的设置。
- 回信、即时消息、请示在创建时冻结收件 Agent 的默认档位，不继承发送 Run 的权限，也不要求发送 Run 仍在运行。收件 Agent 已不可用时，回信保留完整结果并挂起，不静默降为 `ask_for_approval`。
- `accessGrant` 保存权限来源与目标 revision，letter 另存父基线。已冻结的权限不因收件 Agent 后来修改默认值、发送 Run 结束或人工重投而改变。

**控制身份。** 不另存字段。正常投递时，信的投递身份就是发信 Run 的控制身份；人工重投时，换成重投者。信开出的 Run，以及它的回程（回信，和处理它的 Run 发出的即时消息、请示）在发信会话开出的 Run，都使用这封信的投递身份。投递身份只保存在进程内。

**发信方的控制权**由 `delivery` 决定：只有 `started` 的 letter 允许发信方查询和中断该 Run；`inserted` 只能查询这封信本身。回信、即时消息、请示开出的 Run 不授予对方任何控制。

**调用方限制。** 只有主 Agent 根 Run（含 TEAM 总控）能调用 `chat_send`、`chat_notify`、`chat_submit`；BTW、`agent_invoke` 子任务和 Team 成员不能调用，规划 Run 默认排除这三个工具。由信开出的 Run 调用 `chat_start`、`chat_steer`、`chat_get_status`、`chat_interrupt`，或使用 `wait` 的 `run.terminal` 条件，仍返回 `run_chaining_not_allowed`，派完活应使用不带条件的普通 `wait` 等回信唤醒。回信、即时消息、请示唤醒的是收件会话自己的 Run，这些工具照常可用。

## 文件

正文中的 `@chat/...` 属于发件会话，跨会话后会解析到收件会话，不能当作交付。

- `chat_send`、`chat_notify` 的 `files`：受理时先写入 `accepting` 和附件清单（名称、大小、sha256），再从发件会话复制到收件会话的 `mail/<mailId>/`，复制时核对摘要，然后转为 `queued`。投递时再核对一次，不一致即失败。
- 回信：生成时按同样方式复制本 Run 发布的文件。
- 正文不改写；信封头下方列出附件在收件会话中的路径。

## 新建会话与补建主要对话

- 新开会话：在一个事务中写入信（`accepting`，预留 chatId，附件清单），按预留 ID 建会话（已存在则跳过），复制附件，转为 `queued`。同一调用重试时从断点继续。
- 补建主要对话：在会话 mutation 锁内完成"检查指针 → 预留 chatId 并写信 → 建会话 → 指针为空才设置"，该锁与 `PUT /api/chats/primary` 共用。query `primary:true` 的准入不取该锁，因此最后一步是条件写入：若指针已被它设置则不覆盖，信留在新建的会话中。从中断处恢复时重新仲裁，指针已被设置的改投到该主要对话。

## 重启与人工重投

重启不自动投递，也不自动开 Run：

- 信：`accepting`、`queued`、`inflight`、`delivered` 转为 `suspended`。
- 回信：`queued`、`inflight` 转为 `suspended`；已 `delivered` 的回信是终态，永不再投。
- 即时消息、请示：未送达的转为 `withdrawn`，不提供重投。

挂起时写入 `EVIDENCE_`，有歧义按未知处理；启动时扫描收件 JSONL，出现该 mailId 的补为 `consumed`。

| evidence | 判断依据 | 界面含义 |
|---|---|---|
| `none` | 挂起前处于 `accepting` 或 `queued` | 未投递，可以重投 |
| `consumed` | 有消费证据 | 已处理过，重投会重新执行 |
| `unknown` | 挂起前处于 `inflight` 且无消费证据，或远端结果未知 | 可能已处理，重投可能重复执行 |

挂起的信和回信由人重投或丢弃（`discarded`，不发回信）。重投从挂起前的阶段继续：挂起前为 `accepting` 的，先补完受理（建会话、补建主要对话时重新仲裁、按清单复制附件），发件会话中的附件已变化或缺失则转为 `failed`；其他状态直接回到 `queued`。重投后由重投者接管投递身份。

## 删除、归档与保留期

**删除。** 任一端会话被删除时，分别记录 `FROM_CHAT_DELETED_AT_` 或 `TO_CHAT_DELETED_AT_`：

- 寄往被删会话的：未完成的信转为 `failed` 并生成唯一失败回信，未消费的回信丢弃，未送达的即时消息和请示撤回；清理这些行的正文、引用和附件元数据。
- 被删会话发出的：已受理的回信照常投递，使用收件侧的附件副本；未完成的信转为 `failed`（不寄失败回信），即时消息和请示撤回。
- 两端均已删除且邮件为终态时删除该行。删除已归档的 Chat 同样触发清理。

**归档。** 归档不算删除，不清理正文。投递复核把已归档会话视为不存在，因此触发投递时，寄往它的信转为 `failed` 并寄回失败回信，回信挂起，即时消息和请示撤回。但已归档会话不再产生投递触发，归档时已在排队的 mail 会一直停到重启后挂起；归档时的主动状态转换尚未实现。

**保留期。** `mail.retention-days` 默认 30（正整数，最大 365000），启动后执行一次，此后每 24 小时执行：

- 终态时间 `TERMINAL_AT_` 与 `UPDATED_AT_` 相互独立。letter 的 `delivered` 仍在等待回信，不是终态；`suspended` 也不清理。
- 到期后清空 input、message（包括请示的等待项快照）、references、files 元数据及可能包含正文的 error，记录 `BODY_PURGED_AT_`（JSON 中为 `purgedAt`）。保留信封、幂等摘要、answeredBy、parentId 和 openedChat，不改变首次终态时间。
- 已投递的附件副本属于收件 Chat 资源，按 Chat 生命周期保留，定期清理不删除这些物理文件。
- 定时核对活动与归档存储，补偿漏掉的删除钩子；仍在 `accepting` 的新 Chat 预留地址不视为已删除。清理失败只记日志，不阻止启动，也不自动重投。

## 数据结构

`MAILS` 的建表与索引以 [store.go](../internal/mailbox/store.go) 为事实源：表名大写，字段为大写下划线加尾部 `_`，索引以 `IDX_` 开头，与 chats.db 风格一致；Go DTO 和 HTTP JSON 字段名保持原协议。关系字段为 `PARENT_MAIL_ID_`（即时消息、请示所属的信）、`ANSWERED_BY_MAIL_ID_`（回答这封信的回信）、`REPLY_OF_RUN_ID_`（回信来自的 Run）。

Mailbox 尚未上线，当前结构作为 schema V1 基线，开发期结构调整不递增版本号。旧开发库（包括此前开发构建写出的 V2）会被严格校验拒绝，并保留原版本标记和数据；程序不迁移、删除或自动重建，开发环境需停服后显式备份并重建 `mail.db`。chats.db 与 Chat 资源不因此重建。该规则仅适用于尚未发布的 Mailbox 基线，不作为已发布存储的通用降级策略。

| kind | 正常终态 | 其他终态 |
|---|---|---|
| letter | `done`（已被回信） | `failed`、`discarded` |
| reply | `delivered` | `discarded` |
| notify | `delivered` | `withdrawn` |
| ask | `done`（等待项已解决） | `withdrawn` |

信的状态流转：

```
accepting ──▶ queued ──开 Run──────────────────────▶ delivered(started) ──┐
                │  └─插入──▶ inflight ──被消费──▶ delivered(inserted) ───┤
                │              └─未消费/未发出──▶ queued                 └─Run 结束──▶ done（回信）
                ├─复核失败 / 会话删除──▶ failed（寄回失败回信）
                └─重启 / 远端未知──▶ suspended ──重投──▶ 从挂起前阶段继续 ──▶ queued
                                              └─丢弃──▶ discarded
```

回信的流转相同，只是没有 `accepting`，终态为 `delivered`。

## 模型输入：信封头

来自 Agent 的内容在正文前加一行英文信封头，用户消息不加。信封头不落盘，每次组装模型输入时按行上的 mailId 从 mail.db 生成；正文到期清理后，信封仍保留。首词即 kind；有附件时，信封头下方列出附件路径，然后是正文。

```
[Letter M1 from agent "lead" (chat c_100). Your final answer in this run will be sent back as the reply.]
帮我整理 A 项目本周的周报，输出 Markdown。

[Reply P3 from agent "writer" (chat c_8f2a), answering your letters M1, M4: completed]
Attachments: @chat/mail/P3/weekly.md
周报已整理好，本周主要进展……

[Notify N5 from agent "writer" (chat c_8f2a), about your letter M1. No reply needed.]
先给你本周的初稿，上周的还在整理。

[Ask Q7 from agent "writer" (chat c_8f2a, run r_22), about your letter M1: question q_7]
1. 第 4 个仓库没权限，怎么处理？  select: 跳过 | 等我开权限
Answer with chat_submit runId="r_22" awaitingId="q_7", or ask the user if unsure.
```

这改变了"origin 不进入模型输入"的规则，仅适用于来自 Agent 的内容。

## 工具、HTTP 与事件

| 工具 | 说明 |
|---|---|
| `chat_send` | 寄信，返回 `{mailId, chatId, delivery: started\|inserted\|queued, runId?}`；幂等键为 `runId + toolId` |
| `chat_notify` | 即时消息 |
| `chat_submit` | 答复请示 |
| `chat_get_status` | 支持 `mailId`；只有 `started` 的信附带 Run 状态 |
| `chat_interrupt` | 接受 `started` 信开出的 Run |
| `chat_query` | `action: "opened"` 列出本 Agent 开出的会话及其 active/activeRun |
| `chat_start`、`chat_steer` | 保留，说明中写"优先用 `chat_send`" |

- `GET /api/mails?agentKey=&chatId=&kind=&status=&limit=&offset=`：只返回当前 subject 的 mail；挂起的信和回信附带 `evidence` 与 `error`。
- `POST /api/mails/action {mailId, action: redeliver|discard}`：只处理挂起的 letter/reply，校验当前 subject。
- 广播 `mail.updated {mailId, kind, status, delivery, toAgentKey, toChatId, runId}`。
- `request.query`、`request.steer` 与 JSONL 带 `mailId`、`mailKind`、`mailAttempt`、`mailHop`，回信另有 `outcome`；代答的 submit 带平台生成的 `origin`、`accessGrant`。这些字段由平台生成，公共输入不得伪造。

相邻 WebClient 的收件箱和时间线消费上述协议。
