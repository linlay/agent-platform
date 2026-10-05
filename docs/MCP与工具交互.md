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

本地 `plan_add_tasks`、`plan_get_tasks`、`plan_update_task` 使用有序、单活动任务状态机。新 task 固定为 `init` 且不会自动启动；只允许最前面的非终态 task 由 `init` 进入 `in_progress` 或直接进入 `completed/failed/canceled`，且只有当前活动 task 可由 `in_progress` 进入任一终态。相同状态更新幂等，终态重试通过追加新 task 表达。非法更新不改变内存或 snapshot，工具结果分别使用稳定错误码 `plan_task_predecessor_incomplete`、`plan_task_not_current`、`invalid_plan_task_transition`，并在 structured result 中返回未变化的 plan 和状态诊断。 Plan 状态转移错误额外提供 `message` 与 `recovery`，明确本次更新未生效、当前/阻塞任务及纠正方向；应直接使用返回快照纠正调用，必要时再调用 `plan_get_tasks`，不要原样重试或为解除阻塞将任务标为完成。`plan_update_task` 成功时向模型返回与 Structured 相同的 JSON 快照（`planId`、`plan`、有活动项时的 `currentTaskId`），不再只返回 `OK`；`plan_add_tasks` 的新增任务文本返回保持不变。

## 图片生成与产物发布 URL

`image_generate` 统一覆盖文生图、图生图和局部重绘：省略 `images` 是文生图；传入 1–4 张 `images` 时，第一张固定为编辑主体，其余为参考图；可选 `mask` 固定作用于第一张图。图片和 mask 每项使用 `{"sourceType":"referenceName","value":"image.png"}` 或 `{"sourceType":"filePath","value":"@chat/image.png"}`；旧属性和字符串元素会在 provider 调用前失败。路径继续经过 AccessPolicy/HITL。运行时保留输入原始字节和 Alpha 通道，不使用视觉识别工具的大图 JPEG 重编码。

模型请求协议完全由模型 YAML 的 `image.generation` 与 `image.edit` 决定。GPT Image 可分别使用 Images JSON/Multipart；Gemini Image 可让文生图和图生图都使用 Chat Completions。runtime 不按 model key、modelId 或 provider 硬编码路由，profile 也不能覆盖 endpoint。

Images 接口可分别通过 `image.generation.omitResponseFormat` / `image.edit.omitResponseFormat` 省略上游不接受的 `response_format`；默认 `false` 保留旧请求行为。开启后显式工具参数和 JSON compat 均不能恢复该字段。成功结果的 `responseFormat` 表示实际返回的 `b64_json`、`url` 或混合批次的 `mixed`，不保证与请求偏好相同。详见 [配置化说明](配置化说明.md)。

Mask 必须与第一张图同尺寸并显式指定 `mode`：`alpha` 表示透明区重绘，`white_edit` 表示白色区重绘，`black_edit` 表示黑色区重绘；灰度边缘转换为软 Alpha。模型 YAML 未声明 `maskProtocol: openai-alpha` 时返回 `image_generate_mask_unsupported`，不跨 profile 回退。成功结果的 `operation` 为 `generation`、`edit` 或 `inpainting`。

`image_generate` 和 `artifact_publish` 的工具说明共同约束模型输出：`path` 只用于工具间传递，可以是经授权的当前 Workspace/Chat 内 Host 绝对路径，禁止展示、写进 Markdown 或转换成 `file://`；用户可见内容只能逐字复制工具返回的 `url`，禁止手工拼接或编码资源地址。图片生成后使用 `images[n].url`；再次发布后改用 `publishedArtifacts[n].url`，因为后者指向 `artifacts/<runId>/` 发布副本。缺少有效 `url` 时必须报告资源物化或发布失败，不得伪造 Markdown。

```markdown
![夏日海报](generated.png)
[下载夏日海报](artifacts/run_01/generated.png)
```

工具结果与 Markdown 使用相对于当前 Chat、不带 `chatId` 的 `url`。浏览器数据层补入当前 `chatId`，再转换为实际的 `GET /api/resource?file=<chatId>/<relativePath>`，查询参数按 HTTP 规则编码。该 HTTP 地址不作为 Markdown 地址；历史 endpoint Markdown 不迁移且不再预览。

## WorkPanel 反向动作

