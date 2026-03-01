# 认证与授权

## 1. API JWT 鉴权（`/api/ap/**`）
`ApiJwtAuthWebFilter` 对 `/api/ap/**` 生效（`OPTIONS` 放行）。

### 请求要求
- Header: `Authorization: Bearer <token>`

### 失败行为
- token 缺失/格式错误/验签失败/claim 不合法 -> `401`
- body: `{"error":"unauthorized"}`

## 2. JWT 验证规则
`JwksJwtVerifier` 规则：
- token 必须可解析为 `SignedJWT`
- 必须有 `sub`
- 必须有未过期 `exp`
- 若配置了 `issuer`，必须严格匹配
- 验签顺序：local public key -> JWKS keys

### 配置约束
`agent.auth.jwks-uri`、`agent.auth.issuer`、`agent.auth.jwks-cache-seconds` 必须同时配置。

## 3. `/api/ap/data` 特例（chat image token）
当 `agent.chat-image-token.data-token-validation-enabled=true` 且请求为 `GET /api/ap/data?...&t=<token>`：
- JWT Bearer 可不提供。
- 改走 `ChatImageTokenService.verify(t)`。

### chat image token 校验
- scope 必须包含 `ap_data:read`
- token 必须未过期
- 必须能映射到合法 `chatId`
- 还需通过 `ChatAssetAccessService.canRead(chatId,file)`

### 失败行为
- 返回 `403`，并携带 `data.errorCode`
- 错误码：
  - `CHAT_IMAGE_TOKEN_INVALID`
  - `CHAT_IMAGE_TOKEN_EXPIRED`

## 4. chat image token 签发
- 触发点：
  - `GET /api/ap/chat` 返回体字段 `chatImageToken`
  - `POST /api/ap/query` 的 `chat.start` 事件会附加 `chatImageToken`
- 前置条件：请求已通过 JWT，且有 `subject`。
