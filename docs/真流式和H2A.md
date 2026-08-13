# 真流式和H2A

## 当前状态

`POST /api/query` 默认成功时返回 SSE event stream。服务端按 provider 原始流式 chunk 逐步映射为 `content.delta`、`tool.*`、`reasoning.*` 等事件，结束时写入 `data: [DONE]`。默认行为是逐事件 flush。请求体显式传 `stream:false` 时，服务端仍执行完整 run，但会聚合最终回答并返回普通 JSON，默认 `data` 只包含 `content`；可用 `includeUsage:true` / `includeFullText:true` 追加 `usage` / `fullText`。错拼字段 `steam` 不会触发非流式。

Native Agent 与 orchestrated Team 的 live `seq` 是连续的公开事件游标：只有实际发布到 SSE / WebSocket / EventBus 的事件才递增。`llm.request`、除 `usage.snapshot` 外的内部 `*.snapshot`、system-init query、内部 tool result 和 `clientVisible:false` 工具生命周期不占用公开序号。PROXY / CHANNEL 仍保留上游序号语义。

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

从 `/api/chat` 冷启动恢复 active run 时，客户端应使用 `activeRun.lastSeq` 作为 attach 游标。该值来自本次 chat detail 已返回历史 events 的 `liveSeq` 覆盖边界；对于新的 Native / Team run，`liveSeq` 记录对应 JSONL 行处理完成时最近一个已发布的公开序号，内部事件可以复用相同覆盖边界但不能推进它。历史 run 保留原有 `liveSeq`，不迁移。Platform 重启后可恢复的 question/planning 会以原 `runId` 注册 `WAITING_SUBMIT` suspended active run；客户端 replay `/api/chat` 后应立即 attach，即使当前没有新事件也保持 observer。用户后续提交时，Platform 会复用该 EventBus 发布连续 seq 的 submit/answer 和 continuation，不重播 `run.started`；客户端不需要、也不应在 submit 后再补 attach。

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
- 每个业务 stream event 的 `timestamp` 都是必填 epoch milliseconds 整数。proxy / child SSE 的字符串、秒、浮点、零值或缺失 timestamp 不会被补成当前时间：平台发送本地 `run.error(code:"time_contract_violation")` 并结束该流。
- `[DONE]` 是传输结束帧，不是业务 JSON 事件。
- attach 只能续接仍在 retention 范围内的 run backlog。

## 相关文件

- `internal/stream/sse.go`
- `internal/stream/event_bus.go`
- `internal/server/handler_query.go`
- `internal/server/handler_run_stream_test.go`
- `internal/chat/events_writer.go`
- `docs/API与协议.md`
