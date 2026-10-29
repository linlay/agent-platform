# Mailbox（会话投递）

## 一句话

Mailbox 负责会话之间的三类往来：**信**（派活、补充，平台为它生成回信）、**即时消息**（中途告知）、**请示**（等待项转给上级处理）。每条记录统称 mail（`mailId`、`mail.db`），按 `kind` 分为 `letter`、`reply`、`notify`、`ask` 四种。它们都寄给 Chat，按同一条投递规则送达：收件会话空闲就开 Run，正在跑就插进当前 Run，插不进就排队。主要对话也是 Chat，寄给 Agent 等于寄给它的主要对话。

## 已确认

- Run 不做地址，一切都寄给会话。寄到已有会话等于补充那件事，另一件事就新开会话。
- 信持久化；重启后未处理的信保留，等人决定。
- 寄给 Agent 而对方没有主要对话时，新建并设为主要对话；一个 Agent 只有一个主要对话。
- 每封信都会有回信；一个 Run 只正式回一次，内容是该 Run 的完整最终输出（`RunCompletion.AssistantText`，即完整版的 lastRunContent）。
- 中途告知用即时消息，不是信，不需要回复。
- 请示由平台根据等待项自动产生，用 `chat_submit` 结构化作答；分身照常卡在 awaiting 上，等人的 submit 或上级的 `chat_submit`。
- 代批的上限：前台用自己当前的档位做这个操作不需要审批。
- `chat_submit` 支持提问、审批、表单。
- 模型、思考强度、权限档位、会话名只能随新开会话传。
- 回信开出的 Run 使用收件 Agent 的默认档位；挂起的信被丢弃时不回信。
- `chatId` 只允许三类会话。
- 不另存控制身份；重启后由重投者接管控制权。
- 新工具统一用 `chat_` 前缀；`chat_start`、`chat_steer` 本期保留。
- 统称为 mail；kind 为 `letter`（来信）、`reply`（回信）、`notify`（即时消息）、`ask`（请示）。

## 三类往来

| | 信 | 即时消息 | 请示 |
|---|---|---|---|
| kind | `letter`，回信为 `reply` | `notify` | `ask` |
| 性质 | 正式：派活、补充 | 非正式：阶段结果、提醒 | 正式：分身卡在等待项上 |
| 产生 | `chat_send` | `chat_notify` | 平台自动：由信开出的 Run 调用 `ask_user_question`、`ask_user_form` 或卡在审批上，即把分身的 `awaiting.ask` 转给上级 |
| 发往 | 任意可寄的会话 | 开出本 Run 的那封信的发信会话 | 开出本 Run 的那封信的发信会话 |
| 答复 | 平台回信：Run 结束时的完整最终输出 | 无 | `chat_submit`（或人直接 submit），先到先得 |
| 重启后 | 挂起，等人决定 | 未送达的撤回 | 撤回（等待项随 Run 取消） |

由用户直接开出的 Run 进入等待项时照旧由人处理，不产生请示。

## 地址（信）

发信时一次性解析成具体的 `toChatId`，之后不再变化（主要对话指针后来切换，已发出的信仍留在原会话）。

| 写法 | 解析结果 |
|---|---|
| `chatId` | 指定的已有会话：补充那件事 |
| `newChat: true` | 为目标 Agent 新开会话（分身）：另一件事 |
| 都不写 | 目标 Agent 的主要对话；没有就新建并设为主要对话（只补缺，不切换） |

`agentKey` 省略时为发信 Agent 自己；`chatId` 与 `newChat` 互斥。

`chatId` 只允许以下三类会话，并且必须属于目标 Agent、同一用户：

1. 发信 Agent 自己开出的会话（mail 记录中 `opened_chat = 1`，或 `chat_start` 建会话时 query 行的 `origin`）；
2. 用户在本轮输入中引用过的会话（chat 类 reference）；
3. 目标 Agent 的主要对话。

以下一律拒绝：寄给发信 Run 自己所在的会话；会话已归档或已删除（不自动恢复）。mail 上线之前创建的老会话可以照常寄。

三类会话只代表"可以寄进去"；读取会话历史（`chat_query`）、中断 Run（`chat_interrupt`）仍分别授权。

