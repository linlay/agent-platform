# CLAUDE.md

## Project Overview

Spring Boot + Spring AI agent gateway — 基于 WebFlux 的响应式 LLM Agent 编排服务，通过 JSON 配置定义 Agent，支持多种执行模式和原生 OpenAI Function Calling 协议。

**技术栈:** Java 21, Spring Boot 3.3.8, Spring AI 1.0.0, WebFlux (Reactor), Jackson

**LLM 提供商:** Bailian (阿里云百炼/Qwen), SiliconFlow (DeepSeek), Babelark 等；provider 只承载连接配置，实际调用协议由模型定义决定。

## Build & Run

```bash
mvn clean test                          # 构建并运行所有测试
mvn spring-boot:run                     # 本地启动，默认端口 8080
mvn test -Dtest=ClassName               # 运行单个测试类
mvn test -Dtest=ClassName#methodName    # 运行单个测试方法
```

SDK 依赖: `libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar`（`systemPath` 引用）。

## Architecture

```
POST /api/ap/query → AgentController → AgentQueryService → DefinitionDrivenAgent.stream()
  → LlmService.streamDeltas() → LLM Provider → AgentDelta → SSE response
```

### 核心模块

| 包 | 职责 |
|---|------|
| `agent` | Agent 接口、`DefinitionDrivenAgent` 主实现、`AgentRegistry`（WatchService 热刷新）、JSON 定义加载 |
| `agent.mode` | `AgentMode`（sealed：`OneshotMode`/`ReactMode`/`PlanExecuteMode`）、`OrchestratorServices` 流式编排、`StageSettings` |
| `agent.runtime` | `AgentRuntimeMode` 枚举、`ExecutionContext`（状态/预算/对话历史管理）、`ToolExecutionService` |
| `agent.runtime.policy` | `RunSpec`、`ToolChoice`、`ComputePolicy`、`Budget` 等策略定义 |
| `model` | `AgentRequest`、`ModelCatalogProperties`、`ModelDefinition`、`ModelProtocol`、`ViewportType` |
| `model.api` | REST 契约：`ApiResponse`、`AgwQueryRequest`、`AgwSubmitRequest`、`AgwChatDetailResponse` 等 |
| `model.stream` | 流式类型：`AgentDelta` |
| `service` | `LlmService`（WebClient SSE + ChatClient 双路径）、`AgentQueryService`（流编排）、`ChatRecordStore`、`DirectoryWatchService` |
| `tool` | `BaseTool` 接口、`ToolRegistry` 自动注册、`CapabilityRegistryService`（外部工具），内置 bash/city_datetime/mock_city_weather 等 |
| `skill` | `SkillRegistryService`（技能注册与热刷新）、`SkillDescriptor`、`SkillCatalogProperties` |
| `controller` | REST API：`/api/ap/agents`、`/api/ap/agent`、`/api/ap/chats`、`/api/ap/chat`、`/api/ap/query`（SSE）、`/api/ap/submit`、`/api/ap/viewport` |
| `memory` | 滑动窗口聊天记忆（k=20），文件存储于 `chats/` |

### 关键设计

- **定义驱动** — Agent 通过 `agents/` 目录下 JSON 文件配置，文件名即 agentId
- **模型注册中心** — 模型通过 `models/*.json` 管理，启动同步内置模型到外置目录并热加载到内存
- **原生 Function Calling** — `tools[]` + `delta.tool_calls` 流式协议
- **工具参数模板** — `{{tool_name.field+Nd}}` 日期运算和链式引用
- **双路径 LLM** — WebClient 原生 SSE 和 ChatClient，按需选择
- **响应格式** — 非 SSE 接口统一 `{"code": 0, "msg": "success", "data": {}}`
- **会话详情格式** — `GET /api/ap/chat` 的 `data` 字段固定为 `chatId/chatName/rawMessages/events/references`；`events` 必返，`rawMessages` 仅在 `includeRawMessages=true` 返回

## Agent JSON 定义

### 完整 Schema