`desktop.workpanel.*` 是 Platform 发给客户端的反向动作，只操作当前 run 所属 Chat 的工作面板。模型不直接调用它们：`builtin.web-control` 的 `workpanel_state`、`workpanel_open`、`workpanel_close` 工具在 Platform 内映射到这些动作，`desktop_action` 的运行时白名单不再包含 `desktop.workpanel.*`。Platform 从 `ExecutionContext.Session` 生成可信顶层 `source`，模型不能覆盖，也不能通过参数改选所属 Chat。

模型工具与反向动作的对应关系：

| 工具 | 反向动作 |
| --- | --- |
| `workpanel_state` | `desktop.workpanel.getState` |
| `workpanel_open`（`http(s)://`） | `desktop.workpanel.openWeb`；`reload: true` 时随后发送 `desktop.workpanel.refreshWeb` |
| `workpanel_open`（`@workspace/`、`@chat/`） | `desktop.workpanel.openLocalFile`，路径由 Platform 解析为 Workspace 相对路径 |
| `workpanel_close`（文件 `url`） | 先 `desktop.workpanel.getState` 严格匹配已记录的 Workspace 路径，再 `desktop.workpanel.closeTab`；网页使用 `surface_close` |
| `workpanel_close`（`all: true`） | `desktop.workpanel.closeWorkpanel` |

条目 ID 只在 Platform 内部使用，工具结果中的工作区被投影为 `{kind,url,title,active,...}` 条目列表。`desktop.workpanel.openTab`（通用描述符）和 `desktop.workpanel.activateTab` 没有对应的模型工具。

客户端侧各动作的精确参数如下：

- `desktop.workpanel.getState`：无参数，读取工作区、条目和活动条目。
- `desktop.workpanel.openTab({descriptor})`：按规范描述符打开或激活一个确定性 Tab。网页描述符使用 `{kind:"web",url,title?,pinned?,closable?}`；WebClient 描述符必须遵守其 module、route 和 context 契约。
- `desktop.workpanel.openWeb({url})`：规范化无用户名/密码的 HTTP(S) URL，并打开或激活对应 WebView；Desktop 宿主可访问的 `localhost`、`127.0.0.1` 或 `[::1]` 服务同样允许。
- `desktop.workpanel.openLocalFile({path,title?})`：仅在 Desktop runtime 中使用来源 Agent 权威 Workspace 下的相对路径打开本地文件。拒绝绝对路径、盘符、UNC、`..`、`file://`、目录、缺失文件和 realpath 越界；Platform 不把本地文件转换为临时 HTTP 服务。
- `desktop.workpanel.refreshWeb({url})`：按规范化 URL 精确查找已经打开的 WebView，原位重载并激活，不创建新条目。
- `desktop.workpanel.activateTab({tabId})`：激活 `getState` 返回的 `state.items[].itemId`。
- `desktop.workpanel.closeTab({tabId})`：关闭可关闭且未固定的 Tab。
- `desktop.workpanel.closeWorkpanel`：无参数。Desktop 模式关闭当前 Chat 的整个 WorkPanel；Standalone 只隐藏右侧栏并保留已打开的 Web Preview。
- `desktop.display({kind:"effect",effect,durationMs?})`：在 Desktop Main Window 或 Standalone 根页面显示 fireworks、snowfall、nationalDay。时长缺省 8000 ms，必须是 1000–30000 的整数；启动后立即返回 accepted，新请求替换当前效果。


## 网页控制工具的文件参数

`surface_cdp` 可直接传入 `params` 对象，也可通过 `paramsFile` 读取参数文件。两者互斥（包括显式传入 `params: null`）；都省略时保持无参数调用。

```json
{
  "surfaceId": "实际 surfaceId",
  "method": "DOM.querySelector",
  "paramsFile": "params.json"
}
```

`params.json` 只保存 `params` 内容，不包含外层 `method`、`surfaceId` 等请求字段。

`surface_evaluate` 的 `expression` 与 `expressionFile` 互斥；`expressionFile` 指向一个 UTF-8 JavaScript 文件，内容整体作为表达式，用于内联放不下的大脚本。

相对路径按本次执行 Workspace 解析；没有 Workspace 时返回 `workspace_unavailable`，不回退到 Platform 进程目录。也支持 `@workspace`、`@chat`、`@temp` 等通用路径别名，以及经授权的绝对路径；Container 执行路径使用现有映射解析为 Host 路径。文件读取复用 AccessPolicy 和文件预审批，越权读取进入 HITL，批准后继续执行；canonical 路径校验与临时根逃逸阻断仍生效。

