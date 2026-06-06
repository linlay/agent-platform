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

## 核心流程

```text
普通 JSON API -> ApiResponse envelope
POST /api/query -> SSE message events -> data: [DONE]
GET /ws -> request / response / stream / push / error frames
文件上传下载 -> HTTP 数据面
```

文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传走 `POST /api/upload`，下载走 `GET /api/resource`；WebSocket `/api/upload` 与 `/api/pull` 只用于 gateway 发送 `url + metadata` 下载通知，由 platform 自己通过 HTTP 拉取并校验。

## HTTP API 定义

参数位置说明：`query` 表示 URL query，`body` 表示 JSON body，`multipart` 表示 multipart form。

### Catalog

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/agents` | query: `includeChats` | agent 列表，可附带最近 chat 摘要 |
| GET | `/api/channels` | 无 | channel 摘要列表 |
| GET | `/api/agent` | query: `agentKey` | 单个 agent 详情 |
| GET | `/api/teams` | 无 | team 列表 |
| GET | `/api/skills` | 无 | skill 列表 |
| GET | `/api/skill-candidates` | query: `agentKey` | skill candidate 列表 |
| GET | `/api/tools` | query: `kind` | tool 列表 |
| GET | `/api/tool` | query: `toolName` | 单个 tool 详情 |
| GET | `/api/model-options` | 无 | 聊天运行时可选模型与思考深度 |

### Agent 编辑

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/agent/create` | body: `key`、`name`、`role`、`description`、`icon`、`modelConfig`、`toolConfig`、`skillConfig`、`runtimeConfig`、`mode`、`plain` | 创建后的 agent 详情 |
| POST | `/api/agent/update` | body: `agentKey` 及可更新 agent 字段 | 更新后的 agent 详情 |
| POST | `/api/agent/delete` | body: `agentKey` | 删除结果 |
| GET | `/api/agent/editor-options` | 无 | 编辑器可选项 |

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

`/api/chats` 的 chat 摘要在存在可恢复等待项时包含 `awaiting`：`awaitingId`、`runId`、`mode`、`status:"awaiting"`、`createdAt`。

`/api/agents?includeChats=N` 附带的 chat 摘要可能包含局部 `error`，用于展示单个 chat 的可恢复/可诊断异常而不让列表整体失败。当前 `multiple active runs found for chat` 会返回 `error: { "code": "active_run_conflict", "message": "multiple active runs found for chat", "chatId": "...", "runIds": ["..."] }`，此时该 chat 不包含 `activeRun`。

### Archive

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/archives` | query: `agentKey`、`limit`、`offset` | archive 摘要列表 |
| GET | `/api/archive` | query: `chatId` | archive 详情 |
| POST | `/api/archives/search` | body: `query`、`agentKey`、`limit` | archive 搜索结果 |
| POST | `/api/archive/delete` | body: `chatId` | 删除 archive 结果 |

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

### Run

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/query` | body: `message`、`agentKey`、`teamId`、`chatId`、`runId`、`requestId`、`role`、`references`、`params`、`scene`、`stream`、`planningMode`、`accessLevel`、`model` | SSE stream；结束帧为 `data: [DONE]` |
| GET | `/api/attach` | query: `runId`、`agentKey`、`lastSeq` | 续接 run 的 SSE stream |
| POST | `/api/submit` | body: `agentKey`、`runId`、`awaitingId`、`params` | HITL submit ack |
| POST | `/api/steer` | body: `agentKey`、`runId`、`message`、`requestId`、`chatId`、`teamId`、`steerId` | steer ack |
| POST | `/api/interrupt` | body: `agentKey`、`runId`、`message`、`requestId`、`chatId`、`teamId` | interrupt ack |
| POST | `/api/access-level` | body: `agentKey`、`runId`、`accessLevel`、`requestId`、`reason` | 动态更新 native run 的 accessLevel |

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

`model.key` 必须存在于 model registry；`model.modelId` 由后端转发给 ACP CODER 上游时补齐，优先来自 model registry 的 `modelId`，为空时回退到 key；`model.reasoningEffort` 可取 `LOW`、`MEDIUM`、`HIGH`，非空时开启本次 run 的 reasoning。该配置只影响当前 run，不写回 agent 配置。

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

- `models`: 当前 model registry 中的模型，字段为 `key/provider/modelId/protocol/isReasoner/isVision/contextWindow`
- `reasoningEfforts`: 固定为 `NONE`、`LOW`、`MEDIUM`、`HIGH`，其中 `NONE` 表示关闭思考深度
- `defaultModelKey`: model registry 默认模型；无默认模型时为空
- `defaultReasoningEffort`: 固定为 `MEDIUM`

其中 `contextWindow` 是 API 响应字段名；model registry YAML 中对应配置字段为 `maxInputTokens`。

HITL 三态细节见 [HITL协议](HITL协议.md)。真流式、heartbeat、attach backlog 与 H2A 缓冲见 [真流式和H2A](真流式和H2A.md)。

