# CLAUDE.md

## Project Overview

Spring Boot + Spring AI agent gateway — 基于 WebFlux 的响应式 LLM Agent 编排服务，通过 JSON 配置定义 Agent，支持多种执行模式和原生 OpenAI Function Calling 协议。

**技术栈:** Java 21, Spring Boot 3.3.8, Spring AI 1.0.0, WebFlux (Reactor), Jackson

**LLM 提供商:** Bailian (阿里云百炼/Qwen), SiliconFlow (DeepSeek)，均通过 OpenAI 兼容 API 对接。

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
POST /api/query → AgwController → AgwQueryService → DefinitionDrivenAgent.stream()
  → LlmService.streamDeltas() → LLM Provider → AgentDelta → SSE response
```

### 核心模块

| 包 | 职责 |
|---|------|
| `agent` | Agent 接口、`DefinitionDrivenAgent` 主实现、`AgentRegistry`（WatchService 热刷新）、JSON 定义加载 |
| `agent.runtime` | `DefinitionDrivenAgent` + `AgentMode` + `OrchestratorServices` 流式编排、`ToolExecutionService`、`VerifyService` |
| `agent.runtime.policy` | `RunSpec`、`ControlStrategy`、`Budget` 等策略定义 |
| `model` | `AgentRequest`、`ProviderProtocol`、`ProviderType`、`ViewportType` |
| `model.api` | REST 契约：`ApiResponse`、`AgwQueryRequest`、`AgwSubmitRequest`、`AgwChatDetailResponse` 等 |
| `model.stream` | 流式类型：`AgentDelta` |
| `service` | `LlmService`（WebClient SSE + ChatClient 双路径）、`AgwQueryService`（流编排）、`ChatRecordStore`、`DirectoryWatchService` |
| `tool` | `BaseTool` 接口、`ToolRegistry` 自动注册、`CapabilityRegistryService`（外部工具），内置 bash/city_datetime/mock_city_weather 等 |
| `controller` | REST API：`/api/agents`、`/api/agent`、`/api/chats`、`/api/chat`、`/api/query`（SSE）、`/api/submit` |
| `memory` | 滑动窗口聊天记忆（k=20），文件存储于 `chats/` |

### 关键设计

- **定义驱动** — Agent 通过 `agents/` 目录下 JSON 文件配置，文件名即 agentId
- **原生 Function Calling** — `tools[]` + `delta.tool_calls` 流式协议
- **工具参数模板** — `{{tool_name.field+Nd}}` 日期运算和链式引用
- **双路径 LLM** — WebClient 原生 SSE 和 ChatClient，按需选择
- **响应格式** — 非 SSE 接口统一 `{"code": 0, "msg": "success", "data": {}}`
- **会话详情格式** — `GET /api/chat` 的 `data` 字段固定为 `chatId/chatName/rawMessages/events/references`；`events` 必返，`rawMessages` 仅在 `includeRawMessages=true` 返回

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
- `task.*`：仅在“已有 plan 且显式 `task.start` 输入”时出现；不自动创建 task

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
- `GET /api/chat` 历史事件需与新规则对齐；历史使用 `*.snapshot` 替代 `start/end/delta/args` 细粒度流事件，并保留 `tool.result` / `action.result`
- 历史里 `run.complete` 每个 run 都保留，`chat.start` 仅首次一次

## Configuration

主配置 `application.yml`，本地覆盖 `application-local.yml`（含 API key）。

关键环境变量：`SERVER_PORT`、`AGENT_EXTERNAL_DIR`、`AGENT_REFRESH_INTERVAL_MS`、`AGENT_BASH_WORKING_DIRECTORY`、`AGENT_BASH_ALLOWED_PATHS`、`MEMORY_CHAT_DIR`、`MEMORY_CHAT_K`、`MEMORY_CHAT_ACTION_TOOLS`、`AGENT_LLM_INTERACTION_LOG_ENABLED`、`AGENT_LLM_INTERACTION_LOG_MASK_SENSITIVE`

## Agent JSON 定义（v2）

```json
{
  "key": "agent_key",
  "name": "agent_name",
  "icon": "emoji:🤖",
  "description": "描述",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
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
  "mode": "ONESHOT | REACT | PLAN_EXECUTE",
  "output": "PLAIN | REASONING_SUMMARY",
  "toolPolicy": "DISALLOW | ALLOW | REQUIRE",
  "verify": "NONE | SECOND_PASS_FIX",
  "budget": { "maxModelCalls": 20, "maxToolCalls": 10, "maxSteps": 6, "timeoutMs": 120000 },
  "plain": {
    "systemPrompt": "系统提示词",
    "modelConfig": { "providerKey": "bailian", "model": "qwen3-max" },
    "toolConfig": null
  }
}
```

各模式对应配置块（至少需要一个）：
- `ONESHOT` -> `plain.systemPrompt`
- `REACT` -> `react.systemPrompt`
- `PLAN_EXECUTE` -> `planExecute.plan.systemPrompt` + `planExecute.execute.systemPrompt`

规则：
- `modelConfig` 支持外层默认 + stage 内层覆盖；内层优先。
- 外层 `modelConfig` 可省略，但“外层或任一 stage”至少要有一处 `modelConfig`。
- `toolConfig` 支持外层默认 + stage 覆盖；若 stage 显式 `toolConfig: null` 表示清空该 stage 普通工具集合。
- PLAN_EXECUTE 强制工具不受 `toolConfig: null` 影响：plan 固定含 `_plan_add_tasks_`，execute 固定含 `_plan_update_task_`。
- `planExecute.plan.deepThinking`（默认 `false`）控制规划阶段一回合/两回合行为。

## 各模式 JSON 配置示例

**ONESHOT** — 单轮直答；若配置工具可在单轮中调用工具并收敛最终答案：

```json
{
  "mode": "ONESHOT",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
    "reasoning": { "enabled": false }
  },
  "plain": { "systemPrompt": "你是助手" }
}
```

**REACT** — 最多 N 轮循环（默认 6）：思考 → 调 1 个工具 → 观察结果：

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
  "react": { "systemPrompt": "你是助手", "maxSteps": 5 }
}
```