前台要判断"补充还是新开"，需要知道自己开出的会话哪些在跑：`chat_query` 增加"我开出的会话"列表（形式待定）。

## 投递规则

三类往来共用。对收件会话里 `queued` 的内容按时间顺序处理，按会话当前状态决定：

| 会话状态 | 处理 |
|---|---|
| 没有 Run，也没有未决等待项 | 用最早的一条开 Run（`delivery = started`），其余接着插进这个 Run |
| 有 Run，正常运行，或停在普通 `wait` 上 | 走现有 steer 通道插入（`delivery = inserted`）；`wait` 被唤醒，唤醒原因为 `mail` |
| 有 Run，停在提问、审批或表单等待上 | 插入；内容在 Run 内排队，等待项解决后才被模型读到，不会替代作答 |
| 有 Run，是规划 Run | 排队：插入会作废候选计划 |
| 有 Run，正在收尾或压缩 | 排队 |
| 没有 Run，但有未决等待项（如 Run 结束后仍待确认的计划，即现有 query 返回 409 `awaiting_pending` 的情况） | 排队，等等待项清除或续接仲裁完成 |

`wait` 也会登记 PendingAwaiting，但它属于"有 Run"的情况，按第二行处理，不能当作未决等待项挡住回信。

触发投递：发出时、Run 结束时、会话维护结束时、等待项清除时。重启时不投递。

"会话空闲"由 Runtime 在续接仲裁之后发出：计划被批准时，执行 Run 先注册，排队内容再插进执行 Run；被拒绝、超时、执行准入失败时，会话空闲，按普通规则投递。`chat_send` 不能开规划 Run（工具没有 `planningMode`），因此信不会进入规划流程。

### 在途记录（先占位，再调用）

1. 在一个事务里写好：`status = inflight`、`attempts + 1`、`steer_id = <id>:<attempts>`、`delivery`；插入时同时写好目标 `run_id`。
2. 调用 Runtime，query/steer 带上 `(id, attempt)`。
3. 回调（已消费、Run 已开、Run 结束仍未消费、确定未发出、结果未知）都核对 `attempts`，旧尝试迟到的回调直接丢弃。
4. 已消费：转为 `delivered`，写 `consumed_at`；Run 结束仍未消费或确定未发出：回到 `queued`；结果未知：信和回信转为 `suspended`（证据为 `unknown`），即时消息和请示转为 `withdrawn`。

## 信与回信（`letter` / `reply`）

- 每封信都会有回信，没有 `reply` 参数；不需要回信的内容用即时消息。
- **一个 Run 只正式回一次**：Run 结束时，对这个 Run 消费过的信，每个发信会话各寄一封回信，同一会话只寄一封。正文是该 Run 的完整最终输出，即 lastRunContent 的完整版，附带本 Run 用 `artifact_publish` 发布的文件，`outcome` 为 `completed|failed|canceled`。
- 正文取自该 Run 结束时的 `RunCompletion.AssistantText`，在 Run 结束钩子里直接拿到。不读 Chat 摘要的 `lastRunContent`（只存前 200 个字），也不读"会话的最新 Run"。
- 补充：往还在跑的会话寄信，信插进去，与原来的信合在一起只回一封；会话已空闲，补充信开新 Run，新 Run 结束后再回一封。
- 回信不再要求回信。
- 被回答的信写入 `answered_by`。生成回信、写 `answered_by`、原信转为 `done` 在同一事务中完成，只更新 `answered_by` 为空的行。
- 回信按 `reply_key` 唯一，有两种形式：
  - 正常回信：`run:<runId>:<toChatId>`，同一 Run、同一收件会话只有一封，重复的结束回调不会产生第二封。
  - 失败回信（投递复核失败、收件会话被删除，此时还没有 Run）：`letter:<letterId>`，每封原信只有一封。

| 时间 | 发生什么 |
|---|---|
| 10:00 | 前台寄 M1："整理本周周报"。开出分身 R2 |
| 10:05 | 前台看到 R2 还在跑，寄补充信 M4："把上周的也补上"。M4 插进 R2 |
| 10:08 | R2 发即时消息："先给你本周的初稿"。前台收到，不需要回复 |
| 10:20 | R2 结束，前台收到一封回信，内容是 R2 的完整最终输出，同时回答 M1 和 M4 |
| 10:30 | 前台再补一点。R2 已结束，补充信开出 R6，R6 结束后再回一封 |

