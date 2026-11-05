# 主 Chat 的 Mailbox 集成与主要聊天出队

与 [chat-send-unify.md](chat-send-unify.md)、[mailbox-next.md](mailbox-next.md) 交叉。本计划管：

1. 主 Chat（及集成范围内的 Chat）**无条件**经 mailbox 收件
2. Mailbox **将来常开**（不做长期「开/关 mailbox」产品双开关）
3. **先内存**实现，是否持久化另定
4. 「开启 / 不开启主要聊天」只改变 **mailbox 对主 Chat 的出队行为**，不改变是否进箱
5. 相关疑问与待拍板

`chat_send` 的 start / steer / submit 判定合同见 [chat-send-unify.md](chat-send-unify.md)；本文件出队时调用同一套判定。不写工作量。

既有 `mail.db` / `mail.enabled` / mailbox-next 阶段 A–D 作历史与并行参考；**集成主线以本文件为准**（与 mailbox-next 的主从见待拍板 D0）。

## 要解决的问题

1. 主 Chat 若部分路径直触 Run、部分走信，旁路永久存在，mailbox 嵌不干净。
2. 「上 mailbox」与「丝滑主要聊天」若绑成一次大爆炸，集成风险高。
3. 现有持久化 mail.db、重启挂起、默认关闭等合同偏重；集成期需要更轻的第一步（内存）。

## 已定需求

### Mailbox：将来常开；主 Chat 无条件进箱

- **不存在长期的「mailbox 开 / 关」两套产品开关。** 集成完成后，主 Chat（及派活子 Chat 等纳入范围）**永远开着**：凡寄到该 Chat 的消息，**无条件先入 mailbox**，再出队落到 Run。
- 主 Chat 取消直触旁路（集成完成后）：用户输入、分身回信 / notify / 请示回流、其他 Agent 的 `chat_send`、以及需对齐的 Automation / 渠道输入，都进同一只箱。
- 「开启主要聊天」或「不开启」**不决定是否进箱**，只决定 **出队策略**（见下）。

### 先内存，后决定是否持久化

- 集成本期的 mailbox **先做在内存里**（进程内队列 / 投递状态即可）。
- **是否落库、是否沿用或替换现有 `mail.db`、重启后是否挂起重投，后续再定。**
- 内存阶段可接受的后果（默认态度见待拍板）：进程重启队列丢失；无跨进程对账；mailbox-next 里「重启挂起必须人处理」等合同暂不适用或另定。

### 主要聊天：同一 mailbox，两套出队（不是第二套 mailbox 开关）

「主要聊天」是 **主 Chat 上的产品 / 出队形态**，不是第二个 mailbox 使能开关。

#### 未开启主要聊天（兼容出队）

管道已是 mailbox，**倒法尽量像旧直触**：

| 主 Chat 状态 | 出队 |
|---|---|
| 空闲 | 立刻出队 → start |
| 在跑、非 awaiting | 立刻出队 → steer |
| awaiting | 能结构化则 submit；否则见待拍板 |
| 分身回信等 | 同样按上表尽快落地 |
| 队列 | 尽量不堆；仅瞬时冲突短排队 |
| UI | 可接近「发送即进当前对话」 |

用途：先把 mailbox 嵌进主 Chat，前端可先只打统一发送，而不必先接受新前台节奏。

#### 开启主要聊天（主助理出队）

同一进箱，**换节奏**：

| 主 Chat 状态 | 出队 |
|---|---|
| 空闲 | 出队 → start（主 Chat 低思考等） |
| 在跑 | **默认先留在箱内排队**；当前 Run 尽快结束后再按件出队；**更正类允许 steer**（经 mailbox，不是旁路直触） |
| awaiting | submit 继续；用户确认可长期等待 |
| 用户输入与分身回信 | **同一队列，一件一件** |
| UI | 发送即可显示已接收（已进箱）；排队态可另露 |

### 与 chat send / 子 Chat

