# MCP与前端工具

## 当前状态

Go runtime 已具备 MCP registry、availability gate、client、reconnect 与 tool sync。MCP 工具会同步为 runtime tool 定义，并通过 `ToolRouter` 调用远端 MCP server。

frontend tools 目前以 builtin tool definitions、viewport、HITL 和 desktop bridge 能力为主，完整 Java 版 frontend tool 闭环仍未完全对齐。

## 核心流程

```text
AP_RUNTIME_REGISTRIES_DIR/mcp-servers
  -> MCP registry
  -> tool sync
  -> runtime tool registry
  -> ToolRouter invokeMCPTool
  -> normalize MCP result
```

本地 platform tools 从 `internal/resources/tools/*.yml` 装载；自定义 frontend / external tool YAML 目录由 `configs/runtime.yml -> paths.tools-dir` 控制。`/api/admin/tools` 返回扁平来源字段：`sourceType` 表示定义来源类型（如 `local`、`agent-local`、`mcp`），`sourceCategory` 表示来源分类，`platform` 为 runtime 自带工具，`external` 为 tools-dir 接入工具，`mcp` 为 MCP 同步工具；`kind` 只表示调用方式，如 `backend`、`frontend`、`action`、`external`。MCP 工具额外返回 `serverKey`。

Support package 是放在 agent-platform 可执行程序旁的轻量依赖声明目录，不属于 runtime 数据目录。启动时 runtime 会扫描 `${executableDir}/plugins/*/manifest.json`，只读取 `kind: "support-package"` 且 `platform.os` / `platform.arch` 匹配当前进程的 manifest，并把 `executables` 中声明的可执行文件路径解析到内存 registry。当前该机制用于 KBASE PDF 抽取：如果配置里的 `extraction.pdf.binary` 是默认命令名 `pdftotext` / `pdftotext.exe`，且发现了 `executables.pdftotext`，KBASE 会优先使用 manifest 解析出的绝对路径；显式配置的绝对路径或自定义命令不会被覆盖。该机制不解压 zip、不写 PATH、不修改 `configs/kbase-settings.yml`。

示例目录：

```text
agent-platform.exe
plugins/
  pdf-extractor/
    manifest.json
    payload/windows-amd64/Library/bin/pdftotext.exe
```

示例 manifest：

```json
{
  "kind": "support-package",
  "id": "pdf-extractor",
  "version": "v0.3.9",
  "platform": { "os": "windows", "arch": "amd64" },
  "executables": {
    "pdftotext": "payload/windows-amd64/Library/bin/pdftotext.exe"
  }
}
```

## 配置与接口

- `AP_RUNTIME_REGISTRIES_DIR`：registry 根目录。
- `configs/runtime.yml` 的 `paths.tools-dir`：自定义 frontend tool YAML 目录。
- `${executableDir}/plugins`：可执行程序旁 support package 目录，目前用于声明 KBASE PDF 抽取依赖。
- `/api/admin/tools`：工具列表，无 query 过滤参数；响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 `meta`。
- `desktop_action`：Desktop bridge 动作入口。
- `ask_user_question`：内置 HITL question 工具。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用时，调用会返回结构化 MCP 错误。
- support package 只读取已解压目录下的 manifest，不自动安装或解压 zip。
- frontend tool 完整闭环仍属于待对齐能力，不能写成已完成能力。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/frontendtools/`
- `internal/resources/tools/`
- `internal/server/handler_catalog.go`
