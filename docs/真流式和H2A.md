# 真流式和H2A

## 当前状态

`POST /api/query` 成功时返回 SSE event stream。服务端按 provider 原始流式 chunk 逐步映射为 `content.delta`、`tool.*`、`reasoning.*` 等事件，结束时写入 `data: [DONE]`。默认行为是逐事件 flush。

H2A render 是传输层缓冲能力，用于控制前端渲染节奏。启用相关缓冲参数后，客户端看到的输出可能不再表现为逐 token 或逐事件抵达。

## 核心流程

```text
HTTP query
  -> register run
  -> stream writer
  -> request.query / chat.start / run.start
  -> provider chunks -> stream events
  -> content.snapshot / run.complete
  -> chat 持久化
  -> [DONE]
```

`GET /api/attach?runId=...&agentKey=...&lastSeq=...` 用于续接已注册 run 的事件流。服务端会校验 `agentKey` 与 run 归属；run 超过 retention 或序号已过期时返回 `SEQ_EXPIRED`。

## 配置与接口

- `POST /api/query`：发起 run，返回 SSE。
- `GET /api/attach`：按 `runId + agentKey + lastSeq` 续接 backlog。
- `AGENT_SSE_HEARTBEAT_INTERVAL_MS`：SSE heartbeat 间隔。
- `AGENT_H2A_RENDER_FLUSH_INTERVAL_MS`：H2A 定时 flush。
- `AGENT_H2A_RENDER_MAX_BUFFERED_CHARS`：最大缓冲字符数。
- `AGENT_H2A_RENDER_MAX_BUFFERED_EVENTS`：最大缓冲事件数。
- `AGENT_H2A_RENDER_HEARTBEAT_PASS_THROUGH`：是否透传 heartbeat。

默认值统一见 [配置化说明](配置化说明.md)。

## 约束与注意事项

- 现场看起来不像真流式时，优先检查 `AGENT_H2A_RENDER_*` 是否启用了缓冲。
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
