# 接口规范（含错误码）

## 基础协议
- Base URL: `/api/ap`
- 编码：`application/json; charset=UTF-8`
- 流式接口：`POST /api/ap/query`，`text/event-stream`
- 鉴权：Bearer Token（`/api/ap/data?t=...` 可走 chat image token 特例，详见 `AUTH.md`）

## 非 SSE 响应壳
成功：
```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

失败：
```json
{
  "code": 400,
  "msg": "Validation failed",
  "data": {
    "fields": {
      "fieldName": "error message"
    }
  }
}
```

## HTTP 状态码与 `code`
| `code` | HTTP | 含义 |
|---|---|---|
| `0` | `200` | success |
| `400` | `400` | 参数错误 / 校验失败 / 非法业务参数 |
| `401` | `401` | unauthorized（JWT 校验失败） |
| `403` | `403` | token 无权限或无效（主要 `/api/ap/data`） |
| `404` | `404` | 资源不存在 |
| `500` | `500` | Internal server error |

## `/api/ap/data` 扩展错误码
当发生 token 授权失败时，`data.errorCode` 取值：
| errorCode | 含义 |
|---|---|
| `CHAT_IMAGE_TOKEN_INVALID` | token 无效、scope 不匹配、chat/file 未授权 |
| `CHAT_IMAGE_TOKEN_EXPIRED` | token 已过期 |

## 全局异常映射
- `IllegalArgumentException -> 400`
- `MethodArgumentNotValidException -> 400`（`data.fields`）
- `ChatNotFoundException -> 404`
- `ResponseStatusException -> 对应状态码`
- 其他异常 -> `500` + `Internal server error`

## 参数通用约束
- 无全局分页参数规范；若需要由模块文档单独定义。
- UUID 字段（如 `chatId`）必须是合法 UUID。

## SSE 基础约束
所有 SSE 事件均带：
- `seq`：事件序号
- `type`：事件类型
- `timestamp`：毫秒时间戳

SSE 错误表达：
- 运行期错误通过 `run.error` 事件表达。
- SSE 不使用上面的 JSON 错误壳作为流内错误协议。

## SSE 关键事件分组
- 输入/请求：`request.query`、`request.upload`、`request.submit`
- 会话/运行：`chat.start`、`run.start`、`run.complete`、`run.error`
- 文本流：`reasoning.start|delta|end`、`content.start|delta|end`
- 工具流：`tool.start|args|end|result`
- 动作流：`action.start|args|end|result`
- 历史快照：`reasoning.snapshot`、`content.snapshot`、`tool.snapshot`、`action.snapshot`

## `/submit` 事件命名约束（强制）
- `POST /api/ap/submit` 的流内回填事件主名为 `request.submit`。
- 语义：tool 参数回填（tool param backfill）。
- `tool.param` 不是当前契约主事件名，不得作为主名使用。

## [DOC-GAP] 规则
当实现与规范不一致时：
1. 先在相关文档标记 `[DOC-GAP]`。
2. 明确冲突代码位置与现象。
3. 记录修复结论到 `changelog/`。