```json
{
  "key": "agent_key",
  "name": "agent_name",
  "icon": "emoji:🤖",
  "description": "描述",
  "modelConfig": {
    "modelKey": "bailian-qwen3-max",
    "reasoning": { "enabled": true, "effort": "MEDIUM" },
    "temperature": 0.7,
    "top_p": 0.95,
    "max_tokens": 4096
  },
  "toolConfig": {
    "backends": ["_bash_", "city_datetime"],
    "frontends": ["show_weather_card"],
    "actions": ["switch_theme"]
  },
  "skillConfig": {
    "skills": ["math_basic", "screenshot"]
  },
  "skills": ["math_basic", "screenshot"],
  "mode": "ONESHOT | REACT | PLAN_EXECUTE",
  "toolChoice": "NONE | AUTO | REQUIRED",
  "budget": {
    "runTimeoutMs": 120000,
    "model": { "maxCalls": 15, "timeoutMs": 60000, "retryCount": 0 },
    "tool": { "maxCalls": 20, "timeoutMs": 120000, "retryCount": 0 }
  },
  "plain": {
    "systemPrompt": "系统提示词",
    "modelConfig": { "modelKey": "bailian-qwen3-max" },
    "toolConfig": null
  },
  "react": {
    "systemPrompt": "系统提示词",
    "maxSteps": 6
  },
  "planExecute": {
    "plan": { "systemPrompt": "规划提示词", "deepThinking": true },
    "execute": { "systemPrompt": "执行提示词" },
    "summary": { "systemPrompt": "总结提示词" },
    "maxSteps": 10
  },
  "runtimePrompts": {
    "planExecute": {
      "taskExecutionPromptTemplate": "..."
    },
    "skill": {
      "catalogHeader": "...",
      "disclosureHeader": "...",
      "instructionsLabel": "..."
    },
    "toolAppendix": {
      "toolDescriptionTitle": "...",
      "afterCallHintTitle": "..."
    }
  }
}
```

### 模式配置块

各模式对应配置块（至少需要一个）：
- `ONESHOT` → `plain.systemPrompt`
- `REACT` → `react.systemPrompt`
- `PLAN_EXECUTE` → `planExecute.plan.systemPrompt` + `planExecute.execute.systemPrompt`

### 配置规则

**modelConfig 继承：**
- 支持外层默认 + stage 内层覆盖；内层优先。
- 外层 `modelConfig` 可省略，但"外层或任一 stage"至少要有一处 `modelConfig.modelKey`。
- `provider/modelId/protocol` 不在 Agent JSON 中声明，统一由 `models/<modelKey>.json` 解析得到。

**toolConfig 继承：**
- 支持外层默认 + stage 覆盖。
- 若 stage 显式 `toolConfig: null` 表示清空该 stage 普通工具集合。
- PLAN_EXECUTE 强制工具不受 `toolConfig: null` 影响：plan 固定含 `_plan_add_tasks_`，execute 固定含 `_plan_update_task_`。
- `_plan_get_tasks_` 仅在阶段显式配置时对模型可见；框架内部调度始终可读取 plan 快照。

**skillConfig 配置：**
- 支持两种写法（会合并去重）：`"skillConfig": {"skills": [...]}` 或 `"skills": [...]`。

**多行 Prompt 写法：**
- `systemPrompt` 字段支持 `"""..."""` 三引号格式（非标准 JSON，预处理阶段转换）。仅匹配字段名含 `systemPrompt` 的键（大小写不敏感）。

**步骤上限：**
- `react.maxSteps` 控制 REACT 循环上限。
- `planExecute.maxSteps` 控制 PLAN_EXECUTE 执行阶段步骤上限。

### 已移除字段（拒绝加载）

以下旧字段出现在 Agent JSON 中会导致该 agent 被拒绝加载：

| 类别 | 被拒绝的字段 |
|------|------------|
| 顶层 | `verify`, `output`, `toolPolicy` |
| 旧结构 | `modelConfig.providerKey`, `modelConfig.model`, `reasoning`（顶层）, `tools`, `deepThink`, `systemPrompt`（顶层） |
| budget 旧字段 | `maxModelCalls`, `maxToolCalls`, `maxSteps`（budget 内）, `timeoutMs`（budget 内）, `retryCount`（budget 内） |
| runtimePrompts 旧字段 | `verify`, `finalAnswer`, `oneshot`, `react` |
| runtimePrompts.planExecute 旧子字段 | `executeToolsTitle`, `planCallableToolsTitle`, `draftInstructionBlock`, `generateInstructionBlockFromDraft`, `generateInstructionBlockDirect`, `taskRequireToolUserPrompt`, `taskMultipleToolsUserPrompt`, `taskUpdateNoProgressUserPrompt`, `taskContinueUserPrompt`, `updateRoundPromptTemplate`, `updateRoundMultipleToolsUserPrompt`, `allStepsCompletedUserPrompt` |

