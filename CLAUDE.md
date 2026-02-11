# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Spring Boot + Spring AI agent gateway (AGW) — 一个基于 WebFlux 的响应式 LLM Agent 编排服务。通过 JSON 配置文件定义 Agent，支持多种执行模式和原生 OpenAI Function Calling 协议。

**技术栈:** Java 21, Spring Boot 3.3.8, Spring AI 1.0.0, WebFlux (Reactor), Jackson

**LLM 提供商:** Bailian (阿里云百炼/Qwen), SiliconFlow (DeepSeek)，均通过 OpenAI 兼容 API 对接。

## Build & Run Commands

```bash
mvn clean test                # 构建并运行所有测试
mvn spring-boot:run           # 本地启动，默认端口 8080
mvn test -Dtest=ClassName     # 运行单个测试类
mvn test -Dtest=ClassName#methodName  # 运行单个测试方法
```

SDK jar 依赖位于 `libs/agw-springai-sdk-0.0.1-SNAPSHOT.jar`，通过 `systemPath` 引用，无需本地 Maven 安装。

## Architecture

### 请求流程

```
Client POST /api/query → AgwController → AgwQueryService.prepare()/stream()
  → DefinitionDrivenAgent.stream() → LlmService.streamDeltas() → LLM Provider
  → AgentDelta (thinking/content/toolCalls/toolResults) → SSE response
```

### 核心模块

**agent 包** — Agent 系统核心
- `Agent` 接口定义 `stream(AgentRequest) → Flux<AgentDelta>`
- `AgentDefinition` record 承载配置（id, description, providerType, model, systemPrompt, mode, tools）
- `DefinitionDrivenAgent` 是主实现（~1900 行），包含三种执行模式的完整逻辑
- `AgentRegistry` 管理 Agent 目录，每 10 秒热刷新（可配置）
- `AgentDefinitionLoader` 加载内置 + `agents/` 目录下的外部 JSON 定义

**三种 Agent 模式（AgentMode）:**
- `PLAIN` — 直答或单次工具调用决策
- `RE_ACT` — 迭代推理循环（最多 6 轮），每轮最多调 1 个工具
- `PLAN_EXECUTE` — 先规划再执行，支持并行工具调用

**service 包** — 业务逻辑
- `LlmService` (~750 行) — LLM 调用层，WebClient 原生 SSE 解析 + ChatClient 双路径，支持 tool_calls 流式解析
- `AgwQueryService` — 查询编排，Agent 解析 → delta 流转换 → SSE 输出
- `ChatRecordStore` — 基于文件的聊天记录持久化

**tool 包** — 工具系统
- `BaseTool` 接口：`name()`, `description()`, `parametersSchema()`, `invoke(args)`
- `ToolRegistry` 自动注册所有 `BaseTool` Spring Bean
- 内置工具：`bash`, `city_datetime`, `mock_city_weather`, `agent_file_create` 等

**controller 包** — REST API
- `GET /api/agents` — 智能体列表
- `GET /api/agent?agentKey=...` — 智能体详情
- `POST /api/query` — 主查询接口（SSE 流式）
- `POST /api/submit` — Human-in-the-loop 提交

**memory 包** — 滑动窗口聊天记忆（默认 k=20），文件存储于 `chats/`

### 关键设计决策

1. **定义驱动 Agent** — Agent 通过 JSON 文件配置而非 Java 代码，放在 `agents/` 目录，文件名即 agentId
2. **原生 Function Calling** — 使用 OpenAI 兼容的 `tools[]` + `delta.tool_calls` 流式协议，不依赖正文 JSON 解析
3. **工具参数模板** — 支持 `{{tool_name.field+Nd}}` 模板语法做日期运算和工具结果链式引用
4. **双路径 LLM 调用** — WebClient 原生 SSE 和 Spring AI ChatClient 两条路径，按需选择
5. **响应格式** — 非 SSE 接口统一返回 `{"code": 0, "msg": "success", "data": {}}`
6. **systemPrompt 多行语法** — JSON 定义中 `systemPrompt` 支持 `"""..."""` 三引号写法

## Configuration

主配置 `application.yml`，本地覆盖 `application-local.yml`（含 provider API key 和 model 设置）。

关键环境变量：
- `SERVER_PORT` — 服务端口（默认 8080）
- `AGENT_EXTERNAL_DIR` — Agent JSON 目录（默认 `agents`）
- `AGENT_REFRESH_INTERVAL_MS` — Agent 刷新间隔（默认 10000ms）
- `AGENT_BASH_WORKING_DIRECTORY` / `AGENT_BASH_ALLOWED_PATHS` — Bash 工具目录限制
- `MEMORY_CHAT_DIR` / `MEMORY_CHAT_K` — 聊天记忆目录和窗口大小

## Agent JSON Definition Format

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

兼容旧模式名：`THINKING_AND_CONTENT` → `RE_ACT`，`THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS` → `PLAN_EXECUTE`
