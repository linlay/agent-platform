# 模式模块（modes）

## 模式列表
- `OneshotMode`
- `ReactMode`
- `PlanExecuteMode`

## ONESHOT
- 默认 toolChoice：有工具时 `AUTO`，无工具时 `NONE`。
- 两段式（有工具）：`first(tool)` -> `execute` -> `final(no tool)`。

## REACT
- 默认 toolChoice：`AUTO`。
- 循环 `maxSteps`（默认 6），每轮尝试一个工具调用。
- 达到上限后触发 `agent-react-final` 兜底总结轮。

## PLAN_EXECUTE
- 默认 toolChoice：`AUTO`，且显式拒绝 `NONE`。
- plan 阶段必须包含 `_plan_add_tasks_`。
- execute 阶段每 task 需要 `_plan_update_task_` 收敛状态。
- summary 阶段输出最终汇总。

## 任务状态约束
- 合法状态：`init`, `completed`, `failed`, `canceled`
- 兼容映射：`in_progress -> init`