## Models 目录（内部注册）

- 运行目录：`models/`（默认，可通过 `agent.model.external-dir` 覆盖）。
- 启动同步：`src/main/resources/models` 会同步到外置 `models/`；同名内置文件覆盖，外置自定义文件保留。
- 热加载：目录变更会触发模型刷新，并联动 agent 重新加载（因为 agent 依赖 `modelKey` 解析）。
- 文件格式：每个模型一个 JSON（建议 `models/<modelKey>.json`）。
- 必填字段：`key`、`provider`、`protocol`、`modelId`。
- 常用字段：`isReasoner`、`isFunction`、`maxTokens`、`maxInputTokens`、`maxOutputTokens`。
- 计费字段：`pricing.promptPointsPer1k`、`pricing.completionPointsPer1k`、`pricing.perCallPoints`、`pricing.priceRatio`、`pricing.tiers[]`。
- 协议枚举：`OPENAI`、`ANTHROPIC`、`NEWAPI_OPENAI_COMPATIBLE`。
- 约定：`10000 积分 = 1 RMB`，按每 `1K tokens` 计价；运行时只做配置解析与透传，不内置财务结算逻辑。

## Agent 模式行为

### ONESHOT

单轮直答；若配置工具则允许单轮工具调用后输出最终答案。

### REACT

最多 N 轮循环（默认 6）：思考 → 调 1 个工具 → 观察结果，直到给出最终答案。每轮最多 1 个工具。

### PLAN_EXECUTE

plan 阶段按 `planExecute.plan.deepThinking` 分支：

- `deepThinking=false`：单回合 `agent-plan-generate`，关闭 reasoning，`tool_choice=required`，必须调用 `_plan_add_tasks_`。
- `deepThinking=true`：两回合公开流式：
  1. `agent-plan-draft`：开启 reasoning，`tool_choice=none`，只输出思考与规划正文。
  2. `agent-plan-generate`：关闭 reasoning，`tool_choice=required`，仅允许调用 `_plan_add_tasks_`。

execute 阶段每轮最多 1 个工具，完成后在更新回合调用 `_plan_update_task_`（失败可修复 1 次）。

任务状态集合：`init` / `completed` / `failed` / `canceled`（历史 `in_progress` 仅兼容读取并映射为 `init`）。`failed` 为中断状态：任务被更新为 `failed` 后立即停止执行。

## Tool 系统

### 工具文件类型

`tools/` 目录下的文件按后缀区分三种类型：

| 后缀 | CapabilityKind | 说明 |
|------|----------------|------|
| `.backend` | `BACKEND` | 后端工具，模型通过 Function Calling 调用。`description` 用于 OpenAI tool schema，`after_call_hint` 用于注入 system prompt 的"工具调用后推荐指令"章节 |
| `.action` | `ACTION` | 动作工具，触发前端行为（如主题切换、烟花特效）。不等待 `/api/ap/submit`，直接返回 `"OK"` |
| `.frontend` | `FRONTEND` | 前端工具定义文件，触发 UI 渲染并等待 `/api/ap/submit` 提交；实际渲染内容由 `viewports/` 下 `.html/.qlc/.dqlc/.json_schema/.custom` 文件提供 |

文件内容均为 `{"tools":[...]}` 格式的 JSON。工具名冲突策略：冲突项会被跳过，其它项继续生效。

### toolConfig 继承规则

- 顶层 `toolConfig.backends/frontends/actions` 定义默认工具集合。
- 各 stage 可通过自身 `toolConfig` 覆盖：缺失则继承顶层，显式 `null` 则清空。
- PLAN_EXECUTE 强制工具（`_plan_add_tasks_` / `_plan_update_task_`）不受 `toolConfig: null` 影响。

### 前端 tool 提交协议

- SSE `tool.start` / `tool.snapshot` 会包含：`toolType`（html/qlc）、`toolKey`（viewport key）、`toolTimeout`（超时毫秒）。
- 默认等待超时 5 分钟（`agent.tools.frontend.submit-timeout-ms`）。
- `POST /api/ap/submit` 请求体：`runId` + `toolId` + `params`。
- 前端工具返回值提取规则：直接回传 `params`（若为 `null` 则回传 `{}`）。

### Action 行为规则

