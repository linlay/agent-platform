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
- **会话详情格式** — `GET /api/chat` 的 `data` 字段固定为 `chatId/chatName/messages/events/references`；当 `includeEvents=true` 时返回 `message.snapshot/tool.snapshot` 历史快照事件
- **真流式首字符检测** — `handleDecisionChunk` 方法：首个非空字符非 `{`/`` ` `` 则判定为纯文本立即流式推送，否则走 JSON 决策积累

## Configuration

主配置 `application.yml`，本地覆盖 `application-local.yml`（含 API key）。

关键环境变量：`SERVER_PORT`、`AGENT_EXTERNAL_DIR`、`AGENT_REFRESH_INTERVAL_MS`、`AGENT_BASH_WORKING_DIRECTORY`、`AGENT_BASH_ALLOWED_PATHS`、`MEMORY_CHAT_DIR`、`MEMORY_CHAT_K`

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

**实现机制：** `handleDecisionChunk` 中首字符检测——纯文本直接 `sink.next()` 逐 token 推送；JSON 决策格式走积累 + thinking 提取。三种模式共用此方法。

### 3. LLM 调用日志（MUST）

所有大模型调用的完整日志必须打印到控制台：
- 每个 SSE delta（thinking/content/tool_calls）逐条打印 `log.debug`
- 工具调用 delta 打印 tool name、arguments 片段、finish_reason
- `LlmService.appendDeltaLog` 带 traceId/stage 参数，`streamContent`/`streamContentRawSse` 均有逐 chunk debug 日志
