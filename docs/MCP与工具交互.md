# MCP 与工具交互

## 当前状态

Go runtime 使用官方 Go MCP SDK `github.com/modelcontextprotocol/go-sdk` `v1.6.1`，同时支持 `streamable-http` 与 `stdio`。两种 transport 的唯一稳定协议版本都是 `2025-11-25`：client 在 `initialize` 中请求该版本，并在连接完成后严格检查服务端协商结果；返回旧版本、缺失版本或无效版本时会立即关闭会话、停止注册该 server 的工具，并将 server 放入 availability gate。

MCP registry、session client、availability gate、reconnect、tool sync 与热重载已经接通。平台只保留一种 Tool；本地、MCP、用户问题交互和 Desktop 能力共享同一工具定义与 `tool.*` 事件协议。

`registries/mcp-servers/*.yml` 由文件 watcher 热重载；管理端通过 `PUT /api/admin/source` 保存或 `DELETE /api/admin/source` 删除 MCP YAML 时会在响应前主动执行同一条 registry/session/tool sync 链路，不依赖 watcher 的 debounce。删除使用 `baseSha256` 防止误删并发修改，reload 硬失败时恢复原 YAML；删除或禁用 Server 会立即清理对应工具。合法配置即使远端暂时不可用也会保留，ToolSync 标记为 `unavailable` 并由 reconnect loop 重试；已有成功快照在临时失败期间继续保留。重连恢复或工具集合变化会发送 `catalog.updated(reason=mcp-servers)`，客户端无需重启 runtime。

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

本地 platform tools 从 `internal/resources/tools/*.yml` 装载；`configs/runtime.yml -> paths.tools-dir` 中的 YAML 只能覆盖已有 Go 实现的 schema、文案、权限和可选 UI 元数据。没有已注册代码实现的名字会使启动或热重载失败；动态新能力必须由 Go handler 或 MCP 提供。`sourceCategory: external` 仍可作为普通工具的来源分类，但不表示执行类型或子进程协议。

工具 YAML 根级不再接受 `type`、`kind`、`toolAction`、`submitResultFormat`。`viewportType`、`viewportKey` 只是客户端展示元数据，不决定路由、等待或结果格式。MCP 工具在 catalog 中固定返回 `sourceType: mcp`、`sourceCategory: mcp` 和对应 `serverKey`；MCP viewport 元数据也不会自动产生 awaiting。`/api/admin/tools` 不返回 `kind` 或内部 `meta`。MCP `annotations.readOnlyHint:true` 会映射为平台 `meta.readOnly:true`，供 BTW 只读门禁使用。

`tools/call` 优先使用 `structuredContent` 形成 `ToolExecutionResult.Structured`，否则读取 text content。`ToolExecutionResult.Output` 是实现最终回送模型的文本，LLM 层不会按 YAML 二次格式化。`isError:true` 会形成失败的工具结果；如果 `structuredContent.error` 或 `structuredContent.code` 存在，平台保留该业务错误码，例如 qiuerscript 的 `last_digest_required`、`method_not_found` 与 `digest_mismatch`，不会统一降级为 `mcp_tool_error`。

工具定义可选声明 `outputSchema`。没有 `outputSchema` 的 MCP 或 Desktop action result 按不透明 JSON 透传；平台不会根据 `createdAt`、`timestamp`、`iso` 等字段名猜测时间语义。

本地 `plan_add_tasks`、`plan_get_tasks`、`plan_update_task` 使用有序、单活动任务状态机。新 task 固定为 `init`；只允许最前面的非终态 task 进入 `in_progress`，且只有当前活动 task 可进入 `completed/failed/canceled`。相同状态更新幂等，终态重试通过追加新 task 表达。非法更新不改变内存或 snapshot，工具结果分别使用稳定错误码 `plan_task_predecessor_incomplete`、`plan_task_not_current`、`invalid_plan_task_transition`，并在 structured result 中返回未变化的 plan 和状态诊断。

## 图片生成与产物发布 URL

`image_generate` 统一覆盖文生图、图生图和局部重绘：省略 `images` 是文生图；传入 1–4 张 `images` 时，第一张固定为编辑主体，其余为参考图；可选 `mask` 固定作用于第一张图。图片和 mask 每项使用 `{"source_type":"reference_name","value":"image.png"}` 或 `{"source_type":"file_path","value":"@chat/image.png"}`；旧属性和字符串元素会在 provider 调用前失败。路径继续经过 AccessPolicy/HITL。运行时保留输入原始字节和 Alpha 通道，不使用视觉识别工具的大图 JPEG 重编码。

模型请求协议完全由模型 YAML 的 `image.generation` 与 `image.edit` 决定。GPT Image 可分别使用 Images JSON/Multipart；Gemini Image 可让文生图和图生图都使用 Chat Completions。runtime 不按 model key、modelId 或 provider 硬编码路由，profile 也不能覆盖 endpoint。

Mask 必须与第一张图同尺寸并显式指定 `mode`：`alpha` 表示透明区重绘，`white_edit` 表示白色区重绘，`black_edit` 表示黑色区重绘；灰度边缘转换为软 Alpha。模型 YAML 未声明 `maskProtocol: openai-alpha` 时返回 `image_generate_mask_unsupported`，不跨 profile 回退。成功结果的 `operation` 为 `generation`、`edit` 或 `inpainting`。

`image_generate` 和 `artifact_publish` 的工具说明共同约束模型输出：`path` 只用于工具间传递，可以是经授权的当前 Workspace/Chat 内 Host 绝对路径，禁止展示、写进 Markdown 或转换成 `file://`；用户可见内容只能逐字复制工具返回的 `url`，禁止手工拼接或编码资源地址。图片生成后使用 `images[n].url`；再次发布后改用 `publishedArtifacts[n].url`，因为后者指向 `artifacts/<runId>/` 发布副本。缺少有效 `url` 时必须报告资源物化或发布失败，不得伪造 Markdown。

