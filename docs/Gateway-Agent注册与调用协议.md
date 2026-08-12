# Gateway Agent 注册与调用协议 v1

> 状态：Gateway 联调候选稿；日期：2026-08-12。
> Agent Platform 当前只实现第 1～8 节的接出注册、解除、列表查询和 Session 对账。第 9～11 节的新版 Query Stream、Run TTL、`registrationId` 路由、HITL Schema 和控制协议尚未实现。

本文定义 Agent Platform 与 Gateway 之间的 Agent 在线注册、解除注册、注册查询以及注册后的 query / steer / interrupt / HITL 调用协议。

## 1. 设计原则

- 网络协议每次只注册或解除注册一个 Agent，避免批量请求的部分成功、原子失败和超时状态不确定问题。
- 多 Agent Platform 通过多次 `agent.register` 完成注册，建议客户端最多并发 4 个注册请求。
- `agent.register` 在同一 owner session 内是幂等 upsert：首次为注册，再次为完整替换。
- Agent 注册是连接期路由；WebSocket 断开后，Gateway 必须自动清理该 session 剩余的 Agent 路由。
- 注册的 `capabilities` 是路由与能力声明，不得替代 Gateway 鉴权或 Agent Platform 本地 `channelConfig.exports.allow` 授权。
- 请求帧统一使用 `id + payload`，响应帧统一使用 `id + code + msg + data`。

## 2. 连接 Gateway

WebSocket 地址：

```text
wss://<gateway-host>/ws/platform
```

如果 Gateway 没有配置默认 Platform，在地址中指定：

```text
wss://<gateway-host>/ws/platform?platformKey=<platform-key>
```

连接时通过 Header 携带 Token：

```http
Authorization: Bearer <token>
```

Gateway 完成 WebSocket upgrade 与应用层身份初始化后推送 `connected`。无论 `channel.mode` 是 `client` 还是 `server`，只要本 Platform 需要接出 Agent，本 Platform 都等待对端声明后发送注册请求；`channel.mode` 只表示 WebSocket 建连方向：

- `client`：本 Platform 主动连接对端。
- `server`：对端连接本 Platform 的 `/ws/channel?channelId=...`；本 Platform 不发送普通 `/ws` 使用的旧通用 `push connected`。

对端声明示例：

```json
{
  "frame": "push",
  "type": "connected",
  "data": {
    "sessionId": "2f43b83d-82f1-4d8d-95b3-508a90fdb481",
    "platformKey": "desktop-standard-ws",
    "credentialType": "SERVICE",
    "registrationMode": "MULTI_EMPLOYEE",
    "principal": "desktop-app",
    "status": "ACTIVE",
    "agentRegistration": {
      "version": "1",
      "maxAgentsPerPlatformChannel": 100,
      "supportedCapabilities": [
        "query",
        "steer",
        "interrupt",
        "hitl"
      ]
    },
    "timestamp": 1786502400000
  }
}
```

Agent Platform 必须收到有效 `connected` 后再发送 Agent 注册请求。每条 server channel WebSocket Session 独立保存声明和执行对账；相同 channel、相同 `platformKey` 的不同 Session 也不合并。

### 2.1 Platform-channel scope

本文中的“当前 channel”是 Gateway 根据当前认证连接的 `platformKey` 解析出的逻辑 Platform-channel scope。

- scope 必须由 Gateway 从认证连接上下文确定。
- 客户端不在 `agent.register` / `agent.unregister` / `agent.list` payload 中传入 `platformKey`、`channelId` 或 `sessionId`。
- `agentKey` 在该 Platform-channel scope 内唯一。
- 同一 scope 可以同时存在多个活跃 WebSocket session，但一个 `agentKey` 同一时刻只能由一个 session 持有。

## 3. 通用 WebSocket 帧

请求：

```json
{
  "frame": "request",
  "type": "<request-type>",
  "id": "<request-id>",
  "payload": {}
}
```

成功响应：

```json
{
  "frame": "response",
  "type": "<same-request-type>",
  "id": "<same-request-id>",
  "code": 0,
  "msg": "success",
  "data": {}
}
```

业务拒绝也使用相同 `type` 和 `id` 的 `response`，并设置非零 `code`。无法解析 JSON、缺少帧必填字段等协议级错误可使用 `error` frame：

