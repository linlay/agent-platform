# 工具运行时（tool_runtime）

## 关键类
- `ToolRegistry`
- `CapabilityRegistryService`
- `ToolExecutionService`
- `FrontendSubmitCoordinator`

## 工具来源
1. Java 内置工具（`BaseTool` 实现）
2. 外置工具文件（`tools/*.backend|*.frontend|*.action`）

## 冲突规则
- 同名 capability 冲突时，两者都跳过（防止二义性）。
- backend capability 若无同名 Java 实现，会被跳过并告警。

## 调用类型
- backend: `toolType=function`
- frontend: `toolType=frontend`（并补充 `toolKey/toolTimeout`）
- action: `toolType=action`

## Frontend submit 协议
- 执行时注册 pending key: `runId#toolId`
- `/submit` 命中后完成 future 并返回 `accepted`
- 超时抛 `TimeoutException`，转为运行失败提示