```markdown
![夏日海报](chat_01/generated.png)
[下载夏日海报](chat_01/artifacts/run_01/generated.png)
```

新工具结果不返回 `/api/resource?file=...`；该形式仅供既有聊天只读兼容。浏览器数据层负责把逻辑引用转换为实际的 `GET /api/resource?file=<query-encoded-key>`。

## `desktop_action` 的 Chat Work Panel

`desktop.chatWorkPanel.getState/open/close/openTab/activateTab/closeTab` 操作当前 run 所属 Chat 的 Desktop 工作面板。模型参数不包含 `chatId`、`surfaceId` 或 `agentKey`；`tool_desktop_action.go` 与 `desktop_cdp` 都从 `ExecutionContext.Session` 生成同一份可信 `source`，模型不能覆盖。

需要实际操作网页时，先调用 `desktop.chatWorkPanel.openTab({url,title?})` 或 `getState` 取得 Tab 的 `targetId`，再调用 `desktop_cdp`。Desktop 仅允许当前激活 surface target，或 `ownerChatId` 与可信 `source.chatId` 一致的 Work Panel target；Work Panel 不进入普通 `desktop.web.listSurfaces` 或 `Target.getTargets`。

## `desktop_action` 的 WebClient Provider

Agent 仍然只看到一个 `desktop_action`。它的 Action 白名单由 `internal/resources/tools/desktop_action.yml` 静态声明，不存在 WebClient Surface 注册或 capability 协商：

- `desktop.*` 继续调用 Desktop Action Bridge。
- `webclient.*` 通过发起当前 run 的 WebClient WebSocket 发送反向 request；失败不会回退 Desktop。

WebSocket 映射是扁平的：`desktop_action.action` 直接成为 request `type`，`desktop_action.args` 直接成为 `payload`，`requestId` 成为 `id`；`confirmationSummary` 只属于 Desktop Provider，不会发给 WebClient。当前开放 `webclient.sidebar.getState`、`webclient.sidebar.setState`、`webclient.sidebar.openUrl` 与 `webclient.sidebar.refreshUrl`，Platform 和 WebClient 都做精确参数校验。

`webclient.sidebar.openUrl` 使用 `{url, title?}` 创建或激活当前 WebClient 右侧栏中的 Web Preview，并切换到 `web` tab。裸域名会按 HTTPS 规范化；只接受 HTTP(S)，拒绝协议相对 URL、携带用户名或密码的 URL 以及额外参数。该 Action 的成功只代表 WebClient 状态已应用，不保证目标站点允许 iframe 嵌入；遇到 CSP 或 `X-Frame-Options` 拒绝时由现有 Preview 展示加载失败，不回退到 Desktop bridge 或外部浏览器。

`webclient.sidebar.refreshUrl` 使用精确的 `{url}` 重载已存在的规范化 Web Preview。URL 校验与 `openUrl` 一致，但不创建 Preview、不打开右侧栏、不切换 tab 或活动 Preview；当前视图不支持右侧栏或目标 URL 未打开时，WebClient 返回 `unsupported_in_current_view`。成功只表示刷新信号已应用，不保证 iframe 重新加载成功。

WebSocket query 直接绑定当前连接，不检查连接自报的 `source`；即使没有 `surfaceId`，该 run 仍可按 WebSocket session 定位原连接。HTTP SSE query 与 attach 通过 `X-Agent-WebClient-Device-Id`、`X-Agent-WebClient-Surface-Id` 绑定同一认证主体和 device 边界内的逻辑 surface；device header 与 `/ws?deviceId=...` 相同，认证 JWT 已含 device claim 时以 claim 为准。WS attach 直接使用发起 attach 的连接。每次成功且携带有效 WebClient target 的 attach 都以 last-writer-wins 更新该 run 的反向 Action target；失败 attach 或普通无 target attach 不改变已有绑定，已发出的 Action 不迁移。Team 内部成员与 `agent_invoke` 子 run 按根 run 动态读取相同 target，planning 新 execution run 继承 source run 的当前 target，automation 与 `run_query` 创建的独立根 run 不继承。目标元数据只保存在运行内存，不进入 prompt、事件、chat 或数据库。

`desktop_action_target_unavailable` 保持稳定错误码，并用 `details.reason` 区分 `run_target_missing`（run 尚无 target）与 `target_connection_unavailable`（已绑定 target 当前无可用连接）。

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
- `/api/admin/registries`：MCP summary 返回 `transport`、`toolCount`、`syncStatus`，以及可选的 `lastSyncAttemptAt`、`lastSyncSuccessAt`、`syncDiagnostic`。HTTP 项返回 `baseUrl`；stdio 项不返回无意义的 `baseUrl`，也不暴露 `command`、`args`、`env` 或同步错误中的 secret。
- `/api/admin/registries/detail`：用于查看或保存完整 registry YAML；敏感配置不要提交到仓库。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用或协议版本不兼容时，调用返回结构化 MCP unavailable 错误。
- `qiuerscript-tool` 在 stdin 关闭后正常退出，不支持私有 `shutdown` RPC。
- `desktop_action` 的四个 WebClient sidebar Action 已闭环；这里的 Action 是 Desktop/WebClient 业务操作名，不是 Tool 类型，也不会生成 `action.*` stream 事件。
- `ask_user_question` 由 `internal/toolinteraction` 中明确注册的 handler 负责等待、submit 规范化和固定 QA 模型输出；没有通用 YAML 表单 fallback。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/toolinteraction/`
- `internal/resources/tools/`
- `internal/server/handler_admin_registries.go`
