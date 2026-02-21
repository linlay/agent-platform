# springai-agent-platform

本仓库是可独立构建和部署的 Spring AI Agent 服务，已经改为直接引用仓库内的 SDK jar，不依赖本地 Maven 安装。

> 详细架构设计、数据模型、API 契约和开发约束见 [CLAUDE.md](./CLAUDE.md)。

## 提供接口

- `GET /api/agents`: 智能体列表
- `GET /api/agent?agentKey=...`: 智能体详情
- `GET /api/chats`: 会话列表
- `GET /api/chat?chatId=...`: 会话详情（默认返回快照事件流）
- `GET /api/chat?chatId=...&includeRawMessages=true`: 会话详情（附带原始 messages）
- `GET /api/viewport?viewportKey=...`: 获取工具/动作视图内容
- `POST /api/query`: 提问接口（默认返回 SDK 标准 SSE；`requestId` 可省略，缺省时等于 `runId`）
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
- 事件协议仅支持 SDK Event Model v2，不兼容旧命名（如 `query.message`、`message.start|delta|end`、`message.snapshot`）。

`GET /api/chats` 示例（新增 `updatedAt`）：

```json
{
  "code": 0,
  "msg": "success",
  "data": [
    {
      "chatId": "d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656",
      "chatName": "元素碳的简介，100",
      "firstAgentKey": "demoModePlain",
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
├── skills/
├── viewports/
├── tools/
├── pom.xml
├── settings.xml
└── Dockerfile
```

## 构建与运行

### SDK jar 放置方式

从旁边的 `agw-springai-sdk` 项目构建后，将 jar 放到本仓库 `libs/`：

```bash
cp ../agw-springai-sdk/target/agw-springai-sdk-0.0.1-SNAPSHOT.jar ./libs/
```

`pom.xml` 已固定通过 `systemPath` 引用 `libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar`，可以直接提交到 Git。

### 本地运行

```bash
mvn clean test
mvn spring-boot:run
```

默认端口 `8080`。

### settings.xml 说明

- `settings.xml` 作为构建镜像时 Maven 配置，会被 `Dockerfile` 拷贝到 Maven 全局配置目录。
- 当前配置使用 `central` 的阿里云镜像，加速依赖下载。

## 认证配置（JWT）

- `Authorization` 请求头格式：`Bearer <token>`
- 当 `agent.auth.enabled=true` 时，`/api/**`（除 `OPTIONS`）都需要 JWT。
- 验签优先级：
  - 若 `agent.auth.local-public-key` 已配置，先使用本地公钥验签；
  - 本地验签失败后，再回退到 `agent.auth.jwks-uri` 拉取的 JWKS 验签。
- 本地公钥模式为启动期加载，更新密钥后需要重启服务生效。

示例（`application.yml`）：

```yaml
agent:
  auth:
    enabled: true
    local-public-key: |
      -----BEGIN PUBLIC KEY-----
      MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtesttesttesttesttest
      testtesttesttesttesttesttesttesttesttesttesttesttesttesttesttest
      testtesttesttesttesttesttesttesttesttesttesttesttesttesttesttest
      -----END PUBLIC KEY-----
    jwks-uri: https://auth.example.local/api/auth/jwks
    issuer: https://auth.example.local
    jwks-cache-seconds: 300
```

注意：

- 当配置了空的 `local-public-key` 或非法 PEM 时，服务会在启动时失败（fail-fast）。
- `jwks-uri` / `issuer` / `jwks-cache-seconds` 必须三者同时配置；只配部分会启动失败。
- 当前仅支持 RSA 公钥（与 RS256 验签一致）。

## Agent 配置快速上手

