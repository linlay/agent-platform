# chat send 统一入口（可先于主聊天模式改造）

与 [mailbox-next.md](mailbox-next.md) 交叉，但不替代它。本计划只收敛 **Agent/工具侧** 对目标 Chat 的投递入口：对外不再区分 `chat_start` / `chat_steer`，并由同一入口覆盖 awaiting 上的继续（submit）。用户 HTTP/WS 统一发送、主要聊天形态仍见 mailbox-next 阶段 C/D。

本文件记录问题边界、已定需求、行为对比与待拍板项；不写工作量。

## 要解决的问题

1. 调用方必须自己判断目标 Chat 空闲还是在跑，才能选 `chat_start` 或 `chat_steer`；选错就冲突或拒收。
2. 目标 Chat 停在 awaiting（审批 / 提问 / 表单 / 规划等）时，今天要另走 `chat_submit`（或 `/api/submit`），与 start/steer 入口分裂。
3. mailbox 对「干活的子 Chat」在开跑与中途更正上体感接近直触 Run，但回信、排队、重启重投依赖 mailbox；希望先把入口统一，并允许子 Chat 路径挂上 mailbox，而不必等主聊天模式整包完成。

## 已定需求（目标功能）

### 对外入口

- 工具侧对外只保留 **`chat_send`** 作为「向某 Chat 投递」的主入口。
- **不再支持** 对外暴露的 `chat_start`、`chat_steer`（删除或降为内部实现，不对工具表 / 手册开放）。
- `chat_send` 必须能覆盖三类落地能力：
  1. **start**：目标 Chat 无 ActiveRun 时，开新 Run（query start）。
  2. **steer**：目标 Chat 已有进行中的 Run 时，把内容 steer 进该 Run（含主 Chat 发现需要更正子 Chat 的场景）。
  3. **submit**：目标 Chat / 其 ActiveRun 正处于 awaiting 时，用本次投递 **继续该 awaiting**（形状对齐现有 submit：`runId` + `awaitingId` + `param`/`params` 语义），而不是再开一个 start，也不是当成普通 steer 文本插话。

### 自动判定（投递侧，不由调用方选 start/steer/submit 工具）

调用方只表达「寄到哪个 Chat、带什么内容 / 作答」；由平台根据目标 Chat 当前状态选择落地方式：

| 目标 Chat 状态 | `chat_send` 落地 |
|---|---|
| 无 ActiveRun | start：开新 Run |
| 有 ActiveRun，且不在需作答的 awaiting | steer：插入当前 Run |
| 有 ActiveRun，且存在可作答的 awaiting | submit：继续该 awaiting |

- 「主 Chat 通过 mailbox 寄过去再更正」是允许的：信先进入投递，投递时若目标正在跑，则 steer 进当前 Run。
- `newChat: true` 仍只表示「先开新 Chat 再投」，不是 start/steer/submit 三选一；思考强度等仅在全新 Chat 上带新参数（与主聊天合同一致时另见讨论记录）。

### 与 mailbox 的关系（本计划可先做的部分）

- 对 **干活的子 Chat**：开不开 mailbox，在「空闲开跑 / 忙碌更正」上几乎同 start / steer；差别主要在回信自动回主 Chat、多封信排队、重启挂起重投、跳数与权限冻结。
- 因此：**可以先统一 `chat_send`，并让子 Chat 路径走 mailbox**，不必等主聊天「用户输入也进 mailbox」整包完成；子 Chat 侧加 mailbox 对干活姿势影响小，却换齐回信与排队合同。
- 已有 `mail.enabled`（见 docs/Mailbox.md）：关闭时邮件类工具不可用、直触 `chat_start`/`chat_steer` 仍可用。本计划落地后，关闭态下 `chat_send` 仍应能用内部映射完成 start/steer/submit，避免工具表倒退；开启态下投递经 mailbox，判定规则同上。

### 子 Chat：开 / 关 mailbox 行为对比（保留）

前提：子 Chat 很少被用户直接聊；主 Chat 用 `chat_send` 派活与更正；干完要回到主 Chat。