## 即时消息（`notify`）

- `chat_notify {message, files?}`，发往开出当前 Run 的那封信的发信会话；当前 Run 不是由信开出的，则不可用。
- 用于发信方需要尽早知道的事，比如阶段结果、提醒。纯进度不发，走 `run.activity` 和 `chat_get_status`。
- 不会产生回信；收到即时消息而开出的 Run，结束时也不回信。

## 请示与代答（`ask`）

### 产生与撤回

- 由信开出的 Run 进入等待项（提问、审批、表单）时，分身照常卡在 awaiting 上，平台同时生成一条请示，发往开出该 Run 的那封信的发信会话。
- 请示与等待项同生同灭：等待项先被人答复、超时或 Run 被取消，未送达的请示转为 `withdrawn`；已送达的，`chat_submit` 返回 `already_resolved`。
- 等待时限沿用现有规则（审批默认 600 秒，超时按拒绝处理）。

### `chat_submit`

参数与人用的 `/api/submit` 一致：`{runId, awaitingId, param | params}`，`param` 与 `params` 互斥，平台按等待项原结构校验。只能答复寄给本会话的请示。

**按等待项的来源处理，不按 mode 判断。** 工具审批带审阅页面时，也会以 `mode = form` 呈现，不能因此进入普通填表分支。请示记录 `awaiting_source`：

| 来源 `awaiting_source` | 上级能做什么 | 提交形状 |
|---|---|---|
| `ask_user_question` | 回答。前台先自己判断，拿不准就在自己会话里问用户，再提交 | `params`：每题一项，`answer` 或 `answers` |
| `ask_user_form` | 填写。请示中给出从 HTML 解析出的字段清单（name、类型、选项），结果按现有表单规则校验类型与大小 | `param`：`{decision: "approve", data}`，或 `reject`、`dismiss` |
| `tool_approval`（无论 mode 是 approval 还是 form） | 拒绝：随时可以。批准：受代批规则约束 | 与原等待项相同：approval 用 `params` 的 `decision`，审阅页用 `param` |

代批规则（仅 `tool_approval`）：

- 提交时，平台用上级 Run 当前的档位，对分身这次被冻结的调用重新走一遍同一套审批判定：Bash 和文件访问走 AccessPolicy，工具自身的审阅走该工具的审批规划。判定结果仍需审批，则返回 `approval_requires_human`，由上级转告用户。不能用档位高低代替判定，因为 `auto_approve` 下破坏性操作仍要人批。
- 冻结调用的校验照旧：批准只对应原等待项里那次调用的指纹，参数、cwd、脚本内容变了就不算数。
- 硬禁止与只读根不可批（人也不能批）；强制人工审批（如提升权限档位的审阅）永不代批。
- 分身会话的 submit 记录写明代答者（agent、会话、Run），审批另记依据的档位。
- 超出上级权限的审批，本期由用户到分身会话处理；转到主视图直接批放到后续阶段。

请示示例见下文"信封头"。

## 跳数

跳数表示这件事离"人最近一次亲口说话"隔了多远，用来防止 Agent 之间没完没了地派活、通知和请示。

1. 用户、Automation、`/api/execute` 的输入为 0 跳；`chat_start` 开出的 Run 为父 Run 当前跳数 + 1（用户直接开出的父 Run 即为 1）。
2. Run 发出的内容（信、即时消息、请示，以及平台替它发的回信）在发出时记为 Run 当前跳数 + 1，记在这条 mail 的 `hop` 上。
3. Run 的当前跳数：收到用户直接输入时归 0；收到 mail 时取 `max(当前跳数, mail.hop)`；被上级 `chat_submit` 答复时取 `max(当前跳数, 上级 Run 当前跳数 + 1)`。加一只在发出时算一次，接收时不再加。两次用户输入之间只升不降。
4. 达到上限后：`chat_send`、`chat_notify` 返回 `mail_hop_limit`；请示不再发给上级，等待项只等用户；回信照常寄出。`chat_submit` 不受限。
5. 上限为 `configs/runtime.yml` 中的 `mail.max-hops`，默认 4。

