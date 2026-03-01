# 系统架构

## 架构模式
- 形态：模块化单体（Modular Monolith）。
- 通信：单进程内组件协作 + 对外 HTTP(SSE) 接口。
- 关键运行链：`AgentController -> AgentQueryService -> DefinitionDrivenAgent -> AgentMode -> LlmService/ToolExecutionService`。

## 架构图（Mermaid）
```mermaid
flowchart LR
    Client[Web/App Client] -->|POST /api/ap/query (SSE)| Controller[AgentController]
    Controller --> QuerySvc[AgentQueryService]
    QuerySvc --> Registry[AgentRegistry]
    QuerySvc --> SSE[StreamSseStreamer + StreamEventAssembler]

    Registry --> AgentDef[AgentDefinitionLoader]
    Registry --> AgentImpl[DefinitionDrivenAgent]

    AgentImpl --> Mode[AgentMode: ONESHOT/REACT/PLAN_EXECUTE]
    Mode --> Orchestrator[OrchestratorServices]
    Orchestrator --> LLM[LlmService]
    Orchestrator --> ToolExec[ToolExecutionService]

    LLM --> Provider[(OpenAI Compatible / NEWAPI)]
    ToolExec --> ToolReg[ToolRegistry + CapabilityRegistryService]
    ToolExec --> SubmitCoord[FrontendSubmitCoordinator]

    QuerySvc --> ChatStore[ChatRecordStore]
    AgentImpl --> Memory[ChatWindowMemoryStore]
    ChatStore --> SQLite[(chats.db)]
    ChatStore --> JSONL[(chats/{chatId}.json)]

    Controller --> DataCtrl[DataFileController]
    DataCtrl --> DataAuth[ChatAssetAccessService + ChatImageTokenService]
    DataCtrl --> DataDir[(data/)]
```

## 分层规则
- API 层：`controller/*` 仅处理协议映射、参数解析、状态码。
- 编排层：`service/*` 与 `agent/*` 负责运行流程、模式控制、事件映射。
- 能力层：`tool/*`, `skill/*`, `model/*` 负责注册、查找与执行。
- 存储层：`ChatRecordStore`（SQLite + 历史事件）、`ChatWindowMemoryStore`（JSONL 上下文）。

## 关键约束
- 真流式：上游 delta 到达后立即下游发射，不允许整段缓存后再切分。
- Tool 责任分离：`ToolRegistry` 管可用工具集合，`ToolExecutionService` 管调用与事件回填。
- 模式封装：ONESHOT/REACT/PLAN_EXECUTE 行为在 `agent.mode` 内独立实现。
- 鉴权入口：`ApiJwtAuthWebFilter` 为 `/api/ap/**` 全局入口，`/api/ap/data?t=` 走 token 例外分支。
