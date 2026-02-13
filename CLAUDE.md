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
| `agent` | Agent 接口、`DefinitionDrivenAgent` 主实现、`AgentRegistry`（10s 热刷新）、JSON 定义加载 |
| `service` | `LlmService`（WebClient SSE + ChatClient 双路径）、`AgwQueryService`（流编排）、`ChatRecordStore` |
| `tool` | `BaseTool` 接口、`ToolRegistry` 自动注册，内置 bash/city_datetime/mock_city_weather 等 |
| `controller` | REST API：`/api/agents`、`/api/agent`、`/api/chats`、`/api/chat`、`/api/query`（SSE）、`/api/submit` |
| `memory` | 滑动窗口聊天记忆（k=20），文件存储于 `chats/` |

### 关键设计

- **定义驱动** — Agent 通过 `agents/` 目录下 JSON 文件配置，文件名即 agentId
- **原生 Function Calling** — `tools[]` + `delta.tool_calls` 流式协议
- **工具参数模板** — `{{tool_name.field+Nd}}` 日期运算和链式引用
- **双路径 LLM** — WebClient 原生 SSE 和 ChatClient，按需选择
- **响应格式** — 非 SSE 接口统一 `{"code": 0, "msg": "success", "data": {}}`
- **会话详情格式** — `GET /api/chat` 的 `data` 字段固定为 `chatId/chatName/rawMessages/events/references`；`events` 必返，`rawMessages` 仅在 `includeRawMessages=true` 返回
- **真流式首字符检测** — `handleDecisionChunk` 方法：首个非空字符非 `{`/`` ` `` 则判定为纯文本立即流式推送，否则走 JSON 决策积累

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
- 兼容策略：仅支持 V2 JSONL 行格式；不再解析旧 memory 结构。

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

## Agent JSON 定义

```json
{
  "description": "描述",
  "providerType": "BAILIAN",
  "model": "qwen3-max",
  "systemPrompt": "系统提示词",
  "mode": "PLAIN | RE_ACT | PLAN_EXECUTE",
  "tools": ["bash", "city_datetime"]
}
```

`systemPrompt` 支持 `"""..."""` 三引号多行语法。兼容旧名：`THINKING_AND_CONTENT` → `RE_ACT`，`THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS` → `PLAN_EXECUTE`。

## 开发硬性要求（MUST）

以下规则是强制约束，任何代码修改都必须严格遵守。

### 1. Agent 模式行为规范

**PLAIN** — 直接调用 0 或 1 个工具，给出答案，不多轮迭代。

**RE_ACT** — 最多 6 轮循环：思考 → 调 1 个工具 → 观察结果，直到给出最终答案。每轮最多 1 个工具。

**PLAN_EXECUTE** — LLM 逐步决策，每步可调用 0~N 个工具（支持并行 tool_calls），按顺序执行，下一步可引用上一步的工具结果（链式引用）。

### 2. 严格真流式输出（CRITICAL）

**绝对禁止：**
- 等 LLM 完整返回后再拆分发送（假流式）
- 将多个 delta 合并后再切分输出
- 缓存完整响应后再逐块发送

**必须做到：**
- LLM 返回一个 delta，立刻推送一个 SSE 事件（零缓冲）
- thinking/content token 逐个流式输出
- tool_calls delta 立刻输出，细分事件：`tool.start` → `tool.args`（多次）→ `tool.end` → `tool.result`
- **1 个上游 delta 只允许 1 次下游发射（同语义块）**，禁止跨 delta 合并后再发
- `VerifyPolicy.SECOND_PASS_FIX` 必须真流式：首轮候选答案仅内部使用，二次校验输出按 chunk 实时下发

**实现机制：** `AgentOrchestrator.runStream` 统一流式编排；模型轮次使用 `callModelTurnStreaming` 逐 delta 透传；结构化输出使用 `StreamingJsonFieldExtractor` 增量提取 `finalText/reasoningSummary`；二次校验通过 `VerifyService.streamSecondPass` 逐 chunk 输出。

### 3. LLM 调用日志（MUST）

所有大模型调用的完整日志必须打印到控制台：
- 每个 SSE delta（thinking/content/tool_calls）逐条打印 `log.debug`
- 工具调用 delta 打印 tool name、arguments 片段、finish_reason
- `LlmService.appendDeltaLog` 带 traceId/stage 参数，`streamContent`/`streamContentRawSse` 均有逐 chunk debug 日志
- 日志开关：`agent.llm.interaction-log.enabled`（默认 `true`）
- 脱敏开关：`agent.llm.interaction-log.mask-sensitive`（默认 `true`），会脱敏 `authorization/apiKey/token/secret/password`