例 1：派活与回报

| 步骤 | Run 或内容 | 跳数 |
|---|---|---|
| 你："整理周报" | 前台 R1 | 0 |
| R1 派活 | 信，开出分身 R2 | 1 |
| R2 结束，平台回信 | 回信，开 R3 | 2 |
| R3 再派 | 信，开出分身 R4 | 3 |
| R4 结束，平台回信 | 回信，开 R5 | 4 |
| R5 还想派 | 被拒绝，R5 把结果交给你 | — |

例 2：反复请示

| 步骤 | Run 或内容 | 跳数 |
|---|---|---|
| R1 派活 | 分身 R2 | 1 |
| R2 提问 | 请示（hop 2），开出前台 R3 | 2 |
| R3 `chat_submit` 作答 | R2 = max(1, 2 + 1) = 3 | 3 |
| R2 再提问 | 请示（hop 4），开出前台 R4 | 4 |
| R4 作答 | R2 = max(3, 4 + 1) = 5 | 5 |
| R2 再提问 | 已超上限，请示不发给前台，只等你在分身会话里答 | — |

## 授权

### 受理时冻结

`chat_send` 在发信 Run 的权限临界区内判断，写入 mail.db 即为受理。冻结内容：发件身份（agent、subject、chat、run、tool）；生效档位与审批收据的消费结果（`access_grant`），收据在受理时消费；模型、规范化引用、跳数、目标会话。

### 投递时复核

任一项失败，信转为 `failed` 并寄回失败回信：目标 Agent 仍存在且可准入；目标会话存在、未归档，owner 与用户身份一致；引用仍可用（技能存在、知识库可读、chat 引用仍属同一用户）；模型仍在注册表中。不校验发信 Run 是否仍在运行，也不和它当前的档位比较。

### 控制身份

不新增字段。Run 的控制身份本就冻结在进程内，Run 结束后仍保留到进程结束。每封信在本次投递时用到的身份称为它的**投递身份**，只保存在进程内：

- 正常投递：投递身份 = 发信 Run 的控制身份。
- 人工重投：投递身份 = 重投者的身份（重启后原发信 Run 的身份已不存在，不再去查）。

使用规则：

- 信开出的 Run：使用这封信的投递身份。
- 回程（这封信引出的回信，以及处理它的 Run 发出的即时消息、请示）在发信会话开出的 Run：同样使用这封信的投递身份。重投之后，整条回程都跟着重投者走。
- 回信本身被人工重投时：使用重投者的身份。

发信方的控制权由 `delivery` 决定：`started` 可以查 Run 状态、中断该 Run；`inserted` 只能查这封信本身的状态。

### 其他

- 只有主 Agent 根 Run（含 TEAM 总控）能调用 `chat_send`、`chat_notify`、`chat_submit`，包括由信开出的 Run；BTW、`agent_invoke` 子任务、Team 成员不能调用。规划 Run 默认排除这三个工具。
- `accessLevel`、`modelKey`、`reasoningEffort`、`chatName` 只能与 `newChat` 同用，判断规则同 `chat_start`：省略则继承，提升需要人工审批。
- 寄给已有会话的信冻结发信时的继承档位；开新 Run 时用这个档位，模型用目标 Agent 默认值；插入时不改变正在跑的 Run 的设置。
- 回信开出的 Run 使用收件 Agent 的默认档位，不提升。
- 旧工具限制不变：由信开出的 Run 调用 `chat_start`、`chat_steer`、`chat_get_status`、`chat_interrupt`，以及 `wait` 的 `run.terminal` 条件，仍返回 `run_chaining_not_allowed`。派完活用不带条件的普通 `wait` 等回信唤醒；`wait` 新增唤醒原因 `mail`。

## 文件交付

正文里的 `@chat/...` 路径属于发件会话，跨会话后会解析到收件会话，不能当作交付。

- `chat_send`、`chat_notify` 的 `files`：受理时先从发件会话读取文件、算出名称、大小、sha256 并写入 `files`，再复制到收件会话的 `mail/<id>/`；复制时核对摘要。投递时再核对一次，不一致就失败。
- 回信：生成回信时，把本 Run 发布的文件按同样方式复制并记录。
- 正文不改写；信封头下方列出附件在收件会话中的路径。

