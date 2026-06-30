# API与协议

## 当前状态

运行时提供 HTTP REST、SSE 与 WebSocket 三类协议入口。REST 承载 catalog、chat、automation、memory、resource 等请求；`POST /api/query` 使用 SSE 返回实时 run stream；`GET /ws` 是 WebSocket 控制面，复用一批 `/api/*` route，并用 `stream` frame 承载实时事件。

所有非 SSE HTTP JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

除特别标注为可读展示字段外，API、JSONL 与 trace 中的结构化时间字段统一使用 Unix epoch milliseconds（`int64`）。可读时间字符串使用 RFC3339 / RFC3339Nano 并保留对应时区偏移，主要用于日志、调度展示和人工排障。

## 核心流程

```text
普通 JSON API -> ApiResponse envelope
POST /api/query -> SSE message events -> data: [DONE]
GET /ws -> request / response / stream / push / error frames
文件上传下载 -> HTTP 数据面
```

文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传走 `POST /api/upload`，下载走 `GET /api/resource`；WebSocket `/api/upload` 用于 gateway 发送 `url + metadata` 下载通知，由 platform 按 metadata 中的 URL 自己通过 HTTP 拉取并校验（该 URL 可指向 gateway 的 `/api/pull/...`）。反向推送本地资源走 WS `/api/resource`，platform 再把文件字节 HTTP POST 到 gateway 的 `pushURL`（通常是 `/api/push/...`）；WS `/api/push` 不存在。

## HTTP API 定义

参数位置说明：`query` 表示 URL query，`body` 表示 JSON body，`multipart` 表示 multipart form。

### Catalog

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/agents` | query: `includeChats` | agent 列表，可附带最近 chat 摘要 |
| GET | `/api/agent` | query: `agentKey` | 单个运行时 agent 详情，不返回编辑专用字段 |
| POST | `/api/agent/model-config` | body: `agentKey`/`key`、`modelKey`、`reasoningEffort` | 更新 CODER agent 的运行时默认模型配置 |
| GET | `/api/teams` | 无 | team 列表 |
| GET | `/api/skill-candidates` | query: `agentKey` | skill candidate 列表 |
| GET | `/api/model-options` | 无 | 聊天运行时可选模型与思考深度 |

### Admin

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/admin/agents` | 无 | admin agent 列表，包含 invalid agent 诊断 |
| GET | `/api/admin/agents/detail` | query: `agentKey` | admin agent 详情，包含编辑配置、来源和诊断 |
| GET/PUT | `/api/admin/agents/order` | PUT body: `order` | agent 展示顺序 |
| POST | `/api/admin/agents/create` | body: `key`、`definition`、`soulPrompt`、`agentsPrompt` | 创建后的 agent 详情 |
| POST | `/api/admin/agents/update` | body: `key`/`agentKey`、`definition`、`soulPrompt`、`agentsPrompt` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/delete` | body: `key`/`agentKey` | 删除结果 |
| GET | `/api/admin/agents/editor-options` | 无 | agent 编辑器可选项 |
| GET | `/api/admin/skills` | 无 | skill 列表 |
| GET | `/api/admin/tools` | query: `kind` | tool 列表 |
| GET | `/api/admin/registries` | 无 | registry 文件列表与诊断 |
| GET/PUT | `/api/admin/registries/detail` | query/body: `category`、`file`、`content` | registry 文件详情或保存结果 |
| POST | `/api/admin/registries/validate` | body: `category`、`file`、`content` | registry 内容校验结果 |

### Chat

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/chats` | query: `lastRunId`、`agentKey` | chat 摘要列表 |
| GET | `/api/chat` | query: `chatId`、`includeRawMessages` | chat 详情，默认含 events |
| POST | `/api/chats/search` | body: `query`、`agentKey`、`teamId`、`limit` | 全局 chat 搜索结果 |
| POST | `/api/read` | body: `chatId` | 标记已读结果 |
| POST | `/api/feedback` | body: `chatId`、`runId`、`messageId`、`rating`、`reason` | feedback 写入结果 |
| POST | `/api/chat/delete` | body: `chatId` | 删除 chat 结果 |
| POST | `/api/chat/rename` | body: `chatId`、`chatName` | 重命名结果 |
| POST | `/api/chat/archive` | body: `chatId`、`reason` | 归档结果 |
| GET | `/api/chat/export` | query: `chatId` | Markdown 导出 |
| GET | `/api/chat/jsonl` | query: `chatId` | 原始 chat JSONL 文本；active 不存在时回退 archive |
| GET | `/api/chat/llm-trace` | query: `file=<chatId>/.llm-records/<runId>_NNN.json` | 原始 LLM chat trace JSON 文本 |

