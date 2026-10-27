# Agent 级上下文窗口配置（maxInputTokens）

## 方案

在 agent.yml 的 `modelConfig` 下新增 `maxInputTokens`（正整数）：

```yaml
modelConfig:
  modelKey: babelark-claude-opus-5_5   # 注册表 maxInputTokens: 262144
  maxInputTokens: 200000
```

Run 的实际窗口 = min(Agent 值, 本次 Run 所用模型的上限)：

- Agent 未填写：取模型注册表的 `maxInputTokens`；模型也未填写时按 128000。
- Agent 已填写：取 Agent 值；本次 Run 用的模型上限更小时取模型上限，不报错。
- 模型在 Run 受理时冻结，Run 进行中不会换模型（steer 不带模型，HITL 续接沿用原 Run 模型），窗口随之冻结。

## 校验

- 加载 / 保存时：Agent 值超过 `modelKey` 对应模型的上限直接报错；编辑表单同步提示上限、切换模型时重新校验。
- ACP / PROXY / CHANNEL 类 Agent 配置该字段，加载时报错。
- 运行时不做报错校验，只取较小值。

## 各种情况

| 情况 | 实际窗口 |
| --- | --- |
| Agent、模型都没配 | 128000 |
| 只有模型配了 | 模型上限 |
| Agent 配了，用默认模型 | Agent 值（加载时已保证不超过模型上限） |
| 请求级换成上限更大的模型 | Agent 值 |
| 请求级换成上限更小的模型 | 模型上限 |
| chat_start 目标是当前 Agent（继承调用方模型） | 同上两行，按实际模型取小 |
| chat_start 目标是其他 Agent、agent_invoke 子任务、TEAM 成员 | 各自 Agent 的值与各自模型取小 |
| Automation query 指定模型 | 同请求级换模型 |
| 规划 Run 与确认计划后的执行 Run | 同一模型，同一窗口 |
| HITL 等待后续接 | 沿用原 Run 的模型与窗口 |
| 上下文摘要调用 | 与所在 Run 同模型，同一窗口 |
| BTW 旁路问答 | 按其请求模型取小（旁路不压缩，只影响 usage 的 maxSize） |
| 模型注册表后来调低上限到 Agent 值以下 | 运行时取模型上限；该 Agent 下次加载或保存报错，需调小 |
| 手动压缩（`/api/chat/compact`） | 与普通 query 同一规则、同一取值函数：Agent 配了用 Agent 值，没配用模型上限，再没有按 128000 |

## 生效方式

- 取值函数放在中立包（如 `internal/models`），`llm` 与 Server 共用；Server 不依赖 `llm`。
- 值由 `session_builder` 写入会话；`effectiveContextWindow()`（`internal/llm/run_stream_usage.go`）与手动压缩（`internal/server/handler_compact.go`）共用该函数，删除两处重复的 128000。
- 压缩阈值、压缩目标、摘要预算、L1 保留轮数、usage 的 `maxSize` 随之生效。
- TEAM 快照与 clone 需带上该字段。

## 状态

方案已确认，尚未实现。PLAN-EXECUTE 已下线（2026-10-26），不再涉及阶段换模型。