| 场景 | 关 mailbox（直触 Run） | 开 mailbox |
|---|---|---|
| 子 Chat 空闲，主 Chat 派一单活 | 直接 start | 先入队再出队 start → 体感同 start |
| 子 Chat 正在跑，主 Chat 要更正 | 直接 steer | 经 mailbox 投递，有 ActiveRun → steer → 体感同 steer（多一跳投递） |
| 子 Chat 停在 awaiting，主 Chat 代答 / 继续 | 走 `chat_submit` 或 `/api/submit` | 经 `chat_send` 判定为 submit（或等价投递后 submit） |
| 子 Chat 内部模型 / 工具怎么跑 | 不变 | 不变 |
| 干完怎么回主 Chat | `wait` + `chat_get_status` 等，无统一自动回信唤醒 | 回信 letter 自动投主 Chat 并触发 |
| 忙着又来第二封（追问 / 再派） | 再 steer 或第二次 start 冲突 | 入队；更正类投递时可 steer，其余按件处理 |
| 进程重启时已派出未落地的指令 | 多半丢，调用方自重试 | mail.db 可挂起 / 重投 |
| 跳数 / 发信权限冻结 | 无 | 有（跨 Agent 时明显） |

收束：子 Chat 内部「开跑 / 被更正 / 被继续 awaiting」与直触接近；对主 Chat 的关系上，开 mailbox 才有自动回信、排队与重启可重投。子 Chat 建议与 mailbox 投递一起用同一 `chat_send` 合同，而不是为子 Chat 单独关 mailbox。

## 与现有能力的边界

- 现有独立工具 **`chat_submit`**（代答请示）与 `/api/submit`（人的作答）已存在；本计划要求 **`chat_send` 具备 submit 能力**。`chat_submit` 是保留为薄封装、标记废弃，还是并入后删除，见待拍板。
- 人在 WebClient / Desktop 上的直接 `/api/submit`、主视图审批（mailbox-next B4）不在本计划替换范围内；本计划管的是 **Agent 侧统一投递入口**。
- `chat_notify` 仍是即时消息，不承担 start/steer/submit。
- 进度只读探测（若需要）另做，不借用 steer，也不用 notify 塞进度进主时间线。
- mailbox-next 阶段 C（用户消息走 mailbox）、阶段 D2（删除 start/steer）与本计划对齐方向；本计划可先完成工具侧收敛与子 Chat 投递，不阻塞、也不提前完成「用户 query 一律进 mailbox」。

## 待拍板

| # | 问题 | 选项 / 说明 |
|---|---|---|
| 1 | awaiting 有多条时，`chat_send` 作答绑哪一条 | 显式带 `awaitingId`（与现 submit 一致）；或仅当恰好一条时可省略 |
| 2 | 调用方只给了自然语言、未给 submit 结构化 `param`/`params` 时 | 拒绝并提示走结构化作答；或允许当 steer 文本插入（可能无法解除 awaiting） |
| 3 | `chat_submit` 工具 | 保留为调用 `chat_send` submit 落地的别名并文档废弃；或本计划一并删除 |
| 4 | `mail.enabled=false` 时 | `chat_send` 内部直触 start/steer/submit；或禁用 `chat_send` 仍逼用旧工具（与「对外只留 chat_send」冲突，一般不取） |
| 5 | 目标空闲但调用方带了 `awaitingId` | 拒绝；或忽略 awaiting 字段改 start |
| 6 | 目标在跑且非 awaiting，但调用方带了 submit 形状 | 拒绝；或降级为 steer（易误用） |
| 7 | 与 mailbox-next「统一用户发送入口」的命名 | 本计划只管工具 `chat_send`；HTTP 入口名称仍按 mailbox-next 待拍板 4 另定 |

## 验收方向（行为，非工作量）

- 同一 `chat_send` 调用：空闲 → 新 Run；在跑非 awaiting → 当前 Run 出现 steer；awaiting → 等待项解除并继续，不新开并行主 Run。
- 工具表与 task-control 手册不再教调用方在 start/steer 间二选一；手册写清自动判定与 submit 所需字段。
- 子 Chat：`mail.enabled` 打开后，派活 / 更正 / 代答经 `chat_send` 可完成；回信能唤醒主 Chat（在 mailbox 已具备回信合同的前提下）。
- 关闭 `mail.enabled` 时，`chat_send` 仍能完成三类落地，不出现「只能用已删除的 chat_start」的空洞。