> 完整 schema 规范、配置规则和已移除字段列表见 [CLAUDE.md #Agent JSON 定义](./CLAUDE.md#agent-json-定义)。

- `agents/*.json` 以 `key` 作为 agentId；若缺失 `key`，回退为文件名（不含 `.json`）
- 服务启动时会先加载一次，之后每 10 秒刷新一次缓存（默认值）
- 可通过 `AGENT_EXTERNAL_DIR` 指定目录，通过 `AGENT_REFRESH_INTERVAL_MS` 调整刷新间隔
- `systemPrompt` 同时支持标准 JSON 字符串和 `"""` 多行写法（仅 `systemPrompt`）

### ONESHOT 示例

单轮直答；若配置工具可在单轮中调用工具并收敛最终答案。

```json
{
  "key": "fortune_teller",
  "name": "算命大师",
  "icon": "emoji:🔮",
  "description": "算命大师",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
    "reasoning": { "enabled": false }
  },
  "mode": "ONESHOT",
  "plain": {
    "systemPrompt": "你是算命大师"
  }
}
```

### REACT 示例

最多 N 轮循环（默认 6）：思考 → 调 1 个工具 → 观察结果。

```json
{
  "mode": "REACT",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
    "reasoning": { "enabled": true, "effort": "MEDIUM" }
  },
  "toolConfig": {
    "backends": ["_bash_", "city_datetime"],
    "frontends": [],
    "actions": []
  },
  "react": {
    "systemPrompt": """
你是算命大师
请先问出生日期
""",
    "maxSteps": 6
  }
}
```

### PLAN_EXECUTE 示例

先规划后执行（plan 阶段按 `deepThinking` 选择一回合或两回合）。

```json
{
  "mode": "PLAN_EXECUTE",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
    "reasoning": { "enabled": true, "effort": "HIGH" }
  },
  "toolConfig": {
    "backends": ["_bash_", "city_datetime", "mock_city_weather"],
    "frontends": [],
    "actions": []
  },
  "planExecute": {
    "plan": { "systemPrompt": "先规划", "deepThinking": true },
    "execute": { "systemPrompt": "再执行" },
    "summary": { "systemPrompt": "最后总结" }
  }
}
```

### Skills 配置示例

```json
{
  "skills": ["math_basic", "text_utils"],
  "toolConfig": {
    "backends": ["_skill_run_script_"]
  }
}
```

## 工具 / 视图 / 技能目录

> 工具系统设计规范（继承规则、提交协议、action 行为）见 [CLAUDE.md #Tool 系统](./CLAUDE.md#tool-系统)。
> Skills 系统设计见 [CLAUDE.md #Skills 系统](./CLAUDE.md#skills-系统)。
> Viewport 系统设计见 [CLAUDE.md #Viewport 系统](./CLAUDE.md#viewport-系统)。

- 运行目录默认值：
  - agents: `agents/`
  - viewports: `viewports/`
  - tools: `tools/`
  - skills: `skills/`
- 启动时会将 `src/main/resources/agents|viewports|tools|skills` 同步到外部目录：
  - `AGENT_EXTERNAL_DIR`
  - `AGENT_VIEWPORT_EXTERNAL_DIR`
  - `AGENT_TOOLS_EXTERNAL_DIR`
  - `AGENT_SKILL_EXTERNAL_DIR`
- 同名内置文件会覆盖；外部额外自定义文件会保留，不会被删除。
- `viewports` 支持后缀：`.html`、`.qlc`、`.dqlc`、`.json_schema`、`.custom`，默认每 30 秒刷新内存快照。
- `tools`:
  - 后端工具文件：`*.backend`
  - 前端工具文件：`*.frontend`
  - 动作文件：`*.action`
  - 文件内容均为模型工具定义 JSON（`{"tools":[...]}`）
- `skills`:
  - 目录结构：`skills/<skill-id>/SKILL.md`（强约束，目录式）
  - 可选子目录：`scripts/`、`references/`、`assets/`
  - `skill-id` 取目录名，`SKILL.md` frontmatter 的 `name/description` 作为元信息。
- `show_weather_card` 当前仅作为 viewport（`viewports/show_weather_card.html`），不是可调用 tool。

### /api/viewport 约定

- `GET /api/viewport?viewportKey=weather_card`
- `chatId`、`runId` 为可选参数，不参与必填校验。
- 返回：
  - `html` 文件：`data = {"html":"<...>"}`
  - `qlc/dqlc/json_schema/custom`：`data` 直接是文件内 JSON 对象
- `viewportKey` 不存在时返回 `404`。

### 前端 tool 提交流程

- 当前端工具触发时，SSE `tool.start` / `tool.snapshot` 会包含 `toolType`、`toolKey`、`toolTimeout`。
- 默认等待超时 5 分钟（可配置）。
- `POST /api/submit` 请求体：`runId` + `toolId` + `params`。
- 成功命中后会释放对应 `runId + toolId` 的等待；未命中返回 `accepted=false`。
- 动作工具触发 `action.start` 后不等待提交，直接返回 `"OK"` 给模型。

## 内置能力

### 内置智能体

- `demoModePlain`（`ONESHOT`）：单次直答。
- `demoModeThinking`（`ONESHOT`）：开启 reasoning 的单次作答。
- `demoModePlainTooling`（`ONESHOT`）：单轮按需调用工具。
- `demoModeReact`（`REACT`）：按需多轮工具调用。
- `demoModePlanExecute`（`PLAN_EXECUTE`）：先规划后执行。
- `demoViewport`（`PLAN_EXECUTE`）：调用天气工具，输出 viewport 天气卡片。
- `demoAction`（`ONESHOT`）：调用 `switch_theme` / `launch_fireworks` / `show_modal`。
- `demoAgentCreator`（`PLAN_EXECUTE`）：调用 `agent_file_create` 创建/更新 agent。
- `demoModePlainSkillMath`（`ONESHOT`）：加载 skills，调用 `_skill_run_script_` 完成确定性计算。
- `demoConfirmDialog`（`REACT`）：确认对话框 human-in-the-loop 示例。

### 内置 Action

- `switch_theme(theme)`：主题切换，`theme` 仅支持 `light/dark`。
- `launch_fireworks(durationMs?)`：播放烟花特效，`durationMs` 可选（毫秒）。
- `show_modal(title, content, closeText?)`：弹出模态框，`title/content` 必填，`closeText` 可选。

### 内置 Skills

- `screenshot`：截图流程示例（含脚本 smoke test）。
- `math_basic`：算术计算（add/sub/mul/div/pow/mod）。
- `math_stats`：统计计算（summary/count/sum/min/max/mean/median/mode/stdev）。
- `text_utils`：文本指标（字符/词数/行数，可选空白归一化）。
- `slack-gif-creator`：GIF 动画创建。

### 内置工具

- `_skill_run_script_`：执行 skills 目录下脚本或临时 Python 脚本。
- `_bash_`：Shell 命令执行，受 `allowed-paths` 白名单约束。
- `city_datetime`：获取城市当前日期时间。
- `mock_city_weather`：模拟城市天气数据。
- `agent_file_create`：创建/更新 agent JSON 文件（校验 key 仅允许 `A-Za-z0-9_-`，最长 64）。

## Bash 工具配置

`_bash_` 工具默认仅允许访问工作目录（`user.dir`）。工具返回文本包含 `exitCode`、`"workingDirectory"`、`stdout`、`stderr`。若需要让 Agent 在容器内读取 `/opt` 等目录，可配置：

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

## 环境变量速查

> 完整环境变量列表（含属性键、默认值和分类说明）见 [CLAUDE.md #Configuration](./CLAUDE.md#configuration)。

常用运维变量：

| 环境变量 | 默认值 | 说明 |
|---------|-------|------|
| `SERVER_PORT` | `8080` | HTTP 服务端口 |
| `AGENT_EXTERNAL_DIR` | `agents` | Agent 定义目录 |
| `AGENT_REFRESH_INTERVAL_MS` | `10000` | Agent 刷新间隔（ms） |
| `AGENT_VIEWPORT_EXTERNAL_DIR` | `viewports` | Viewport 目录 |
| `AGENT_TOOLS_EXTERNAL_DIR` | `tools` | 工具目录 |
| `AGENT_SKILL_EXTERNAL_DIR` | `skills` | 技能目录 |
| `AGENT_BASH_WORKING_DIRECTORY` | `${user.dir}` | Bash 工作目录 |
| `AGENT_BASH_ALLOWED_PATHS` | （空） | Bash 允许路径 |
| `AGENT_TOOLS_FRONTEND_SUBMIT_TIMEOUT_MS` | `300000` | 前端工具提交超时 |
| `AGENT_AUTH_ENABLED` | `true` | JWT 认证开关 |
| `MEMORY_CHAT_DIR` | `./chats` | 聊天记忆目录 |
| `MEMORY_CHAT_K` | `20` | 滑动窗口大小 |
| `AGENT_LLM_INTERACTION_LOG_ENABLED` | `true` | LLM 日志开关 |

## curl 测试用例

### 会话接口测试

```bash
curl -N -X GET "$BASE_URL/api/chats" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "$BASE_URL/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "$BASE_URL/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656&includeRawMessages=true" \
  -H "Content-Type: application/json"
```

```bash
curl -N -X GET "$BASE_URL/api/chat?chatId=d0e5b9ab-af21-4e3b-8e1a-a977dc6d5656&includeEvents=true" \
  -H "Content-Type: application/json"
```

### Query 回归测试

```bash
BASE_URL="http://localhost:11949"
ACCESS_TOKEN=""
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"元素碳的简介，200字","agentKey":"demoModePlain"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"chatId":"","message":"下一个元素的简介","agentKey":"demoModePlain"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"给我一个微服务网关的落地方案，100字内","agentKey":"demoModePlanExecute"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"给我一个机房搬迁风险分析摘要，300字左右","agentKey":"demoModeThinking"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"我周日要搬迁机房到上海，检查下服务器(mac)的硬盘和CPU，然后决定下搬迁条件","agentKey":"demoModeReact"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"规划上海机房明天搬迁的实施计划，重点关注下天气","agentKey":"demoModePlanExecute"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"查上海明天天气","agentKey":"demoViewport"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"切换到深色主题","agentKey":"demoAction"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"请计算 (2+3)*4，并说明过程","agentKey":"demoModePlainSkillMath"}'
```

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"【确认是否有敏感信息】本项目突破传统竖井式系统建设模式，基于1+1+3+N架构（1个企业级数据库、1套OneID客户主数据、3类客群CRM系统整合优化、N个展业数字化应用），打造了覆盖展业全生命周期、贯通公司全客群管理的OneLink分支一体化数智展业服务平台。在数据基础层面，本项目首创企业级数据库及OneID客户主数据运作体系，实现公司全域客户及业务数据物理入湖，并通过事前注册、事中应用管理、事后可分析的机制，实现个人、企业、机构三类客群千万级客户的统一识别与关联。","agentKey":"demoModePlainTooling"}'
```

### 确认对话框（Human-in-the-Loop）

confirm_dialog 是前端工具，LLM 调用后 SSE 流会暂停等待用户提交。需要两个终端配合测试。

**终端 1：发起 query（SSE 流会在 LLM 调用 confirm_dialog 时暂停）**

```bash
curl -N -X POST "$BASE_URL/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"帮我规划周六的旅游，给我几个目的地选项让我选","agentKey":"demoConfirmDialog"}'
```

观察 SSE 输出，当看到 `toolName` 为 `confirm_dialog` 且事件携带 `toolType/toolKey/toolTimeout` 后，
流会暂停等待。记录事件中的 `runId` 和 `toolId` 值。

**终端 2：提交用户选择（用终端 1 中的 runId 和 toolId 替换占位符）**

```bash
curl -X POST "$BASE_URL/api/submit" \
  -H "Content-Type: application/json" \
  -d '{
    "runId": "<RUN_ID>",
    "toolId": "<TOOL_ID>",
    "params": {
      "selectedOption": "杭州西湖一日游",
      "selectedIndex": 1,
      "freeText": "",
      "isCustom": false
    }
  }'
```

提交后终端 1 的 SSE 流会恢复，LLM 根据用户选择继续输出。
若未命中等待中的 `runId + toolId`，接口仍返回 HTTP 200，但 `accepted=false` / `status=unmatched`。

submit 响应示例：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "accepted": true,
    "status": "accepted",
    "runId": "<RUN_ID>",
    "toolId": "<TOOL_ID>",
    "detail": "Frontend submit accepted for runId=<RUN_ID>, toolId=<TOOL_ID>"
  }
}
```