- Action 触发 `action.start` 后不等待提交，直接返回 `"OK"` 给模型。
- 事件顺序：`action.start` → `action.args`（可多次）→ `action.end` → `action.result`。

### 内置工具

- `_skill_run_script_`：执行 `skills/<skill>/` 目录下脚本或临时 Python 脚本。`script` 与 `pythonCode` 二选一；支持 `.py` / `.sh`；内联 Python 写入 `/tmp/agent-platform-skill-inline/`，执行后清理。
- `_bash_`：Shell 命令执行，需显式配置 `allowed-commands` 与 `allowed-paths` 白名单。
- `city_datetime`：获取城市当前日期时间。
- `mock_city_weather`：模拟城市天气数据。
- `agent_file_create`：创建/更新 agent JSON 文件。

### 工具参数模板

支持 `{{tool_name.field+Nd}}` 格式的日期运算和链式引用。

## Skills 系统

### 目录结构

```
skills/<skill-id>/
├── SKILL.md          # 必须，含 frontmatter（name/description）+ 正文指令
├── scripts/          # 可选，Python/Bash 脚本
├── references/       # 可选，参考资料
└── assets/           # 可选，静态资源
```

- `skill-id` 取目录名（小写归一化）。
- `SKILL.md` frontmatter 格式：`name: "显示名"` / `description: "描述"`。
- 正文作为 LLM prompt 注入，超过 `max-prompt-chars`（默认 8000）时截断。

### skillConfig 配置

Agent JSON 中引用 skills：

```json
{ "skillConfig": { "skills": ["math_basic", "screenshot"] } }
```

或简写：

```json
{ "skills": ["math_basic", "screenshot"] }
```

两种写法会合并去重。运行时，技能目录摘要注入 system prompt；LLM 调用 `_skill_run_script_` 时补充完整技能说明。

### Prompt 注入定制

通过 `runtimePrompts.skill` 可自定义注入头：

- `catalogHeader`：技能目录标题（默认："可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:"）
- `disclosureHeader`：完整说明标题
- `instructionsLabel`：指令字段标签

### 内置 Skills

| skill-id | 说明 |
|----------|------|
| `screenshot` | 截图流程示例（含脚本 smoke test） |
| `math_basic` | 算术计算（add/sub/mul/div/pow/mod） |
| `math_stats` | 统计计算（summary/count/sum/min/max/mean/median/mode/stdev） |
| `text_utils` | 文本指标（字符/词数/行数，可选空白归一化） |
| `slack-gif-creator` | GIF 动画创建 |

## Viewport 系统

### /api/ap/viewport 端点契约

```
GET /api/ap/viewport?viewportKey=<key>[&chatId=<id>][&runId=<id>]
```

- `viewportKey` 必填，`chatId`/`runId` 可选。
- 返回：
  - `html` 文件：`data = {"html":"<...>"}`
  - `qlc/dqlc/json_schema/custom`：`data` 直接是文件内 JSON 对象
- `viewportKey` 不存在时返回 `404`。

### 支持后缀

| 文件后缀 | ViewportType | 说明 |
|----------|-------------|------|
| `.html` | `HTML` | 静态 HTML 渲染 |
| `.qlc` | `QLC` | QLC 表单 schema |
| `.dqlc` | `QLC` | 动态 QLC 表单 |
| `.json_schema` | — | JSON Schema 格式 |
| `.custom` | — | 自定义格式 |

### Viewport 输出协议

Agent 通过代码块协议输出 viewport 渲染指令：

```viewport
type=html, key=show_weather_card
{
  "city": "Shanghai",
  "date": "2026-02-13",
  "temperatureC": 22
}
```

## SSE 事件契约（最新）

### 1. 基础字段（所有 SSE 事件）

- 必带字段：`seq`, `type`, `timestamp`
- 不再输出：`rawEvent`

### 2. 输入与会话事件

- `request.query`：`requestId`, `chatId`, `role`, `message`, `agentKey?`, `references?`, `params?`, `scene?`, `stream?`
- `request.upload`：`requestId`, `chatId?`, `upload:{type,name,sizeBytes,mimeType,sha256?}`
- `request.submit`：`requestId`, `chatId`, `runId`, `toolId`, `payload`, `viewId?`
- `chat.start`：`chatId`, `chatName?`（仅该 chat 首次 run 发送一次）
- `chat.update`：当前不发送

### 3. 计划、运行与任务事件

