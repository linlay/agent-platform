# Mailbox 集成、chat send 统一与主要聊天

一份计划，覆盖：

1. `chat_send` 统一入口（自动 start / steer / submit）
2. 子 Chat 挂 mailbox 的行为对比（可先改）
3. 主 Chat **无条件**经 mailbox 收件；Mailbox **将来常开**（不做长期双开关）
4. 先内存实现 mailbox，是否持久化另定
5. 「开启 / 不开启主要聊天」只改变 **mailbox 对主 Chat 的出队行为**，不改变「是否进箱」
6. 疑问与待拍板

与 [mailbox-next.md](mailbox-next.md) 交叉：既有 `mail.db` / `mail.enabled` / 阶段 A–D 仍作历史与并行参考；**本文件以「常开 + 先内存 + 主 Chat 无条件进箱 + 主要聊天改出队」为集成主线。** 不写工作量。

---

## 要解决的问题

1. 调用方必须在 `chat_start` / `chat_steer` / submit 间自选，易冲突。
2. 主 Chat 若部分路径直触 Run、部分走信，旁路永久存在，mailbox 嵌不干净。
3. 「上 mailbox」与「丝滑主要聊天」若绑成一次大爆炸，集成风险高。
4. 现有持久化 mail.db、重启挂起、默认关闭等合同偏重；集成期需要更轻的第一步。

---

## 已定需求

### A. `chat_send` 统一入口

- 工具侧对外只保留 **`chat_send`** 作为向目标 Chat 投递的主入口。
- **不再对外支持** `chat_start`、`chat_steer`（可删或仅内部使用）。
- 一次 `chat_send` 须能落地三类能力，由**投递侧按目标 Chat 状态自动判定**，调用方不选三个工具：

| 目标 Chat 状态 | 落地 |
|---|---|
| 无 ActiveRun | **start**（开新 Run） |
| 有 ActiveRun，非需作答的 awaiting | **steer**（插入当前 Run；含主 Chat 对子 Chat 的更正） |
| 有 ActiveRun，且存在可作答的 awaiting | **submit**（继续该 awaiting；形状对齐现有 submit） |

- `newChat: true` 只表示先开新 Chat 再投，不是 start/steer/submit 三选一。
- `chat_notify` 仍是即时消息，不承担上述三类落地。
- 人的 Web `/api/submit`、主视图直接审批等不在「工具入口合并」里被替换；Agent 侧投递以 `chat_send` 为准。
- 现有 `chat_submit` 与 `chat_send` 的 submit 能力如何收束 → 见待拍板。

### B. Mailbox：将来常开；主 Chat 无条件进箱

- **不存在「mailbox 开 / 关」两套长期开关。** Mailbox 集成完成后对主 Chat（及派活子 Chat）**永远开着**：凡寄到该 Chat 的消息，**无条件先入 mailbox**，再出队落到 Run。
- 主 Chat 取消直触旁路（集成完成后）：用户输入、分身回信 / notify / 请示回流、其他 Agent 的 `chat_send`、以及需对齐的 Automation / 渠道输入，都进同一只箱。
- 「开启主要聊天」或「不开启」**不决定是否进箱**，只决定 **出队策略**（见 D）。

### C. 先内存，后决定是否持久化

- 集成本期的 mailbox **先做在内存里**（进程内队列 / 投递状态即可）。
- **是否落库、是否沿用或替换现有 `mail.db`、重启后是否挂起重投，后续再定。**
- 内存阶段可接受的后果（需在待拍板里确认默认态度）：进程重启队列丢失；无跨进程对账；与 mailbox-next 里「重启挂起必须人处理」等合同暂不适用或另定。

### D. 主要聊天：同一 mailbox，两套出队（不是第二套 mailbox 开关）

「主要聊天」是 **主 Chat 上的产品 / 出队形态**，不是第二个 mailbox 使能开关。

#### D1. 未开启主要聊天（兼容出队）

管道已是 mailbox，**倒法尽量像旧直触**：