```json
{
  "frame": "error",
  "type": "invalid_request",
  "id": "<request-id>",
  "code": 400,
  "msg": "invalid request",
  "data": {}
}
```

`request.id` 在单连接的当前 in-flight request 中必须唯一。

## 4. Agent 注册数据结构

```json
{
  "agentKey": "agent-001",
  "name": "售后助手",
  "role": "售后支持",
  "description": "协助处理售后问题",
  "capabilities": [
    "query",
    "steer",
    "interrupt",
    "hitl"
  ]
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `agentKey` | 是 | Platform-channel scope 内唯一的可调用标识，大小写敏感 |
| `name` | 是 | Agent 展示名称 |
| `role` | 否 | 简短角色标签，不是 system prompt |
| `description` | 否 | 展示与调度说明 |
| `capabilities` | 是 | 非空能力列表；可调用 Agent 必须包含 `query` 或使用 `all` |

v1 校验限制：

- `agentKey`、`name`、`role` 最多 256 个 Unicode code point。
- `description` 最多 2048 个 Unicode code point。
- 字段不得包含 NUL 和非法控制字符。v1 暂不对凭据、Token、隐私或提示词内容做严格识别和拒绝，但调用方仍不得主动上传敏感信息。
- 本协议不注册 Skill、Tool、Tag、Prompt、Workspace 或模型配置。
- Gateway 日志不得输出完整注册 payload。

## 5. Capabilities

v1 稳定能力：

| Capability | 语义 | Agent Platform 当前授权映射 |
|---|---|---|
| `query` | 可以启动新 run | `channelConfig.exports.allow.query` |
| `steer` | 可以向运行中的 run 增补指令 | `channelConfig.exports.allow.steer` |
| `interrupt` | 可以中断运行中的 run | `channelConfig.exports.allow.interrupt` |
| `hitl` | run 可以进入 HITL，Gateway 可通过 `/api/submit` 提交答案 | `channelConfig.exports.allow.submit` |

`all` 使用数组形式：

```json
{
  "capabilities": ["all"]
}
```

规则：

- `all` 必须单独出现，`["all", "query"]` 无效。
- `all` 只展开为当前 `agentRegistration.version` 和 `supportedCapabilities` 同时支持的能力。
- Gateway 在 register/list 响应中返回展开后的真实能力列表，不返回 `all`。
- 后续协议版本增加新能力时，已注册 session 不会因旧的 `all` 自动获得新能力。
- 四项能力全部开启且对端 v1 正好支持这四项时，Agent Platform 发送 `["all"]`；否则按 `query, steer, interrupt, hitl` 固定顺序发送本地 allow policy 导出的明确列表。
- `fileTransfer` 不属于 v1 注册能力，现有文件权限也不会因本协议扩大。

`accessLevel` 未进入 v1 capability；双方补齐独立授权与 ACK 语义前不应被 `all` 隐式开放。

## 6. 注册 Agent

### 6.1 请求

```json
{
  "frame": "request",
  "type": "agent.register",
  "id": "register-agent-001",
  "payload": {
    "agentKey": "agent-001",
    "name": "售后助手",
    "role": "售后支持",
    "description": "协助处理售后问题",
    "capabilities": [
      "query",
      "steer",
      "interrupt",
      "hitl"
    ]
  }
}
```

### 6.2 语义

- 当前 session 首次注册 `agentKey`：创建路由，状态为 `REGISTERED`。
- 当前 session 再次注册同一 `agentKey`：完整替换 `name/role/description/capabilities`，状态为 `UPDATED`。
- 同一 Platform-channel scope 中的其他活跃 session 已持有该 `agentKey`：拒绝并返回 `AGENT_KEY_IN_USE`，不允许静默抢占。
- 同一 Platform-channel scope 的活跃 Agent 总数不得超过 `connected.data.agentRegistration.maxAgentsPerPlatformChannel`。
- `SINGLE_EMPLOYEE` 模式在整个 Platform-channel scope 中最多只允许 1 个活跃 Agent；`MULTI_EMPLOYEE` 模式按 Gateway 下发上限执行。

### 6.3 成功响应

```json
{
  "frame": "response",
  "type": "agent.register",
  "id": "register-agent-001",
  "code": 0,
  "msg": "success",
  "data": {
    "accepted": true,
    "agentKey": "agent-001",
    "registrationId": "reg-001",
    "status": "REGISTERED",
    "capabilities": [
      "query",
      "steer",
      "interrupt",
      "hitl"
    ],
    "registeredAt": 1786502400000,
    "updatedAt": 1786502400000
  }
}
```

`status` 只返回：

- `REGISTERED`：首次注册。
- `UPDATED`：由当前 owner session 幂等完整替换，并保持原 `registrationId`。

### 6.4 业务拒绝

```json
{
  "frame": "response",
  "type": "agent.register",
  "id": "register-agent-001",
  "code": 400,
  "msg": "agent rejected",
  "data": {
    "accepted": false,
    "agentKey": "agent-001",
    "errorCode": "INVALID_CAPABILITY",
    "message": "unknown capability: execute"
  }
}
```

## 7. 解除注册

### 7.1 请求

```json
{
  "frame": "request",
  "type": "agent.unregister",
  "id": "unregister-agent-001",
  "payload": {
    "agentKey": "agent-001"
  }
}
```

### 7.2 成功响应

```json
{
  "frame": "response",
  "type": "agent.unregister",
  "id": "unregister-agent-001",
  "code": 0,
  "msg": "success",
  "data": {
    "accepted": true,
    "agentKey": "agent-001",
    "status": "UNREGISTERED",
    "drainingRunCount": 0
  }
}
```

幂等与所有权规则：

- Agent 由当前 session 持有：移除新 query 路由，返回 `UNREGISTERED`。
- `agentKey` 在当前 Platform-channel scope 不存在：返回 `code:0` 和 `NOT_REGISTERED`，便于安全重试。
- Agent 由同 scope 的其他活跃 session 持有：返回 `AGENT_NOT_OWNED`，当前 session 不得解除其他 session 的注册。

解除注册后，Gateway 立即拒绝该 Agent 的新 Query。已有 Query Stream 可以继续到终态，但不再允许 Steer、Interrupt 或 HITL Submit；重新注册相同 `agentKey` 也不会恢复旧 Run 的控制能力。`drainingRunCount` 表示仍在等待 stream 结束的 Run 数量。

## 8. 查询当前 Platform-channel 的全部注册 Agent

`agent.list` 查询的不是“当前 WebSocket session 自己注册的 Agent”，而是：

> 当前认证 Platform 在当前 channel scope 下，由所有活跃 session 成功注册且尚未解除的全部 Agent。

### 8.1 请求

```json
{
  "frame": "request",
  "type": "agent.list",
  "id": "list-agents-001",
  "payload": {}
}
```

v1 不接受客户端传入查询 scope、分页或过滤条件。Gateway 按 `agentKey` 稳定升序返回结果。

### 8.2 响应

```json
{
  "frame": "response",
  "type": "agent.list",
  "id": "list-agents-001",
  "code": 0,
  "msg": "success",
  "data": {
    "platformKey": "desktop-standard-ws",
    "count": 2,
    "currentSessionOwnedCount": 1,
    "agents": [
      {
        "agentKey": "agent-001",
        "name": "售后助手",
        "role": "售后支持",
        "description": "协助处理售后问题",
        "capabilities": [
          "query",
          "steer",
          "interrupt",
          "hitl"
        ],
        "ownedByCurrentSession": true,
        "registeredAt": 1786502400000,
        "updatedAt": 1786502400000
      },
      {
        "agentKey": "agent-002",
        "name": "数据助手",
        "description": "协助处理数据查询",
        "capabilities": [
          "query"
        ],
        "ownedByCurrentSession": false,
        "registeredAt": 1786502401000,
        "updatedAt": 1786502401000
      }
    ]
  }
}
```

语义：

- `count` 是当前 Platform-channel scope 下全部活跃注册 Agent 数。
- `currentSessionOwnedCount` 只是当前连接持有的数量，不影响 `agents` 的全 scope 返回范围。
- `ownedByCurrentSession` 用于告知当前连接是否有权调用 `agent.unregister`，不回显其他 session 的真实 `sessionId`。
- 结果只包含已成功且当前活跃的注册，不包含 pending、rejected、error、offline 或历史记录。
- 断开 session 自动清理或显式 unregister 完成后，对应 Agent 不再出现于列表。

Gateway 管理端如需查询跨 Platform、跨 channel、离线或历史注册，应使用独立管理 HTTP API，不扩大本 WebSocket `agent.list` 的权限范围。

Agent Platform 自动对账固定先执行一次 `agent.list`。只有初始列表成功后，才会解除当前 Session 已持有但本地不再导出的 Agent，并注册缺失项或完整更新差异项；完成后再次 list 验证。其他 Session 的项目只读不改。初始 list 失败时不执行任何变更，`NOT_REGISTERED` 按幂等成功处理。

## 9. 注册后的 Agent Query（Agent Platform 尚未升级）

Gateway 对外接收 Agent 调用后，先在当前 Platform-channel scope 中按 `agentKey` 查找活跃注册路由：

```text
(platformKey, agentKey) -> registrationId + owner session
```

只有同时满足以下条件才转发 query：

- Agent 仍处于活跃注册状态。
- owner session 仍在线。
- Agent 的展开后 capabilities 包含 `query`。
- Gateway 调用方已通过自身鉴权。
- Agent Platform 侧本地 channel export 权限仍允许 query。

### 9.1 Query 请求

Gateway 通过该 Agent 的 owner session 向 Agent Platform 发送：

```json
{
  "frame": "request",
  "type": "/api/query",
  "id": "query-001",
  "payload": {
    "requestId": "query-001",
    "runId": "run-001",
    "chatId": "chat-001",
    "agentKey": "agent-001",
    "role": "user",
    "message": "帮我查询最近一笔订单",
    "references": [],
    "params": {},
    "stream": true
  }
}
```

`payload.agentKey` 必须是 Gateway 注册表中的精确 `agentKey`，不允许 Gateway 使用 Agent Platform 未申报的本地 key 绕过注册路由。

### 9.2 Query Stream

Agent Platform 使用相同 `id` 返回流事件：

```json
{
  "frame": "stream",
  "id": "query-001",
  "streamId": "run-001",
  "event": {
    "type": "content.delta",
    "timestamp": 1786502402000,
    "payload": {
      "text": "正在查询。"
    }
  },
  "lastSeq": 12
}
```

run 以 `run.complete` / `run.error` / `run.cancel` 终态事件或明确的 stream 结束帧结束。Gateway 在 query 成功准入时必须建立：

```text
runId -> platform-channel scope + agentKey + owner session
```

后续 steer / interrupt / HITL 必须复用该 run 路由，不得根据同名 Agent 的新 session 重新选路。

## 10. Run 控制协议（Agent Platform 尚未升级）

### 10.1 Steer

前置条件：Agent 注册 capabilities 包含 `steer`。

```json
{
  "frame": "request",
  "type": "/api/steer",
  "id": "steer-001",
  "payload": {
    "requestId": "steer-request-001",
    "runId": "run-001",
    "chatId": "chat-001",
    "agentKey": "agent-001",
    "steerId": "steer-001",
    "message": "优先检查最近一笔订单"
  }
}
```

Gateway 必须等待相同 `id` 的 `response` 或 `error`，不得仅因为 WebSocket 写入成功就向上游声明 steer 已成功。

### 10.2 Interrupt

前置条件：Agent 注册 capabilities 包含 `interrupt`。

```json
{
  "frame": "request",
  "type": "/api/interrupt",
  "id": "interrupt-001",
  "payload": {
    "requestId": "interrupt-001",
    "runId": "run-001",
    "chatId": "chat-001",
    "agentKey": "agent-001",
    "message": "停止执行",
    "source": "gateway"
  }
}
```

Gateway 必须等待相同 `id` 的 `response` 或 `error`。

### 10.3 HITL Submit

前置条件：Agent 注册 capabilities 包含 `hitl`。

Agent Platform 在 query stream 中发送 question / approval / form / planning 等 awaiting 事件，Gateway 收集用户答案后向原 run owner session 发送：

```json
{
  "frame": "request",
  "type": "/api/submit",
  "id": "submit-001",
  "payload": {
    "runId": "run-001",
    "chatId": "chat-001",
    "agentKey": "agent-001",
    "awaitingId": "awaiting-001",
    "submitId": "submit-001",
    "params": {
      "answer": "确认"
    }
  }
}
```

Gateway 必须等待 submit ACK。`hitl` 是业务能力名，`/api/submit` 是传输接口；Agent 注册中不重复申报 `submit` capability。

## 11. 路由错误（Agent Platform 尚未升级）

Gateway 在转发 query 或 run 控制前应返回稳定错误：

| errorCode | 建议 code | 语义 |
|---|---:|---|
| `AGENT_NOT_REGISTERED` | 404 | 当前 Platform-channel scope 不存在该 Agent |
| `AGENT_OFFLINE` | 503 | 注册记录正在清理或 owner session 不可用 |
| `CAPABILITY_NOT_SUPPORTED` | 409 | Agent 未申报对应能力 |
| `RUN_NOT_FOUND` | 404 | run 不存在或已清理 |
| `RUN_OWNER_MISMATCH` | 403 | run 不属于该 Agent / Platform-channel scope |
| `UPSTREAM_DISCONNECTED` | 503 | run owner session 断开，无法继续调用 |

## 12. 注册错误码

| errorCode | 建议 code | 语义 |
|---|---:|---|
| `REGISTRATION_NOT_READY` | 409 | 尚未完成 `connected` 应用层初始化 |
| `REGISTRATION_MODE_MISMATCH` | 403 | Token / registration mode 不允许当前注册方式 |
| `INVALID_AGENT_KEY` | 400 | `agentKey` 缺失或格式不合法 |
| `INVALID_AGENT_NAME` | 400 | `name` 缺失或格式不合法 |
| `INVALID_AGENT_METADATA` | 400 | role / description 超限或含不允许内容 |
| `INVALID_CAPABILITY` | 400 | capabilities 为空、未知、重复或 `all` 混用 |
| `AGENT_KEY_IN_USE` | 409 | 同 scope 的其他活跃 session 已持有该 key |
| `AGENT_LIMIT_EXCEEDED` | 409 | 超过 Platform-channel scope 注册上限 |
| `AGENT_NOT_OWNED` | 409 | 当前 session 尝试解除其他 session 的 Agent |
| `INTERNAL_ERROR` | 500 | Gateway 内部错误 |

5xx 和网络超时可重试；4xx 默认不自动重试，应等待配置变更、旧 session 清理或重连。`agent.register` 是同 owner session 下的幂等完整替换，因响应丢失而重试不会创建重复 Agent。

## 13. 时间字段

所有 `timestamp`、`registeredAt`、`updatedAt` 均为 Unix epoch milliseconds（JSON number / Go `int64`），不使用秒级 Unix 时间，也不在同一协议中混用 ISO-8601 字符串。

## 14. 推荐时序

```text
Agent Platform                         Gateway
      |                                  |
      |------ WebSocket + Bearer ------->|
      |<--------- push connected --------|
      |                                  |
      |----------- agent.list ---------->|
      |<-- current platform-channel all --|
      |                                  |
      |-------- agent.unregister -------->|
      |<-- UNREGISTERED/NOT_REGISTERED ---|
      |--------- agent.register -------->|
      |<-------- REGISTERED/UPDATED ------|
      |--------- agent.register -------->|
      |<-------- REGISTERED/UPDATED ------|
      |                                  |
      |----------- agent.list ---------->|
      |<-- current platform-channel all --|
      |                                  |
      |<----------- /api/query -----------|
      |------------ stream -------------->|
      |<----- /api/steer|interrupt -------|
      |------------ response ------------>|
      |<---------- /api/submit -----------|
      |------------ response ------------>|
      |                                  |
      |----------- disconnect ----------->|
      |     Gateway cleans session-owned  |
```

## 15. 版本切换

本协议不兼容替换旧 Agent Card 协议，不保留 Skill / Tool / Tag / KBASE 隐式 Skill 卡片字段。建议 Gateway 与 Agent Platform 同版本发布，并通过 `connected.data.agentRegistration.version` 在应用层显式协商：

- Gateway 未声明 `agentRegistration.version: "1"` 时，Agent Platform 不发送新注册请求。
- Agent Platform 不发送旧注册帧，也不做兼容回退。
- Gateway 不应把未识别的 capability 默认解释为已授权能力。
