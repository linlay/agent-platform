# 核心业务逻辑

## 1. ONESHOT
```text
输入 message
-> 选择 stage 工具集
-> 若无工具: 单轮模型流式输出 final
-> 若有工具:
   - 首轮尝试产出 tool call（必要时 repair）
   - 执行工具并发回 tool.result
   - 二轮模型总结输出 final
-> run.complete
```

关键点：
- 当 `ToolChoice.REQUIRED` 且模型不出工具，会进入修复重试。
- frontend tool 超时会输出用户可见超时文案并结束运行。

## 2. REACT
```text
for step in [1..maxSteps]
  调模型（可 reasoning + content + tool call）
  if 有 tool call:
     执行首个工具并继续下一轮
  else if ToolChoice.REQUIRED:
     继续下一轮
  else if 有 final text:
     输出并结束
超出上限后进行 final 强制总结轮
```

关键点：
- 每轮最多一个工具调用（由模式流程控制）。
- `maxSteps` 默认 6。

## 3. PLAN_EXECUTE
```text
校验 ToolChoice != NONE
plan 阶段:
  - deepThinking=true: 先 draft(无工具) 再 generate(必须 _plan_add_tasks_)
  - deepThinking=false: 直接 generate(必须 _plan_add_tasks_)
execute 阶段:
  - 逐 task 执行
  - 每任务 work rounds + forced update rounds
  - 必须通过 _plan_update_task_ 进入 completed/canceled/failed
summary 阶段:
  - 汇总执行结果输出最终回答
```

关键点：
- `failed` 任务会中断后续执行。
- `_plan_add_tasks_` / `_plan_update_task_` 是强制路径。

## 4. Tool 执行与事件
- `ToolExecutionService` 负责参数解析、执行、记录、回填 `AgentDelta`。
- backend tool: `tool.start/args/end/result`
- frontend tool: 等待 `/submit` 回填
- action tool: 直接返回 `OK`，不等待提交

## 5. 预算与超时
- `ExecutionContext` 中维护 model/tool 调用计数和预算。
- LLM 超时使用 `LlmCallSpec.timeoutMs` 或默认值。
- frontend submit 超时由 `FrontendToolProperties.submitTimeoutMs` 控制。