`paramsFile` 必须是 UTF-8 编码的单个 JSON 对象，拒绝目录、设备等非普通文件、空内容、`null`、数组、JSON 后的额外内容及非法 JSON；`expressionFile` 必须是非空 UTF-8 文本。读取上限复用 `configs/tools.yml -> file-tools.max-read-bytes`（默认 1 MiB），超限报错，不截断。路径、读取、JSON 或大小校验失败时均不向 Desktop 发送请求。

Platform 读取后保留原始 JSON 类型并发送 `params`，不再将字符串布尔值静默转换；`paramsFile`、`expressionFile` 路径不进入 `desktop.cdp.call` payload。Desktop CDP 协议和页面目标授权保持现有契约。

## Desktop 反向 Provider

平台控制连接器提供 12 个静态工具，各自 action 枚举固定；网页连接器提供 15 个固定参数工具。Desktop 动作在内部映射到原有反向请求，注册表由 internal/connector/control_actions.go 维护。详见 [平台控制连接器](Platform控制工具设计.md)。

- Desktop 模式：`desktop_action` 与 `workpanel_*` 以具体 Action 名作为反向 request `type` 发给 Desktop Main Broker；`surface_*` 使用 `desktop.cdp.call`（`surface_element` 使用 `desktop.web.interactElement` 动作），`awcp_manual` / `awcp_invoke` 分别映射到 `desktop.awcp.manual` 与 `desktop.awcp.invoke`。Broker 分别调用普通 Action、AWCP 或 CDP 核心 handler。
- Standalone 模式：只有 `desktop.workpanel.*`（不含 `openLocalFile`）与 `desktop.display` 具体类型发给当前 agent-webclient；其他 `desktop.*` 返回 `desktop_action_unsupported_runtime`。会话只暴露 `workpanel_*`，`surface_*` 与 `awcp_*` 不提供给模型，直接调用返回 `desktop_cdp_unsupported_runtime`。

`desktop_action` 的动作名称跟随 `desktop/src/shared/desktop-actions.ts` 的 `DESKTOP_ACTION_DEFINITIONS`，排除 Desktop 明确限制为 WebApp page 调用的 11 个动作（`desktop.assistant.image/image.cancel`、`desktop.capabilities.list` 与八个 `desktop.native.*`），以及归 `builtin.web-control` 所有的 20 个 `desktop.workpanel.*` 与 `desktop.web.*` 页面动作（`desktop.web.exportArtifact` 仍属 `desktop_action`）。模型必须传递 Skill 中的具体动作名，通配符和未登记动作均在 Platform 分发前拒绝；参数校验、目标授权和确认仍由 Desktop 执行。两个旧 `desktop.webapp.manifest.*` 动作已删除，工程初始化使用 `desktop.webapp.package.init`。

本地存在相邻 `desktop` checkout 时，`go test ./internal/tools -run TestDesktopActionContractMatchesDesktopSource -count=1` 会直接核对上游定义。CI 或非相邻 checkout 可设置 `DESKTOP_SOURCE` 为 Desktop 仓库路径；显式配置路径缺失会失败，未配置且无相邻仓库时跳过这项跨仓检查。更新 Desktop 动作后需同步内部白名单、技能目录和契约测试；新增动作不需要向模型 Schema 添加枚举或业务域；契约测试同时核对归网页控制连接器所有的动作仍存在于 Desktop 定义中。Skill 负责操作帮助，不能授予权限；Standalone 限制、Run 来源校验、WebApp-page-only 排除及 Desktop 确认机制不变。内部白名单或内嵌工具 Schema 的变更需要重新构建并重启 Platform。

Action 的工具 `requestId` 只映射到帧 `id`；帧顶层 `source` 只由可信 run context 生成，并保留实际调用 run 的 `runId/chatId` 与至多一个 `agentKey/teamId`，不借用父 run 身份；`payload` 始终是纯 Action 参数对象。`desktop.cdp.call` payload 继续为 `{requestId,method,params,surfaceId,source}`。小结果以标准 `response/error` 收口；大 JSON 通过 `desktop.bridge.response.delta` 分块，截图通过 `desktop.cdp.screenshot.delta` 分块，每个 chunk 不超过 256 KiB，解码后总量不超过 64 MiB。Platform 校验 streamId、连续 seq、编码、chunkCount、totalBytes 和最终响应；截图边收边写入当前 Chat 临时文件，成功后原子改名。超时或取消发送 `desktop.bridge.cancel`，迟到帧被丢弃且不会触发重发。