- `plan.create`：`planId`, `chatId`, `plan`
- `plan.update`：`planId`, `chatId`, `plan`（总是带 `chatId`）
- `run.start`：`runId`, `chatId`
- `run.complete`：`runId`, `finishReason?`
- `run.cancel`：`runId`
- `run.error`：`runId`, `error`
- `task.*`：仅在"已有 plan 且显式 `task.start` 输入"时出现；不自动创建 task

### 4. 推理与内容事件

- `reasoning.start`：`reasoningId`, `runId`, `taskId?`
- `reasoning.delta`：`reasoningId`, `delta`
- `reasoning.end`：`reasoningId`
- `reasoning.snapshot`：`reasoningId`, `text`, `taskId?`
- `content.start`：`contentId`, `runId`, `taskId?`
- `content.delta`：`contentId`, `delta`
- `content.end`：`contentId`
- `content.snapshot`：`contentId`, `text`, `taskId?`

### 5. 工具与动作事件

- `tool.start`：`toolId`, `runId`, `taskId?`, `toolName?`, `toolType?`, `toolApi?`, `toolParams?`, `description?`
- `tool.args`：`toolId`, `delta`, `chunkIndex?`（字段名保持 `delta`，不使用 `args`）
- `tool.end`：`toolId`
- `tool.result`：`toolId`, `result`
- `tool.snapshot`：`toolId`, `toolName?`, `taskId?`, `toolType?`, `toolApi?`, `toolParams?`, `description?`, `arguments?`
- `action.start`：`actionId`, `runId`, `taskId?`, `actionName?`, `description?`
- `action.args`：`actionId`, `delta`
- `action.end`：`actionId`
- `action.param`：`actionId`, `param`
- `action.result`：`actionId`, `result`
- `action.snapshot`：`actionId`, `actionName?`, `taskId?`, `description?`, `arguments?`

### 6. 来源事件

- `source.snapshot`：`sourceId`, `runId?`, `taskId?`, `icon?`, `title?`, `url?`

### 7. 补充行为约束

- 无活跃 task 出错时：只发 `run.error`（不补 `task.fail`）
- plain 模式（当前无 plan）不应出现 `task.*`，叶子事件直接归属 `run`
- `GET /api/ap/chat` 历史事件需与新规则对齐；历史使用 `*.snapshot` 替代 `start/end/delta/args` 细粒度流事件，并保留 `tool.result` / `action.result`
- 历史里 `run.complete` 每个 run 都保留，`chat.start` 仅首次一次

## Chat Memory V3（JSONL）

- 存储文件：`chats/{chatId}.json`，JSONL 格式，**一行一个 step**，逐步增量写入。
- 行类型通过 `_type` 字段区分：
  - `"query"`：用户原始请求行。必带 `chatId`、`runId`、`updatedAt`、`query`。
  - `"step"`：一个执行步骤行。必带 `chatId`、`runId`、`_stage`、`_seq`、`updatedAt`、`messages`；可选 `taskId`、`system`、`plan`（旧名 `planSnapshot`，读取时兼容）。
- `_stage` 标识步骤阶段：`"oneshot"` / `"react"` / `"plan"` / `"execute"` / `"summary"`。
- `_seq` 全局递增序号，标识 run 内的步骤顺序。
- `query` 保存完整 query 结构（`requestId/chatId/agentKey/role/message/references/params/scene/stream`）。
- `system` 快照规则：每个 run 的第一个 step 写入；stage 切换且 system 变化时再写入；后续 step 如果 system 未变化则省略。
- `messages` 采用 OpenAI 风格：
  - `role=user`：`content[]`（text parts）+ `ts`
  - `role=assistant`：三种快照形态之一：`content[]` / `reasoning_content[]` / `tool_calls[]`
  - `role=tool`：`name` + `tool_call_id` + `content[]` + `ts`
- assistant/tool 扩展字段支持：`_reasoningId`、`_contentId`、`_msgId`、`_toolId`、`_actionId`、`_timing`、`_usage`。
- action/tool 判定：通过 `memory.chat.action-tools` 白名单；命中写 `_actionId`，否则写 `_toolId`。
- memory 回放约束：`reasoning_content` **不回传**给下一轮模型上下文。
- 滑动窗口：k=20 单位仍然是 **run**；`trimToWindow` 按 `runId` 分组，保留最近 k 个 run 的所有行。

## Chat Memory V3.1 变更

