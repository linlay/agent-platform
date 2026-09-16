# MCP 与工具交互

## 当前状态

Go runtime 使用官方 Go MCP SDK `github.com/modelcontextprotocol/go-sdk` `v1.6.1`，同时支持 `streamable-http` 与 `stdio`。两种 transport 都在 `initialize` 中优先请求 `2025-11-25`，并接受 SDK 支持的 `2025-06-18`、`2025-03-26` 和 `2024-11-05`。兼容范围由锁定的 SDK 校验，不要求连接器配置版本；返回缺失、无效或未知版本时会关闭连接、停止注册该 server 的工具，并将 server 放入 availability gate。

SDK 使用实际协商版本处理后续消息；HTTP 的 `notifications/initialized`、`tools/list`、`tools/call` 和关闭请求均携带协商后的 `MCP-Protocol-Version`，初始化响应日志也记录实际版本。Platform 只在 Transport 层保留初始化失败的清理引用，不包装 SDK Connection，避免遮蔽 SDK 内部的 session 状态更新和 SSE 启动。OAuth 授权成功不代表 MCP 同步成功；上游 `429 Too Many Requests` 属于独立的限流失败，协议兼容不会绕过限流或自动重放已发出的工具调用。

MCP registry、session client、availability gate、后台同步/重连与热重载已经接通。平台只保留一种 Tool；本地、MCP、用户问题交互和 Desktop 能力共享同一工具定义与 `tool.*` 事件协议。Platform 启动只同步装载和校验本地 MCP Registry，首次远端连接、初始化与 `tools/list` 由单 worker 在后台执行，不属于 HTTP 服务监听或 `/healthz` 的就绪条件。

外部连接器放在 `<AP_RUNTIME_DIR>/connectors/<id>/`；内置连接器放在 Platform 随包 `connectors/`，使用相同的加载与 Agent 挂载契约。MCP 通过包内 `mcp.json` 注册，CLI 可以与 MCP、`bin/` 和技能放在同一个包里；`dbx/httpx` 已成为 `builtin.dbx/builtin.httpx`，不再处于全局 builtin bin。

## 连接器与 MCP

```text
runtime/connectors-center/<id>/{connector.json,mcp.json} + Platform builtin
  -> 本地校验原包，按 Agent 组装 runtime/ru-agents/<agentKey>/connectors/<id>
  -> MCP registry
  -> 后台 official SDK initialize + notifications/initialized
  -> per-server session / tools/list
  -> 已挂载 connector 的 Agent run 工具集合
  -> ToolRouter tools/call
```

Agent 使用 `connectorConfig.connectors: [remote-search]` 挂载。挂载同时导入该包技能并添加 bin PATH；连接器技能禁止通过 mustUseSkills 选择。JSON 示例、组件边界、builtin 包布局和迁移命令见 [连接器](连接器.md)。旧 `registries/mcp-servers` 目录直接忽略，不影响启动；`toolConfig.mcp-servers` 与对应 Registry 管理入口已移除，运行时只加载新连接器定义。

连接器目录变化先校验本地来源，再发布空闲 Agent 的运行包并绑定 MCP 实例；活动 Agent 在使用者结束后更新。实例按 Agent、连接器和组件隔离，工具路由使用稳定实例标识，并在远端调用时恢复原始工具名。未挂载组件不建立连接，认证仍共享 `.state/connectors/<id>`。`PUT /api/admin/connectors/detail` 校验完整候选包并立即 reload；失败恢复原文件。远端初始化、工具发现与重连由后台协调器串行、合并执行；删除、禁用或连接配置变化会清除对应旧工具快照，只有匹配当前 Registry version 的结果可以发布。远端暂时不可用时保留合法配置，标记 unavailable 并重试；同步状态或工具集合变化发送 `catalog.updated(reason=connectors)`。

SDK 负责 session ID、协议头、JSON/SSE、初始化通知和标准关闭；stdio session 关闭时回收子进程。已发出的 tools/call 不自动重放。清单 `auth_mode` 和已准备环境的当前支持范围见连接器专题，部署级 CLI 登录和 MCP OAuth 已实现，按用户绑定凭据和通用 runtime 安装尚未实现。迁移后的 `platform.authSource=identity-file` 归入 oneid-token，复用 Desktop SSO 的 `AP_ACCESS_TOKEN` 身份环境，不复制 Token 到 `.state/connectors`。stdio MCP 仅该模式注入此变量，身份变化后下次调用重建进程；HTTP MCP 即时生成 Bearer Header，限定 HTTPS 默认端口和完整 endpoint，拒绝跨目的地转发。

