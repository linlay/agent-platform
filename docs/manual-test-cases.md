# 手动测试用例 (curl)

## 环境变量

```bash
# 本地 make run 与 Docker Compose 默认都走 HOST_PORT=11949
BASE_URL="http://localhost:11949"
# 若你绕过 make run 直接执行 go run，可切换为 SERVER_PORT 或默认 8080
# BASE_URL="http://localhost:8080"
```

## Catalog 接口

```bash
curl -X GET "$BASE_URL/api/agents"
```

```bash
curl -X GET "$BASE_URL/api/agent?agentKey=go_runner"
```

```bash
curl -X GET "$BASE_URL/api/teams"
```

```bash
curl -X GET "$BASE_URL/api/skills"
```

```bash
curl -X GET "$BASE_URL/api/tools"
```

```bash
curl -X GET "$BASE_URL/api/tool?toolName=_bash_"
```

## 会话接口

```bash
curl -X GET "$BASE_URL/api/chats"
```

```bash
curl -X GET "$BASE_URL/api/chats?lastRunId=test-run-id"
```

```bash
curl -X GET "$BASE_URL/api/chats?agentKey=go_runner"
```

```bash
curl -X POST "$BASE_URL/api/read" \
  -H "Content-Type: application/json" \
  -d '{"chatId":"replace-me"}'
```

```bash
curl -X GET "$BASE_URL/api/chat?chatId=replace-me"
```

```bash
curl -X GET "$BASE_URL/api/chat?chatId=replace-me&includeRawMessages=true"
```

## Query 回归测试

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"元素碳的简介，100字","agentKey":"go_runner"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"列出当前仓库的 Go 文件数量，并说明你会调用什么工具","agentKey":"go_runner"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"chatId":"replace-me","message":"继续上一轮内容","agentKey":"go_runner"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","message":"使用指定 runId 发起一次请求","agentKey":"go_runner"}'
```

## Submit / Steer / Interrupt 占位测试

这些接口当前返回最小 ack，主要用于校验 API 契约，而不是完整的人机协作能力。

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","toolId":"tool_01","params":{"confirmed":true}}'
```

```bash
curl -X POST "$BASE_URL/api/steer" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","message":"换个角度总结"}'
```

```bash
curl -X POST "$BASE_URL/api/interrupt" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me"}'
```

## Remember / Learn

```bash
curl -X POST "$BASE_URL/api/remember" \
  -H "Content-Type: application/json" \
  -d '{"requestId":"remember_01","chatId":"replace-me"}'
```

```bash
curl -X POST "$BASE_URL/api/learn" \
  -H "Content-Type: application/json" \
  -d '{"requestId":"learn_01","chatId":"replace-me"}'
```

## Upload / Resource

```bash
curl -X POST "$BASE_URL/api/upload" \
  -F "requestId=upload_01" \
  -F "chatId=replace-me" \
  -F "file=@./README.md"
```

上传成功后，使用响应中的 `data.upload.url` 访问资源：

```bash
curl -X GET "$BASE_URL/api/resource?file=replace-me%2FREADME.md"
```

## Viewport

```bash
curl -X GET "$BASE_URL/api/viewport?viewportKey=confirm_dialog"
```
