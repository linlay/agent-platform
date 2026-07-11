# 真流式和H2A

## 当前状态

`POST /api/query` 默认成功时返回 SSE event stream。服务端按 provider 原始流式 chunk 逐步映射为 `content.delta`、`tool.*`、`reasoning.*` 等事件，结束时写入 `data: [DONE]`。默认行为是逐事件 flush。请求体显式传 `stream:false` 时，服务端仍执行完整 run，但会聚合最终回答并返回普通 JSON，默认 `data` 只包含 `content`；可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`。错拼字段 `steam` 不会触发非流式。

H2A render 是传输层缓冲能力，用于控制前端渲染节奏。当前服务默认禁用 H2A 缓冲并逐事件 flush，heartbeat 透传；这些是源码内部默认值，不提供 runtime YAML 配置。

## 核心流程

```text
HTTP query
  -> register run
  -> stream writer
  -> chat.start(仅新建 chat) / request.query / run.start
  -> provider chunks -> stream events
  -> content.snapshot / run.complete
  -> chat 持久化
  -> [DONE]
```

`GET /api/attach?runId=...&agentKey=...&lastSeq=...` 用于续接 Agent-owned run；orchestrated Team 改传 `teamId`。服务端按公开 owner 校验归属；run 超过 retention 或序号已过期时返回 `SEQ_EXPIRED`。

从 `/api/chat` 冷启动恢复 active run 时，客户端应使用 `activeRun.lastSeq` 作为 attach 游标。该值来自本次 chat detail 已返回历史 events 的 `liveSeq` 覆盖边界；`liveSeq` 由 `chatId.jsonl` 每行顶层字段 replay 注入，不是内存 event bus 的最新 seq。

WebSocket 客户端切换 current chat 时，应对旧 chat 的 active run 发送 `/api/detach`，关闭当前 WS 连接上的 live stream observer；新 chat 打开后再按需 `/api/attach`。detach 只释放 UI 订阅流，不中断后台 run，也不会暂停 HITL / awaiting timeout。HTTP/SSE 不新增 detach endpoint，仍由客户端关闭 EventSource 或 fetch stream。

## 配置与接口

- `POST /api/query`：发起 run，默认返回 SSE；`stream:false` 返回 JSON。
- `GET /api/attach`：按 `runId + (agentKey | teamId) + lastSeq` 续接 backlog。
- WS `/api/detach`：按 `runId + (agentKey | teamId)` 关闭当前连接上的 run observer。
- SSE heartbeat 固定为 30 秒。
- H2A render 默认值在 `internal/stream/defaults.go`，默认不缓冲、heartbeat 透传。

## 约束与注意事项

- 现场看起来不像真流式时，优先检查代理、浏览器、网关或调用方是否缓冲；runtime YAML 不再提供 `h2a.render.*`。
- SSE 事件名统一为 `message`，业务事件类型写在 payload 内。
- `[DONE]` 是传输结束帧，不是业务 JSON 事件。
- attach 只能续接仍在 retention 范围内的 run backlog。

## 相关文件

- `internal/stream/sse.go`
- `internal/stream/event_bus.go`
- `internal/server/handler_query.go`
- `internal/server/handler_run_stream_test.go`
- `internal/chat/events_writer.go`
- `docs/API与协议.md`