AWCP 按网站操作手册渐进披露，`awcp_manual` 与 `awcp_invoke` 的工具定义始终固定。模型先读取 `site.description` 中整个页面的纯文本操作说明和动作目录；页面说明包含 Action 样例。模型再按任务读取某个动作的详细参数约束及示例，最后使用通用 `awcp_invoke`。页面说明是普通工具结果，不注册成工具，不注入 system prompt，也不改写下一轮模型请求的 Schema。网站新增动作无需修改 Platform 核心。

```json
awcp_manual  {"surfaceId":"surface-1"}
awcp_manual  {"surfaceId":"surface-1","section":"orders.select","revision":"page-revision"}
awcp_invoke  {"surfaceId":"surface-1","revision":"page-revision","action":"orders.select","args":{"ids":["order-1"]}}
```

- 目录读取：`awcp_manual` 只带 `surfaceId`，返回当前 revision、`site.description` 页面操作说明和动作目录，不提前加载各 Action 的完整参数约束。
- 单项手册读取：`awcp_manual` 同时给出 `section` 与 `revision`，返回网站原始 v1 章节 `{revision,section,description,inputSchema,examples?}`。Platform 不编译 Schema，不要求 examples 存在或非空，也不限制为模型提供商支持的 Schema 子集。
- 页面由工具的 `surfaceId` 选择，限定在可信 Run grant 内。Platform 把工具字段组装为既有 wire payload（目录为空，章节为 `{section,revision}`，并附带可选 `surfaceId`）；Desktop 和页面原生按目录/单章节获取，不在 Platform 投影完整合同，也不缓存页面状态。
- 调用：`awcp_invoke` 接收内联 `revision/action/args` 或互斥的 `paramsFile`（及可选 `surfaceId`），模型使用手册返回的 revision。Platform 校验固定外壳并以工具自身的字段名报告错误，生成 request ID 并注入可信 source；Desktop 校验当前授权页和版本，网站自己的 validator/handler 负责业务参数校验和执行。

`awcp_invoke` 也可传 `{"surfaceId":"page:xxx","paramsFile":"@chat/awcp-sections.json"}`，与内联 revision/action/args 互斥。文件必须是 UTF-8 JSON 对象，完整对应 params，且仅包含 `revision/action/args`，例如 `{"revision":"手册返回值","action":"forum.sections.list","args":{}}`。surfaceId 留在外层，不传 method，不合并两种参数来源。Platform 复用普通 CDP 的文件读取器、路径别名、canonical 路径/权限审批、普通文件检查及大小限制；文件解析后执行相同 AWCP envelope 校验，业务 schema 仍由页面负责。Desktop wire payload 不增加文件路径；手册、revision、Surface grant 与 requestId 边界保持原样。

参数错误在发出请求前终止，返回 `stage: platform_parse`、`executionStarted:false`，保留 `path/expectedType/actualType` 并以 `parameterSource` 区分 params/paramsFile。JSON 语法错误给出从 1 开始的行和 Unicode 列，不回显正文。提示要求修正字段或文件后重新调用：args 必须是原生 JSON 对象；无参数动作传 args: {}，有参数动作按手册填写对象，不接受空字符串或字符串 {}，不自动转换。文件授权错误保留原审批字段与错误码，不能通过修参绕过权限。超时、断连和未知执行结果不套用参数错误重试提示，也不自动重放。

该改动随 Platform 二进制及内嵌 Desktop 技能发布，重新构建并重启 Platform 后由标准连接器装配加载；不修改已安装的 ru-connectors 缓存。Desktop/网站桥仍使用原协议，无需为文件路径增加处理。

运行核心不保存 AWCP revision、动作集合、模型请求绑定、失效 generation 或纠错预算；发现、调用和失败都走普通工具循环。错误原样按既有 response/error 分层返回，模型结合网站手册和执行状态决定修参、重新读取或向用户解释，Platform 不自动重放、不强制最终回答、不移除工具，也不因批次出现 AWCP 就改变通用排序和并发规则。有先后依赖的操作须逐步发起；不要在执行状态未知时盲目重复可能有副作用的操作。权限、审批、取消、通用 Run 限额和 Desktop 授权页面边界继续生效。

