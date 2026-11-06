# Mailbox 投递来源盘点

评估讨论汇集稿。不写工作量，不写选型结论。与 [mailbox-main-chat.md](mailbox-main-chat.md)、[mailbox-next.md](mailbox-next.md)、[chat-send-unify.md](chat-send-unify.md) 交叉；本文件只管「还有哪些东西会（或可能）往某个 Chat 投递」，以及主 Chat 箱子与这些来源如何碰撞。出队节奏、主要聊天形态、`chat_send` 三态判定不在此展开。

背景：除了用户触发的 mailbox 投递，还有计划任务（Automation）的投递，以及其它看起来像投递、或今天还不走 mailbox 的入口。本文件把已核对的入口、已按「信」设计的 kind、应先分开的控制/旁路，以及开放疑问写在一起，便于一次拍清。

---

## 要解决的问题

1. 今天能开 Chat / 开 Run / 插话的入口不止用户输入和 `chat_send`；若只把「用户 → mailbox」当成集成范围，旁路仍在。
2. 计划任务默认另开 Chat，但定义里可以指定已有 `chatId`；一旦指向主 Chat，就会和用户、分身回信抢出队。
3. `/api/execute`、渠道、语音今天多半直触 Query Runtime；Origin 分类与 hop 合同并不一致；是否进同一只箱子尚未拍板。
4. 有些操作「看着像投递」（submit、计划批准后的执行 Run、重启后重投、BTW/Explain），混进来源表会把收件合同搅乱。

---

## 现状（已核对）

### Origin 与准入

- 平台正式判定的 Origin 类型只有三种（`internal/contracts/queryinput/control.go`）：`user`、`agent`、`automation`。由平台赋予，请求体不可伪造。
- 准入在 `Origin == nil` 时补成 `user`（`internal/runtime/query/admission.go`）。因此未显式设 Origin 的路径（含 `/api/execute`、普通 `/api/query`、语音定稿 Query、渠道改写后的受信 query）都会落成 `user`。
- `ExecutionRole`：仅 `automation` 在模型输入侧标为自动化角色；其余按普通用户消息。
- [mailbox-next.md](mailbox-next.md) 阶段 D1 点名渠道将来用 origin `channel`；代码常量里尚无 `channel`，也无 execute / voice 独立类型。

### 跳数（docs/Mailbox.md）

- 用户亲口说、Automation、`/api/execute` 的输入为 **0 跳**。
- Agent 之间的 mail（含平台代生成的回信）记为发信 Run 当前跳数 + 1；上限 `mail.max-hops`，默认 4。
- 达到上限后 `chat_send` / `chat_notify` 返回 `mail_hop_limit`，请示不再上报；回信与 `chat_submit` 不受限。

### 今天 mailbox 何时真正投递（docs/Mailbox.md + 代码）

触发：发出时、Run 结束时、会话维护结束时、等待项清除时。对应实现侧有 `Kick`（发信/受理完成/人工重投、`Completed` 回信、`Notify`/`ask` 生成后、`RuntimeIdle`、`SetMaintenanceIdle`、submit 解除 awaiting 后等）。

- 重启时**不**对同一封信自动再投；未完成的信/回信挂起，等人重投或丢弃。
- 计划被批准时：先注册执行 Run，再把排队内容插入其中（续跑路径，不是新来源）。
- `mail.enabled` 默认关闭；关闭时 `chat_send` 等不可用，普通 `chat_start` / `chat_steer` / `/api/query` 等仍直触。

### 与主 Chat 进箱已有定论的交叉（见 mailbox-main-chat，不重复出队表）

- 集成完成后：主 Chat（及纳入范围）**无条件先入 mailbox**；用户输入、分身回信 / notify / 请示回流、其他 Agent 的 `chat_send`、以及**需对齐的 Automation / 渠道输入**，都进同一只箱。
- 「开启主要聊天」只改出队，不改是否进箱；用户消息与分身回信同一队列、一件一件；更正才允许 steer。
- 本文件不改写上述定论；execute、语音、出队优先级、origin 扩类等仍开放。

---

## 已能进会话、今天多半还不走 mailbox 的入口