基于 V3 格式的增量改进，向后兼容旧 V3 数据。

### 字段重命名

- step 行的 `planSnapshot` 字段重命名为 `plan`；内层 `PlanSnapshot.plan` 数组字段重命名为 `tasks`。
- 读取时兼容旧字段名：先查 `"plan"` 再 fallback `"planSnapshot"`；`@JsonAlias("plan")` 兼容旧 `tasks` 字段。

### _msgId

- 新增 `_msgId`（格式 `m_xxxxxxxx`，8 位 hex）标识同一 LLM 响应拆分的多条 assistant 消息。
- 同一模型回复中的 reasoning、content、tool_calls 消息共享相同 `_msgId`。
- tool result 到来后，下一个 reasoning/content delta 会重新生成 `_msgId`。

### tool_calls 拆分规则

- 每条 `role=assistant` 的 `tool_calls` 数组只含 **1 个**工具调用。
- 并行多工具调用拆分为多条 assistant 消息，通过共享 `_msgId` 关联。

### _toolId / _actionId 位置

- `_toolId` 和 `_actionId` 写入 `StoredMessage` 外层（与 `_reasoningId`、`_contentId` 同级）。
- `StoredToolCall` 内层的 `_toolId` / `_actionId` 仅用于反序列化旧 V3 数据，新数据不再写入。
- 读取时先查外层，再 fallback 内层（兼容旧数据）。

### _toolId 生成规则

| 工具类型 | 生成规则 |
|----------|---------|
| backend（`type=function`） | 直接使用 LLM 原始 `tool_call_id`（如 `call_b7332997a5b1490ca7195293`） |
| frontend（`type=frontend`） | `t_` + 8 位 hex（系统生成） |
| action（`type=action`） | `a_` + 8 位 hex（系统生成） |

### ID 前缀简化

| ID 类型 | 旧前缀 | 新前缀 |
|---------|--------|--------|
| reasoningId | `reasoning_` | `r_` |
| contentId | `content_` | `c_` |
| toolId (frontend) | `tool_` | `t_` |
| actionId | `action_` | `a_` |
| msgId | (新增) | `m_` |

SSE 事件中的 reasoningId/contentId 同步使用新前缀格式：`{runId}_r_{seq}` / `{runId}_c_{seq}`。

### _usage 真实填充

- 通过 `stream_options.include_usage=true` 请求 LLM provider 返回真实 usage 数据。
- `LlmDelta` record 新增 `Map<String, Object> usage` 字段，SDK parser 解析最后一个 chunk 的 usage。
- usage 通过管道穿透：`LlmDelta` → `AgentDelta` → `StepAccumulator.capturedUsage` → `RunMessage` → `StoredMessage._usage`。
- 不再写入 placeholder null 值；当 LLM 未返回 usage 时 `_usage` 仍使用默认占位结构。

## Configuration

主配置事实源：`src/main/resources/application.yml`。本地覆盖：`application-local.yml`（可放私有 provider key）。

### Spring/Server

| 项 | 默认值 | 说明 |
|----|--------|------|
| `server.port` | `8080` | HTTP 端口（环境变量 `SERVER_PORT`） |
| `spring.application.name` | `springai-agent-platform` | 服务名 |
| `spring.config.import` | `optional:file:./application-local.yml, optional:file:/opt/application.yml` | 启动时按顺序加载本地和容器外部覆盖文件 |
| `spring.ai.openai.api-key` | `${OPENAI_API_KEY:dummy-openai-key}` | Spring AI 占位 key；真实模型调用走 `agent.providers.*` |

### 环境变量完整列表

#### Agent Catalog / Model / Viewport / Data

| 环境变量 | 属性键 | 默认值 | 说明 |
|---------|--------|-------|------|
| `AGENT_EXTERNAL_DIR` | `agent.catalog.external-dir` | `agents` | Agent JSON 定义目录 |
| `AGENT_REFRESH_INTERVAL_MS` | `agent.catalog.refresh-interval-ms` | `10000` | Agent 目录刷新间隔（ms） |
| `AGENT_MODEL_EXTERNAL_DIR` | `agent.model.external-dir` | `models` | Model JSON 定义目录 |
| `AGENT_MODEL_REFRESH_INTERVAL_MS` | `agent.model.refresh-interval-ms` | `30000` | Model 目录刷新间隔（ms） |
| `AGENT_VIEWPORT_EXTERNAL_DIR` | `agent.viewport.external-dir` | `viewports` | Viewport 目录 |
| `AGENT_VIEWPORT_REFRESH_INTERVAL_MS` | `agent.viewport.refresh-interval-ms` | `30000` | Viewport 刷新间隔（ms） |
| `AGENT_DATA_EXTERNAL_DIR` | `agent.data.external-dir` | `data` | 静态文件目录 |