- 出队落到 Run 时，一律按 [chat-send-unify.md](chat-send-unify.md) 的 start / steer / submit 判定。
- 子 Chat 经 mailbox 与直触的对比、可先改工具入口，见该文件；子 Chat 回信进入**主 Chat 的 mailbox** 再按本文件出队。

### 主聊合同（已定方向，挂在「开启主要聊天」出队上）

- 主 Chat：最强模型 + 低思考；分身由主 Chat 主动给出上下文；确认可永久等；语音暂不纳入本主要聊天合同；需要能主动探测分身进度（只读，不借用 steer / notify 灌进度）。
- 双强度不拆台：思考强度跟 Chat；仅 `newChat` 带新 `reasoningEffort`。
- `agent_invoke` 仍可作短时内联；后台派活统一 `chat_send`。

### 分层（帮助理解；不是两个 mailbox 开关）

| 层 | 含义 | 常开后 |
|---|---|---|
| 收件 | 消息是否进 mailbox | 主 Chat（及集成范围）**无条件进箱** |
| 出队 | 箱内如何变成 start/steer/submit | 由「是否开启主要聊天」选兼容出队或主助理出队 |

集成顺序意图：**先收件常开（内存）+ 默认兼容出队 → 再打开主要聊天出队。** 全程不是「先关后开 mailbox」。

## 待拍板 / 疑问

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
| I3 | 兼容期是否还允许任何直触 Run | 不允许；或仅内部测试旁路（非产品开关） |
| I4 | 用户侧统一发送入口名称与协议 | 原拟 `POST /api/send` 等；HTTP 与主 WS 同一套逻辑 |

### 主要聊天出队

| # | 问题 | 选项 / 说明 |
|---|---|---|
| P1 | 「开启主要聊天」的配置挂在哪 | Agent；Workspace；Platform 全局形态；或 WebClient + 服务端出队策略键 |
| P2 | 兼容出队下，分身回信碰上主 Chat 在跑 | 立刻 steer；还是短排队 |
| P3 | 主助理出队下，何种信可立刻 steer（更正） | 显式标志；仅用户来源；工具参数；模型不可自助插队 |
| P4 | 主助理出队下，awaiting 时用户又发普通句 | 优先 submit；进队排在确认后；当 steer |
| P5 | 排队中的用户消息在 UI 是否展示全文 | 展示；只显示「已收 N 条」；可取消单条 |
| P6 | 主 Chat 低思考 / 分身高思考的默认与可否配置 | 已定方向；具体默认值 |
| P7 | 主动探测分身进度的 API/工具形状 | 另列最小只读接口 |

### 与 mailbox-next

| # | 问题 | 选项 / 说明 |
|---|---|---|
| D0 | 本文件与 mailbox-next 的主从 | 集成主线以本文件为准，mailbox-next 改文末指向；或 mailbox-next 只保留 db 迁移与收件箱 UI，出队/主聊以本文件为准 |

## 验收方向（行为，非工作量）

1. **收件：** 主 Chat 在集成范围内不存在「发送直触 Run、不进箱」的产品路径。
2. **内存：** 单进程内用户发送、分身回信、出队判定可跑通；重启行为符合 M2 的选定。
3. **出队形态：** 同一进箱，切换「未开 / 开启主要聊天」时，仅出队策略变，进箱不变。
4. **与 chat_send：** 出队落地符合 chat-send-unify 的 start / steer / submit 合同。

## 文档关系

- [chat-send-unify.md](chat-send-unify.md)：工具入口与三态判定、子 Chat 对比。
- 本文件：主 Chat 进箱、常开、内存、主要聊天出队差异。
- [mailbox-next.md](mailbox-next.md)：既有阶段与 `mail.db`；冲突时以 D0 裁决后改指向，**未裁决前不改写其正文。**
- [docs/Mailbox.md](../docs/Mailbox.md)：现行实现合同；内存阶段允许子集，差异在实现时写明。