| 主 Chat 状态 | 出队 |
|---|---|
| 空闲 | 立刻出队 → start |
| 在跑、非 awaiting | 立刻出队 → steer |
| awaiting | 能结构化则 submit；否则见待拍板 |
| 分身回信等 | 同样按上表尽快落地 |
| 队列 | 尽量不堆；仅瞬时冲突短排队 |
| UI | 可接近「发送即进当前对话」 |

用途：先把 mailbox 嵌进主 Chat，前端 / 工具可先只打统一发送，而不必先接受新前台节奏。

#### D2. 开启主要聊天（主助理出队）

同一进箱，**换节奏**：

| 主 Chat 状态 | 出队 |
|---|---|
| 空闲 | 出队 → start（主 Chat 低思考等合同另见） |
| 在跑 | **默认先留在箱内排队**；当前 Run 尽快结束后再按件出队；**更正类允许 steer**（经 mailbox，不是旁路直触） |
| awaiting | submit 继续；用户确认可长期等待 |
| 用户输入与分身回信 | **同一队列，一件一件** |
| UI | 发送即可显示已接收（已进箱）；排队态可另露 |

### E. 子 Chat：开 mailbox 与直触对比（保留；支持先改）

前提：子 Chat 少被用户直聊；主 Chat 用 `chat_send` 派活 / 更正；干完回主 Chat。

| 场景 | 旧直触 Run | 经 mailbox（内存或日后持久化） |
|---|---|---|
| 空闲派活 | start | 进箱后立刻出队 start → 体感同 |
| 在跑更正 | steer | 进箱后判定 steer → 体感同（多一跳） |
| awaiting 继续 | 另走 submit | `chat_send` 判定 submit |
| 子 Chat 内部怎么跑 | 不变 | 不变 |
| 干完回主 Chat | wait/status 等，回流弱 | 回信进**主 Chat 的 mailbox** 再出队 |
| 忙时又来一封 | 再 steer 或 start 冲突 | 入队或按判定 steer |
| 重启 | 调用方自重试 | 内存阶段：可能丢；持久化后另定 |

收束：子 Chat 干活姿势几乎不变；差别在回流、排队与日后持久化。**子 Chat 路径可与 `chat_send` 统一一起先改。**

### F. 与既有合同的关系（已定方向）

- 主 Chat：最强模型 + 低思考；分身由主 Chat 主动给出上下文；确认可永久等；语音暂不纳入本主要聊天合同；需要能主动探测分身进度（只读，不借用 steer / notify 灌进度）。
- 双强度不拆台：思考强度跟 Chat；仅 `newChat` 带新 `reasoningEffort`。
- `agent_invoke` 仍可作短时内联；后台派活统一 `chat_send`。
- 本计划 **不**再保留「mailbox 关着走直触、开着走信」的长期产品双模式。

---

## 分层（帮助理解；不是两个 mailbox 开关）

| 层 | 含义 | 常开后 |
|---|---|---|
| 收件 | 消息是否进 mailbox | 主 Chat（及集成范围内的 Chat）**无条件进箱** |
| 出队 | 箱内如何变成 start/steer/submit | 由「是否开启主要聊天」选 D1 或 D2 |

集成顺序意图：**先收件常开（内存）+ `chat_send` 三态判定 + 默认 D1 兼容出队 → 再打开 D2 主要聊天出队。** 全程不是「先关后开 mailbox」。

---

## 待拍板 / 疑问

### `chat_send` / submit

| # | 问题 | 选项 / 说明 |
|---|---|---|
| S1 | awaiting 多条时作答绑谁 | 显式 `awaitingId`；或仅一条时可省略 |
| S2 | 只给自然语言、无 submit 结构化字段 | 拒绝；或降级 steer（可能解不开 awaiting） |
| S3 | `chat_submit` 工具 | 收作 `chat_send` 的别名并废弃；或删除 |
| S4 | 空闲却带了 `awaitingId` | 拒绝；或忽略后 start |
| S5 | 在跑非 awaiting 却带了 submit 形状 | 拒绝；或降级 steer |