## 工具来源与结果

本地 platform tools 从 `internal/resources/tools/*.yml` 装载；`<AP_RUNTIME_DIR>/tools` 中的 YAML 只能覆盖已有 Go 实现的 schema、文案、权限和可选 UI 元数据。没有已注册代码实现的名字会使启动或热重载失败；动态新能力必须由 Go handler 或 MCP 提供。`sourceCategory: external` 仍可作为普通工具的来源分类，但不表示执行类型或子进程协议。

工具 YAML 根级不再接受 `type`、`kind`、`toolAction`、`submitResultFormat`。`viewportType`、`viewportKey` 只是客户端展示元数据，不决定路由、等待或结果格式。MCP 工具在 catalog 中固定返回 `sourceType: mcp`、`sourceCategory: mcp` 和对应 `serverKey`；MCP viewport 元数据也不会自动产生 awaiting。`/api/admin/tools` 不返回 `kind` 或内部 `meta`。MCP `annotations.readOnlyHint:true` 会映射为平台 `meta.readOnly:true`，供 BTW 只读门禁使用。

`tools/call` 优先使用 `structuredContent` 形成 `ToolExecutionResult.Structured`，否则读取 text content。`ToolExecutionResult.Output` 是实现最终回送模型的文本，LLM 层不会按 YAML 二次格式化。`isError:true` 会形成失败的工具结果；如果 `structuredContent.error` 或 `structuredContent.code` 存在，平台保留该业务错误码，例如 qiuerscript 的 `last_digest_required`、`method_not_found` 与 `digest_mismatch`，不会统一降级为 `mcp_tool_error`。

工具定义可选声明 `outputSchema`。没有 `outputSchema` 的 MCP 或 Desktop action result 按不透明 JSON 透传；平台不会根据 `createdAt`、`timestamp`、`iso` 等字段名猜测时间语义。

本地 `plan_add_tasks`、`plan_get_tasks`、`plan_update_task` 使用有序、单活动任务状态机。新 task 固定为 `init` 且不会自动启动；只允许最前面的非终态 task 由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，且只有当前活动 task 可由 `in_progress` 进入任一终态。相同状态更新幂等，终态重试通过追加新 task 表达。非法更新不改变内存或 snapshot，工具结果分别使用稳定错误码 `plan_task_predecessor_incomplete`、`plan_task_not_current`、`invalid_plan_task_transition`，并在 structured result 中返回未变化的 plan 和状态诊断。

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

## `desktop_action` 的 WorkPanel

`desktop.workpanel.*` 只操作当前 run 所属 Chat 的工作面板。动作 `args` 顶层不接受 `chatId`、`workspaceId`、`surfaceId`、`agentKey`、`stableKey`、`preload` 或 `webPreferences`；Platform 从 `ExecutionContext.Session` 生成可信顶层 `source`，模型不能覆盖。合法 WebClient descriptor 的 `context` 可以携带契约要求且与可信 Chat 一致的上下文身份，但不能用它改选所属 Chat。

当前公开动作及精确参数如下：