普通 provider 的非法参数及尾帧诊断保持独立；不为 AWCP 修补半截 JSON 或增加模型重试。Skill 只需说明如何发现、阅读网站手册和调用通用方法，网站知识通过工具结果按需进入上下文，不要求安装网站专属 Skill 或修改 Agent Catalog。

WebSocket query 直接绑定当前连接，不检查连接自报的 `source`；即使没有 `surfaceId`，该 run 仍可按 WebSocket session 定位原连接。HTTP SSE query 与 attach 通过 `X-Agent-WebClient-Device-Id`、`X-Agent-WebClient-Surface-Id` 绑定同一认证主体和 device 边界内的逻辑 surface；device header 与 `/ws?deviceId=...` 相同，认证 JWT 已含 device claim 时以 claim 为准。WS attach 直接使用发起 attach 的连接。每次成功且携带有效 WebClient target 的 attach 都以 last-writer-wins 更新该 run 的反向 Action target；失败 attach 或普通无 target attach 不改变已有绑定，已发出的 Action 不迁移。Team 内部成员与 `agent_invoke` 子 run 按根 run 动态读取相同 target，planning 新 execution run 继承 source run 的当前 target。

Desktop 模式只将 `scope=app` 且 JWT `device_id` 与握手 `deviceId` 完全一致的已认证 `source=desktop-main` WebSocket 作为默认连接 generation；`source` 或 query device metadata 本身不构成授权。已有且可达的 run target 始终优先；automation、`run_query` 等独立根 run 首次调用 Desktop 工具时若无 target，才把当前默认连接写入该 run。旧 target 已无法解析且请求尚未发送时，可以原子改绑新 generation 并发送一次；连接在请求发送后断开时返回 `*_client_disconnected`，不得自动重放。父 run 终态不撤销独立子 run 的绑定。Standalone 不读取该默认连接。目标元数据只保存在运行内存，不进入 prompt、事件、chat 或数据库。

`desktop_action_target_unavailable` 保持稳定错误码。Standalone 无 target 使用 `run_target_missing`；Desktop Main 从未建立使用 `desktop_main_missing`，曾建立但当前离线使用 `desktop_main_disconnected`，无法进一步归类的旧绑定仍使用 `target_connection_unavailable`。`surface_*` 与 `awcp_*` 工具沿用传输层的 `desktop_cdp_*` 错误码和相同 reason。

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
- MCP streamable HTTP 和 stdio session、ACP、Proxy、Channel、LSP、KBASE sidecar 与其他长期驻留服务不继承动态 run env。stdio MCP 子进程仍只使用 registry 启动时的静态 `env`；运行中 `run_env` 的 `set/unset/update` 不重启或修改已存在 session。
- `qiuerscript-tool` 在 stdin 关闭后正常退出，不支持私有 `shutdown` RPC。
- `desktop_action`、网页控制工具组及 Desktop Main Broker 反向 Action/AWCP/CDP 已闭环；这里的 Action 是 Desktop/WebClient 业务操作名，不是 Tool 类型，也不会生成 `action.*` run stream 事件。
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

已挂载 `builtin.web-control` 的 Agent 通过 `surface_click` 使用整合点击能力：`surfaceId` 选择页面，`selector` 或 `x`/`y`、可选 `waitFor` 直接作为工具参数。它映射为一次 `desktop.cdp.call` 的 `Input.click`，复用可信 Run 来源和现有页面/WorkPanel 授权，不增加网络入口。`Input.click` 由 Desktop 编排，不直接转发给 Chromium。

Desktop 校验 selector 与 x/y 互斥、数值/布尔类型及有界超时；模型不构造按下/释放事件、不写参数文件。Desktop 执行唯一定位、滚动和命中检查、真实左键单击、可选的后置条件观察。`waitFor` 完全可省略，此时只证明输入完成。等待超时、取消、导航和输入结果不确定分别保留动作阶段，不自动重放点击。Windows/macOS 均使用 Chromium CSS 视口坐标，不做宿主 DPI 换算。

