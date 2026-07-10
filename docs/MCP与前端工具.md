# MCP与前端工具

## 当前状态

Go runtime 已具备 MCP registry、availability gate、client、reconnect 与 tool sync。MCP 工具会同步为 runtime tool 定义，并通过 `ToolRouter` 调用远端 MCP server。

frontend tools 目前以 builtin tool definitions、viewport、HITL 和 desktop bridge 能力为主，完整 Java 版 frontend tool 闭环仍未完全对齐。

服务包根目录的 `bin/{rg,dbx,httpx}` 属于 Host builtin executable，由 agent-platform 发布锁文件管理；它们不是 MCP server，也不是 support package。结构化 `file_grep/file_glob` 包装 rg，dbx/httpx 首版保持 CLI 架构并可由 Host Bash 调用。

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

Support package 是放在 agent-platform 程序包旁的轻量依赖声明目录，不属于 runtime 数据目录。启动时 runtime 会扫描 `${executableDir}/plugins/*/manifest.json`；如果可执行程序位于服务包的 `backend/` 目录下，则只扫描服务包根目录的 `plugins/*/manifest.json`，不扫描 `backend/plugins`。只读取 `kind: "support-package"` 且 `platform.os` / `platform.arch` 匹配当前进程的 manifest，并把 `executables` 中声明的可执行文件路径解析到内存 registry。当前该机制用于 KBASE PDF 抽取：如果配置里的 `extraction.pdf.binary` 是默认命令名 `pdftotext` / `pdftotext.exe`，且发现了 `executables.pdftotext`，KBASE 会优先使用 manifest 解析出的绝对路径；显式配置的绝对路径或自定义命令不会被覆盖。该机制不解压 zip、不写 PATH、不修改 `configs/kbase-settings.yml`。

Windows 示例目录：

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

macOS 示例目录：

```text
agent-platform
plugins/
  pdf-extractor/
    manifest.json
    payload/darwin-arm64/bin/pdftotext
    payload/darwin-arm64/lib/*.dylib
```

macOS manifest 中 `platform.os` 为 `darwin`，`platform.arch` 为 `arm64`，`executables.pdftotext` 指向包内 `payload/darwin-arm64/bin/pdftotext`。darwin-arm64 包会携带 Poppler 所需 dylib，Desktop 使用该 support package 时不要求用户额外安装 Homebrew Poppler。

Desktop 内置服务场景下，agent-platform 通常安装在品牌程序数据目录的 `services/agent-platform/<version>/` 下，真实二进制位于 `backend/agent-platform` 或 `backend/agent-platform.exe`。推荐把 support package 放在服务包根目录：

```text
<program-data-root>/services/agent-platform/<version>/
  backend/agent-platform
  plugins/
    pdf-extractor/
      manifest.json
```

`<program-data-root>` 由 Desktop 宿主按品牌解析，例如 macOS `~/Library/Application Support/<BRAND>/`、Windows `%APPDATA%\<BRAND>\`。support package 只依赖 Desktop 部署传入的服务包位置；platform 不需要内置具体品牌名，也不应把 support package 放进运行态数据目录。

本机源码验证 support package 时使用 `release-local/` 作为 Desktop 服务包镜像，不使用 `runtime/`。`make run` 会先构建并从 `release-local/backend/agent-platform` 启动；插件放在 `release-local/plugins/`。`runtime/` 继续作为 agents、chats、skills-market、registries、memory 等运行数据目录。

## 配置与接口

- `AP_RUNTIME_REGISTRIES_DIR`：registry 根目录。
- `configs/runtime.yml` 的 `paths.tools-dir`：自定义 frontend tool YAML 目录。
- `${executableDir}/plugins`：可执行程序旁 support package 目录；仅用于可执行程序不在 `backend/` 目录下的场景。
- `${serviceBundleRoot}/plugins`：当可执行程序位于 `backend/` 下时扫描的 Desktop / 服务包根目录。
- `/api/admin/tools`：工具列表，无 query 过滤参数；响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 `meta`。
- `desktop_action`：Desktop bridge 动作入口。
- `ask_user_question`：内置 HITL question 工具。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用时，调用会返回结构化 MCP 错误。
- support package 只读取已解压目录下的 manifest，不自动安装或解压 zip。
- Host builtins 与 support package 分开：`bin/` 进入进程级 PATH，`plugins/` 只按 manifest 解析绝对 executable；两者均不会自动进入 sandbox。
- frontend tool 完整闭环仍属于待对齐能力，不能写成已完成能力。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/frontendtools/`
- `internal/resources/tools/`
- `internal/server/handler_catalog.go`