## 新建会话与补建主要对话

- 新开会话：在一个事务里写入信（`status = accepting`，预留 chatId，附件清单），按预留 ID 建会话（已存在就跳过），复制附件，转为 `queued`。同一调用重试时从断点继续；重启时 `accepting` 转为 `suspended`（证据为 `none`）。
- 补建主要对话：在会话 mutation 锁内完成"检查指针 → 预留 chatId 并写信 → 建会话 → 指针为空才设置"；该锁与 `PUT /api/chats/primary` 共用。query `primary:true` 的准入保持原样、不取该锁，因此最后一步是条件写入：指针已被它设置时不覆盖，信留在新建的会话。从中断处恢复时重新仲裁：指针已被设置的，改投到该主要对话。

## 重启与人工重投

- **信**：`accepting`、`queued`、`inflight`、`delivered` 转为 `suspended`，不自动开任何 Run。
- **回信**：`queued`、`inflight` 转为 `suspended`，可以重投；已 `delivered` 的回信是终态，永不再投。回信里是分身的工作成果，不能因为重启丢掉。
- 挂起时写入 `evidence`，有歧义按未知处理；启动时扫描收件会话 JSONL，凡出现该 `mailId` 的补为 `consumed`。

| evidence | 判断依据 | 界面文案 |
|---|---|---|
| `none` | 挂起前处于 `accepting` 或 `queued` | 未投递，可以重投 |
| `consumed` | 有消费证据 | 已处理过，重投会重新执行 |
| `unknown` | 挂起前处于 `inflight` 且无消费证据，或远端结果未知 | 可能已处理，重投可能重复执行 |

- 由人重新投递或丢弃（`discarded`，不发回信）。重投从挂起前的阶段接着做，不一律跳到 `queued`：
  - 挂起前是 `accepting`：先补完受理，即建会话（补建主要对话时重新仲裁）、按清单复制附件，再进 `queued`。发件会话里的附件已变化或缺失，则转为 `failed`，因为不能把新内容当作原信附件。
  - 其他状态：直接回到 `queued`，按投递规则重新投递。
- 重投后的投递身份为重投者，见"控制身份"。
- **即时消息、请示**：未送达的转为 `withdrawn`，不提供重投。

## 会话删除

- 任一端会话被删除：未完成的信转为 `failed`（收件端删除时寄回失败回信），未送达的即时消息和请示转为 `withdrawn`。
- 正文类字段（`input`、`message`、`refs`、`files`）清空并写 `purged_at`；收据字段保留，保证幂等不失效。复制过去的文件随收件会话目录一起删除。

## 工具

| 工具 | 本期 | 稳定后 |
|---|---|---|
| `chat_send` | 新增：寄信 | 保留 |
| `chat_notify` | 新增：即时消息 | 保留 |
| `chat_submit` | 新增：答复请示 | 保留 |
| `chat_start`、`chat_steer` | 保留，说明里写"优先用 `chat_send`" | 删除 |
| `chat_get_status` | 增加 `mailId` 参数；`started` 的信可查其 Run | 保留 |
| `chat_interrupt` | 接受 `started` 信开出的 Run | 保留 |
| `chat_query` | 增加"我开出的会话"列表 | 保留 |
| `chat_manage` | 不变 | 保留 |

`wait` 增加唤醒原因 `mail`。

### `chat_send`

```json
{
  "message": "分析权限设计",
  "agentKey": "writer",
  "chatId": "c_123",
  "newChat": true,
  "chatName": "权限检查",
  "modelKey": "gpt-x",
  "reasoningEffort": "HIGH",
  "accessLevel": "auto_approve",
  "references": [],
  "files": ["@chat/weekly.md"]
}
```

- `message` 必填，其余可选；`chatId` 与 `newChat` 互斥；`chatName`、`modelKey`、`reasoningEffort`、`accessLevel` 仅限 `newChat`。
- 幂等键为 `runId + toolId`：参数相同返回同一结果，参数不同返回 `idempotency_conflict`；幂等记录落盘。

返回：

```json
{"mailId":"mail_...","chatId":"c_123","delivery":"started|inserted|queued","runId":"r_..."}
```

`queued` 时没有 `runId`。`chat_get_status {mailId}` 返回信的状态与 `delivery`；只有 `started` 的信附带 Run 状态。

