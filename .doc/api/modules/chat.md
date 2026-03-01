# Chat 模块 API（chat / chats / read / data / viewport）

## 1. `GET /api/ap/chats`
查询会话摘要列表。

### Query 参数
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `lastRunId` | string | 否 | 增量拉取：返回 `LAST_RUN_ID_ > lastRunId` 的会话 |

### 成功响应
`data` 为 `ChatSummaryResponse[]`：
- `chatId`, `chatName`, `agentKey`
- `createdAt`, `updatedAt`
- `lastRunId`, `lastRunContent`
- `readStatus`（0 未读 / 1 已读）, `readAt`

## 2. `POST /api/ap/read`
标记会话已读。

### Body
| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `chatId` | string | 是 | `@NotBlank` + 合法 UUID |

### 失败场景
| 场景 | HTTP | code |
|---|---|---|
| `chatId` 空字符串 | 400 | 400 |
| `chatId` 非 UUID | 400 | 400 |
| `chatId` 不存在 | 404 | 404 |

## 3. `GET /api/ap/chat`
读取单个 chat 详情与历史回放。

### Query 参数
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `chatId` | string | 是 | UUID |
| `includeRawMessages` | boolean | 否 | 默认 `false` |

### 成功响应（`ChatDetailResponse`）
- `chatId`, `chatName`
- `chatImageToken`（请求上下文有 JWT subject 时返回）
- `events`（总是返回）
- `rawMessages`（仅 `includeRawMessages=true` 返回）
- `references`（query 引用聚合）

### 回放规则
- 历史默认用 snapshot 事件（`reasoning/content/tool/action`）。
- `chat.start` 在同一 chat 历史中只保留一次。
- 每个 run 保留 `run.complete`。

### 失败场景
| 场景 | HTTP | code |
|---|---|---|
| `chatId` 非 UUID | 400 | 400 |
| chat 不存在 | 404 | 404 |

## 4. `GET /api/ap/data`
按文件路径读取静态文件。

### Query 参数
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `file` | string | 是 | 相对 `agent.data.external-dir` 路径 |
| `download` | boolean | 否 | 默认 `false` |
| `t` | string | 否 | chat image token |

### 规则
- `download=true` 或非 image 类型：`Content-Disposition=attachment`
- image 且 `download=false`：`inline`
- 若开启 data token 校验且提供 `t`：需通过 token + scope(`ap_data:read`) + chat/file 访问校验

### 失败场景
| 场景 | HTTP | code | 说明 |
|---|---|---|---|
| `file` 非法/路径逃逸 | 400 | 400 | `Invalid file parameter` 等 |
| token 无效/过期/无权限 | 403 | 403 | `data.errorCode` 给出 token 错误码 |
| 文件不存在 | 404 | 404 | `File not found` |
| 文件读取异常 | 500 | 500 | `Failed to read file` |

## 5. `GET /api/ap/viewport?viewportKey=...`
按 key 返回渲染模板。

### Query 参数
| 字段 | 类型 | 必填 |
|---|---|---|
| `viewportKey` | string | 是 |

### 响应规则
- `.html`：`data = {"html":"<...>"}`
- `.qlc`：`data = <qlc JSON 对象>`

### 失败场景
| 场景 | HTTP | code |
|---|---|---|
| `viewportKey` 为空 | 400 | 400 |
| key 不存在 | 404 | 404 |

## chatImageToken 关联关系
- 签发入口：
  - `GET /api/ap/chat` 响应字段 `chatImageToken`
  - `POST /api/ap/query` 的 `chat.start` 事件附加 `chatImageToken`
- 典型用途：访问 `/api/ap/data?file=...&t=<chatImageToken>`