- `desktop.workpanel.getState`：无参数，读取工作区、条目和活动条目。
- `desktop.workpanel.openTab({descriptor})`：按规范描述符打开或激活一个确定性 Tab。网页描述符使用 `{kind:"web",url,title?,pinned?,closable?}`；WebClient 描述符必须遵守其 module、route 和 context 契约。
- `desktop.workpanel.openWeb({url})`：规范化无用户名/密码的 HTTP(S) URL，并打开或激活对应 WebView；Desktop 宿主可访问的 `localhost`、`127.0.0.1` 或 `[::1]` 服务同样允许。
- `desktop.workpanel.openLocalFile({path,title?})`：仅在 Desktop runtime 中使用来源 Agent 权威 Workspace 下的相对路径打开本地文件。拒绝绝对路径、盘符、UNC、`..`、`file://`、目录、缺失文件和 realpath 越界；Platform 不把本地文件转换为临时 HTTP 服务。
- `desktop.workpanel.refreshWeb({url})`：按规范化 URL 精确查找已经打开的 WebView，原位重载并激活，不创建新条目。
- `desktop.workpanel.activateTab({tabId})`：激活 `getState` 返回的 `state.items[].itemId`。
- `desktop.workpanel.closeTab({tabId})`：关闭可关闭且未固定的 Tab。
- `desktop.workpanel.closeWorkpanel`：无参数。Desktop 模式关闭当前 Chat 的整个 WorkPanel；Standalone 只隐藏右侧栏并保留已打开的 Web Preview。
- `desktop.display({kind:"effect",effect,durationMs?})`：在 Desktop Main Window 或 Standalone 根页面显示 fireworks、snowfall、nationalDay。时长缺省 8000 ms，必须是 1000–30000 的整数；启动后立即返回 accepted，新请求替换当前效果。

`openWeb` 与 `refreshWeb` 只接受显式 `http:` 或 `https:` URL，拒绝用户名和密码，但不排除宿主可达的本地 HTTP 服务；`file://` 仍不是 Web URL。`openLocalFile` 只允许普通 Agent 的 Desktop Platform Run，Standalone、Team、Desktop WebSocket、HTTP Action Bridge、调试入口、WebApp 与 Agent WebClient 均无此能力。WorkPanel 的 `tabId` 是条目 ID，不是 CDP `targetId`；不要把它传给 `desktop_cdp`。WorkPanel 也不进入普通 `desktop.web.listSurfaces` 或 `Target.getTargets`，因此高层 Tab/WebView 操作应使用上述动作；只有已经获得独立 CDP `targetId` 时才能进行页面级 CDP 调用，Desktop 仍会校验其 `ownerChatId` 与可信 `source.chatId` 一致。

## `desktop_cdp` 参数文件

`desktop_cdp` 可直接传入 `params` 对象，也可通过 `paramsFile` 读取参数文件。两者互斥（包括显式传入 `params: null`）；都省略时保持无参数调用。工具层使用 `paramsFile`，没有 `param-file` 或 `params-file` 别名。

```json
{
  "method": "Runtime.evaluate",
  "targetId": "实际 targetId",
  "paramsFile": "params.json"
}
```

`params.json` 只保存原来的 `params` 内容，不包含外层 `method`、`targetId` 等请求字段：

```json
{
  "expression": "document.title",
  "returnByValue": true
}
```

相对路径按本次执行 Workspace 解析；没有 Workspace 时返回 `workspace_unavailable`，不回退到 Platform 进程目录。也支持 `@workspace`、`@chat`、`@temp` 等通用路径别名，以及经授权的绝对路径；Container 执行路径使用现有映射解析为 Host 路径。文件读取复用 AccessPolicy 和文件预审批，越权读取进入 HITL，批准后继续执行；canonical 路径校验与临时根逃逸阻断仍生效。

文件必须是 UTF-8 编码的单个 JSON 对象，拒绝目录、设备等非普通文件、空内容、`null`、数组、JSON 后的额外内容及非法 JSON。读取上限复用 `configs/tools.yml -> file-tools.max-read-bytes`（默认 1 MiB），超限报错，不截断。路径、读取、JSON 或大小校验失败时均不向 Desktop 发送请求。

Platform 读取后保留原始 JSON 类型并发送 `params`，不再将字符串布尔值静默转换；`paramsFile` 路径不进入 `desktop.cdp.call` payload。Desktop CDP 协议和页面目标授权保持现有契约。

## Desktop 反向 Provider

Agent 可看到静态 Desktop 工具 `desktop_action` 与 `desktop_cdp`。普通 Action 白名单由 `internal/resources/tools/desktop_action.yml` 静态声明；AWCP 动态页面 Action 不进入该白名单，而通过 `desktop_cdp` 的 `AWCP.getSnapshot` / `AWCP.invoke` 两个静态 method 使用。Platform 为每个 run 保留独立的内存 target；Desktop 模式还在现有 WebSocket Hub 中维护唯一 `desktop-main` 默认连接，但它不是新的窗口/surface registry，也不允许 HTTP 或其他浏览器 fallback：