| 入口 | 今天怎么进 Run | Origin / 可见性 | 忙时 | 与 hop |
|---|---|---|---|---|
| **用户 HTTP/WS `/api/query`、`/api/steer`** | 直触 Query Runtime | 空 → 准入标 `user` | 活动 Run / awaiting / 压缩 → 409 等 | 0 跳（用户输入） |
| **Automation**（cron、`POST /api/automation/trigger`、`automation_manage` 的 `trigger`） | dispatcher → Query Runtime；**绕过 mailbox** | 显式 `origin:{type:automation,automationId}`；省略 `query.hidden` 时有效 `true`（只藏 query 行，不藏 chat/run/回复） | 沿 Query 冲突语义 | 文档记为 0 跳 |
| **`/api/execute`** | 同步等待一次合格的 `result_submit`；新建或复用 `chatId` | 不设 Origin → 准入标 `user`（分类不准） | 同 Chat 有活动 Run → 409 | 文档与用户、Automation 同列 0 跳 |
| **渠道 / Gateway 导出** | 对端可对导出 Agent 做 query / steer / interrupt；HITL 经 `/api/submit`；本地 `channelConfig.exports.allow` 授权 | 改写后走受信 query，通常无 Origin → 落成 `user` | 与普通 Query 相同（忙则冲突/拒绝） | 未单独定义；将来 D1 拟改发信 |
| **实时语音** | 定稿后普通 `QueryCommand`（`planningMode:false`）；可带 `Primary` / `PrimaryIfMissing` 打到主 Chat | 不设 Origin → `user` | 他处活动 Run → `chat_busy` 等 | 未单独定义 |

补充核对：

- **Automation Chat**：首次创建的 chat `source` 为 `automation:<automationId>`；定义可带 `query.chatId` 写入**已有**会话，不会覆盖原 chat source。指定主 Chat 时与用户、回信同会话抢占。见 docs/自动化.md。
- **mailbox-next D1**：Automation 与渠道改为走阶段 C 的统一发信入口，origin 为 `automation` 或 `channel`，目标忙则排队不再拒绝；Automation 的初始权限与执行历史合同沿用现有源码，不套用 Agent 发信规则。
- **语音**：主聊合同写明语音暂不纳入主要聊天形态；语音不能代批审批（awaiting 只朗读提示，人在原界面 Submit）。见 [realtime-voice.md](realtime-voice.md)、mailbox-main-chat 主聊合同。

---

## 已按「信」设计、集成后应进同一只箱子的

四类 mail（docs/Mailbox.md）及用户侧统一发送（计划中）：

| 形态 | 产生方式 | 备注 |
|---|---|---|
| 用户输入（未来统一 send；含今天的 query / steer 语义） | 人 → 平台发信（mailbox-next 阶段 C；mailbox-main-chat 进箱） | hop 0；候选形态 letter + `ORIGIN_TYPE_=user` 等，见 mailbox-next 待定 |
| `letter` | `chat_send`（分身或其它 Agent） | 正式派活 / 补充；`started` 才授予发信方 Run 控制 |
| `reply` | Run 结束由平台生成（含投递失败回信） | 一 Run 只正式回一次；回信本身不再要求回信 |
| `notify` | `chat_notify` | 非正式；不产生回信 |
| `ask` | 分身卡在提问 / 审批 / 表单上时由平台生成 | 与等待项同生同灭；上级可用 `chat_submit` 代答 |

工具侧统一入口见 [chat-send-unify.md](chat-send-unify.md)：对外收敛为 `chat_send` 的 start / steer / submit 判定；出队落到 Run 时共用该合同。

---

## 看着像投递、最好先分开的

| 入口 | 为何先不算「新投递来源」 | 依据 |
|---|---|---|
| 人在界面上的 **`/api/submit`** | 继续当前 awaiting，不是新开一封信；Agent 侧才考虑由 `chat_send` 覆盖 submit | chat-send-unify；docs HITL / API |
| **计划批准后的执行 Run** | 沿用规划 Run 的 origin，链接 `planRunId`；是续跑，不是新进箱 | `awaiting_continuation.go` 注释与实现；Mailbox 投递规则 |
| **`agent_invoke` / `agent_delegate`** | 短时内联或 Team 成员调度，复用父/根 Run；后台派活才走 `chat_send` | docs/子智能体调度.md；mailbox-next / mailbox-main-chat |
| **Desktop BTW / Explain（顺便问、划词解释）** | 隐藏旁路：写 `<chatId>/.btw|/.explain/<sideId>.jsonl`，不写主 JSONL，不更新主 chat 摘要/未读/搜索 | docs/API与协议.md；真流式和 H2A |
| **中断、改权限、压缩** | 控制面或会话维护，不产生寄往 Chat 的新内容来源 | 现有 Run 控制与 compact 合同 |
| **重启后对同一封信人工重投** | 同一 mail 从挂起阶段继续，不是新来源；重启本身不自动投递 | docs/Mailbox.md |

---

## 主 Chat 箱子碰撞

### 已有定论（只引用，不重写）

见 [mailbox-main-chat.md](mailbox-main-chat.md)：

