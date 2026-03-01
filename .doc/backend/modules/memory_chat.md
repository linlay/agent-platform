# 聊天记忆与历史（memory_chat）

## 关键类
- `ChatWindowMemoryStore`
- `ChatRecordStore`

## ChatWindowMemoryStore
- 用于模型上下文窗口回放（最近 K 个 run）。
- 按 `query/step` 行写入 JSONL。
- 读取时排除 `reasoning_content`，避免污染下一轮上下文。

## ChatRecordStore
- 维护 sqlite `CHATS` 会话索引。
- 持久化关键事件（`request.query`, `run.start`, `run.complete` 等）。
- `GET /chat` 时把 JSONL 重建为 `events + rawMessages`。

## 回放规则
- 历史返回以 snapshot 事件为主：`reasoning.snapshot/content.snapshot/tool.snapshot/action.snapshot`。
- `chat.start` 只在会话历史中保留一次。
- 每个 run 保留 `run.complete`。

## 已读状态
- 运行完成时设 `readStatus=0`。
- `POST /read` 后更新为 `readStatus=1` 并记录 `readAt`。