- Desktop 模式：97 个普通 `desktop.*` 由 `desktop_action` 直接以具体 Action 名作为反向 request `type` 发给 Desktop Main Broker；`desktop_cdp` 的普通 CDP method 使用 `desktop.cdp.call`，两个 AWCP method 分别映射到 `desktop.awcp.snapshot` 与 `desktop.awcp.invoke`。Broker 分别调用普通 Action、AWCP 或 CDP 核心 handler。
- Standalone 模式：只有七个 `desktop.workpanel.*` 与 `desktop.display` 具体类型发给当前 agent-webclient；其他 `desktop.*` 返回 `desktop_action_unsupported_runtime`，CDP 返回 `desktop_cdp_unsupported_runtime`。

普通 Action 名称跟随 `zenmind-desktop/src/shared/desktop-actions.ts` 的 `DESKTOP_ACTION_DEFINITIONS`，排除 Desktop 明确限制为 WebApp page 调用的 11 个动作（`desktop.assistant.image/image.cancel`、`desktop.capabilities.list` 与八个 `desktop.native.*`）。当前 108 个定义中，97 个进入 Platform 白名单；参数校验、目标授权和确认仍由 Desktop 执行。两个旧 `desktop.webapp.manifest.*` 动作已删除，工程初始化使用 `desktop.webapp.package.init`。

本地存在相邻 `zenmind-desktop` checkout 时，`go test ./internal/tools -run TestDesktopActionContractMatchesDesktopSource -count=1` 会直接核对上游定义。CI 或非相邻 checkout 可设置 `ZENMIND_DESKTOP_SOURCE` 为 Desktop 仓库路径；显式配置路径缺失会失败，未配置且无相邻仓库时跳过这项跨仓检查。更新 Desktop 后需同步 YAML 和契约测试；内嵌工具 Schema 的变更需要重新构建并重启 Platform。

Action 的工具 `requestId` 只映射到帧 `id`；帧顶层 `source` 只由可信 run context 生成，并保留实际调用 run 的 `runId/chatId` 与至多一个 `agentKey/teamId`，不借用父 run 身份；`payload` 始终是纯 Action 参数对象。`desktop.cdp.call` payload 继续为 `{requestId,method,params,targetId,sessionId,surfaceId,source}`。小结果以标准 `response/error` 收口；大 JSON 通过 `desktop.bridge.response.delta` 分块，截图通过 `desktop.cdp.screenshot.delta` 分块，每个 chunk 不超过 256 KiB，解码后总量不超过 64 MiB。Platform 校验 streamId、连续 seq、编码、chunkCount、totalBytes 和最终响应；截图边收边写入当前 Chat 临时文件，成功后原子改名。超时或取消发送 `desktop.bridge.cancel`，迟到帧被丢弃且不会触发重发。

AWCP snapshot 由 Agent 直接调用 `desktop_cdp {method:"AWCP.getSnapshot"}` 从当前授权页发现。Platform 验证快照及完整输入 Schema，在当前 Run 保存 revision、Action 描述符和失效 generation。下一次模型请求仅在 `desktop_cdp` 参数副本的可选 `params.action` 下附加各 Action 的明确输入类型；工具名称、数量及普通 CDP 的开放 params 保持不变，静态 system-init/cachedTools 不被原地修改。每个 Action 的 inputSchema 副本直接挂到 `params.action.properties[Action名称]`，根类型和字段直接可见；不再添加平台生成的 `awcp_aN` 根引用包装。页面 Schema 自带的 `$defs` 保留在该 Action 内，本地 `$ref` 按 Action 在完整工具 Schema 中的位置重定位，保留递归与约束，支持结构完整的 anyOf 互斥对象分支；oneOf/allOf 与 if/then/else 不用于 AWCP 输入，也不接受外部引用和无类型业务值。

模型调用形状为 `{"method":"AWCP.invoke","params":{"action":{"orders.read":{}}}}`：action 恰好一个已发现键，键值就是业务输入。模型不传 revision 或并列 args。调用捕获产生它的模型请求绑定；准备及审批后执行均不能借用最新快照，也不能跨 Run、跨 generation 执行。Platform 通过可信执行上下文传递 revision，在原 tools 层解包为内部 `{revision,action,args}`，生成 request ID、注入 source，并沿既有 `desktop.awcp.invoke` 链路发送。旧模型包装和旧 desktop_awcp 工具入口不提供兼容。