### `chat_notify`

```json
{"message": "先给你本周的初稿，上周的还在整理", "files": ["@chat/draft.md"]}
```

### `chat_submit`

提问（`params`，每题一项）：

```json
{"runId": "r_22", "awaitingId": "q_7", "params": [{"answer": "跳过"}]}
```

审批（`params`，每个待批项一项）：

```json
{"runId": "r_22", "awaitingId": "a_9", "params": [{"decision": "approve"}]}
```

表单或审阅页（`param`，单个对象）：

```json
{"runId": "r_22", "awaitingId": "f_3", "param": {"decision": "approve", "data": {"region": "cn-east"}}}
```

- `param` 与 `params` 互斥，工具定义同时声明二者并标明互斥。形状与 `/api/submit` 完全一致：问题项为 `answer`（多选为 `answers`），审批项为 `decision`（`approve|approve_rule_run|reject`），表单为 `decision`（`approve|reject|dismiss`），`approve` 时必须带 `data`。
- 用哪种形状由等待项决定，与 `awaiting_source` 的约束同时生效。

## 模型输入：信封头

来自 Agent 的内容在正文前加一行信封头；用户消息不加。信封头用英文，正文原样。每条都先写自己的 ID 和发件人。

```
[Letter M1 from agent "lead" (chat c_100). Your final answer in this run will be sent back as the reply.]
帮我整理 A 项目本周的周报，输出 Markdown。

[Reply P3 from agent "writer" (chat c_8f2a), answering your letters M1, M4: completed]
周报已整理好，本周主要进展……
Attachments: @chat/mail/P3/weekly.md

[Notify N5 from agent "writer" (chat c_8f2a), about your letter M1. No reply needed.]
先给你本周的初稿，上周的还在整理。
Attachments: @chat/mail/N5/draft.md

[Ask Q7 from agent "writer" (chat c_8f2a, run r_22), about your letter M1: question q_7]
第 4 个仓库没权限，怎么处理？  select: 跳过 | 等我开权限
Answer with chat_submit runId="r_22" awaitingId="q_7", or ask the user if unsure.

[Ask Q8 from agent "writer" (chat c_8f2a, run r_22), about your letter M1: approval a_9]
Bash: rm -rf build/   cwd: /work/proj
Options: approve | approve_rule_run | reject. Approval is accepted only if your own access level allows this operation.
```

- 信封头的首词就是 kind：`Letter`、`Reply`、`Notify`、`Ask`。
- "about your letter M1"：即时消息和请示所属的那封信，即开出发件 Run 的信。
- 信封头不落盘，每次组装模型输入时按行上的 `mailId` 和 mail.db 生成；正文已清空时省略引用部分。
- 这改变了现有"origin 不进入模型输入"的规则，仅适用于来自 Agent 的内容。

## 数据结构

### mail.db

位于 `AP_RUNTIME_CHATS_DIR/mail.db`，独立库，`application_id = 0x41504D4C`（APML），`user_version = 1`，遵守 `sqlitecontract` 的身份规则。不改 `chats.db`。