`/api/chats` 的 chat 摘要在存在可恢复等待项时包含 `awaiting`：`awaitingId`、`runId`、`mode`、`status:"awaiting"`、`createdAt`。

`/api/chat` 返回 active run 时，`activeRun.lastSeq` 是本次 chat detail 已返回历史 events 覆盖到的 live stream 游标，客户端应用这些 events 后可把它作为 `/api/attach.lastSeq`。它来自 `chatId.jsonl` 每行顶层 `liveSeq` 的 replay 结果，不是内存 run 当前最新 seq；内存最新 seq 只用于服务端运行状态。

`/api/chat/llm-trace` 返回原始 LLM trace JSON；新写入的 trace 中 `sentAt`、`responseStartedAt`、`completedAt` 以及 `interrupt.interruptedAt` 均为 epoch milliseconds，对应的 `sentTime`、`responseStartedTime`、`completedTime`、`interrupt.interruptedTime` 为 RFC3339Nano 可读时间。历史 trace 文件不迁移，接口会按原始内容返回。

`/api/agents?includeChats=N` 附带的 chat 摘要可能包含局部 `error`，用于展示单个 chat 的可恢复/可诊断异常而不让列表整体失败。当前 `multiple active runs found for chat` 会返回 `error: { "code": "active_run_conflict", "message": "multiple active runs found for chat", "chatId": "...", "runIds": ["..."] }`，此时该 chat 不包含 `activeRun`。

`/api/agent` 会返回 agent 配置中的 `greetings` 与 `wonders` 数组。客户端可将 `greetings` 作为开场/占位介绍，并随机挑选一条显示在聊天输入框 placeholder 或空状态里；`wonders` 用于展示可直接提交的具体 query 示例。`/api/agents` 是列表摘要接口，不返回 `greetings` 或 `wonders`。`/api/agent` 是运行时详情接口，不返回 `definition`、`soulPrompt`、`agentsPrompt`、`source`；编辑器应使用 `/api/admin/agents/detail` 获取这些字段，以及 `status`、`diagnostics`。

### Archive

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/archives` | query: `agentKey`、`limit`、`offset` | archive 摘要列表 |
| GET | `/api/archive` | query: `chatId` | archive 详情 |
| POST | `/api/archives/search` | body: `query`、`agentKey`、`limit` | archive 搜索结果 |
| POST | `/api/archive/delete` | body: `chatId` | 删除 archive 结果 |

Archive 摘要、详情和搜索结果都会返回时间字段：`createdAt` 为 chat 创建时间，`lastRunAt` 为最后一次 run 完成时间，`archivedAt` 为归档时间。`updatedAt` 保留为兼容字段，不应再作为 last run 时间使用。

### Automation

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/automations` | body: `tag` | automation 列表 |
| POST | `/api/automation` | body: `id` 或 `automationId` | automation 详情 |
| POST | `/api/automation/create` | body: `name`、`description`、`cron`、`agentKey`、`enabled`、`teamId`、`zoneId`、`remainingRuns`、`query` | 创建后的 automation 详情 |
| POST | `/api/automation/update` | body: `id` 或 `automationId`，以及可更新字段 | 更新后的 automation 详情 |
| POST | `/api/automation/delete` | body: `id` 或 `automationId` | 删除结果 |
| POST | `/api/automation/toggle` | body: `id` 或 `automationId`、`enabled` | 启停后的 automation 详情 |
| POST | `/api/automation/executions` | body: `id` 或 `automationId`、`limit`、`offset` | execution history |

`query` 对象包含 `message`、`chatId`、`role`、`params`。`role` 可选值为 `user`、`assistant`、`automation`、`system`；automation 未显式配置时默认为 `automation`。

Automation 摘要和详情中的 `nextFireAt` 是下次触发时间的 epoch milliseconds；`nextFireTime` 是按 automation `zoneId` 格式化的 RFC3339 展示时间。Execution history 中的 `startedAt`、`completedAt` 仍为 epoch milliseconds。