Desktop 在 handler 前按当前快照的 `inputSchema` 校验，页面 Registry 随后调用自身 validator 和 handler；成功输出按可选 `outputSchema` 校验。发现结果正文列 revision、名称、说明和每个 Action 唯一完整 example；完整描述符仍留在宿主，完整输入 Schema 放入 tools，原 wire 响应仍留给校验与诊断。非法快照明确返回合同错误，不允许转入 DOM。可信 Desktop preflight 的 `input_schema_mismatch` 可有限修参，`stale_snapshot` 可有限重新发现；页面业务失败、页面切换、权限或不确定执行结果不自动重放。串行、审批、取消和 AGW 错误分层沿用原链路。

Platform 的 AWCP 参数从模型原始 JSON 严格解析，拒绝重复键、不可安全表示的数字和半截 JSON；AWCP 不进入通用模板展开或普通 CDP 的类型归一化路径。OpenAI 和 Anthropic 当前按普通工具调用模式传递完整 Schema；实际模型接口是否接受其完整约束必须通过真实请求联调确认，不能把 Schema 可序列化当成约束生成已启用。AWCP 执行准备不创建纠错状态：同批合法 Action 可按原队列和审批规则执行，只有实际格式拒绝或可信 preflight 失败才建立纠错目标。目标从失败调用绑定，不从最后准备的调用推断。同一模型响应内的错误分别返回，但不重复扣纠错预算；同批或旧回合成功保留结果，不清除模型尚未收到反馈的错误。同 revision、Action、参数和值错误的无变化重试在派发前终止；后续响应的有效纠正成功才结束恢复，Run 总恢复预算不清零。stale 仍使快照失效并要求重新发现；该规则不为页面业务失败或不确定副作用增加重放机会。

AWCP 纠错门禁只限制 AWCP 方法和待纠正的 Action，不拦截文件、Bash、普通 CDP 或用户交互。纠错耗尽或不可恢复的 AWCP 失败只停止当前 Run 的 AWCP：后续请求从 `desktop_cdp.method.enum` 的副本移除两个 AWCP 方法，保留普通 CDP、其他工具和原权限审批；执行入口也拒绝已排队的 AWCP 调用。迟到的成功或重新发现不能解除停止状态，新 Run 使用独立状态。不含 AWCP 的批次继续采用原并发、排序和批量审批规则；实际含 AWCP 的批次仍保留顺序屏障。技能负责禁止通过 DOM 或其他通道替代失败操作，不妨碍其他独立获授权的工作。

Desktop 的类型校验错误携带受限的预期类型和实际类型，Platform 保留这些诊断字段及错误路径，不回显业务值。AWCP 停止原因附在对应的模型工具反馈中；历史错误仍保留确定事实，最终说明不得因 Action Schema 不再出现而猜测类型或声称未发现合同。通用 Run 限额及其总结流程不受 AWCP 状态影响。

OpenAI 流严格按原协议配置处理 finish_reason 和尾帧等待，不因非法 JSON 增加等待。非法参数数量继续记录；仅在既有协议尾帧处理和 usage drain 读取路径中记录原始帧及结束后工具增量数量，保持原脱敏规则，后续增量不修改已结束的调用。原始非法参数返回未执行的格式错误，能读到完整 AWCP method 的半截 JSON 才附加 AWCP 纠错提示并纳入按轮预算，其他工具保留通用错误。平台不补括号、不重建参数、不自动重放模型请求。

WebSocket query 直接绑定当前连接，不检查连接自报的 `source`；即使没有 `surfaceId`，该 run 仍可按 WebSocket session 定位原连接。HTTP SSE query 与 attach 通过 `X-Agent-WebClient-Device-Id`、`X-Agent-WebClient-Surface-Id` 绑定同一认证主体和 device 边界内的逻辑 surface；device header 与 `/ws?deviceId=...` 相同，认证 JWT 已含 device claim 时以 claim 为准。WS attach 直接使用发起 attach 的连接。每次成功且携带有效 WebClient target 的 attach 都以 last-writer-wins 更新该 run 的反向 Action target；失败 attach 或普通无 target attach 不改变已有绑定，已发出的 Action 不迁移。Team 内部成员与 `agent_invoke` 子 run 按根 run 动态读取相同 target，planning 新 execution run 继承 source run 的当前 target。

