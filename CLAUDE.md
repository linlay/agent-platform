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

## Chat Memory V2（JSONL）

- 存储文件：`chats/{chatId}.json`，JSONL 格式，**每个 run 一行标准 JSON**。
- 每行 run 顶层字段：`chatId`、`runId`、`transactionId`（同 `runId`）、`updatedAt`、`query`、`messages`，以及按需 `system`。
- `query` 放顶层，保存完整 query 结构（`requestId/chatId/agentKey/role/message/references/params/scene/stream`）。
- `system` 只在 chat 首次 run 写入；若后续 system 配置发生变化则再次写入；仅写可获取字段（当前为 `model/messages/tools/stream`）。
- `messages` 采用 OpenAI 风格：
  - `role=user`：`content[]`（text parts）+ `ts`
  - `role=assistant`：三种快照形态之一：`content[]` / `reasoning_content[]` / `tool_calls[]`
  - `role=tool`：`name` + `tool_call_id` + `content[]` + `ts`
- assistant/tool 扩展字段支持：`_reasoningId`、`_contentId`、`_toolId`、`_actionId`、`_timing`、`_usage`。
- action/tool 判定：通过 `memory.chat.action-tools` 白名单；命中写 `_actionId`，否则写 `_toolId`。
- memory 回放约束：`reasoning_content` **不回传**给下一轮模型上下文。

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
  "providerKey": "bailian",
  "model": "qwen3-max",
  "mode": "ONESHOT | REACT | PLAN_EXECUTE",
  "tools": ["bash", "city_datetime"],
  "reasoning": { "enabled": true, "effort": "MEDIUM" },
  "plain": {
    "systemPrompt": "系统提示词"
  }
}
```

各模式对应配置块（至少需要一个）：
- `ONESHOT` -> `plain.systemPrompt`
- `REACT` -> `react.systemPrompt`
- `PLAN_EXECUTE` -> `planExecute.plan.systemPrompt` + `planExecute.execute.systemPrompt`

## 各模式 JSON 配置示例

**ONESHOT** — 单轮直答；若配置工具可在单轮中调用工具并收敛最终答案：

```json
{
  "mode": "ONESHOT",
  "reasoning": { "enabled": false },
  "plain": { "systemPrompt": "你是助手" }
}
```

**REACT** — 最多 N 轮循环（默认 6）：思考 → 调 1 个工具 → 观察结果：

```json
{
  "mode": "REACT",
  "tools": ["bash", "city_datetime"],
  "reasoning": { "enabled": true, "effort": "MEDIUM" },
  "react": { "systemPrompt": "你是助手", "maxSteps": 5 }
}
```

**PLAN_EXECUTE** — LLM 逐步决策，每步可调用 0~N 个工具（支持并行 tool_calls）：

```json
{
  "mode": "PLAN_EXECUTE",
  "tools": ["bash", "city_datetime"],
  "reasoning": { "enabled": true, "effort": "HIGH" },
  "planExecute": {
    "plan": { "systemPrompt": "先规划" },
    "execute": { "systemPrompt": "再执行" },
    "summary": { "systemPrompt": "最后总结" }
  }
}
```

## Tool 类型定义

`tools/` 目录下的文件按后缀区分三种类型：

| 后缀 | CapabilityKind | 说明 |
|------|----------------|------|
| `.backend` | `BACKEND` | 后端工具，模型通过 Function Calling 调用。`description` 用于 OpenAI tool schema，`prompt` 用于注入 system prompt |
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
  "reasoning": { "enabled": true, "effort": "HIGH" },
  "output": "REASONING_SUMMARY",
  "toolPolicy": "REQUIRE",
  "verify": "SECOND_PASS_FIX",
  "budget": { "maxModelCalls": 20, "maxToolCalls": 10, "maxSteps": 6, "timeoutMs": 120000 },
  "plain": { "systemPrompt": "..." }
}
```

可覆盖字段：`reasoning.enabled`、`reasoning.effort`（`LOW/MEDIUM/HIGH`）、`output`（`PLAIN/REASONING_SUMMARY`）、`toolPolicy`（`DISALLOW/ALLOW/REQUIRE`）、`verify`（`NONE/SECOND_PASS_FIX`）、`budget`。

## 设计原则

Agent 行为应由 LLM 推理和工具调用驱动（通过 prompt 引导），Java 层只负责编排、流式传输和工具执行管理。

## 开发硬性要求（MUST）

以下规则是强制约束，任何代码修改都必须严格遵守。

### 1. Agent 模式行为规范

**ONESHOT** — 单轮直答；若配置工具则允许单轮工具调用后输出最终答案。

**REACT** — 最多 6 轮循环：思考 → 调 1 个工具 → 观察结果，直到给出最终答案。每轮最多 1 个工具。

**PLAN_EXECUTE** — LLM 逐步决策，每步可调用 0~N 个工具（支持并行 tool_calls），按顺序执行，下一步可引用上一步的工具结果（链式引用）。

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
