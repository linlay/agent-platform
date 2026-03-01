# Request 模块 API（query / submit / upload规划）

## 1. `POST /api/ap/query`
发起一次 Agent 运行并返回 SSE 事件流。

### 请求体（`QueryRequest`）
| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `requestId` | string | 否 | 不传时默认等于本次 `runId` |
| `chatId` | string | 否 | 不传则生成 UUID；传入必须是 UUID |
| `agentKey` | string | 否 | 可为空；若 chat 已绑定 agent，优先使用绑定值 |
| `role` | string | 否 | 默认 `user` |
| `message` | string | 是 | `@NotBlank` |
| `references` | array | 否 | 外部引用元信息 |
| `params` | object | 否 | 扩展参数 |
| `scene` | object | 否 | `{url,title}` |
| `stream` | boolean | 否 | 保留字段 |

### 关键服务端行为
- 生成 `runId`：base36 的 epoch millis。
- `chat.start` 仅在会话首次 run 发出一次。
- 结束发 `run.complete`；运行异常发 `run.error`。
- frontend tool 事件会补充 `toolKey`、`toolType`、`toolTimeout`。

### 失败场景
| 场景 | HTTP | 表现 |
|---|---|---|
| `message` 为空 | 400 | 请求直接失败 |
| `chatId` 非 UUID | 400 | 请求直接失败 |
| 运行中模型/工具异常 | 200 | SSE 流内出现 `run.error` |

## 2. `POST /api/ap/submit`
用于 frontend tool 的人机回填。

### 请求体（`SubmitRequest`）
| 字段 | 类型 | 必填 | 约束 |
|---|---|---|---|
| `runId` | string | 是 | `@NotBlank` |
| `toolId` | string | 是 | `@NotBlank` |
| `params` | any | 是 | `@NotNull` |

### 成功响应（`SubmitResponse`）
| 字段 | 含义 |
|---|---|
| `accepted` | 是否命中 pending frontend tool |
| `status` | `accepted` 或 `unmatched` |
| `runId` | 原样返回 |
| `toolId` | 原样返回 |
| `detail` | 详细说明 |

### 行为规则
- 命中 pending：`accepted=true`，唤醒 `awaitSubmit`。
- 未命中：`accepted=false,status=unmatched`，HTTP 仍为 200。
- `params=null` 非法（400）。
- 流式主事件名为 `request.submit`（语义：tool 参数回填）。

## 3. Upload（规划中）
当前版本无公开 upload endpoint。

约束：
- upload 仅作为 request 模块预留能力，不在当前文档声明未实现路径与字段。
- 若后续落地，需同步更新 `SPEC.md` 与 `DATA_FLOW.md` 的请求事件章节。

## SSE 事件序列边界（强约束）
### Tool 参数流边界
- `tool.start`
- `tool.args`（可多次）
- `tool.end`
- `tool.result`

说明：`tool.end` 表示该 `tool.args` 参数流完整结束。

### Action 参数流边界
- `action.start`
- `action.args`（可多次）
- `action.end`
- `action.result`

说明：`action.end` 表示该 `action.args` 参数流完整结束。

### Frontend tool 回填链路（事件视角）
`tool.start(frontend)` -> 等待 `/submit` -> `request.submit` -> `tool.result`。

## 历史回放说明
`GET /api/ap/chat` 回放的是 snapshot 粒度：
- `reasoning.snapshot`
- `content.snapshot`
- `tool.snapshot`
- `action.snapshot`
并保留 `tool.result` / `action.result` / `run.complete`。
