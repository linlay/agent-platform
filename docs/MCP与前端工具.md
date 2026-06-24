# MCP与前端工具

## 当前状态

Go runtime 已具备 MCP registry、availability gate、client、reconnect 与 tool sync。MCP 工具会同步为 runtime tool 定义，并通过 `ToolRouter` 调用远端 MCP server。

frontend tools 目前以 builtin tool definitions、viewport、HITL 和 desktop bridge 能力为主，完整 Java 版 frontend tool 闭环仍未完全对齐。

## 核心流程

```text
REGISTRIES_DIR/mcp-servers
  -> MCP registry
  -> tool sync
  -> runtime tool registry
  -> ToolRouter invokeMCPTool
  -> normalize MCP result
```

本地 builtin tools 从 `internal/resources/tools/*.yml` 装载；自定义 frontend tool YAML 目录由 `configs/runtime.yml -> paths.tools-dir` 控制。

## 配置与接口

- `REGISTRIES_DIR`：registry 根目录。
- `configs/runtime.yml` 的 `paths.tools-dir`：自定义 frontend tool YAML 目录。
- `/api/admin/tools`：工具列表。
- `desktop_action`：Desktop bridge 动作入口。
- `ask_user_question`：内置 HITL question 工具。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用时，调用会返回结构化 MCP 错误。
- frontend tool 完整闭环仍属于待对齐能力，不能写成已完成能力。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/frontendtools/`
- `internal/resources/tools/`
- `internal/server/handler_catalog.go`
