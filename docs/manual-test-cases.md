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

## Submit / Steer / Interrupt

`/api/submit` 当前使用统一 HITL 协议：

- 顶层固定是 `runId + awaitingId + params`
- `params` 永远是数组
- 不在 submit 里再传 `mode`
- 后端按 `awaitingId` 反查当前是 `question / approval / form`
- `params[i]` 固定对应 `awaiting.ask` 下发数组的第 `i` 项；`id` 可省略，即使携带也只作审计用途

question:

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","awaitingId":"tool_question","params":[{"id":"q1","answer":"Weekend"},{"id":"q2","answers":["产品更新","使用教程"]}]}'
```

也可以省略 `id`，只要顺序与 `awaiting.ask.questions` 一致：

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","awaitingId":"tool_question","params":[{"answer":"Weekend"},{"answers":["产品更新","使用教程"]}]}'
```

approval:

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","awaitingId":"await_tool_bash","params":[{"id":"tool_bash","decision":"approve_prefix_run","reason":"同类命令本轮一并放行"}]}'
```

form:

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","awaitingId":"await_tool_bash","params":[{"id":"form-1","payload":{"applicant":{"name":"Lin","department":"engineering","employee_id":"E1001"},"leave_type":"事假","start_date":"2026-04-21","end_date":"2026-04-22","duration_days":2,"reason":"family_trip","urgent_contact":"Amy","urgent_phone":"13800138000","backup_person":"E2001","notes":"请协助处理审批"}}]}'
```

整批取消：

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{"runId":"replace-me","awaitingId":"tool_question","params":[]}'
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

如果部署开启了 resource ticket，需要把 chat 详情或其他接口返回的 ticket 透传到 `t` 参数：

```bash
curl -X GET "$BASE_URL/api/resource?file=replace-me%2FREADME.md&t=replace-me-ticket"
```

说明：

- 浏览器/普通客户端的文件字节始终走 `POST /api/upload` 与 `GET /api/resource`
- WebSocket `/api/upload` 仅用于网关中转 `url + metadata`，由 platform 自己下载文件，不支持直接在 WS payload 中放 base64/二进制内容

## Viewport

question / approval 使用 builtin dialog，不必单独取 viewport；只有 `form` 需要 HTML viewport：

```bash
curl -X GET "$BASE_URL/api/viewport?viewportKey=leave_form"
```
