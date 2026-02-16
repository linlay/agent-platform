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
  -d '{"message":"给我一个微服务网关的落地方案，100字内","agentKey":"demoModePlanExecute"}'
```

## settings.xml 说明

- `settings.xml` 作为构建镜像时 Maven 配置，会被 `Dockerfile` 拷贝到 Maven 全局配置目录。
- 当前配置使用 `central` 的阿里云镜像，加速依赖下载。

## agents 目录

- `agents/*.json` 以 `key` 作为 agentId；若缺失 `key`，回退为文件名（不含 `.json`）
- 服务启动时会先加载一次，之后每 10 秒刷新一次缓存（默认值）
- 可通过 `AGENT_EXTERNAL_DIR` 指定目录，通过 `AGENT_REFRESH_INTERVAL_MS` 调整刷新间隔
- `systemPrompt` 同时支持标准 JSON 字符串和 `"""` 多行写法（仅 `systemPrompt`）

标准 JSON：

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
  "toolConfig": null,
  "mode": "ONESHOT",
  "plain": {
    "systemPrompt": "你是算命大师"
  }
}
```

多行写法：

```json
{
  "description": "算命大师",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max"
  },
  "toolConfig": {
    "backends": ["_bash_", "city_datetime"],
    "frontends": [],
    "actions": []
  },
  "mode": "REACT",
  "react": {
    "systemPrompt": """
你是算命大师
请先问出生日期
""",
    "maxSteps": 6
  }
}
```

`mode` 支持：
- `ONESHOT`：单轮直答，若配置工具可在同一轮完成“调用工具 + 最终答案”
- `REACT`：多轮工具循环推理
- `PLAN_EXECUTE`：先规划再逐步执行
  - plan 阶段固定 2 个公开子回合：`agent-plan-draft`（深度思考与规划正文，禁工具）+ `agent-plan-generate`（调用 `_plan_add_tasks_` 落盘计划）。
  - execute 阶段为小 ReAct：每个工作回合最多执行 1 个工具（串行），随后进入更新回合调用 `_plan_update_task_` 推进状态，更新失败允许修复 1 次。
  - `failed` 为中断状态：任务被更新为 `failed` 后立即停止执行。
  - 任务状态集合：`init` / `completed` / `failed` / `canceled`（历史 `in_progress` 仅兼容读取并映射为 `init`）。

工具仅通过 `toolConfig` 配置：
- 顶层：`toolConfig.backends/frontends/actions`
- 阶段：`planExecute.plan|execute|summary.toolConfig`
- 阶段继承规则：
  - 阶段缺失 `toolConfig`：继承顶层
  - 阶段显式 `toolConfig: null`：禁用该阶段全部工具
  - `_plan_get_tasks_` 仅在阶段显式配置时对模型可见；框架内部调度始终可读取 plan 快照。

当工具非空时，服务会按 OpenAI 兼容的原生 Function Calling 协议请求模型：
- 请求体包含 `tools[]`
- 流式消费 `delta.tool_calls`
- 不再依赖正文中的 `toolCall/toolCalls` JSON 字段（仍保留向后兼容解析）

Agent JSON 已仅支持新结构：`modelConfig/toolConfig/skillConfig`。旧字段 `providerKey/providerType/model/reasoning/tools` 不再兼容。

顶层 skills 配置支持两种写法（会合并去重）：

```json
{
  "skillConfig": {
    "skills": ["screenshot", "doc"]
  }
}
```

```json
{
  "skills": ["screenshot", "doc"]
}
```

### 真流式约束（CRITICAL）

- `/api/query` 全链路严格真流式：上游 LLM 每到一个 delta，立即下发对应 AGW 事件，禁止先 `collect/reduce/block` 再输出。
- 禁止将多个 delta 合并后再切片发送；输出粒度以“上游 delta 语义块”为准。
- 工具调用必须保持事件顺序：`tool.start` -> `tool.args`（可多次）-> `tool.end` -> `tool.result`。
- `VerifyPolicy.SECOND_PASS_FIX` 场景下，首轮候选答案仅内部使用；对外只流式下发二次校验生成的 chunk。

## viewports / tools / skills 目录

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
  - 正例：`skills/math_basic/SKILL.md`
  - 反例：`skills/SKILL.md`、`skills/math_basic.md`
  - 启动同步策略与 agents 一致：同名内置覆盖外部同名文件；外部额外文件保留；不做删除清理。
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

- 前端工具触发后会发送 `tool.start`（`toolType` 为 `frontend`），并等待 `/api/submit`。
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

### 内置脚本执行工具

- `_skill_run_script_(skill, script?, pythonCode?, args?, timeoutMs?)`：执行 `skills/<skill>/` 目录下脚本，或执行临时 Python 脚本。
- `script` 与 `pythonCode` 二选一，不能同时提供。
- `script` 仅支持 skill 内相对路径，文件类型仅支持 `.py` / `.sh`，不允许越权访问外部目录。
- `pythonCode` 会临时写入 `/tmp/agw-skill-inline/<skill>/inline_<uuid>.py`，执行后自动清理。
- 破坏性变更：旧工具名 `skill_script_run` 已移除，agent 配置需改为 `_skill_run_script_`。

### 内置 skills

- `screenshot`：截图流程示例（含脚本 smoke test）。
- `math_basic`：算术计算（`add/sub/mul/div/pow/mod`）。
- `math_stats`：统计计算（`summary/count/sum/min/max/mean/median/mode/stdev`）。
- `text_utils`：文本指标（字符/词数/行数，可选空白归一化）。

## 内置智能体

- `demoModePlain`（`ONESHOT`）：单次直答。
- `demoModeThinking`（`ONESHOT`）：开启 reasoning 的单次作答。
- `demoModePlainTooling`（`ONESHOT`）：单轮按需调用工具。
- `demoModeThinkingTooling`（`ONESHOT`）：开启 reasoning 的单轮工具模式。
- `demoModeReact`（`REACT`）：按需多轮工具调用。
- `demoModePlanExecute`（`PLAN_EXECUTE`）：先规划后执行，execute 阶段由框架下发任务列表与当前 taskId，模型完成后调用 `_plan_update_task_` 推进 plan（可选调用 `_plan_get_tasks_` 查看快照）。
- `demoViewport`（`PLAN_EXECUTE`）：调用 `city_datetime`、`mock_city_weather`，最终按 `viewport` 代码块协议输出天气卡片数据。
- `demoAction`（`ONESHOT`）：根据用户意图调用 `switch_theme` / `launch_fireworks` / `show_modal`。
- `demoAgentCreator`（`PLAN_EXECUTE`）：调用 `agent_file_create` 创建/更新 `agents/{agentId}.json`。
- `demoModePlainSkillMath`（`ONESHOT`）：加载 `math_basic/math_stats/text_utils` skills，并调用 `_skill_run_script_` 完成确定性计算。
- 使用 `demoAgentCreator` 时建议提供：`key`、`name`、`icon`、`description`、`modelConfig`、`mode`、`toolConfig` 与各 mode 的 prompt 字段。
- `agent_file_create` 会校验 `key/agentId`（仅允许 `A-Za-z0-9_-`，最长 64）。
- `providerKey/providerType` 不做白名单校验；未提供时默认 `bailian`。
- 生成格式：

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
  "toolConfig": null,
  "mode": "ONESHOT",
  "plain": {
    "systemPrompt": "你是算命大师"
  }
}
```

- `systemPrompt` 为多行时会写成标准 JSON 字符串（含 `\\n` 换行）。

## Bash 工具目录授权

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

动态目录相关环境变量：

```bash
AGENT_VIEWPORT_EXTERNAL_DIR=/opt/viewports
AGENT_VIEWPORT_REFRESH_INTERVAL_MS=30000
AGENT_TOOLS_EXTERNAL_DIR=/opt/tools
AGENT_CAPABILITY_REFRESH_INTERVAL_MS=30000
AGENT_SKILL_EXTERNAL_DIR=/opt/skills
AGENT_SKILL_REFRESH_INTERVAL_MS=30000
AGENT_SKILL_MAX_PROMPT_CHARS=8000
AGENT_TOOLS_FRONTEND_SUBMIT_TIMEOUT_MS=300000
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"元素碳的简介，200字","agentKey":"demoModePlain"}'
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"chatId":"","message":"下一个元素的简介","agentKey":"demoModePlain"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"【确认是否有敏感信息】本项目突破传统竖井式系统建设模式，基于1+1+3+N架构（1个企业级数据库、1套OneID客户主数据、3类客群CRM系统整合优化、N个展业数字化应用），打造了覆盖展业全生命周期、贯通公司全客群管理的OneLink分支一体化数智展业服务平台。在数据基础层面，本项目首创企业级数据库及OneID客户主数据运作体系，实现公司全域客户及业务数据物理入湖，并通过事前注册、事中应用管理、事后可分析的机制，实现个人、企业、机构三类客群千万级客户的统一识别与关联。","agentKey":"demoModePlainTooling"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"给我一个机房搬迁风险分析摘要，300字左右","agentKey":"demoModeThinking"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"请查上海当前时间并评估是否适合安排变更窗口","agentKey":"demoModeThinkingTooling"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"查一下上海今天天气并给出出行建议","agentKey":"demoModePlainTooling"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"我周日要搬迁机房到上海，你先对当前服务器做一下检测，然后决定下搬迁条件","agentKey":"demoModeReact"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"规划上海机房明天搬迁的实施计划，重点关注下天气","agentKey":"demoModePlanExecute"}'
```

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"查上海明天天气","agentKey":"demoViewport"}'
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

```bash
curl -N -X POST "http://localhost:8080/api/query" \
  -H "Content-Type: application/json" \
  -d '{"message":"请计算 (2+3)*4，并说明过程","agentKey":"demoModePlainSkillMath"}'
```