Desktop 模式只将 `scope=app` 且 JWT `device_id` 与握手 `deviceId` 完全一致的已认证 `source=desktop-main` WebSocket 作为默认连接 generation；`source` 或 query device metadata 本身不构成授权。已有且可达的 run target 始终优先；automation、`run_query` 等独立根 run 首次调用 Desktop 工具时若无 target，才把当前默认连接写入该 run。旧 target 已无法解析且请求尚未发送时，可以原子改绑新 generation 并发送一次；连接在请求发送后断开时返回 `*_client_disconnected`，不得自动重放。父 run 终态不撤销独立子 run 的绑定。Standalone 不读取该默认连接。目标元数据只保存在运行内存，不进入 prompt、事件、chat 或数据库。

`desktop_action_target_unavailable` 保持稳定错误码。Standalone 无 target 使用 `run_target_missing`；Desktop Main 从未建立使用 `desktop_main_missing`，曾建立但当前离线使用 `desktop_main_disconnected`，无法进一步归类的旧绑定仍使用 `target_connection_unavailable`。普通 CDP 与 AWCP 均通过 `desktop_cdp` 返回对应的 `desktop_cdp_*` 工具错误和相同 reason。

默认 Desktop target 只决定反向请求送达位置，不扩大 WorkPanel 或页面权限。`desktop.workpanel.*` 到达 Desktop 后仍必须通过该 run 的 canonical Chat/grant；没有 grant 的独立 run 返回 `source_chat_not_ready`，不能借用当前可见 Chat。Team WorkPanel 的现有限制同样不变。

## 旧 external stdio 配置已删除

私有 external stdio JSON-RPC 协议、`ExternalToolManager`、私有 `initialize/shutdown/tools/call` 和 `kind: external` 调用分支均已删除，不提供兼容期。`<AP_RUNTIME_DIR>/tools` 中出现以下任一内容时，启动和热重载都会返回带迁移提示的硬错误：

- `service.yml` 或 `service.yaml`
- `type: external`
- `external:` 字段，包括空对象
- `kind: external-service`

迁移方式是删除旧 service/tool YAML，把子进程改为标准 MCP server，并将其作为连接器包，在 `mcp.json` 中声明 `type: stdio`。平台二进制、stdio server 二进制和 registry 配置必须同批发布，旧私有配置不能与新版 runtime 滚动混用。

Qiuerscript 已按此方式迁移。`qs_read`、`qs_glob`、`qs_grep`、`qs_write`、`qs_edit`、`qs_delete` 的工具名、参数、默认值和结构化业务结果保持不变；前三项声明只读 annotations，后三项声明写入/破坏性 annotations。

## 管理接口

- `/api/admin/tools`：MCP 工具返回 `sourceType/sourceCategory: mcp` 与 `serverKey`。
- `/api/admin/registries`：MCP summary 返回 `transport`、`toolCount`、`syncStatus`，以及可选的 `lastSyncAttemptAt`、`lastSyncSuccessAt`、`syncDiagnostic`。HTTP 项返回 `baseUrl`；stdio 项不返回无意义的 `baseUrl`，也不暴露 `command`、`args`、`env` 或同步错误中的 secret。
- `/api/admin/registries/detail`：用于查看或保存完整 registry YAML；敏感配置不要提交到仓库。

## 约束与注意事项

- MCP tool 名称与本地工具冲突时，本地工具优先。
- MCP server 暂时不可用或协议版本不兼容时，调用返回结构化 MCP unavailable 错误。
- MCP streamable HTTP 和 stdio session、ACP、Proxy、Channel、LSP、KBASE sidecar 与其他长期驻留服务不继承动态 run env。stdio MCP 子进程仍只使用 registry 启动时的静态 `env`；运行中 `platform_control run.env.set/unset` 不重启或修改已存在 session。
- `qiuerscript-tool` 在 stdin 关闭后正常退出，不支持私有 `shutdown` RPC。
- `desktop_action`、`desktop_cdp` 及 Desktop Main Broker 反向 Action/AWCP/CDP 已闭环；这里的 Action 是 Desktop/WebClient 业务操作名，不是 Tool 类型，也不会生成 `action.*` run stream 事件。
- `ask_user_question` 由 `internal/toolinteraction` 中明确注册的 handler 负责等待、submit 规范化和固定 QA 模型输出；没有通用 YAML 表单 fallback。
- HITL viewport 细节见 [HITL协议](HITL协议.md)。