```sql
CREATE TABLE mails (
  id              TEXT PRIMARY KEY,           -- mail_<ulid>
  kind            TEXT NOT NULL,              -- letter|reply|notify|ask
  status          TEXT NOT NULL,              -- accepting|queued|inflight|delivered|done|failed|suspended|discarded|withdrawn
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,

  -- 收件
  to_agent_key    TEXT NOT NULL,
  to_chat_id      TEXT NOT NULL,
  opened_chat     INTEGER NOT NULL DEFAULT 0, -- 1 = 这封信新建了 to_chat_id

  -- 发件（与 JSONL origin 同义）
  origin_type     TEXT NOT NULL,              -- agent（本期）；user|automation 留给后续
  from_agent_key  TEXT NOT NULL DEFAULT '',
  from_chat_id    TEXT NOT NULL DEFAULT '',
  from_run_id     TEXT NOT NULL DEFAULT '',
  from_tool_id    TEXT NOT NULL DEFAULT '',   -- 平台生成的回信、请示为空
  subject         TEXT NOT NULL DEFAULT '',

  -- 正文（会话删除时清空）
  input           TEXT,                       -- 工具参数原样 JSON；平台生成的为空
  message         TEXT NOT NULL DEFAULT '',   -- 收件会话收到的正文；请示为等待项结构快照 JSON
  refs            TEXT,                       -- 规范化后的 references JSON
  files           TEXT,                       -- [{name,size,sha256,path,source}]，path 为收件会话内路径
  purged_at       INTEGER NOT NULL DEFAULT 0,

  -- 受理时冻结
  hop             INTEGER NOT NULL,
  access_level    TEXT NOT NULL DEFAULT '',   -- 信开 Run 时使用的档位
  access_grant    TEXT,                       -- 提升审计，同现有 accessGrant
  model           TEXT,                       -- {key,reasoningEffort}，仅新开会话
  request_digest  TEXT NOT NULL DEFAULT '',   -- 幂等比较

  -- 按类型
  answered_by     TEXT NOT NULL DEFAULT '',   -- letter：回答它的 reply id
  reply_key       TEXT NOT NULL DEFAULT '',   -- reply：run:<runId>:<toChatId> 或 letter:<letterId>
  reply_of_run    TEXT NOT NULL DEFAULT '',   -- reply：正文来自哪个 Run；失败回信为空
  outcome         TEXT NOT NULL DEFAULT '',   -- reply：completed|failed|canceled
  parent_id       TEXT NOT NULL DEFAULT '',   -- notify/ask：开出发件 Run 的 letter
  awaiting_id     TEXT NOT NULL DEFAULT '',   -- ask：分身的等待项
  awaiting_mode   TEXT NOT NULL DEFAULT '',   -- ask：question|approval|form，只决定呈现与提交形状
  awaiting_source TEXT NOT NULL DEFAULT '',   -- ask：ask_user_question|ask_user_form|tool_approval，决定能否代答

  -- 投递
  delivery        TEXT NOT NULL DEFAULT '',   -- started|inserted
  run_id          TEXT NOT NULL DEFAULT '',   -- 当前在途或已消费它的 Run
  steer_id        TEXT NOT NULL DEFAULT '',
  attempts        INTEGER NOT NULL DEFAULT 0,
  consumed_at     INTEGER NOT NULL DEFAULT 0,
  resume_phase    TEXT NOT NULL DEFAULT '',   -- 重投恢复的挂起前阶段
  evidence        TEXT NOT NULL DEFAULT '',   -- 挂起时：none|consumed|unknown
  error           TEXT
);

CREATE UNIQUE INDEX mails_idem    ON mails(from_run_id, from_tool_id) WHERE from_tool_id <> '';
CREATE UNIQUE INDEX mails_reply   ON mails(reply_key) WHERE kind = 'reply';
CREATE UNIQUE INDEX mails_ask     ON mails(from_run_id, awaiting_id) WHERE kind = 'ask';
CREATE INDEX mails_deliver  ON mails(to_chat_id, status, created_at);
CREATE INDEX mails_inbox    ON mails(to_agent_key, created_at);
CREATE INDEX mails_run      ON mails(run_id);
CREATE INDEX mails_status   ON mails(status);
CREATE INDEX mails_answered ON mails(answered_by);
```

各类型的终态：

| kind | 正常终态 | 其他终态 |
|---|---|---|
| letter | `done`（已被回信） | `failed`、`discarded`；重启后 `suspended` |
| reply | `delivered`（终态，永不再投） | `withdrawn`（收件会话已删除）、`discarded`；重启后未送达的 `suspended` |
| notify | `delivered` | `withdrawn` |
| ask | `done`（等待项已解决） | `withdrawn` |

信的状态流转：

```
accepting ──▶ queued ──开 Run──────────────────────▶ delivered(started) ──┐
                │  └─插入──▶ inflight ──被消费──▶ delivered(inserted) ───┤
                │              └─未消费/未发出──▶ queued                 └─Run 结束──▶ done（回信）
                ├─复核失败 / 会话删除──▶ failed（寄回失败回信）
                └─重启 / 远端未知──▶ suspended ──重投──▶ 从挂起前阶段继续（accepting 先补完受理）──▶ queued
                                              └─丢弃──▶ discarded
```

回信的流转相同，只是没有 `accepting`，终态是 `delivered`。