- 用户消息与分身回信：**同一队列，一件一件**。
- 主助理出队下：默认先留箱内；**更正类允许 steer**（经 mailbox，不是旁路直触）。
- 集成范围内取消「发送直触 Run、不进箱」的产品路径。

### 碰撞风险（事实）

- Automation 默认新建 `source=automation:<id>` 的 Chat，但 **`query.chatId` 可指向主 Chat**。
- `/api/execute`、渠道 query/steer、语音定稿均可打到**已有会话或主对话**（语音还可补主对话指针）。
- 若「寄到该 Chat 的全部都进」同一只箱子，上述来源与用户、回信、notify、ask 共享出队；若主 Chat 只收用户 + 分身回流，则计划任务 / execute 等需另有落点合同。

---

## 待拍板 / 开放疑问

下列问题本文件**不代答**。

| # | 问题 | 选项 / 说明 |
|---|---|---|
| Q1 | **进主 Chat 箱子的范围** | 「寄到该 Chat 的全部都进」（含 Automation / execute / 渠道 / 语音）；或主 Chat 只收用户 + 分身回流，计划任务与 execute 默认只能打到专用会话；或其它分层 |
| Q2 | **出队是否严格按进箱顺序** | FIFO；计划任务 / 渠道 / 回信可否插在用户前面；或用户永远优先；与「更正才 steer」如何叠加 |
| Q3 | **`/api/execute` 要不要进箱** | 调用方同步等 `result_submit`，与「先入队再出队」不同构；进箱后忙着排队时，对调用方是继续同步阻塞、改异步、还是禁止指向忙会话 / 主 Chat |
| Q4 | **Origin 要不要加类型** | `channel` 已在 mailbox-next D1 点名；execute、语音是继续冒充 `user`，还是单独标出（影响记忆采集、搜索导出、时间线是否隐藏、审计） |
| Q5 | **语音** | 进主 Chat 箱子但出队另算；或在主聊纳入语音之前继续直触 Query；与「语音暂不纳入主聊合同」如何衔接 |
| Q6 | **Automation 指定 `query.chatId=主 Chat`** | 允许并进同一箱排队；禁止指向主 Chat；或仅兼容出队下允许、主助理出队下拒绝 / 改新建 |
| Q7 | **渠道 HITL 的 `/api/submit`** | 始终保持「人/对端作答 awaiting」语义，不进箱；或与 Agent 侧 `chat_send` submit 落地对齐时另定边界 |
| Q8 | **内存阶段这些来源是否同一套队列实现** | 与 mailbox-main-chat 的 M1（仅主 Chat / 主+分身 / 全平台）一并定；本文件不提前选定 |

---

## 核对范围与未纳入项

已对照：`docs/Mailbox.md`、`docs/自动化.md`、`docs/智能体执行接口.md`、`docs/API与协议.md`、`docs/Gateway-Agent注册与调用协议.md`、`docs/子智能体调度.md`、`plans/mailbox-next.md`、`plans/mailbox-main-chat.md`、`plans/chat-send-unify.md`、`plans/realtime-voice.md`、`internal/contracts/queryinput/control.go`、`internal/runtime/query/admission.go`、`internal/automation/types.go`、`internal/voice/session.go`、`internal/runtime/query/mailbox.go` / `execute_result.go` / `awaiting_continuation.go`、`internal/mailbox/service.go`、`configs/channels.example.yml`。

本轮未发现清单之外、又真正构成「往 Chat 投递新内容」的独立产品入口。下列保持为控制或旁路，不升格为来源：`/api/interrupt`、access-level 切换、compact、resource/upload  alone、Desktop Action、connector 管理。若后续实现里出现新的受信 Query 入口且不设 Origin，默认仍会落入「空 → user」桶，应回到本表增补。

---

## 文档关系

- [mailbox-main-chat.md](mailbox-main-chat.md)：主 Chat 进箱、常开、内存、主要聊天出队；**未裁决前不改其正文。**
- [mailbox-next.md](mailbox-next.md)：既有阶段 A–D 与 `mail.db`；D1 渠道/Automation 发信；**D0 裁决前不改其正文。**
- [chat-send-unify.md](chat-send-unify.md)：工具侧 `chat_send` 与 start/steer/submit。
- [realtime-voice.md](realtime-voice.md)：语音级联与「不能代批」。
- [docs/Mailbox.md](../docs/Mailbox.md)：现行 mail 合同；内存阶段允许子集时，差异在实现时写明。
- 本文件：投递**来源盘点**与主 Chat 碰撞的开放疑问；不替代出队或 `chat_send` 合同。
