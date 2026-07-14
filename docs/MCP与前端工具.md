# MCP与前端工具

## 当前状态

Go runtime 已具备 MCP registry、availability gate、client、reconnect 与 tool sync。MCP 工具会同步为 runtime tool 定义，并通过 `ToolRouter` 调用远端 MCP server。

frontend tools 目前以 builtin tool definitions、viewport、HITL 和 desktop bridge 能力为主，完整 Java 版 frontend tool 闭环仍未完全对齐。

服务包根目录的 `bin/{rg,dbx,httpx,pdftotext}` 属于 Host builtin executable，由 agent-platform 发布锁文件管理；它们不是 MCP server，也不是 support package。结构化 `file_grep/file_glob` 包装 rg，dbx/httpx 保持 CLI 架构；KBASE 的 PDF 抽取默认调用 `pdftotext`。

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

工具定义可以可选声明 `outputSchema`，它描述 `ToolExecutionResult.Structured`，不是 tool 调用参数。runtime 只遍历 `properties`、`items`、`oneOf` 来找到显式时间标记：`x-platform-time: "epoch-ms"` 校验为 epoch-ms 整数，`format: "date-time"` 校验 RFC3339，`x-platform-time-pair` 校验配对时刻一致。没有 `outputSchema` 的 MCP、external 或 Desktop action result 完全按不透明 JSON 透传；平台不会依据 `createdAt`、`timestamp`、`iso` 等字段名猜测含义。

KBASE PDF 不再通过 `plugins/*/manifest.json` 发现 extractor。正式服务包为受支持目标把 launcher 写入 `bin/pdftotext[.exe]`，并把 Poppler 原生 runtime 写入 `libexec/poppler-pdftotext/<os>-<arch>/`。launcher 透传命令行与退出码，动态库与数据文件不进入全局 PATH。默认 `extraction.pdf.binary: pdftotext` 因此无需额外配置；自定义绝对路径或命令名仍由使用者负责提供。

## 配置与接口

- `AP_RUNTIME_REGISTRIES_DIR`：registry 根目录。
- `configs/runtime.yml` 的 `paths.tools-dir`：自定义 frontend tool YAML 目录。
- `${serviceBundleRoot}/bin/pdftotext[.exe]`：受支持服务包随附的 PDF 提取 launcher。
- `${serviceBundleRoot}/libexec/poppler-pdftotext/<os>-<arch>/`：隔离的 Poppler runtime（含 CJK CMap data）；不可手动替换单个文件。
- `/api/admin/tools`：工具列表，无 query 过滤参数；响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 `meta`。
- `desktop_action`：Desktop bridge 动作入口。
- `ask_user_question`：内置 HITL question 工具。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用时，调用会返回结构化 MCP 错误。
- Poppler 的目标矩阵为 darwin-arm64 与 windows-amd64；当前 lock 已校验 darwin-arm64。windows-amd64 必须先提交完整的 Poppler 26.06.0 runtime、DLL 与 `share/poppler` tree，才可启用该 target 的发布。未包含的目标可设置自定义 `extraction.pdf.binary` 或由系统 PATH 提供 `pdftotext`。
- Host builtins 的 `bin/` 进入进程级 PATH，但不会自动进入 sandbox；Poppler runtime 本身仅由 launcher 调用。
- frontend tool 完整闭环仍属于待对齐能力，不能写成已完成能力。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/frontendtools/`
- `internal/resources/tools/`
- `internal/server/handler_catalog.go`
