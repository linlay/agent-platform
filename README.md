# agw-springai-agent

本仓库是可独立构建和部署的 Spring AI Agent 服务，已经改为直接引用仓库内的 SDK jar，不依赖本地 Maven 安装。

## 提供接口

- `GET /api/agents`: 智能体列表
- `GET /api/agent?agentKey=...`: 智能体详情
- `GET /api/chats`: 会话列表
- `GET /api/chat?chatId=...`: 会话详情（默认返回快照事件流）
- `GET /api/chat?chatId=...&includeRawMessages=true`: 会话详情（附带原始 messages）
- `GET /api/viewport?viewportKey=...`: 获取工具/动作视图内容
- `POST /api/query`: 提问接口（默认返回 AGW 标准 SSE；`requestId` 可省略，缺省时等于 `runId`）
- `POST /api/submit`: Human-in-the-loop 提交接口

## 返回格式约定

- `POST /api/query` 返回 SSE event stream。
- 其它 JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

- `code = 0` 表示成功，`code > 0` 表示失败（整型错误码），`msg` 为错误信息，`data` 为返回数据。
- `data` 直接放业务内容，不再额外包同名字段，例如：
  - 智能体列表：`data` 直接是 `agents[]`
  - 智能体详情：`data` 直接是 `agent`
  - 会话详情：`data` 直接是 `chat`
  - 视图详情：`data` 直接是视图内容（`html` 时为 `{ "html": "..." }`，`qlc/dqlc` 时为 schema JSON）
- `GET /api/chat` 默认始终返回 `events`；仅当 `includeRawMessages=true` 时才返回 `messages`。
- `includeEvents` 参数已废弃，传入将返回 `400`。
- 事件协议仅支持 AGW Event Model v2，不兼容旧命名（如 `query.message`、`message.start|delta|end`、`message.snapshot`）。

`GET /api/chats` 示例（新增 `updatedAt`）：

```json
{
  "code": 0,
  "msg": "success",
  "data": [
    {
      "chatId": "d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656",
      "chatName": "元素碳的简介，100",
      "firstAgentKey": "demoPlain",
      "createdAt": 1770866044047,
      "updatedAt": 1770866412459
    }
  ]
}
```

`GET /api/chat` 的 `data` 结构如下：

```json
{
  "chatId": "8cdb2094-9dbf-47d1-a17f-bc989a236a5c",
  "chatName": "元素碳的简介，100",
  "events": [
    {
      "seq": 1,
      "type": "request.query",
      "requestId": "8ad0081d-191b-4990-9432-664ea0c38c3e",
      "chatId": "8cdb2094-9dbf-47d1-a17f-bc989a236a5c",
      "role": "user",
      "message": "元素碳的简介，100字",
      "timestamp": 1770863186548
    },
    {
      "seq": 5,
      "type": "content.snapshot",
      "contentId": "8ad0081d-191b-4990-9432-664ea0c38c3e_content_0",
      "text": "碳是一种非金属元素...",
      "timestamp": 1770863186549
    }
  ],
  "references": []
}
```

当 `includeRawMessages=true` 时，会额外返回：

```json
"messages": [
  {
    "role": "user",
    "content": "元素碳的简介，100字",
    "ts": 1770863186548,
    "runId": "8ad0081d-191b-4990-9432-664ea0c38c3e"
  }
]
```

## 目录约定

```text
.
├── libs/
│   └── agw-springai-sdk-0.0.1-SNAPSHOT.jar
├── src/
├── agents/
├── viewports/
├── tools/
├── pom.xml
├── settings.xml
└── Dockerfile
```

## SDK jar 放置方式

从旁边的 `agw-springai-sdk` 项目构建后，将 jar 放到本仓库 `libs/`：

```bash
cp ../agw-springai-sdk/target/agw-springai-sdk-0.0.1-SNAPSHOT.jar ./libs/
```

`pom.xml` 已固定通过 `systemPath` 引用 `libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar`，可以直接提交到 Git。

## 本地运行

```bash
mvn clean test
mvn spring-boot:run
```

默认端口 `8080`。

## 接口测试用例

### 会话接口测试

