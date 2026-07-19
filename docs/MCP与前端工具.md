# MCP 与前端工具

## 当前状态

Go runtime 使用官方 Go MCP SDK `github.com/modelcontextprotocol/go-sdk` `v1.6.1`，同时支持 `streamable-http` 与 `stdio`。两种 transport 的唯一稳定协议版本都是 `2025-11-25`：client 在 `initialize` 中请求该版本，并在连接完成后严格检查服务端协商结果；返回旧版本、缺失版本或无效版本时会立即关闭会话、停止注册该 server 的工具，并将 server 放入 availability gate。

MCP registry、session client、availability gate、reconnect、tool sync 与热重载已经接通。frontend tools 目前以 builtin tool definitions、viewport、HITL 和 desktop bridge 为主，完整 Java 版 frontend tool 闭环仍未完全对齐。

服务包根目录的 `bin/{rg,dbx,httpx,pdftotext}` 属于 Host builtin executable，不是 MCP server。只有明确注册到 `registries/mcp-servers/*.yml` 的 HTTP endpoint 或 stdio command 才进入 MCP 生命周期。

## 核心流程

```text
AP_RUNTIME_REGISTRIES_DIR/mcp-servers
  -> MCP registry
  -> official SDK initialize + notifications/initialized
  -> one concurrent-safe session per serverKey
  -> paginated tools/list
  -> runtime tool registry
  -> ToolRouter tools/call
  -> normalize content / structuredContent / isError
```

SDK 负责 session ID、MCP 协议头、JSON/SSE 响应、`notifications/initialized` 和标准关闭流程。registry 删除、连接字段变更、连接失效或应用关闭都会释放 session；stdio session 同时终止并回收子进程。连接和工具同步可按 `retry` 重试，但已经发出的 `tools/call` 不会自动重放，避免写工具重复执行。

## Registry 配置

`transport` 默认为 `streamable-http`，所以已有合法 HTTP 配置不需要补字段。

HTTP 示例：

```yaml
serverKey: remote-search
name: Remote Search
transport: streamable-http
baseUrl: http://127.0.0.1:8080
endpointPath: /mcp
authToken: ${REMOTE_MCP_TOKEN}
headers:
  X-Tenant: local
connect-timeout: 3
read-timeout: 30
retry: 1
```

stdio 示例：

```yaml
serverKey: qiuerscript
name: Qiuerscript
transport: stdio
command: ../../tools/qiuerscript/qiuerscript-tool
args: [serve, --datasource, dev]
env: {}
workingDirectory: ../..
startup-timeout: 5
read-timeout: 30
retry: 1
```

字段约束：

- `streamable-http` 必须提供 `baseUrl`，不得出现 `command`、`args`、`env` 或 `workingDirectory`。
- `stdio` 必须提供 `command`，不得出现 `baseUrl`、`endpointPath`、`authToken` 或 `headers`。
- 相对 `command` 与 `workingDirectory` 都相对于当前 registry YAML 所在目录解析。
- stdio 环境继承 runtime 进程环境，并保留 Host builtin PATH；`env` 只覆盖或追加显式变量。
- `startup-timeout` 控制初始化期限，`read-timeout` 控制 `tools/list` 和 `tools/call` 的单次操作期限，单位均为秒。
- 任意非法 transport、缺少必填字段或字段混用都会使启动/热重载硬失败；registry 不会静默跳过这些文件。

## 工具来源与结果

本地 platform tools 从 `internal/resources/tools/*.yml` 装载；自定义普通 frontend/action/agent-local tool YAML 目录由 `configs/runtime.yml -> paths.tools-dir` 控制。`sourceCategory: external` 仍可作为普通工具的来源分类，但不再表示子进程协议。

MCP 工具在 catalog 中固定返回 `sourceType: mcp`、`sourceCategory: mcp` 和对应 `serverKey`。`/api/admin/tools` 只返回公开扁平字段，不透出内部 `meta`。MCP `annotations.readOnlyHint:true` 会映射为平台 `meta.readOnly:true`，供 BTW 只读门禁使用。

`tools/call` 优先使用 `structuredContent` 形成 `ToolExecutionResult.Structured`，否则读取 text content。`isError:true` 会形成失败的工具结果；如果 `structuredContent.error` 或 `structuredContent.code` 存在，平台保留该业务错误码，例如 qiuerscript 的 `last_digest_required`、`method_not_found` 与 `digest_mismatch`，不会统一降级为 `mcp_tool_error`。

工具定义可选声明 `outputSchema`。没有 `outputSchema` 的 MCP 或 Desktop action result 按不透明 JSON 透传；平台不会根据 `createdAt`、`timestamp`、`iso` 等字段名猜测时间语义。

## 旧 external stdio 配置已删除

私有 external stdio JSON-RPC 协议、`ExternalToolManager`、私有 `initialize/shutdown/tools/call` 和 `kind: external` 调用分支均已删除，不提供兼容期。`paths.tools-dir` 中出现以下任一内容时，启动和热重载都会返回带迁移提示的硬错误：

- `service.yml` 或 `service.yaml`
- `type: external`
- `external:` 字段，包括空对象
- `kind: external-service`

迁移方式是删除旧 service/tool YAML，把子进程改为标准 MCP server，并新增一个 `registries/mcp-servers/*.yml` 的 `transport: stdio` 定义。平台二进制、stdio server 二进制和 registry 配置必须同批发布，旧私有配置不能与新版 runtime 滚动混用。

Qiuerscript 已按此方式迁移。`qs_read`、`qs_glob`、`qs_grep`、`qs_write`、`qs_edit`、`qs_delete` 的工具名、参数、默认值和结构化业务结果保持不变；前三项声明只读 annotations，后三项声明写入/破坏性 annotations。

## 管理接口

- `/api/admin/tools`：MCP 工具返回 `sourceType/sourceCategory: mcp` 与 `serverKey`。
- `/api/admin/registries`：MCP summary 返回 `transport`。HTTP 项返回 `baseUrl`；stdio 项不返回无意义的 `baseUrl`，也不暴露 `command`、`args` 或 `env`。
- `/api/admin/registries/detail`：用于查看或保存完整 registry YAML；敏感配置不要提交到仓库。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用或协议版本不兼容时，调用返回结构化 MCP unavailable 错误。
- `qiuerscript-tool` 在 stdin 关闭后正常退出，不支持私有 `shutdown` RPC。
- frontend tool 完整闭环仍属于待对齐能力，不能写成已完成能力。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/frontendtools/`
- `internal/resources/tools/`
- `internal/server/handler_admin_registries.go`