### Chat JSONL 与事件

- 收件会话：由 mailbox 内容开出的 Run，其 query 行的 `query` 存正文 `{message, references}`（附件以文件引用出现在 references 中），顶层加 `mailId`、`mailKind`（`letter|reply|notify|ask`），回信再加 `outcome`；插入的内容，steer 对象加同样的字段，`steerId` 即 `steer_id`。`origin` 不变。
- 分身会话：上级的 `chat_submit` 写成普通 submit 记录，`origin` 为代答者；代批审批另记依据的档位。
- `request.query`、`request.steer` 事件同步带这些字段。
- 新增广播 `mail.updated {mailId, kind, status, delivery, toAgentKey, toChatId, runId}`。

### HTTP

- `GET /api/mails?agentKey=&chatId=&kind=&status=`：按 Agent 列出收件箱，或按会话、类型、状态筛选；挂起的信和回信带 `evidence` 与 `error`。
- `POST /api/mails/action {mailId, action: "redeliver"|"discard"}`：只作用于 `suspended` 的信和回信。

## 模块

- 新包 `internal/mailbox`：负责 store、地址解析、受理冻结与投递复核、投递器（按 chatId 串行）、在途记录、回信与请示生成、文件复制、跳数计算、启动挂起与证据核对。
- Runtime 通过窄接口调用 mailbox：Run 结束、消费 query/steer、会话空闲（维护结束、等待项清除、续接仲裁完成）、等待项产生与解决。mailbox 经 Runtime 的 query 准入、steer 入口与 submit 入口投递和作答，不依赖 Server。
- 现有 `chat_start` 的"权限判断"与"开 Run"拆开：`chat_send` 在受理时完成判断和收据消费，开 Run 时只使用冻结结果，不要求发信 Run 仍在运行。
- 代批评估：对被冻结的调用"以指定档位重新走一遍审批判定"的入口，覆盖 AccessPolicy（Bash、文件）与工具自身的审批规划，供 `chat_submit` 使用。
- 等待项登记来源（`ask_user_question`、`ask_user_form`、工具审批），请示据此写入 `awaiting_source`。
- 回信正文由 Run 结束钩子直接传入 `RunCompletion.AssistantText`。
- `internal/runops` 新增 `chat_send`、`chat_notify`、`chat_submit` 三个 handler；`chat_get_status`、`chat_interrupt` 增加按 `delivery` 读取 mail.db 的归属判断。

## 实施步骤

1. **信的完整闭环**：mail.db、`chat_send`（含 `files`）、地址与资格、新建会话与补建主要对话的幂等、受理冻结与投递复核、开 Run/插入/排队与在途记录、回信、跳数、信封头、`chat_get_status {mailId}`、`wait` 唤醒原因、启动挂起、证据核对与 `/api/mails` 人工重投。
2. **请示、代答与即时消息**：请示的产生与撤回、`chat_submit`（提问、表单、受上限约束的审批）、`chat_notify`、`chat_query` 的"我开出的会话"列表。
3. **展示**：`mail.updated`；WebClient 显示来信、回信、即时消息、请示，挂起的信及其证据（相邻仓库）。

需要同步修改：AGENTS.md（会话工具组、主要对话只能由 query/PUT 设置的规则、请示与代批）、`docs/子智能体调度.md`、`docs/API与协议.md`、`docs/HITL协议.md`、`docs/原生等待工具.md`、`docs/工具目录权限.md`（代批评估）、`agent-settings.example.yml` 的 planning 排除项（加入三个新工具）、task-control skill 与工具说明（新旧工具分工、补充还是新开、由信开出的 Run 用普通 `wait`）。

`chat_query` 的列表形式已采用 `action: "opened"`，返回当前 active/activeRun，覆盖 mailbox 和旧 chat_start 开出的会话。

## 待定
- 分身审批等待时限是否需要比默认 600 秒更长（前台与用户可能不在线）。

## 不在本期

- 用户消息走 mailbox（`/api/query` 与 `/api/steer` 合并），以及客户端改为订阅 Agent。
- 前台/分身的产品形态、待办与并发上限；超出代批上限的审批转到主视图直接批。
- 删除 `chat_start`、`chat_steer`。
- Automation 和外部渠道改为发信。