```bash
curl -N -X GET "http://localhost:8080/api/chats" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "http://localhost:8080/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "http://localhost:8080/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656&includeRawMessages=true" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "http://localhost:8080/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656&includeEvents=true" \
  -H "Content-Type: application/json"
```

### Query 回归测试

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"给我一个微服务网关的落地方案，100字内","agentKey":"demoPlanExecute"}'
```

## settings.xml 说明

- `settings.xml` 作为构建镜像时 Maven 配置，会被 `Dockerfile` 拷贝到 Maven 全局配置目录。
- 当前配置使用 `central` 的阿里云镜像，加速依赖下载。

## agents 目录

- `agents/*.json` 文件名（不含 `.json`）即 `agentId`
- 服务启动时会先加载一次，之后每 10 秒刷新一次缓存（默认值）
- 可通过 `AGENT_EXTERNAL_DIR` 指定目录，通过 `AGENT_REFRESH_INTERVAL_MS` 调整刷新间隔
- `systemPrompt` 同时支持标准 JSON 字符串和 `"""` 多行写法（仅 `systemPrompt`）

标准 JSON：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": "你是算命大师",
  "mode": "PLAIN",
  "tools": []
}
```

多行写法：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": """
你是算命大师
请先问出生日期
""",
  "mode": "RE_ACT",
  "tools": ["bash", "city_datetime"]
}
```

`mode` 支持：
- `PLAIN`（默认直答；当配置了 tools 时，会先决策是否调用工具，最多调用 1 次）
- `RE_ACT`（兼容旧值 `THINKING_AND_CONTENT`）
- `PLAN_EXECUTE`（兼容旧值 `THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS`）

当 `tools` 非空时，服务会按 OpenAI 兼容的原生 Function Calling 协议请求模型：
- 请求体包含 `tools[]`
- 流式消费 `delta.tool_calls`
- 不再依赖正文中的 `toolCall/toolCalls` JSON 字段（仍保留向后兼容解析）

### 真流式约束（CRITICAL）

- `/api/query` 全链路严格真流式：上游 LLM 每到一个 delta，立即下发对应 AGW 事件，禁止先 `collect/reduce/block` 再输出。
- 禁止将多个 delta 合并后再切片发送；输出粒度以“上游 delta 语义块”为准。
- 工具调用必须保持事件顺序：`tool.start` -> `tool.args`（可多次）-> `tool.end` -> `tool.result`。
- `VerifyPolicy.SECOND_PASS_FIX` 场景下，首轮候选答案仅内部使用；对外只流式下发二次校验生成的 chunk。

## viewports / tools 目录

- 运行目录默认值：
  - `viewports/`
  - `tools/`
- 启动时会尝试将 `src/main/resources/viewports|tools` 下文件同步到运行目录（不覆盖同名文件）。
- `viewports` 支持后缀：`.html`、`.qlc`、`.dqlc`、`.json_schema`、`.custom`，默认每 30 秒刷新内存快照。
- `tools`:
  - 后端工具文件：`*.backend`
  - 前端工具文件：`*.html` / `*.qlc` / `*.dqlc`
  - 动作文件：`*.action`
  - 文件内容均为模型工具定义 JSON（`{"tools":[...]}`）
- `show_weather_card` 当前仅作为 viewport（`viewports/show_weather_card.html`），不是可调用 tool。
- 工具名冲突策略：冲突项会被跳过，其它项继续生效。

### /api/viewport 约定

- `GET /api/viewport?viewportKey=weather_card`
- `chatId`、`runId` 为可选参数，不参与必填校验。
- 返回：
  - `html` 文件：`data = {"html":"<...>"}`
  - `qlc/dqlc/json_schema/custom`：`data` 直接是文件内 JSON 对象
- `viewportKey` 不存在时返回 `404`。

### viewport 输出协议示例

```viewport
type=html, key=show_weather_card
{
  "city": "Shanghai",
  "date": "2026-02-13",
  "temperatureC": 22,
  "humidity": 61,
  "windLevel": 3,
  "condition": "Partly Cloudy",
  "mockTag": "idempotent-random-json"
}
```

### 前端 tool 提交流程

- 前端工具触发后会发送 `tool.start`（`toolType` 为 `html/qlc/dqlc`），并等待 `/api/submit`。
- 默认等待超时 `5 分钟`（可配置）。
- `POST /api/submit` 成功命中后会释放对应 `runId + toolId` 的等待。
- 工具返回值提取规则：
  - 优先返回 `payload.params`
  - 若无 `params`，返回 `{}`。
- 动作工具触发 `action.start` 后不等待提交，直接返回 `"OK"` 给模型。
- 动作事件顺序：`action.start` -> `action.args`（可多次）-> `action.end` -> `action.result`。

### 内置 action 能力

- `switch_theme(theme)`：主题切换，`theme` 仅支持 `light/dark`。
- `launch_fireworks(durationMs?)`：播放烟花特效，`durationMs` 可选（毫秒）。
- `show_modal(title, content, closeText?)`：弹出模态框，`title/content` 必填，`closeText` 可选。

## 内置智能体

- `demoViewport`（`PLAN_EXECUTE`）：调用 `city_datetime`、`mock_city_weather`，最终按 `viewport` 代码块协议输出天气卡片数据。
- `demoAction`（`PLAIN`）：根据用户意图调用 `switch_theme` / `launch_fireworks` / `show_modal`。
- `agentCreator`（`PLAN_EXECUTE`）：调用 `agent_file_create` 创建/更新 `agents/{agentId}.json`。
- 使用 `agentCreator` 时建议提供：`agentId`、`description`、`systemPrompt`、`providerType`、`model`、`deepThink`。
- `agent_file_create` 会校验 `agentId`（仅允许 `A-Za-z0-9_-`，最长 64）。
- `providerType` 不做白名单校验；未提供时默认 `BAILIAN`。
- 生成格式：

```json
{
  "description": "算命大师",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": "你是算命大师",
  "deepThink": false
}
```

- `systemPrompt` 为多行时会写成 `"""` 形式

## Bash 工具目录授权

`bash` 工具默认仅允许访问工作目录（`user.dir`）。若需要让 Agent 在容器内读取 `/opt` 等目录，可配置：

```yaml
agent:
  tools:
    bash:
      working-directory: /opt/app
      allowed-paths:
        - /opt
```

也可使用环境变量：

```bash
AGENT_BASH_WORKING_DIRECTORY=/opt/app
AGENT_BASH_ALLOWED_PATHS=/opt,/data
```

动态目录相关环境变量：

```bash
AGENT_VIEWPORT_EXTERNAL_DIR=/opt/viewports
AGENT_VIEWPORT_REFRESH_INTERVAL_MS=30000
AGENT_TOOLS_EXTERNAL_DIR=/opt/tools
AGENT_CAPABILITY_REFRESH_INTERVAL_MS=30000
AGENT_TOOLS_FRONTEND_SUBMIT_TIMEOUT_MS=300000
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"元素碳的简介，200字","agentKey":"demoPlain"}'
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"chatId":"","message":"下一个元素的简介","agentKey":"demoPlain"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"【确认是否有敏感信息】本项目突破传统竖井式系统建设模式，基于1+1+3+N架构（1个企业级数据库、1套OneID客户主数据、3类客群CRM系统整合优化、N个展业数字化应用），打造了覆盖展业全生命周期、贯通公司全客群管理的OneLink分支一体化数智展业服务平台。在数据基础层面，本项目首创企业级数据库及OneID客户主数据运作体系，实现公司全域客户及业务数据物理入湖，并通过事前注册、事中应用管理、事后可分析的机制，实现个人、企业、机构三类客群千万级客户的统一识别与关联。","agentKey":"demoPlain"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"我周日要搬迁机房到上海，你先对当前服务器做一下检测，然后决定下搬迁条件","agentKey":"demoReAct"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"规划上海机房明天搬迁的实施计划，你要先列给我看计划，然后再一步步落实","agentKey":"demoPlanExecute"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"帮我查上海明天天气并展示卡片","agentKey":"demoViewport"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"切换到深色主题","agentKey":"demoAction"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"放一场 8 秒的烟花","agentKey":"demoAction"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"弹一个模态框，标题是系统通知，内容是发布成功，按钮写关闭","agentKey":"demoAction"}'
```