### 内存 mailbox

| # | 问题 | 选项 / 说明 |
|---|---|---|
| M1 | 内存队列的范围 | 仅主 Chat；主 + 其分身子 Chat；全平台所有 Chat |
| M2 | 进程重启 | 丢弃并打日志；启动时提示；尽快上持久化前暂不保证 |
| M3 | 与现有 `mail.db` / `mail.enabled` | 新路径完全旁路旧库；或内存为前端、旧库停用；或日后内存语义迁入/替换 db |
| M4 | 跳数、权限冻结、回信 letter 合同 | 内存阶段做精简子集，还是行为对齐 docs/Mailbox.md |
| M5 | 幂等键 | 仍 `runId+toolId` 等；进程内去重即可还是要稳定跨重启 |

### 主 Chat 收件与旁路

| # | 问题 | 选项 / 说明 |
|---|---|---|
| I1 | `/api/query`、`/api/steer` | 内部改写入箱；还是对外废弃只留统一 send；Desktop/gateway/execute/渠道谁先切 |
| I2 | 「无条件进箱」是否含 notify / ask | 是全部；或 letter/用户输入先，notify 仍旁路 |
| I3 | 兼容期是否还允许任何直触 Run | 不允许；或仅内部测试开关（非产品开关） |

### 主要聊天出队

| # | 问题 | 选项 / 说明 |
|---|---|---|
| P1 | 「开启主要聊天」的配置挂在哪 | Agent；Workspace；Platform 全局形态；或仅 WebClient 表现 + 服务端出队策略键 |
| P2 | D1 下分身回信碰上主 Chat 在跑 | 立刻 steer；还是短排队 |
| P3 | D2 下何种信可立刻 steer（更正） | 显式标志；仅用户来源；工具参数；模型不可自助插队 |
| P4 | D2 下 awaiting 时用户又发普通句 | 优先 submit；进队排在确认后；当 steer |
| P5 | 排队中的用户消息在 UI 是否展示全文 | 展示；只显示「已收 N 条」；可取消单条 |
| P6 | 主 Chat 低思考 / 分身高思考 | 已定方向；具体默认值与是否可配置 |
| P7 | 主动探测分身进度的 API/工具形状 | 另列最小只读接口 |

### 与 mailbox-next 文档

| # | 问题 | 选项 / 说明 |
|---|---|---|
| D0 | 本文件与 mailbox-next 的主从 | 集成主线以本文件为准，mailbox-next 归档/改指向；或 mailbox-next 只保留 db 迁移与收件箱 UI，出队/主聊以本文件为准 |

---

## 验收方向（行为，非工作量）

1. **`chat_send`：** 空闲 → 新 Run；在跑非 awaiting → steer；awaiting → 解除等待并继续；手册不再教 start/steer 二选一。
2. **收件：** 主 Chat 在集成范围内不存在「发送直触 Run、不进箱」的产品路径。
3. **内存：** 单进程内派活、更正、回信、用户发送可完成出队判定；重启行为符合 M2 的选定。
4. **出队形态：** 同一进箱，切换「未开 / 开启主要聊天」时，仅出队策略在 D1/D2 间变，进箱不变。
5. **子 Chat：** 经 `chat_send` 派活/更正/submit 与旧 start/steer/submit 体感接近；回信进入主 Chat 箱再出队。

---

## 文档关系

- 本文件：Mailbox 集成主线 + `chat_send` + 主要聊天出队差异 + 疑问。
- [mailbox-next.md](mailbox-next.md)：既有阶段 A–D、`mail.db`、收件箱 UI 等；与本文件冲突时以待拍板 D0 裁决后改文末指向，**不在未裁决前改写其正文。**
- [docs/Mailbox.md](../docs/Mailbox.md)：现行实现合同；内存阶段允许子集，差异在实现时写明。