### Run

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/query` | body: `message`、`agentKey`、`teamId`、`chatId`、`runId`、`requestId`、`role`、`references`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`planningMode`、`accessLevel`、`model` | 默认 SSE stream；`stream:false` 时返回 JSON |
| GET | `/api/attach` | query: `runId`、`agentKey`、`lastSeq` | 续接 run 的 SSE stream |
| POST | `/api/submit` | body: `agentKey`、`runId`、`awaitingId`、`params` | HITL submit ack |
| POST | `/api/steer` | body: `agentKey`、`runId`、`message`、`requestId`、`chatId`、`teamId`、`steerId` | steer ack |
| POST | `/api/interrupt` | body: `agentKey`、`runId`、`message`、`requestId`、`chatId`、`teamId` | interrupt ack |
| POST | `/api/access-level` | body: `agentKey`、`runId`、`accessLevel`、`requestId`、`reason` | 动态更新 native run 的 accessLevel |

`/api/query` 的 `stream` 是 JSON body 字段；省略或传 `true` 时返回 SSE，结束帧为 `data: [DONE]`。传 `false` 时服务端仍执行完整 run、持久化 chat，并在结束后返回普通 JSON。默认只返回最终回答：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "content": "最终回答"
  }
}
```

`includeUsage:true` 会在 `data` 中追加本轮用量；`includeFullText:true` 会追加面向阅读的全过程文本：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "content": "最终回答",
    "fullText": "Tool: datetime\n{}\n\nTool result: datetime\n...\n\nAnswer\n最终回答",
    "usage": {
      "promptTokens": 10,
      "completionTokens": 5,
      "totalTokens": 15
    }
  }
}
```

`steam` 不是支持字段；如果误传 `steam:false`，不会触发非流式响应。

实时 SSE / WS stream 的工具事件形状不变：仍按单个工具发送 `tool.snapshot`、`tool.result`、`action.snapshot`、`action.result`。持久化到 `chatId.jsonl` 时，同一 assistant turn 的多个工具调用会合并为一条 assistant message 的 `tool_calls[]`；如果该组存在 awaiting，确认前不会执行任何 sibling tool，确认后的所有结果写入同 `seq` 的 `_type:"react-tool"` continuation。

可运行的 HTTP JSON 模式 curl：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false}'
```

带用量：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false,"includeUsage":true}'
```

带全过程：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false,"includeFullText":true}'
```

`params` 是业务透传对象，平台不读取、不写入、不约定内部 key。

`role` 可选值为 `user`、`assistant`、`automation`、`system`，普通 query 缺省为 `user`。`automation` / `system` 的 `request.query` 会保留在 trace 中，但不会作为可见用户消息参与搜索或 Markdown 导出。

`model` 可做本次 run 的模型覆盖：

```json
{
  "agentKey": "coder",
  "message": "实现这个改动",
  "accessLevel": "auto_approve",
  "model": {
    "key": "qwen3-coder",
    "modelId": "qwen3-coder",
    "reasoningEffort": "HIGH"
  }
}
```

`model.key` 必须存在于 model registry；`model.modelId` 由后端转发给 ACP CODER 上游时补齐，优先来自 model registry 的 `modelId`，为空时回退到 key；`model.reasoningEffort` 一般可取 `LOW`、`MEDIUM`、`HIGH`，CODER agent 额外支持 `NONE` 用于关闭本次 run 的 reasoning。该配置只影响当前 run，不写回 agent 配置。

`accessLevel` 在 `/api/query` 中作为 run 初始值；运行中可通过 `/api/access-level` 调整：

```json
{
  "agentKey": "default_agent",
  "runId": "run-id",
  "accessLevel": "auto_approve",
  "reason": "user toggled permission"
}
```

响应包含 `accepted`、`status`、`runId`、`previousAccessLevel`、`accessLevel`、`version`、`detail`。更新只影响后续 host bash 与 file tools 的 access-policy 判断；已经开始执行的工具不会被中断。若 run 正在等待 access-policy approval，权限提升后会重新评估当前等待项，满足新权限时自动清理 awaiting 并继续执行。PROXY / ACP CODER run 当前返回 `status=unsupported`，不隐式透传。

#### CODER model options

`GET /api/model-options` 返回聊天输入区运行时可选项。前端按当前 agent `mode` 自行决定是否展示该控件：