**PLAN_EXECUTE** — 先规划后执行（plan 阶段按 `deepThinking` 选择一回合或两回合）：

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

PLAN_EXECUTE 规划阶段行为：
- `deepThinking=false`：单回合 `agent-plan-generate`，关闭 reasoning，`tool_choice=required`，必须调用 `_plan_add_tasks_`。
- `deepThinking=true`：两回合公开流式。
1. `agent-plan-draft`：开启 reasoning，`tool_choice=none`，只输出思考与规划正文。
2. `agent-plan-generate`：关闭 reasoning，`tool_choice=required`，仅允许调用 `_plan_add_tasks_`。

## Tool 类型定义

`tools/` 目录下的文件按后缀区分三种类型：

| 后缀 | CapabilityKind | 说明 |
|------|----------------|------|
| `.backend` | `BACKEND` | 后端工具，模型通过 Function Calling 调用。`description` 用于 OpenAI tool schema，`after_call_hint` 用于注入 system prompt 的“工具调用后推荐指令”章节 |
| `.action` | `ACTION` | 动作工具，触发前端行为（如主题切换、烟花特效）。不等待 `/api/submit`，直接返回 `"OK"` |
| `.html` / `.qlc` / `.dqlc` | `FRONTEND` | 前端工具，触发 UI 渲染并等待 `/api/submit` 提交 |

文件内容均为 `{"tools":[...]}` 格式的 JSON。

## 多行 Prompt 写法

`systemPrompt` 字段支持 `"""..."""` 三引号格式（非标准 JSON，预处理阶段转换）：

```json
{
  "react": {
    "systemPrompt": """
你是算命大师
请先问出生日期
"""
  }
}
```

仅匹配字段名含 `systemPrompt` 的键（大小写不敏感）。