#### Tools / Skills

| 环境变量 | 属性键 | 默认值 | 说明 |
|---------|--------|-------|------|
| `AGENT_TOOLS_EXTERNAL_DIR` | `agent.capability.tools-external-dir` | `tools` | 工具定义文件目录 |
| `AGENT_CAPABILITY_REFRESH_INTERVAL_MS` | `agent.capability.refresh-interval-ms` | `30000` | 工具目录刷新间隔（ms） |
| `AGENT_TOOLS_FRONTEND_SUBMIT_TIMEOUT_MS` | `agent.tools.frontend.submit-timeout-ms` | `300000` | 前端工具提交等待超时（ms） |
| `AGENT_TOOLS_AGENT_FILE_CREATE_DEFAULT_SYSTEM_PROMPT` | `agent.tools.agent-file-create.default-system-prompt` | `你是通用助理，回答要清晰和可执行。` | `agent_file_create` 默认 system prompt |
| `AGENT_BASH_WORKING_DIRECTORY` | `agent.tools.bash.working-directory` | `${user.dir}` | Bash 工具工作目录 |
| `AGENT_BASH_ALLOWED_PATHS` | `agent.tools.bash.allowed-paths` | （空） | Bash 工具路径白名单（逗号分隔） |
| `AGENT_BASH_ALLOWED_COMMANDS` | `agent.tools.bash.allowed-commands` | （空=拒绝执行） | Bash 允许命令列表（逗号分隔） |
| `AGENT_BASH_PATH_CHECKED_COMMANDS` | `agent.tools.bash.path-checked-commands` | （空=默认等于 allowed-commands） | 启用路径校验的命令列表（逗号分隔） |
| `AGENT_SKILL_EXTERNAL_DIR` | `agent.skill.external-dir` | `skills` | 技能目录 |
| `AGENT_SKILL_REFRESH_INTERVAL_MS` | `agent.skill.refresh-interval-ms` | `30000` | 技能刷新间隔（ms） |
| `AGENT_SKILL_MAX_PROMPT_CHARS` | `agent.skill.max-prompt-chars` | `8000` | 技能 prompt 最大字符数 |

#### Auth / Chat Image Token / Memory / LLM 日志

| 环境变量 | 属性键 | 默认值 | 说明 |
|---------|--------|-------|------|
| `AGENT_AUTH_ENABLED` | `agent.auth.enabled` | `true` | JWT 认证开关 |
| `AGENT_AUTH_JWKS_URI` | `agent.auth.jwks-uri` | （空） | JWKS 地址 |
| `AGENT_AUTH_ISSUER` | `agent.auth.issuer` | （空） | JWT issuer |
| `AGENT_AUTH_JWKS_CACHE_SECONDS` | `agent.auth.jwks-cache-seconds` | （空） | JWKS 缓存秒数 |
| `CHAT_IMAGE_TOKEN_SECRET` | `agent.chat-image-token.secret` | （空） | 图片令牌签名密钥（为空则 token 机制禁用） |
| `CHAT_IMAGE_TOKEN_PREVIOUS_SECRETS` | `agent.chat-image-token.previous-secrets` | （空） | 历史密钥列表（逗号分隔），用于密钥轮换验证 |
| `CHAT_IMAGE_TOKEN_TTL_SECONDS` | `agent.chat-image-token.ttl-seconds` | `86400` | 图片令牌过期秒数 |
| `MEMORY_CHAT_DIR` | `memory.chat.dir` | `./chats` | 聊天记忆目录 |
| `MEMORY_CHAT_K` | `memory.chat.k` | `20` | 滑动窗口大小（按 run） |
| `MEMORY_CHAT_CHARSET` | `memory.chat.charset` | `UTF-8` | 记忆文件编码 |
| `MEMORY_CHAT_ACTION_TOOLS` | `memory.chat.action-tools` | （空） | action 工具白名单 |
| `AGENT_LLM_INTERACTION_LOG_ENABLED` | `agent.llm.interaction-log.enabled` | `true` | LLM 交互日志开关 |
| `AGENT_LLM_INTERACTION_LOG_MASK_SENSITIVE` | `agent.llm.interaction-log.mask-sensitive` | `true` | 日志脱敏开关 |

