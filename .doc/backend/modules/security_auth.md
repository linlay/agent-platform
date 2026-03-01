# 安全与认证（security_auth）

## 关键类
- `ApiJwtAuthWebFilter`
- `JwksJwtVerifier`
- `ChatImageTokenService`
- `ChatAssetAccessService`

## API JWT 流程
1. 命中 `/api/ap/**` 且非 OPTIONS。
2. 若是 `/api/ap/data` 且带 `t` 且 data-token 校验开启：跳过 Bearer JWT。
3. 否则解析 Bearer token 并调用 `JwksJwtVerifier.verify`。
4. 验证通过后把 principal 放入 exchange attributes。

## chat image token 流程
- 使用 HMAC SHA-256（secret 经 SHA-256 派生 key）。
- claims: `e`(过期), `c`(chatId), `u`(uid)。
- 支持 `previous-secrets` 做密钥轮换。

## 文件级访问控制
`/api/ap/data` 在 token 校验后还需：
- scope = `ap_data:read`
- `ChatAssetAccessService.canRead(chatId,file)` 为 true

## 安全边界
- 所有 `/api/ap/**` 默认受 JWT 保护（除 data token 特例）。
- token 校验失败不泄露内部细节，仅返回统一错误。