## 策略覆盖能力

Agent JSON 中可显式覆盖模式预设的策略值：

```json
{
  "mode": "ONESHOT",
  "modelConfig": {
    "providerKey": "bailian",
    "model": "qwen3-max",
    "reasoning": { "enabled": true, "effort": "HIGH" }
  },
  "output": "REASONING_SUMMARY",
  "toolPolicy": "REQUIRE",
  "verify": "SECOND_PASS_FIX",
  "budget": { "maxModelCalls": 20, "maxToolCalls": 10, "maxSteps": 6, "timeoutMs": 120000 },
  "plain": { "systemPrompt": "..." }
}
```

可覆盖字段：`modelConfig.reasoning.enabled`、`modelConfig.reasoning.effort`（`LOW/MEDIUM/HIGH`）、`output`（`PLAIN/REASONING_SUMMARY`）、`toolPolicy`（`DISALLOW/ALLOW/REQUIRE`）、`verify`（`NONE/SECOND_PASS_FIX`）、`budget`。

## 设计原则

Agent 行为应由 LLM 推理和工具调用驱动（通过 prompt 引导），Java 层只负责编排、流式传输和工具执行管理。

## 开发硬性要求（MUST）

以下规则是强制约束，任何代码修改都必须严格遵守。

### 1. Agent 模式行为规范

**ONESHOT** — 单轮直答；若配置工具则允许单轮工具调用后输出最终答案。

**REACT** — 最多 6 轮循环：思考 → 调 1 个工具 → 观察结果，直到给出最终答案。每轮最多 1 个工具。

**PLAN_EXECUTE** — plan 阶段按 `planExecute.plan.deepThinking` 分支：
- `false`：单回合 required（必须 `_plan_add_tasks_`，且关闭 reasoning）；
- `true`：两回合（draft: no-tool + reasoning；generate: required `_plan_add_tasks_` + no reasoning）；
execute 阶段每轮最多 1 个工具，完成后在更新回合调用 `_plan_update_task_`（失败可修复 1 次）。

### 2. 严格真流式输出（CRITICAL）

**绝对禁止：**
- 等 LLM 完整返回后再拆分发送（假流式）
- 将多个 delta 合并后再切分输出
- 缓存完整响应后再逐块发送

**必须做到：**
- LLM 返回一个 delta，立刻推送一个 SSE 事件（零缓冲）
- reasoning/content token 逐个流式输出
- tool_calls delta 立刻输出，细分事件：`tool.start` → `tool.args`（多次）→ `tool.end` → `tool.result`
- **1 个上游 delta 只允许 1 次下游发射（同语义块）**，禁止跨 delta 合并后再发
- `VerifyPolicy.SECOND_PASS_FIX` 必须真流式：首轮候选答案仅内部使用，二次校验输出按 chunk 实时下发

**实现机制：** `DefinitionDrivenAgent` 驱动 `AgentMode` 执行；模型轮次使用 `OrchestratorServices.callModelTurnStreaming` 逐 delta 透传；二次校验通过 `VerifyService.streamSecondPass` 逐 chunk 输出。

### 3. LLM 调用日志（MUST）

所有大模型调用的完整日志必须打印到控制台：
- 每个 SSE delta（reasoning/content/tool_calls）逐条打印 `log.debug`
- 工具调用 delta 打印 tool name、arguments 片段、finish_reason
- `LlmService.appendDeltaLog` 带 traceId/stage 参数，`streamContent`/`streamContentRawSse` 均有逐 chunk debug 日志
- 日志开关：`agent.llm.interaction-log.enabled`（默认 `true`）
- 脱敏开关：`agent.llm.interaction-log.mask-sensitive`（默认 `true`），会脱敏 `authorization/apiKey/token/secret/password`

## 变更记录

一次性改造记录迁移到独立文档，`CLAUDE.md` 仅保留长期有效的架构与契约信息：
- `docs/changes/2026-02-13-streaming-refactor.md`
