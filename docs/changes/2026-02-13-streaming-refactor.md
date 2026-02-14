# 2026-02-13 Streaming Refactor

## 目标

- 完成 `/api/query` 全链路真流式改造：禁止先聚合后发送，保证上游 delta 到达后立即下游发射。
- 保留 `VerifyPolicy.SECOND_PASS_FIX`，并将第二轮校验结果改为实时流式输出。
- 工具决策轮文本按策略“外发全部文本”，不隐藏中间文本。

## 核心实现

- 主编排接口改为流式：`AgentOrchestrator.runStream(...)`，替代列表式聚合返回。
- `DefinitionDrivenAgent.stream(...)` 改为直接消费 `orchestrator.runStream(...)`，并保留 `TurnTrace` 的 `doOnNext` 累积和 `doOnComplete` 持久化。
- 新增 `callModelTurnStreaming(...)`：对 `llmService.streamDeltas(...)` 的 `content/tool_calls` 增量即时透传，同时在本地累积 `finalText/plannedToolCalls` 供后续决策。
- REASONING 场景新增增量结构化提取：按 chunk 实时发射，禁止最终整段回放。
- Verify 流式化：`VerifyService.streamSecondPass(...)`，`SECOND_PASS_FIX` 下首轮答案仅内部候选，不对外发；仅第二轮校验文本按 chunk 流式对外发。
- 工具事件语义保持不变：`tool.start -> tool.args(多次) -> tool.end -> tool.result`，`tool.args` 来源于实时 `tool_calls.arguments` 增量，不做合并。

## 覆盖模式

- 已覆盖：`ONESHOT`、`REACT`、`PLAN_EXECUTE`。

## 关键文件

- `src/main/java/com/linlay/springaiagw/agent/DefinitionDrivenAgent.java`
- `src/main/java/com/linlay/springaiagw/agent/runtime/AgentOrchestrator.java`
- `src/main/java/com/linlay/springaiagw/agent/runtime/VerifyService.java`
- `src/main/java/com/linlay/springaiagw/agent/runtime/StreamingJsonFieldExtractor.java`
- `src/test/java/com/linlay/springaiagw/agent/DefinitionDrivenAgentTest.java`
- `src/test/java/com/linlay/springaiagw/service/AgentDeltaToAgwInputMapperTest.java`
- `src/test/java/com/linlay/springaiagw/controller/AgwControllerTest.java`

## 验证结果

- `mvn -Dtest=DefinitionDrivenAgentTest,AgwControllerTest,AgentDeltaToAgwInputMapperTest test`：通过
- `mvn test`：全量通过