- `models`: 当前 model registry 中可展示的模型，字段为 `key/provider/modelId/protocol/isReasoner/isVision/contextWindow`。普通模型要求 provider 存在且 `apiKey` 非空；`protocol: ACP_PASSTHROUGH` 的 ACP 透传模型不要求 provider。
- `reasoningEfforts`: 固定为 `NONE`、`LOW`、`MEDIUM`、`HIGH`，其中 `NONE` 表示关闭思考深度
- `defaultModelKey`: 可展示模型中的默认模型；优先普通可调用模型，没有时可回退到 ACP 透传模型，无默认模型时为空
- `defaultReasoningEffort`: 固定为 `MEDIUM`

其中 `contextWindow` 是 API 响应字段名；model registry YAML 中对应配置字段为 `maxInputTokens`。

HITL 三态细节见 [HITL协议](HITL协议.md)。真流式、heartbeat、attach backlog 与 H2A 缓冲见 [真流式和H2A](真流式和H2A.md)。

### KBASE

KBASE API 只接受 `mode: KBASE` agent；非 KBASE agent 会返回 forbidden/unsupported。手工 refresh 与运行时工具 `kbase_refresh` 调用同一个后端入口。

KBASE agent 在运行时调用 `kbase_search` 且召回到内容时，会额外通过 live stream 发布 `source.publish` 事件。事件包含 `kind: "kbase"`、`query`、`sourceCount`、`chunkCount` 与按 source 聚合的 `sources[].chunks[]`，chunk 可携带 `path`、行号、页码、slide、`sourceType`、`matchType`、`score` 等定位字段；该事件会写入 chat JSONL 的 event line 并保留 `liveSeq`，供 `/api/chat` replay 与 `/api/attach.lastSeq` 使用。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/kbase/{agentKey}/status` | 无 | 当前索引状态，包含 `indexing`、`stale`、`lastIndexedAt`、文件数、chunk 数、embedding 与 storage |
| POST | `/api/kbase/{agentKey}/refresh` | body: `force` 可选 | refresh 结果，包含扫描文件数、变更文件数、删除文件数、索引 chunk 数与错误信息 |

refresh 示例：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/kbase/docs_kbase/refresh \
  -H "Content-Type: application/json" \
  -d '{"force":false}'
```

### Memory

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/learn` | body: `requestId`、`chatId`、`subjectKey` | learn / auto memory 结果 |
| GET | `/api/memory/meta` | 无 | memory category/type/scope/status 元数据 |
| POST | `/api/memory/context-preview` | body: `chatId`、`message` | memory context 预览 |
| GET | `/api/memory/scope/list` | query: `agentKey` | scope 列表 |
| GET | `/api/memory/scope/detail` | query: `agentKey`、`scopeType`、`scopeKey` | scope 详情 |
| POST | `/api/memory/scope/save` | body: `agentKey`、`scopeType`、`scopeKey`、`mode`、`markdown`、`records`、`archiveMissing` | scope 保存结果 |
| POST | `/api/memory/scope/validate` | body: `agentKey`、`scopeType`、`markdown` | scope markdown 校验结果 |
| GET | `/api/memory/record/list` | query: `agentKey`、`scopeType`、`scopeKey`、`category`、`status`、`limit`、`cursor` | memory record 列表 |
| GET | `/api/memory/history` | query: `agentKey`、`memoryId`、`limit`、`cursor` | memory history |
| GET | `/api/memory/record/detail` | query: `id` | memory record 详情 |
| GET | `/api/memory/record/timeline` | query: `id`、`limit` | memory record timeline |

### Viewport / Resource

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/viewport` | query: `viewportKey`、`viewportType` | viewport 模板或 fallback |
| GET | `/api/resource` | query: `file`、`t` | chat 资源文件；`t` 为可选 resource ticket |
| GET | `/api/tool-result` | query: `chatId`、`path`、`t` | `.tools/results/<toolId>.json` 完整工具结果；`t` 为可选 resource ticket |
| POST | `/api/upload` | multipart: `requestId`、`chatId`、`file` | upload ticket 与资源访问信息 |

resource ticket、JWT 与 CORS 见 [鉴权与安全边界](鉴权与安全边界.md)。

### Monitor

