# 编排模块（orchestration）

## 关键类
- `DefinitionDrivenAgent`
- `OrchestratorServices`
- `ExecutionContext`
- `AgentDeltaToStreamInputMapper`

## 职责
- 把 Agent 定义、历史消息、技能提示、工具提示整合为模型回合。
- 将模型增量（reasoning/content/tool_calls/usage）透传为 `AgentDelta`。
- 驱动 tool 执行并把结果回灌上下文。

## 模型回合规则
`OrchestratorServices.callModelTurnStreaming`：
1. 组装 stage system prompt + skill deferred disclosure + backend tool appendix。
2. 调 `LlmService.streamDeltas`。
3. 每个 delta 立即发射到下游（reasoning/content/tool_calls）。
4. 累积 `usage` 并透传。

## 事件边界
- 文本块和工具块在 mapper 层转换为 `StreamInput`。
- `stageMarker` 会关闭当前文本块，避免跨语义块串联。

## 失败处理
- frontend submit timeout -> 显式提示并结束。
- 运行异常 -> 兜底内容 + finish(stop)；SSE 层可进一步转换为 `run.error`。
