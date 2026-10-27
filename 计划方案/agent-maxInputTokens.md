# Agent 级上下文窗口配置（maxInputTokens）

## 方案

在 agent.yml 的 `modelConfig` 下新增 `maxInputTokens`（正整数）：

- 未填写：使用所选模型注册表中的 `maxInputTokens`；模型也未填写时按 128000 处理。
- 已填写：使用 Agent 自己的值，但不得超过模型的上限。

## 校验

- 加载 / 保存时：超过 `modelKey` 对应模型的上限直接报错，拒绝加载或保存；编辑表单同步提示上限。
- 运行时换模型：请求级换模型、PLAN-EXECUTE 阶段换模型时，若当前模型上限小于 Agent 的值，本次 Run 直接报错，不静默截断。

## 生效方式

- 值由 `session_builder` 写入会话；`effectiveContextWindow()`（`internal/llm/run_stream_usage.go`）与手动压缩（`internal/server/handler_compact.go`，目前写死 128000）共用同一个取值函数。
- 压缩阈值、压缩目标、摘要预算、usage 的 `maxSize` 随之生效。
- `agent_invoke` 子任务、TEAM 成员使用各自 Agent 的配置（TEAM 快照与 clone 需带上该字段）。
- ACP / Proxy / CHANNEL 类 Agent 不支持该字段。

## 状态

方案已确认，尚未实现。