监控接口是 HTTP polling snapshot，不使用 WebSocket 实时订阅；鉴权沿用普通 `/api/*` 链路。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/monitor` | query: `messageLimit` 可选，默认 5，范围 1..50 | 总览与 WS 摘要 |
| GET | `/api/monitor/ws/connections` | query: `limit` 默认 100，范围 1..500；`sessionId`、`source`、`deviceId` 可选 | 当前/最近 WS 连接列表 |
| GET | `/api/monitor/ws/messages` | query: `limit` 默认 5，范围 1..50；`sessionId`、`source`、`deviceId` 可选 | 最近 WS 消息列表 |

`/api/monitor` 返回：

```json
{
  "generatedAt": 1710000000000,
  "ws": {
    "connectionCount": 1,
    "latestConnection": {},
    "recentMessages": []
  }
}
```

连接项包含 `sessionId`、`kind`、`active`、`subject`、`gatewayId`、`channel`、`source`、`deviceId`、`remoteAddr`、`userAgent`、`connectedAt`、`closedAt`、`closeReason`、`lastSeenAt`、`lastMessageAt`、`receivedMessages`、`sentMessages`、`errors`、`inflightRequests`、`activeStreams`、`writeQueueDepth`。

消息项包含 `seq`、`timestamp`、`sessionId`、`source`、`deviceId`、`direction`、`frame`、`type`、`id`、`sizeBytes`、`payloadPreview`、`truncated`、`error`。`payloadPreview` 只保存脱敏后的截断摘要，最多 512 字符；不会记录完整 payload，不记录 ping/pong/control frame，并跳过 `push.heartbeat`。

## WebSocket 定义

### 入口与鉴权

- 入口：`GET /ws`，HTTP upgrade 为 WebSocket。
- 鉴权：复用 HTTP token 校验链路。
- token 可通过 `Sec-WebSocket-Protocol: bearer.<token>` 或 query token 传递；服务端会在握手成功时回写匹配的 subprotocol。
- 客户端可通过 query 自报监控元数据：`source` 与 `deviceId`，例如 `/ws?source=webclient&deviceId=device-123`；`source` 转小写后展示，缺省时可从 JWT claim `deviceId` 兜底。
- WebSocket 控制面常开；没有单独的关闭开关。

### 帧类型

客户端请求帧：

```json
{
  "frame": "request",
  "type": "/api/agents",
  "id": "req-1",
  "payload": {}
}
```

服务端响应帧：

```json
{
  "frame": "response",
  "type": "/api/agents",
  "id": "req-1",
  "code": 0,
  "msg": "success",
  "data": {}
}
```

实时流帧：

```json
{
  "frame": "stream",
  "id": "req-1",
  "streamId": "run-id",
  "event": {},
  "lastSeq": 12
}
```

推送帧与错误帧：

```json
{"frame":"push","type":"connected","data":{}}
{"frame":"error","type":"invalid_request","id":"req-1","code":400,"msg":"...","data":{}}
{"frame":"error","type":"active_run_conflict","id":"req-1","code":409,"msg":"multiple active runs found for chat","data":{"code":"active_run_conflict","message":"multiple active runs found for chat","chatId":"chat-id","runIds":["run-1","run-2"]}}
```

当前 platform 主动发送的 `push.type`：

| Push type | data |
|---|---|
| `connected` | `sessionId` |
| `heartbeat` | `timestamp` |
| `auth.expiring` | `expiresAt` |
| `run.started` | `runId`、`chatId`、`agentKey` |
| `run.finished` | `runId`、`chatId` |
| `chat.created` | `chatId`、`chatName`、`agentKey`、`timestamp` |
| `chat.updated` | `chatId`、`lastRunId`、`lastRunContent`、`updatedAt` |
| `chat.unread` / `chat.read` | `chatId`、`agentKey`、`lastRunId`、`readAt`、`readRunId`、`agentUnreadCount` |
| `chat.read_all` | `agentKey`、`updatedCount`、`agentUnreadCount` |
| `chat.deleted` | `chatId` |
| `chat.renamed` | `chatId`、`chatName`、`agentKey` |
| `chat.archived` | `chatId`、`agentKey` |
| `archive.deleted` | `chatId` |
| `catalog.updated` | `reason`、可选 `timestamp` |
| `awaiting.asking` | `chatId`、`runId`、`agentKey`、`awaitingId`、`mode`、`timeout`、`createdAt`、可选 `viewportType` / `viewportKey` |
| `awaiting.answered` | `chatId`、`runId`、`awaitingId`、`mode`、`status`、`resolvedAt`、可选 `errorCode` / `submitId` |
| `resource.pushed` | `chatId`、`artifactId`、`name`、`mimeType`、`sha256`、`sizeBytes`、`timestamp` |

`awaiting.asking.timeout` 与 stream 中的 `awaiting.ask.timeout` 语义一致：`0` 表示无限等待、不自动超时；大于 `0` 时由后端按真实时间独立倒计时，observer / attach / detach 状态不会暂停或延长后端超时。

stream `awaiting.answer` 的 `error.code == "timeout"` 时，`error.message` 会显示超时秒数和原因；`error` 可附带 `timeoutSeconds`、`elapsedSeconds`、`reason:"submit_not_received_before_timeout"`。

字段说明：

| 字段 | 适用帧 | 说明 |
|---|---|---|
| `frame` | 全部 | `request` / `response` / `stream` / `push` / `error` |
| `type` | request / response / push / error | route 或 push/error 类型 |
| `id` | request / response / stream / error | 客户端请求 id，用于关联响应和流 |
| `payload` | request | route payload，通常对应 HTTP query/body 的 JSON 化形态 |
| `code` / `msg` / `data` | response / error | 与 HTTP JSON envelope 对齐 |
| `streamId` | stream | runId 或流 id |
| `event` | stream | `stream.EventData` |
| `reason` | stream | stream 结束或中断原因 |
| `lastSeq` | stream | 已发送事件序号，可用于 attach |

历史重建事件的 `seq` 是展示序号。`chatId.jsonl` 使用每行顶层 `liveSeq` 记录该行覆盖到的原始 live stream 序号；replay 时会把它注入到对应的历史事件 payload，供 attach cursor 使用。

### WS Route

`/ws` 可转发的 route 由 `internal/server/ws_routes.go` 注册。除 `/api/query` 与 `/api/attach` 外，大多数 route 返回一次 `response` frame。

| Route | Payload | 返回 |
|---|---|---|
| `/api/agents` | `includeChats` | `response` |
| `/api/agent` | `agentKey` | `response` |
| `/api/agent/model-config` | `agentKey`/`key`、`modelKey`、`reasoningEffort` | `response` |
| `/api/model-options` | 无 | `response` |
| `/api/teams` | 无 | `response` |
| `/api/chats` | `lastRunId`、`agentKey` | `response` |
| `/api/chat` | `chatId`、`includeRawMessages` | `response` |
| `/api/read` | `chatId` | `response` |
| `/api/feedback` | feedback 字段 | `response` |
| `/api/chat/delete` | `chatId` | `response` |
| `/api/chat/rename` | `chatId`、`chatName` | `response` |
| `/api/chat/archive` | `chatId`、`reason` | `response` |
| `/api/chat/jsonl` | `chatId` | `response`，data 为原始 JSONL 字符串；HTTP 仍返回 text/plain |
| `/api/chat/llm-trace` | `file` | `response`，data 为原始 LLM trace JSON 字符串；HTTP 返回 application/json |
| `/api/archives` | `agentKey`、`limit`、`offset` | `response` |
| `/api/archive` | `chatId` | `response` |
| `/api/archives/search` | `query`、`agentKey`、`limit` | `response` |
| `/api/archive/delete` | `chatId` | `response` |
| `/api/automations` | `tag` | `response` |
| `/api/automation` | `id` 或 `automationId` | `response` |
| `/api/automation/executions` | `id` 或 `automationId`、`limit`、`offset` | `response` |
| `/api/chats/search` | `query`、`agentKey`、`teamId`、`limit` | `response` |
| `/api/query` | `QueryRequest` | `stream` |
| `/api/attach` | `runId`、`agentKey`、`lastSeq` | `stream` |
| `/api/detach` | `runId`、`agentKey`、`reason` | `response`；关闭当前 WS 连接上该 run 的 observer，不中断 run |
| `/api/terminal/open` | `agentKey`、可选 `chatId`、`cols`、`rows` | `stream` |
| `/api/terminal/input` | `terminalId`、`data` | `response` |
| `/api/terminal/resize` | `terminalId`、`cols`、`rows` | `response` |
| `/api/terminal/close` | `terminalId` | `response` |
| `/api/submit` | `SubmitRequest` | `response` |
| `/api/steer` | `SteerRequest` | `response` |
| `/api/interrupt` | `InterruptRequest` | `response` |
| `/api/learn` | `LearnRequest` | `response` |
| `/api/memory/meta` | 无 | `response` |
| `/api/memory/context-preview` | `chatId`、`message` | `response` |
| `/api/memory/scope/list` | `agentKey` | `response` |
| `/api/memory/scope/detail` | `agentKey`、`scopeType`、`scopeKey` | `response` |
| `/api/memory/scope/save` | scope 保存字段 | `response` |
| `/api/memory/scope/validate` | `agentKey`、`scopeType`、`markdown` | `response` |
| `/api/memory/record/list` | memory record 过滤字段 | `response` |
| `/api/memory/record/detail` | `id` | `response` |
| `/api/viewport` | `viewportKey`、`viewportType` | `response` |
| `/api/resource` | `file`、`pushURL` | `response` |
| `/api/upload` | gateway upload metadata | `response` |

## 约束与注意事项

- HTTP query 参数在 WS payload 中通常以同名 JSON 字段传入。
- `GET /api/attach`、WS `/api/detach`、`POST /api/submit`、`POST /api/steer`、`POST /api/interrupt` 都要求 `agentKey`，并校验 run 归属。
- WS 客户端切换 current chat 时，应先对原 chat 的 active run 发送 `/api/detach`，再对新 chat 的 active run 发送 `/api/attach`；detach 只释放当前 WS 连接上的订阅流，不停止后台 run。
- WS `/api/resource` 要求 `file + pushURL`，用于将本地资源推给 gateway；`pushURL` 是 gateway HTTP 目的地址，通常为 `/api/push/...`，WS `/api/push` 不存在；HTTP `/api/resource` 直接返回文件字节。
- `.tools` 是隐藏工具内部目录，不通过 `/api/resource` 或 WS `/api/resource` 暴露；HTTP `/api/tool-result` 接受 `.tools/results/<toolId>.json`。
- 反向 gateway 配置在 `configs/channels.yml`。
- 完整 DTO 字段以 `internal/api/*.go` 为事实源。

### CODER Terminal

CODER 终端只复用主 `/ws` 连接，不提供独立 `/ws/terminal`，也不新增顶层 `frame` 类型。终端协议仍使用 `frame:"request"` / `frame:"stream"` / `frame:"response"` / `frame:"error"`。

`/api/terminal/open` 是长生命周期 stream：

```json
{"frame":"request","type":"/api/terminal/open","id":"term-1","payload":{"agentKey":"coder","chatId":"chat-1","cols":120,"rows":32}}
```

open 成功后返回 stream 事件：

```json
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.opened","seq":1,"payload":{"terminalId":"term_xxx","agentKey":"coder","cwd":"/workspace","shell":"/bin/zsh"}}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.output","seq":2,"payload":{"terminalId":"term_xxx","data":"..."}}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.exit","seq":3,"payload":{"terminalId":"term_xxx","exitCode":0}}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","reason":"exit","lastSeq":3}
```

键盘输入、窗口大小变化和关闭使用普通 request/response：

```json
{"frame":"request","type":"/api/terminal/input","id":"term-input-1","payload":{"terminalId":"term_xxx","data":"ls\r"}}
{"frame":"request","type":"/api/terminal/resize","id":"term-resize-1","payload":{"terminalId":"term_xxx","cols":120,"rows":32}}
{"frame":"request","type":"/api/terminal/close","id":"term-close-1","payload":{"terminalId":"term_xxx"}}
```

CODER 终端支持 macOS/Linux 上的 Unix PTY，以及 Windows 上的 ConPTY / PowerShell PTY。Windows 需要 ConPTY 可用的系统版本（Windows 10 1809 / Windows Server 2019 及以上）；旧系统会返回 `unsupported`。非 CODER agent、sandbox/ACP CODER agent、缺失 agent、空 workspace、`@chat` workspace 缺少 `chatId` 都会拒绝。cwd 只由 platform 根据 agent workspace 反查，不信任前端传入任意 cwd。终端输入与输出不会写入 chat/event log；WS monitor 只记录 terminal 输入/输出的类型、id 与字节数，不记录原始 preview。错误沿用现有 error frame，`type` 为 `invalid_request`、`forbidden`、`terminal_not_found`、`unsupported` 或 `internal_error`。

## 相关文件

- `internal/server/server.go`
- `internal/server/ws_routes.go`
- `internal/server/ws_query_routes.go`
- `internal/server/ws_resource_routes.go`
- `internal/api/types.go`
- `internal/api/types_automation.go`
- `internal/api/types_memory_console.go`
- `internal/ws/protocol.go`
- `docs/手工测试用例.md`