### Memory

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/remember` | body: `requestId`、`chatId` | 兼容 remember 结果 |
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
| GET | `/api/tool-result` | query: `chatId`、`path`、`t` | `.tools/results/<toolId>.json` 完整工具结果；兼容旧 `.tool-results/<toolId>.json`；`t` 为可选 resource ticket |
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

连接项包含 `sessionId`、`kind`、`active`、`subject`、`gatewayId`、`channel`、`source`、`deviceId`、`remoteAddr`、`userAgent`、`connectedAt`、`closedAt`、`lastSeenAt`、`lastMessageAt`、`receivedMessages`、`sentMessages`、`errors`、`inflightRequests`、`activeStreams`、`writeQueueDepth`。

消息项包含 `seq`、`timestamp`、`sessionId`、`source`、`deviceId`、`direction`、`frame`、`type`、`id`、`sizeBytes`、`payloadPreview`、`truncated`、`error`。`payloadPreview` 只保存脱敏后的截断摘要，最多 512 字符；不会记录完整 payload，不记录 ping/pong/control frame，并跳过 `push.heartbeat`。

## WebSocket 定义

### 入口与鉴权

- 入口：`GET /ws`，HTTP upgrade 为 WebSocket。
- 鉴权：复用 HTTP token 校验链路。
- token 可通过 `Sec-WebSocket-Protocol: bearer.<token>` 或 query token 传递；服务端会在握手成功时回写匹配的 subprotocol。
- 客户端可通过 query 自报监控元数据：`source` 与 `deviceId`，例如 `/ws?source=webclient&deviceId=device-123`；`source` 转小写后展示，`deviceId` 兼容 `device_id`，缺省时可从 JWT claim `deviceId` / `device_id` 兜底。
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

`awaiting.asking.timeout` 与 stream 中的 `awaiting.ask.timeout` 语义一致：`0` 表示无限等待、不自动超时；大于 `0` 时按毫秒倒计时。

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

### WS Route

`/ws` 可转发的 route 由 `internal/server/ws_routes.go` 注册。除 `/api/query` 与 `/api/attach` 外，大多数 route 返回一次 `response` frame。

| Route | Payload | 返回 |
|---|---|---|
| `/api/agents` | `includeChats` | `response` |
| `/api/channels` | 无 | `response` |
| `/api/agent` | `agentKey` | `response` |
| `/api/agent/create` | agent 创建字段 | `response` |
| `/api/agent/update` | agent 更新字段 | `response` |
| `/api/agent/delete` | `agentKey` | `response` |
| `/api/agent/editor-options` | 无 | `response` |
| `/api/model-options` | 无 | `response` |
| `/api/teams` | 无 | `response` |
| `/api/skills` | 无 | `response` |
| `/api/tools` | `kind` | `response` |
| `/api/tool` | `toolName` | `response` |
| `/api/chats` | `lastRunId`、`agentKey` | `response` |
| `/api/chat` | `chatId`、`includeRawMessages` | `response` |
| `/api/read` | `chatId` | `response` |
| `/api/feedback` | feedback 字段 | `response` |
| `/api/chat/delete` | `chatId` | `response` |
| `/api/chat/rename` | `chatId`、`chatName` | `response` |
| `/api/chat/archive` | `chatId`、`reason` | `response` |
| `/api/archives` | `agentKey`、`limit`、`offset` | `response` |
| `/api/archive` | `chatId` | `response` |
| `/api/archives/search` | `query`、`agentKey`、`limit` | `response` |
| `/api/archive/delete` | `chatId` | `response` |
| `/api/automations` | `tag` | `response` |
| `/api/automation` | `id` 或 `automationId` | `response` |
| `/api/automation/create` | automation 创建字段 | `response` |
| `/api/automation/update` | automation 更新字段 | `response` |
| `/api/automation/delete` | `id` 或 `automationId` | `response` |
| `/api/automation/toggle` | `id` 或 `automationId`、`enabled` | `response` |
| `/api/automation/executions` | `id` 或 `automationId`、`limit`、`offset` | `response` |
| `/api/chats/search` | `query`、`agentKey`、`teamId`、`limit` | `response` |
| `/api/query` | `QueryRequest` | `stream` |
| `/api/attach` | `runId`、`agentKey`、`lastSeq` | `stream` |
| `/api/submit` | `SubmitRequest` | `response` |
| `/api/steer` | `SteerRequest` | `response` |
| `/api/interrupt` | `InterruptRequest` | `response` |
| `/api/remember` | `RememberRequest` | `response` |
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
| `/api/pull` | gateway pull metadata | `response` |

## 约束与注意事项

- HTTP query 参数在 WS payload 中通常以同名 JSON 字段传入。
- `GET /api/attach`、`POST /api/submit`、`POST /api/steer`、`POST /api/interrupt` 都要求 `agentKey`，并校验 run 归属。
- WS `/api/resource` 要求 `file + pushURL`，用于将本地资源推给 gateway；HTTP `/api/resource` 直接返回文件字节。
- `.tools` 是隐藏工具内部目录，不通过 `/api/resource` 或 WS `/api/resource` 暴露；HTTP `/api/tool-result` 接受 `.tools/results/<toolId>.json`，并兼容旧 `.tool-results/<toolId>.json`。
- 反向 gateway 配置在 `configs/channels.yml`，不再通过旧单 gateway env 合成。
- 完整 DTO 字段以 `internal/api/*.go` 为事实源。

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
