# 智能体站内信（Agent 间消息投递）

## 目标

让一个 Agent 能给另一个 Agent 投递消息：目标 Agent 收到后在自己的会话里处理，结果可自动回传给发送方，全程可追溯、可审批、防失控。

## 现状

- `builtin.task-control` 已支持同步投递：`chat_start` 在目标 Agent 会话中起 Run，`wait` 等其结束，`chat_get_status` 取回结果；准入、审批、幂等、`origin` 记录均已具备。
- 缺口：
  - 发送方 Run 结束后结果无法回传；
  - 固定一层（`callerOrigin` 返回 `run_chaining_not_allowed`），接收方不能转发或回复；
  - 目标会话忙时返回 `active_run_conflict`，没有排队；
  - 定时任务、外部渠道触发的 Run 不能投递；
  - 发起方信息只在内存，重启即丢；
  - WebClient 不显示“来自哪个 Agent”。

## 规划

1. **基线验证（不改代码）**：用两个 GENERAL Agent 走通 `chat_start → wait → chat_get_status`，必要时写进 prompt 或 skill。
2. **站内信工具 `agent_message_send`**：参数 `agentKey`、`message`、`chatId?`、`reply: callback|none`、`correlationId?`；返回 `messageId`、`status: started|queued`、`chatId`、`runId?`。
3. **收件箱 `internal/agentmail`**：消息落盘（含 from/to、hop、status、correlationId），目标空闲即投递，忙则入队，重启可恢复。
4. **结束回调与回传**：新增 Run 结束监听；接收方完成后，把结果投回发送方会话（发送方仍在运行则注入，已结束则新起一个 Run）。
5. **防失控**：跳数上限（默认 2）、`agent.yml` 目标白名单、单链预算与频率限制、`messageId` 去重、回传不再要求回传、禁止自发自收；权限沿用现有审批。
6. **前端展示**：接收方会话显示“来自 A”，发送方会话显示回传并可跳转；WS 增加 `agent.message.*` 通知。

## 待定

- 最大跳数；
- 回传方式：注入运行中的 Run，还是总是新起一个 Run；
- 白名单放在 `agent.yml` 还是全局配置；
- 打破 AGENTS.md 中“固定一层”的约定需同步改文档。