## 相关文件

- `internal/mcp/`
- `internal/tools/tool_router.go`
- `internal/tools/tool_registry.go`
- `internal/toolinteraction/`
- `internal/resources/tools/`
- `internal/server/handler_admin_registries.go`

## VIEW 展示元数据

工具 YAML、MCP 工具声明与配置覆盖可使用 `view: {connectorId,key}` 绑定展示连接器。`tool.result` 独立携带服务端冻结的 `view`；不把展示定义混入工具结果。旧 `viewportType/viewportKey` 保留兼容。完整定义、隔离和迁移步骤见 [VIEW连接器](VIEW连接器.md)。

### CDP 失败诊断

Desktop 拥有按方法的参数预检和目标授权；Platform 不复制 Chromium 参数校验器，不修改参数类型。预检失败明确说明当前命令尚未执行，必须修正输入后重试；已执行命令的超时或脚本异常不能声称无副作用。反向错误保留 Desktop 的字段级诊断与恢复建议，按公开字段白名单有界投影，不透传原始参数、宿主身份或任意嵌套数据。

`Runtime.evaluate` 的传输成功与脚本成功独立。Platform 保留原始 CDP 响应，同时将包含 `exceptionDetails` 的结果标为工具失败，显式提示异常位置沿用 CDP 零基行列。普通脚本返回值中的 `ok` 不视为宿主协议状态，页面业务是否完成由调用方回读期望状态核验。参数错误不得作为刷新、关闭表单或切换应用的理由；目标失效只在当前 Run 授权范围内重新发现。

## 整合点击能力

已挂载 `desktop_cdp` 的 Agent 可通过 `method: "Input.click"` 使用整合点击能力，参数放在 `params` 中，目标使用顶层 `targetId`。该方法复用一次 `desktop.cdp.call` 反向请求、可信 Run 来源和现有页面/WorkPanel 授权，不增加网络入口。`Input.click` 由 Desktop 编排，不直接转发给 Chromium。

Desktop 校验 selector 与 x/y 互斥、数值/布尔类型及有界超时；模型不构造按下/释放事件、不写参数文件。Desktop 执行唯一定位、滚动和命中检查、真实左键单击、可选的后置条件观察。`waitFor` 完全可省略，此时只证明输入完成。等待超时、取消、导航和输入结果不确定分别保留动作阶段，不自动重放点击。Windows/macOS 均使用 Chromium CSS 视口坐标，不做宿主 DPI 换算。

`params` 使用标准 CSS `selector` 或数字 `x/y`，二者互斥；可选整数 `timeoutMs` 为 100–10000，默认 3000。可选 `waitFor` 支持 visible/hidden（selector）、value（selector 与字符串 value）、checked（selector 与布尔 checked）、url（仅字符串 value）条件。Desktop 的执行结果区分 `action.outcome` 与 `conditionMatched`。工具结果外层成功只证明协议成功，不能代替页面/业务成功。原始 CDP 仍保留，但正常点击不再拆为多次模型调用。Desktop 与 Platform 需配套更新；旧 Desktop 拒绝新方法时明确报告版本能力缺失，不把它当成点击失败后重试。

## Desktop Action 错误诊断

普通 Desktop Action 保留宿主的错误类别、阶段、执行状态、直接原因、结构化恢复建议和诊断编号。Platform 在固定诊断位置按白名单有界投影，仅对明确的密码及认证凭据值脱敏，路径、堆栈、版本号及其他非凭据内容保持原文；不把宿主内部失败推断成参数错误，不自动重放写动作。Workspace 始终来自当前 Execution Session，不能从 Chat 目录或模型参数补造；build 返回的相对路径只能在同一 Workspace 下交给 install。Desktop 与 Platform 配套发布。