工具参数使用标准 CSS `selector` 或数字 `x/y`，二者互斥；可选整数 `timeoutMs` 为 100–10000，默认 3000。可选 `waitFor` 支持 visible/hidden（selector）、value（selector 与字符串 value）、checked（selector 与布尔 checked）、url（仅字符串 value）条件。Desktop 的执行结果区分 `action.outcome` 与 `conditionMatched`。工具结果外层成功只证明协议成功，不能代替页面/业务成功。底层输入事件仍可经 `surface_cdp` 发送，但正常点击不再拆为多次模型调用。Desktop 与 Platform 需配套更新；旧 Desktop 拒绝新方法时明确报告版本能力缺失，不把它当成点击失败后重试。

## Desktop Action 错误诊断

普通 Desktop Action 保留宿主的错误类别、阶段、执行状态、直接原因、结构化恢复建议和诊断编号。Platform 在固定诊断位置按白名单有界投影，仅对明确的密码及认证凭据值脱敏，路径、堆栈、版本号及其他非凭据内容保持原文；不把宿主内部失败推断成参数错误，不自动重放写动作。Workspace 始终来自当前 Execution Session，不能从 Chat 目录或模型参数补造；build 返回的相对路径只能在同一 Workspace 下交给 install。Desktop 与 Platform 配套发布。

## WebApp Action 路径别名

Platform 对 WebApp init、validate、build 和 install 的指定路径字段复用 Session 路径解析，支持 `@chat` 与 `@workspace`。别名只来自当前 Run 的可信上下文，目标必须同时位于别名根和 Workspace 内；解析后转换为 Workspace 相对路径发送给 Desktop，不修改 source 根，不解析其他 Action 或嵌套业务字段。普通相对路径及 build 返回路径保持兼容。Desktop 继续在执行前检查真实路径、链接边界和文件权限；Platform 解析不是文件访问授权。无项目默认 Chat 内落盘，有项目默认项目内落盘；缺根、跨卷及越界明确拒绝，不自动选择替代目录。

## 网页 Surface 契约

每个网页是一个独立 Surface，由 `surfaceId` 标识；导航和刷新保留身份，关闭重开使旧身份失效。网页控制工具以 `surface_list` 发现本次 Run 已授权的网页，`surface_state` 省略 `surfaceId` 时读取所属应用的当前页；所有页面操作只使用 `surfaceId`。Desktop 协议中的 `containerId` 与 WorkPanel 条目 ID 不出现在模型工具的参数和结果里。`surface_*` 工具在 Platform 内映射为 `desktop.cdp.call` 的 Surface.list / Surface.getCurrent / Surface.getState / Surface.goBack / Surface.close 等 Desktop 方法和受限 Chromium 方法，由 Desktop 定位到精确 webContents 后执行。

普通 Chat 用 `workpanel_open` 打开网址，返回 `surfaceId` 与状态；这是打开新页面的唯一入口，Website 内的多个标签在 `surface_list` 中是多个独立 Surface。Website/WebApp Copilot 沿用所属应用 Run grant。发现与操作使用相同授权范围，后台页面不因隐藏失效，其他 Chat、文件预览与任意应用不可借此访问。`awcp_manual` / `awcp_invoke` 接受可选 `surfaceId`，只能在已有应用 grant 内选页；省略时沿用该应用活动页，不改变页面桥权限。Platform、Desktop 与技能必须配套发布。

### Desktop 确认期限与取消

反向 request 的可选顶层 `deadlineAt` 是 Platform 从实际工具 context 截止时间生成的 epoch milliseconds，不接受模型参数声明。Desktop 确认队列从入队开始计时，包括尚未展示的请求；确认期限不能超过工具剩余期限，并为执行与返回预留时间。Platform 到期或取消仍发送 `desktop.bridge.cancel`，Desktop 必须终止尚未执行的确认并关闭对应弹窗，不能在迟到确认后继续执行。已经执行的副作用不因取消而被视为回滚。Desktop 确认超时、用户取消、窗口不可用的错误原因通过既有诊断位置透传；Platform 自身工具超时和取消保持独立错误。


## 工具输入命名

工具名保持小写单词或 snake_case；平台自有输入键统一 camelCase，嵌套对象同样遵守。旧键不再接受：模型调用准备阶段、ToolRouter 与原生执行器入口检查已移除字段，返回 `invalid_tool_arguments` 和替代字段名，新旧键同时出现也拒绝。检查只覆盖指定平台工具及字段路径，不改写参数或枚举值，不遍历 CDP/MCP/AWCP 透传业务对象。