说明：`agent.auth.local-public-key` 仅支持在 YAML 中配置 PEM 文本，不提供环境变量映射。

### CORS（主配置默认）

| 属性键 | 默认值 | 说明 |
|--------|-------|------|
| `agent.cors.enabled` | `false` | 默认关闭 CORS 过滤器 |
| `agent.cors.path-pattern` | `/api/**` | CORS 匹配路径 |
| `agent.cors.allowed-origin-patterns` | `http://localhost:8081` | 允许源（列表） |
| `agent.cors.allowed-methods` | `GET,POST,PUT,PATCH,DELETE,OPTIONS` | 允许方法（列表） |
| `agent.cors.allowed-headers` | `*` | 允许请求头 |
| `agent.cors.exposed-headers` | `Content-Type` | 暴露响应头 |
| `agent.cors.allow-credentials` | `false` | 是否允许凭证 |
| `agent.cors.max-age-seconds` | `3600` | 预检缓存秒数 |

### Provider 配置（通常在 `application-local.yml`）

`agent.providers.<providerKey>` 支持：
- `base-url`
- `api-key`
- `model`（可选，作为 provider 默认 model）
- `new-api-path`（可选，仅 `NEWAPI_OPENAI_COMPATIBLE` 协议使用）

说明：
- provider 不再绑定 protocol；协议由 `models/*.json` 中 `protocol` 字段决定。
- `NEWAPI_OPENAI_COMPATIBLE` 首版请求/响应同 OpenAI SSE，仅 endpoint 路径取 `new-api-path`（默认 `/v1/chat/completions`）。

### Logging（主配置默认）

| 属性键 | 默认值 |
|--------|--------|
| `logging.level.root` | `INFO` |
| `logging.level.com.linlay.agentplatform` | `INFO` |
| `logging.level.com.linlay.agentplatform.service.LlmService` | `DEBUG` |
| `logging.level.com.linlay.agentplatform.service.OpenAiCompatibleSseClient` | `DEBUG` |
| `logging.level.com.linlay.agentplatform.service.LlmCallLogger` | `DEBUG` |
| `logging.level.com.linlay.agentplatform.llm.wiretap` | `DEBUG` |

## 真流式约束（CRITICAL）

**绝对禁止：**
- 等 LLM 完整返回后再拆分发送（假流式）
- 将多个 delta 合并后再切分输出
- 缓存完整响应后再逐块发送

**必须做到：**
- LLM 返回一个 delta，立刻推送一个 SSE 事件（零缓冲）
- reasoning/content token 逐个流式输出
- tool_calls delta 立刻输出，细分事件：`tool.start` → `tool.args`（多次）→ `tool.end` → `tool.result`
- **1 个上游 delta 只允许 1 次下游发射（同语义块）**，禁止跨 delta 合并后再发
- 不再进行二次校验回合（无 `agent-verify`）；每次模型回合只输出一次真实流式内容，避免重复答案

**实现机制：** `DefinitionDrivenAgent` 驱动 `AgentMode` 执行；模型轮次使用 `OrchestratorServices.callModelTurnStreaming` 逐 delta 透传。

## 开发硬性要求（MUST）

### LLM 调用日志

所有大模型调用的完整日志必须打印到控制台：
- 每个 SSE delta（reasoning/content/tool_calls）逐条打印 `log.debug`
- 工具调用 delta 打印 tool name、arguments 片段、finish_reason
- `LlmService.appendDeltaLog` 带 traceId/stage 参数，`streamContent`/`streamContentRawSse` 均有逐 chunk debug 日志
- 日志开关：`agent.llm.interaction-log.enabled`（默认 `true`）
- 脱敏开关：`agent.llm.interaction-log.mask-sensitive`（默认 `true`），会脱敏 `authorization/apiKey/token/secret/password`

## 设计原则

Agent 行为应由 LLM 推理和工具调用驱动（通过 prompt 引导），Java 层只负责编排、流式传输和工具执行管理。

## 变更记录

一次性改造记录迁移到独立文档，`CLAUDE.md` 仅保留长期有效的架构与契约信息：
- `docs/changes/2026-02-13-streaming-refactor.md`