| 工具 | 旧字段 | 新字段 |
| --- | --- | --- |
| file_read / file_write / file_edit | file_path | filePath |
| file_read | add_line_numbers | addLineNumbers |
| file_edit | old_string / new_string / replace_all | oldString / newString / replaceAll |
| file_glob / file_grep / kbase_files | head_limit | headLimit |
| file_grep | output_mode | outputMode |
| file_grep | -A / -B / -C | afterContext / beforeContext / context |
| file_grep | -i / -n | caseInsensitive / lineNumbers |
| regex | case_insensitive | caseInsensitive |
| sleep | duration_ms | durationMs |
| image_generate | images[].source_type / mask.source_type | images[].sourceType / mask.sourceType |
| image_generate | response_format | responseFormat |
| vision_recognize | images[].file_path / images[].reference_name / output_format | images[].filePath / images[].referenceName / outputFormat |

`image_generate` 的 `sourceType` 枚举由 `reference_name/file_path` 改成 `referenceName/filePath`；`files_with_matches`、`white_edit`、`b64_json` 等其他枚举不改。`desktop_action` 返回的 `visionRecognizeImage.reference_name` 改成 `visionRecognizeImage.referenceName`，可直接作为识图工具的图片对象。

工具定义只接受 `inputSchema`；包括 agent-local 定义在内，使用旧 `parameters` 字段将加载失败。JSON Schema 标准关键字、上游 Images 请求体的 `response_format`、Lance 的 `source_type` 存储列不在本次改名范围。

历史 JSONL 与模型原始消息不做字段迁移；旧会话继续调用旧参数时返回明确错误，由模型使用新名重试。文件写入的旧名或混合名调用即使被拒绝，也必须在展示、历史与 trace 副本中隐藏内容，不能依赖执行校验替代脱敏。Desktop 图片调用方需与 Platform 同批升级；本地提示词及自建技能中的旧示例也需更新。

## 工具展示多语言

工具 YAML 不再接受顶层 `label`，显示名称仅在 `i18n` 中声明。工具 YAML 支持可选顶层 `i18n`，语言复用 Platform 的 `en` / `zh-CN` 及其归一化规则。每个语言对象只接受字符串 `label`、`description`；空白字段按缺省处理，不支持的语言、重复归一化语言或非法字段在加载时拒绝。

```yaml
name: desktop_settings
description: "Manage Desktop settings using action and args."
i18n:
  en:
    label: Desktop Settings
  zh-CN:
    label: 桌面设置
```

`i18n.description` 是受支持的可选界面说明，但 Platform 内嵌的全部工具（含 builtin.platform-control / builtin.web-control 的 native 工具）仅配置翻译名称，不配置翻译描述。内嵌工具原始 description、输入与输出 Schema 中的 description 均使用英文。dbx/httpx 是 CLI 连接器，不新增独立模型工具，仍通过 Bash 调用。

展示名称仅使用当前语言的 i18n.label，缺失时回退到工具 name，不能回退到上一次显示的其他语言名称。界面描述优先使用当前语言的 i18n.description，缺失时回退原始英文 description。API 的 label/toolLabel 是解析结果，不是源码中的重复配置。模型协议只从原始定义构造 name、description、parameters/input_schema 等协议字段，不发送 label、翻译表或界面翻译后的 description。工具名称、action、参数、权限与执行逻辑不受界面语言影响。

工具元数据内部使用 `meta.toolI18n` 携带翻译。Native 调用在 tool.start / tool.snapshot 保存冻结的 `toolI18n` 展示快照，以支持多个客户端和历史读取；它不进入模型上下文。HTTP/WS 目录响应、SSE/WS 工具事件、Chat/Archive 回放和会话导出在输出边界按查看者语言解析，并移除内部翻译表，不修改共享定义或原始事件。英文请求同样进行工具展示解析。

旧 JSONL 不迁移。旧工具事件可从当前 Platform 内置定义补齐展示翻译；已有冻结快照优先，不用全局目录猜测旧 Agent-local/MCP 工具的同名定义，无法解析时保留原始显示名。Desktop 在连接握手时设置当前全局语言，语言切换后通过 `/api/locale` 更新已连接通道；普通 WS 响应与后续流事件统一按连接语言解析。业务 payload 不携带 locale，不冻结请求或 Run 级语言，也不因语言变化重建观察订阅。Bash 的动态参数 description 是原始调用内容，不自动翻译。
