# API与协议

## 当前状态

标准连接器执行使用 `/api/connectors/execution/*`，凭据管理使用 `/api/connectors/auth`，详见 [连接器执行协议](连接器执行协议.md)。已发布产物使用独立 `/api/chat/artifacts/*`，不接受连接器执行 token。

运行时提供 HTTP REST、SSE 与 WebSocket 三类协议入口。REST 承载 catalog、chat、automation、memory、resource 等请求；`POST /api/query` 使用 SSE 返回实时 run stream；`GET /ws` 是 WebSocket 控制面，复用一批 `/api/*` route，并用 `stream` frame 承载实时事件。

所有非 SSE HTTP JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

管理接口通过统一 Agent HTTP 错误出口返回失败时，外层保留数字 `code` 与 `msg`，`data.error` 同时保留业务错误码、原因、状态及领域诊断。HTTP 状态码不能代替业务错误码；前端依业务错误码提供处理建议，领域诊断用于解释具体阻塞对象。

普通 Agent query 准入失败时，已存在但配置无效的 Agent 返回 `422 agent_configuration_invalid`，不存在或不可用且无 invalid 管理记录的 Agent 返回 `404 agent_not_found`，均不可重试。HTTP（包括 SSE 启动前）与 WebSocket 在 `data.error` 保留应用错误码、`status`、`retryable` 和可选 `diagnostics`，按请求/连接语言返回提示。旧 `contextConfig.agents` 和 agents 标签忽略并生成一条非阻断弃用 warning，不影响主 Agent 有效性；已有 Chat 历史仍可读取。

## Desktop ACP 注册

`PUT /api/desktop/acp-bridges` 接受 `sourcePluginId`、`bridgeId`、`baseUrl`、可选 `timeoutMs` 和 `authToken`；省略 token 时保留已有值，显式空字符串清除。`DELETE` 接受同一插件身份与 bridge ID。插件身份由 Desktop 在认证的插件 socket 上确定，不能从插件请求体透传覆盖。

该入口即使全局 auth 关闭也强制验证 JWT，且要求已验证的 app scope、非空 subject 和 device claim；网页或无身份请求不能注册。app scope 的本机宿主是断言插件身份的信任边界。响应仅包含 `changed`、`removed`、`restartRequired`，不回显配置路径或 token。参数错误返回 400，归属或配置冲突返回 409，持久化失败返回 500。旧 Platform 不支持该入口时 Desktop 显式失败，无文件写入降级。持久化与生效边界见 [ACP CODER Backend](智能体配置说明.md#ACP-CODER-Backend)。

## 统一时间契约

platform 自己定义和拥有的 API、JSONL、SSE、WebSocket 与 trace 生命周期时间点，统一使用未加引号的 Unix epoch milliseconds JSON 整数（Go `int64`、客户端 `number`）。可接受范围固定为 `1000000000000..9007199254740991`：这既拒绝十位 Unix 秒，也保证 JavaScript number 精确表示。

- 已声明的平台字段（例如 chat/run 的 `createdAt`、`updatedAt`、`startedAt`、`completedAt`，stream envelope 的 `timestamp`，以及 platform auth 的 `expiresAt`）必须是 epoch-ms。可选字段缺失时必须省略；除协议明确声明 nullable 的字段外，不得输出 `0`、`null`、数字字符串、ISO 字符串或浮点数。
- 除 Automation 展示时间外，已声明的可读时间（`*Time` 或 `iso`）必须是带 `Z` 或 offset 的 RFC3339 / RFC3339Nano；若协议声明它与 epoch-ms 字段配对，两者必须表示同一毫秒时刻。Automation 的 `nextFireTime`、`startedTime`、`completedTime` 是例外：它们统一按 Platform `automation.default-zone-id`（无效或未配置时回退进程 `time.Local`）输出 `YYYY-MM-DD HH:mm:ss`，只用于展示，秒精度且不携带时区，不能用于还原精确时间点。
- 名字不是契约：外部 tool result、MCP content、Desktop action result、trace request/response/tool payload 的 `createdAt`、`timestamp`、`iso` 等业务字段不会因名称被平台推断为时间。
- 工具结果只有在其可选 `outputSchema` 显式声明时才校验时间：`x-platform-time: "epoch-ms"` 表示严格毫秒整数，`format: "date-time"` 表示 RFC3339 可读字符串，`x-platform-time-pair` 表示显式配对。未声明 `outputSchema` 的工具结果是透明 JSON。
- 任何 producer、持久化 JSONL/archive、trace 或上游 child/proxy stream 违反此契约，HTTP/WS 返回 `422`，其 `data.error` 固定含有 `code:"time_contract_violation"`、`field`、`location`、`expected:"epoch_ms_int64"`。已开始的 stream 会先发送平台本地 `run.error` 后结束；服务端绝不以当前时间、run ID 或完成时间修补原事件。
- 不符合时间契约的 chat、archive、trace 或上游 stream 直接失败。

JWT `exp` / `iat` 与 resource ticket payload 的 `e` 仍是 token 内部的 NumericDate 秒值；`durationMs`、`timeout`、cron、本地日期拆分、ID、文件名和日志前缀不是结构化时间点。

## Chat JSONL schema 错误

Chat JSONL 每条物理行只允许一个 JSON object，`_type` 必填且只允许 `query`、`react`、`react-tool`、`event`、`steer`、`submit`、`compact.checkpoint`、`compact.run.checkpoint`、`compact.tool`、`compact`。空行、多行 object、同行多个 JSON 值、数组、标量、语法错误、非法 `_type` 及非法 system/planning/awaiting 结构统一返回 HTTP/WS `422 chat_storage_schema_violation`。

`_type:"steer"` 的持久化行使用专用 `steer` object，不使用通用 `event`：顶层 `updatedAt/liveSeq` 分别保存事件时间与公开 live cursor，`steer` 保存 `requestId/chatId/runId/steerId/message/role` 与可选 `references`、`origin` 业务字段；新记录不写顶层 `messages`（旧快照兼容读取），且 `requestId` 为空时省略。回放时重新合成扁平 `type:"request.steer"` 与 `timestamp`；SSE、WebSocket stream 和 `/api/chat.events[]` 的对外事件结构不变。旧 `_type:"steer" + event` 以及 `_type:"event" + event.type:"request.steer"` 均属于不支持的存储 schema，不兼容读取或迁移。

HTTP 的 `data.error` 与 WebSocket error frame 的 `data` 包含 `code`、`field`、`location`、`expected`、可选 `actual`、`status:422`、`retryable:false`。`location` 使用 1-based 物理行号；响应不会携带完整 JSONL 行或 system prompt。时间字段不合法仍使用 `time_contract_violation`。

## 核心流程

`builtin.platform-control` 提供 run 内的平台工具，不是 HTTP 协议扩展。动态环境变量由普通 native root Agent 在当前 run 中调用 `run_env` 的 `set/unset/update` 修改；`POST /api/query`、前端和 Chat API 都不传 `documentId`、run env 或 platform-control 授权字段。

```text
普通 JSON API -> ApiResponse envelope
POST /api/query -> SSE message events -> data: [DONE]
POST /api/query (lane=btw) -> hidden read-only branch -> same SSE message events
GET /ws -> request / response / stream / push / error frames
文件上传下载 -> HTTP 数据面
```

文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传走 `POST /api/upload`，下载走 `GET /api/resource`；WebSocket `/api/upload` 用于 gateway 发送 `url + metadata` 下载通知，由 platform 按 metadata 中的 URL 自己通过 HTTP 拉取并校验（该 URL 可指向 gateway 的 `/api/pull/...`）。反向推送本地资源走 WS `/api/resource`，platform 再把文件字节 HTTP POST 到 gateway 的 `pushURL`（通常是 `/api/push/...`）；WS `/api/push` 不存在。

资源协议分为 Markdown 地址与隐藏 HTTP 数据面两层，不能混用。当前 Chat 文件在工具结果和 Markdown 中使用不带 `chatId`、前导 `/` 的 ChatScope 相对 URI，例如 `generated.png` 或 `artifacts/run_01/generated.png`；普通 Agent 还可在 Markdown 中使用当前 Workspace 或冻结临时根内的实际 Host 绝对路径，HTTP(S)、`data:`、`blob:` 原样使用。`@temp` 只是工具语义根，不是 Markdown 地址。前端只在统一资源 adapter 内将 ChatScope 地址转换为 `GET /api/resource?file=<chatId>/<relativePath>`，将绝对路径转换为 `GET /api/resource?chatId=<chatId>&file=<absolutePath>`。真实 `/api/resource` 请求地址与 `<currentChatId>/<relativePath>` 都不是 Markdown 地址；后端工具、模型和公开事件不得生成它们，历史 endpoint Markdown 不迁移且不再预览。

## HTTP API 定义

参数位置说明：`query` 表示 URL query，`body` 表示 JSON body，`multipart` 表示 multipart form。

### Catalog

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/agents` | query: `includeChats`、`chatsPinned`、`scope`、`mode`、`hasWorkspace` | agent 列表；包含 TEAM 的 Agent 列表，可附带最近 chat 摘要；有主要聊天的 Agent 带 `primaryChat` |
| GET/PUT | `/api/agents/order` | PUT body: `order` | 全部有效 runtime Agent 的 catalog 顺序 |
| GET | `/api/agent` | query: `agentKey` | 单个运行时 agent 详情，不返回编辑专用字段 |
| GET | `/api/skills` | query: 可选 `agentKey` | 全局有效技能目录、configured 与用户 pinned |
| PUT | `/api/skills` | body: `id`, `pinned` | 更新单条用户置顶，返回最新 pinned |
| GET | `/api/skills/icon` | query: `id`，可选 `agentKey` | 无 Agent 时读取全局中心 SVG/PNG；兼容旧 Agent 图标，沿用接口鉴权 |
| POST | `/api/agent/model-config` | body: `agentKey`、可选 `modelKey/reasoningEffort/serviceTier`（至少一项） | 更新 Agent 模型配置，省略保持，等级 null 清除 |
| POST | `/api/agent/open-directory` | body: `agentKey`、`directoryType` | 打开 Agent 工作目录或配置目录 |
| GET | `/api/skill-candidates` | query: `agentKey` | skill candidate 列表 |
| GET | `/api/model-options` | 无 | 聊天运行时可选模型与思考深度 |

`/api/agents` 的 `scope` 可取 `nav`、`copilot`、`invoke`、`internal`、`all`，省略时为 `all`；`includeChats` 为 `0..50`，省略时不附带 chat。可选 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；只接受 `GENERAL`、`CODER`、`KBASE`、`TEAM`、`EXECUTOR`、`PROXY`、`CHANNEL`（大小写无关）；`REACT` 仅在历史 Chat/Run 数据读取时兼容，作为筛选输入返回 400。已下线的 `PLAN-EXECUTE`/`PLAN_EXECUTE`、`ONESHOT`、ACP 别名和未知值均返回 400。响应的 `mode` 统一返回 `GENERAL`，并附带 `engine`（`native` / `acp`，PROXY 与 CHANNEL 不返回）。`mode` 与 `scope` 为 AND，筛选普通 agent catalog 自身的 `mode`，不改变 `includeChats` 按 agentKey 获取 chat 的规则。

`hasWorkspace` 是可选布尔筛选，作用于 Agent catalog 本身：`true` 只返回配置了具体项目目录的 Agent，`false` 只返回没有的 Agent（未配置 Workspace 或使用 `@root`），省略时不筛选；与 `scope`、`mode` 为 AND 关系。TEAM 按自身 Workspace 配置参与筛选。取值规则与 `chatsPinned` 相同。

每个 Agent 有主要聊天时返回 `primaryChat`（与 `/api/chats` 相同的摘要结构和活动 Run 投影），没有时省略；它不受 `includeChats`、`chatsPinned` 影响，所有主要聊天由一次列表读取得到。规则见 [主要聊天](#主要聊天)。

`chatsPinned` 是可选布尔筛选，仅作用于 `includeChats` 附带的 `chats[]`：`true` 只返回置顶 Chat，`false` 只返回未置顶 Chat，省略时保留全部。按 owner 先筛选，再按 recent 顺序取 N 条；不筛选 Agent/Team catalog，不改变 `stats` 的总数和未读数。HTTP 只接受单个 `true` 或 `false` 字符串，WebSocket 使用 JSON boolean；空值、重复参数、null 或其他类型返回 400。

`GET /api/agents/order` 返回所有有效 runtime Agent 的完整 catalog 顺序，不接受 `scope` 或 `mode` 过滤，也不暴露 invalid Agent。`PUT` 接受 `{ "order": ["agent-b", "agent-a"] }`：key 会裁剪空白并校验为空、重复、数量上限和当前有效 catalog 成员；请求未携带的当前有效 Agent 按现有 catalog 顺序追加。Platform 再把这份有效顺序替换进完整 admin 序列的有效 Agent 槽位，invalid Agent 的位置和相对顺序保持不变，并原子写入既有 `agent-order.json`、reload catalog、发布一次 `catalog.updated`。该接口仅提供 HTTP；`/api/admin/agents/order` 继续面向管理台，允许完整 admin catalog 与 invalid Agent，两者共享同一顺序文件且不迁移已有数据。

`GET /api/skills` 返回全局有效技能中心目录，响应为 `{agentKey,skills,pinned,packages?}`。每项包含 `id/displayName/configured` 与可选 `description/icon/version/revision`；不返回 `name`，未配置显示名称时由服务端回退到 SKILL.md 的 `name`；不返回 `items/meta`。可选 `agentKey` 仅计算当前智能体是否已配置该技能，不筛选或重排目录；不存在的 Agent 返回 404 `agent_not_found`。不传时 `agentKey:""`、所有 `configured:false`，仍返回完整目录。技能按中心稳定顺序返回，不追加 Agent 私有技能；`skills` 和 `pinned` 均不为 null。`configured` 表示已配置，并不表示本次必须使用。

`packages` 是技能包声明成员与当前文件状态的展示投影，每项为 `{id,name,displayName,description?,version?,skills:[{id,version?}],missingSkillIds,status}`。包信息读 package.json，成员信息读自身 SKILL.md；name 与 skills 成员列表必填，缺少展示名时回退 name。成员 ID 为 `<package>/<skill>`，只包含当前有效的共享包成员，连接器与 Agent 私有技能不能由同短名混入。HTTP/WS 列表与置顶写响应提供相同投影；置顶写保留原有空 skills 响应语义。客户端将包选择展开为具体完整 ID 并去重，query 的 skill 引用不接受包 ID。不增加 SQL 存储或单独的包执行链路。

普通 Agent 摘要中的 `workspaceDir` 表示该 Agent 的运行工作区，`agentConfigDir` 表示 catalog 已解析的 Agent 配置目录；两者互不替代。`agentConfigDir` 原样返回运行时 `AgentDefinition.AgentDir`，为空时省略。`/api/agent` 继续通过现有的 `source.agentDir` 返回编辑来源目录，不新增顶层字段。

`POST /api/agent/open-directory` 只接受 `agentKey` 和 `directoryType`。`directoryType:"workspace"` 从运行时 registry 的 `AgentDefinition.Workspace.Root` 解析目录，`directoryType:"config"` 从 `AgentDefinition.AgentDir` 解析目录。客户端不得提交 `key`、`workspaceDir`、`agentConfigDir`、`path` 或任何其他未声明字段；出现额外字段时返回 400。实际打开路径完全由后端 registry 决定，并在验证为已存在目录后转换为绝对路径。

请求：

```json
{
  "agentKey": "zenmi",
  "directoryType": "workspace"
}
```

成功响应继续使用统一 `ApiResponse`：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "agentKey": "zenmi",
    "directoryType": "workspace",
    "directoryPath": "/resolved/server/path",
    "opened": true
  }
}
```

TEAM 正常出现在 `/api/agents`，详情 `/api/agent` 带 `teamConfig`；scope、mode、Workspace 和排序规则与普通 Agent 一致。

### Admin

Agent 创建的请求级 `isProject:true` 要求 `definition.runtimeConfig.workspaceRoot` 显式为非空字符串，指向具体、现存且非 canonical 文件系统/驱动器/共享根的绝对目录，拒绝缺字段、null、非字符串、空白值与 `@root`；失败在写盘前返回 HTTP 400 `invalid_project_workspace`。模型工具的 validate/apply 携带同名标志时复用后端强校验：validate 返回无效候选，apply 在审批前拒绝，并在实际发布前再次核验目录；知识库绑定及 access level 均不豁免。省略或 false 保留普通 Agent 契约，标志不持久化。具体创建与模型工具的同名校验见 [智能体创建](智能体配置说明.md#智能体创建)。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/admin/agents` | 无 | admin agent 列表，包含 invalid agent 诊断 |
| GET | `/api/admin/agent` | query: `agentKey` | admin agent 详情，包含编辑配置、来源和诊断 |
| GET | `/api/connectors` | 可选 query: `agentKey` | 使用目录 connectors[] 含 id/name、可选展示字段、readiness/MCP 快照；Agent 范围含 reloadPending，过滤预置 |
| PUT（GET 兼容） | `/api/agents/connectors` | PUT: `{agentKey,connectorId,enabled}`；旧 GET: query `agentKey` | `{agentKey,connectorIds,reloadPending}`，connectorIds 过滤预置；正常读取复用 `/api/agent.connectors` |
| GET | `/api/admin/connectors` | 无 | 完整管理目录：包摘要、组件、技能和 MCP 同步状态 |
| GET/PUT | `/api/admin/agents/connectors` | GET: query `agentKey`；PUT: `{agentKey,connectorId,enabled}` | 完整配置、预置、声明和已生效挂载状态；预置切换返回 403 |
| GET | `/api/connectors/icon` | query: `id`；可选缓存标识 `v` | 显式配置或默认规则选中的 SVG/PNG 图片；沿用服务鉴权，支持 ETag/304，缺失返回 404 |
| GET/PUT | `/api/admin/connectors/detail` | GET: `id/file`；PUT: `id/file/content/baseSha256` | 读取或原子保存连接器定义，旧 MCP Registry 管理接口已移除 |
| DELETE | `/api/admin/connectors/detail?id=<id>` | 外部连接器 id | 无活动运行占用时自动移除 Agent 源码挂载并删除包、授权与 CLI 状态，返回 `{id,deleted:true}`；内置包 403、运行占用 409 `connector_in_use`（`data.error.agentKeys`）、预置引用 409 `preset_connector_readonly`、挂载修改冲突 409 `connector_detach_conflict`、不存在 404；重载失败回滚，提交后清理失败可重试 |
| POST | `/api/admin/connectors/import` | multipart `file` ZIP、可选 `overwrite` | 原子安装或覆盖外部连接器 |
| POST | `/api/admin/connectors/import-mcp` | JSON `mcpUrl`、可选 `authMode`（mcp/none，缺省 mcp） | 按主机名创建 HTTP MCP 连接器，重名不覆盖；见[安装与授权](连接器安装与授权.md#mcp-地址导入) |
| GET/POST/DELETE | `/api/admin/connectors/auth?id=<id>` | 连接器 id | 查询状态、发起登录、退出登录 |
| POST | `/api/admin/connectors/auth/cancel?id=<id>` | 连接器 id | 取消当前登录会话 |
| GET/PUT/DELETE | `/api/admin/source` | GET query: `type`、`key`/`id`、`path`、`category`、`file`；PUT body: `target`、`content`、`baseSha256`；DELETE body: `target`、`baseSha256` | 读取或保存受控的 Agent、Skill、Skill Package、Automation、Registry 文本 source；旧 MCP source 删除入口已退役；mutation 使用哈希防止覆盖并发修改（技能包必填） |
| GET/PUT | `/api/admin/agents/order` | PUT body: `order` | agent 展示顺序 |
| POST | `/api/admin/agents/create` | body: `key`、可选 boolean `isProject`、`definition`、`soulPrompt`、`agentsPrompt` | 创建后的 agent 详情 |
| POST | `/api/admin/agents/import` | multipart: `file`、可选 `overwrite` | 导入完整 Agent ZIP，返回包含 `status` 与 `diagnostics` 的 admin agent 详情 |
| POST | `/api/admin/agents/update` | body: `key`/`agentKey`、`definition`、`soulPrompt`、`agentsPrompt` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/update-name` | body: `key`/`agentKey`、`name` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/delete` | body: `key`/`agentKey` | 删除结果 |
| POST | `/api/admin/agents/skills/import` | multipart: `agentKey`、`file`；兼容可选 `id` | 为一个目录型 Agent 导入并启用专属 ZIP Skill，返回更新后的 admin agent detail |
| POST | `/api/admin/agents/skills/delete` | body: `agentKey`、`id` | 删除该 Agent 的专属 Skill 与其配置引用，返回更新后的 admin agent detail |
| GET | `/api/admin/agents/editor-options` | 无 | agent 编辑器可选项 |
| GET | `/api/admin/agents/creation-defaults` | 无 | 创建所需的类型、默认模型、基础工具与 ACP 引擎，见 [智能体创建](智能体配置说明.md#智能体创建) |
| GET | `/api/admin/host/directories` | `path`、`showHidden` | 列出 Platform 宿主目录的子目录，只返回目录名 |
| GET | `/api/admin/skills` | 无 | 管理目录 `{skills,packages,pinned}`；技能保留状态、图标 URL、版本、诊断、更新时间、大小与引用 agent，包保留有序成员与完整性，pinned 为当前用户偏好 |
| PUT | `/api/admin/skills/pin` | body: `id`、`pinned` | HTTP-only，更新当前用户单项置顶，返回 `{pinned}` |
| GET | `/api/admin/skills/detail` | query: `id`、`openPath` | skill 详情，返回 `fileManifest.entries[]` 与可选 `openedFile` |
| POST | `/api/admin/skills/create` | body: `id`、`skillMd`、`files[]` | 创建后的 skill 详情 |
| POST | `/api/admin/skills/import` | multipart: `id`、`file`；可选 `overwrite` | 原子校验并导入完整 ZIP，返回 skill 详情 |
| POST | `/api/admin/skills/delete` | body: `id` | 删除结果；仍被 agent 引用时返回 409 和 `usedByAgents` |
| GET | `/api/admin/skill-packages` | 无 | 保留供 Desktop 技能市场使用；WebClient 管理页改用聚合 `/api/admin/skills` |
| POST | `/api/admin/skill-packages/import` | query: `id`、可选 `version`；raw ZIP body | 原子校验并安装或更新技能包，返回包状态与实际安装的子技能 |
| GET/PUT | `/api/admin/source`（`type: skill-package`） | GET query `type/id`；PUT body `target/content/baseSha256` | 统一读取或条件保存包自身 `package.json` |
| POST | `/api/admin/skill-packages/delete` | body: `id` | 原子卸载技能包及其子技能，返回删除的子技能列表 |
| POST | `/api/admin/skill-packages/skills/delete` | body: `packageId`、`skillId` | 原子删除包内单个子技能并更新包状态 |
| GET/PUT | `/api/admin/skills/file` | query/body: `id`、`path`、`content`、`baseSha256` | 读取或保存 UTF-8 文本文件 |
| POST | `/api/admin/skills/file/create` | body: `id`、`path`、`content` | 创建文本文件 |
| POST | `/api/admin/skills/file/delete` | body: `id`、`path`、`recursive`、`baseSha256` | 删除 skill 内文件或目录 |
| POST | `/api/admin/skills/file/mkdir` | body: `id`、`path` | 创建 skill 内目录 |
| POST | `/api/admin/skills/file/rename` | body: `id`、`fromPath`、`toPath`、`overwrite` | 重命名 skill 内文件或目录 |
| POST | `/api/admin/skills/file/upload` | multipart: `id`、`path`、`overwrite`、`file` | 上传 skill 内二进制或大文件 |
| GET | `/api/admin/skills/file/download` | query: `id`、`path` | 下载 skill 内非目录文件 |
| GET | `/api/admin/skills/download` | query: `id` | 下载 skill 的安全分发 ZIP 包 |
| POST | `/api/admin/skills/validate` | body/query: `id` | 重新加载并返回该 skill 当前校验结果 |
| GET | `/api/admin/tools` | 无 | tool 列表，含扁平化工具来源字段 |
| GET | `/api/admin/registries` | 无 | registry 文件列表摘要，含状态、脱敏 summary、首条诊断摘要与诊断数量 |
| GET/PUT | `/api/admin/registries/detail` | query/body: `category`、`file`、`content` | registry 文件详情或保存结果 |
| POST | `/api/admin/registries/validate` | body: `category`、`file`、`content` | registry 内容校验结果 |

`POST /api/admin/agents/import` 不接受单独的 Agent Key；Key 固定读取 ZIP 根目录或唯一一层包装目录内的 `agent.yml` / `agent.yaml`。导入会保留 Agent 目录的全部普通文件，包括 prompt、专属 Skills、`.config`、知识文件与其他资源。ZIP 校验拒绝路径逃逸、反斜杠路径、symlink、非普通文件、大小写冲突、重复路径及文件/目录冲突，并忽略 `__MACOSX` 与 `.DS_Store`。限制为 32 MiB 上传、32 MiB 单文件、256 MiB 解压总量、4096 个条目和 1 MiB UTF-8 Agent YAML。

同 Key 已存在且 `overwrite` 省略或为 `false` 时返回 409，`data.error` 包含 `code`、`agentKey`、`existingName` 与 `overwriteRequired:true`。确认后以同一 ZIP 和 `overwrite=true` 重试会整目录替换旧来源；目录型 Agent 原位替换，平铺 YAML Agent 转换为规范目录来源，不合并或保留旧 `.config`、专属 Skills 或资源。导入使用隐藏 staging/backup 完成原子切换；catalog 硬重载失败时恢复旧来源，回滚失败返回明确的 500 诊断。ZIP 布局、YAML、Key 或公共 mode 等结构性错误返回 422 和文件级 diagnostics，非 ZIP 返回 415，超限返回 413。若 catalog 可以重载、但该 Agent 因本机模型、工具、Workspace、KBASE 或 Skill 缺失而为 `invalid`，导入结果仍保留并以 200 返回无效状态与 diagnostics。

`/api/admin/source` 的 target 是逻辑标识而不是文件系统路径：`agent`、`automation` 使用 `key`；`skill-package` 使用 `id`（固定定位 package.json，不接受 path），`skill` 使用 `id` 与相对 `path`，`registry` 使用 `category` 与 `file`。GET 响应固定返回 target、实际受控来源、原始 `content`、`encoding`、`sha256`、`size` 和 `updatedAt`；文本必须为 UTF-8 且不超过 1 MiB。Agent 保存只允许该 agent 的 `agent.yml`，并 reload agent catalog；Skill 与 Automation 保存分别 reload 对应 catalog / orchestrator。MCP 配置编辑改走连接器接口，旧 category=mcp-servers 的 GET/PUT/DELETE 均拒绝。旧的 Skill 文件结构、二进制上传下载接口，以及 Registry detail 接口仍保留兼容。

`/api/admin/tools` 中 `kind` 表示调用方式（如 `backend`、`frontend`、`action`），`sourceType` 表示定义来源类型（如 `local`、`agent-local`、`mcp`），`sourceCategory` 表示来源分类：`platform` 为 runtime 自带工具，`external` 可用于 `<AP_RUNTIME_DIR>/tools` 下普通 frontend/action/agent-local YAML 的来源分类，`mcp` 为 MCP registry 同步工具。`external` 不再表示子进程调用协议。MCP 工具额外返回 `serverKey`。列表响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 tool definition `meta`；接口不接收 query 过滤参数。

`/api/admin/skills` 只编辑 `<AP_RUNTIME_DIR>/skills-center` 下的共享 skill 目录，不直接编辑 agent 本地 `skills/` 同步副本。文件路径必须是相对路径，服务端拒绝目录逃逸和 symlink 跟随；JSON 文本读写限制为 UTF-8 且不超过 1 MiB，二进制或大文件通过 upload/download 接口处理。保存、上传、删除或重命名 skill 文件后会触发 `skills` reload 并级联 reload `agents`，使声明该 skill 的 agent 本地副本重新同步。

`POST /api/admin/agents/skills/import` 只面向目录型普通 Agent。它沿用共享 Skill ZIP 的校验和限额，但将内容写入 `<agents>/<agentKey>/skills/<id>/`，并原子地把 id 加入该 Agent 的 `skillConfig.skills` 后 reload agents；导入失败或 reload 失败都会恢复 Agent YAML 并清理本地目录。未传 `id` 时，服务端从 ZIP 的 `SKILL.md` frontmatter 优先读取 `id`，兼容旧 `key`，都没有则读取 `name` 作为 Skill ID；调用方可显式传 `id`。专属 Skill 不会出现在 `/api/admin/skills`，也不能由共享 Skill 删除接口删除。导入准入不查询 skills-center；若两者 ID 相同，该专属版本只对当前 Agent 优先，其他 Agent 仍可使用技能中心版本。Admin Agent Detail 的 `privateSkills[]` 返回本地摘要、是否启用及 `overridesCenter`，不返回本地路径或文件内容。专属删除同样只在 Agent 路由执行，并同时删目录和配置引用。

`/api/admin/skills` 管理 Skill 的结构和二进制文件操作；可编辑文本内容可通过 `/api/admin/source` 的 Skill target 读取和保存。`detail` 不内联全量文件内容，而返回轻量 `fileManifest`：`revision`、`defaultOpenPath`、文件统计和预排序扁平 `entries[]`。每个 entry 使用完整相对 `path` 作为稳定 ID，并带 `parentPath/depth/order/contentKind/language/role/editable/downloadable/uploadable/renamable/deletable`。`openPath` 指向可编辑 UTF-8 文本文件时，`detail` 额外返回 `openedFile`；二进制或过大文件只返回 metadata。保存使用 `baseSha256` 做并发保护，冲突返回 409。文本保存（`PUT /api/admin/source` 的 Skill target 与兼容 `PUT /api/admin/skills/file`）通过 `adminsource` 串行执行写入和 catalog reload；reload 失败时按本次写入内容的哈希校验后恢复原文件与 SHA-256，前端可保留草稿并使用原 `baseSha256` 重试。回滚后的 catalog 恢复不受客户端断连影响；若文件已被其他操作修改，则保留新内容并报告恢复失败。创建、删除、重命名、上传和 mkdir 的 mutation 响应会返回新的 `fileManifest` 与 `selectedPath`，方便前端直接刷新文件树。列表和详情摘要会按 `assets/icon.svg`、`assets/icon.png`、`assets/<skill-id>.svg`、`assets/<skill-id>.png` 的顺序查找 regular、非 symlink 的图标，找到后返回 `icon` 下载 URL；未提供图标时省略字段，由客户端负责默认图。`/api/skills/icon` 对 SVG 使用与连接器图标相同的静态内容校验及响应安全头。skill 摘要从 `SKILL.md` frontmatter 提取可选 `version`：顶层 `version` 优先，缺失或空白时回退 `metadata.version`；两者皆无或空白时省略字段。`file/download` 只下载单一文件；`download` 返回 ZIP，包含安全的普通 skill 文件、跳过 symlink 与 `.runtime-env.json`，并限制未压缩内容为 256 MiB。

`POST /api/admin/skills/import` 是 WebClient 统一 ZIP 导入入口：multipart `file` 必填，`id` 可选，上传上限 512 MiB。Platform 识别安全 ZIP 的 `package.json`，或旧根 `manifest.json` 的 `type: skill-package`，调用同一包安装/更新事务，忽略单技能 ID 提示，并返回 `{kind:"skill-package", package:{id,name?,version,sha256,skills,installedAt}}`。声明技能包却无效时返回诊断，不回退单技能。其他 ZIP 沿用单技能导入，缺省 id 从 SKILL.md 的 frontmatter.id 或 name 读取，返回 `{kind:"skill", ...AdminSkillDetailResponse}`，旧客户端仍可读取顶层 skill/capabilities/fileManifest/openedFile。单技能 ZIP 仍限 32 MiB；默认重名返回 409。可通过 query 或 multipart `overwrite=true` 显式整目录替换，旧目录保留到 reload 成功；完整校验失败不触碰旧目录，reload 失败恢复旧目录及原有 `skill.json` 等文件。成员文件可在编辑器中编辑，元信息仍保留在自身 SKILL.md；市场独立技能更新只写顶层技能，不更新同名包成员。multipart 大文件落进程临时目录，所有返回路径清理临时文件；ZIP 文件字节不在浏览器解压或经 WS 传输。ZIP 可直接以 `SKILL.md` 为根，也可只有一层包装目录；`__MACOSX` 与 `.DS_Store` 被忽略。服务端拒绝目录逃逸、反斜杠路径、symlink、非普通文件、重复或大小写冲突路径、文件/目录冲突，并限制单文件 32 MiB、未压缩总量 256 MiB、最多 4096 个 entry。解包先进入 catalog 与 watcher 都忽略的隐藏 staging，完整验证 `SKILL.md`、`.runtime-env.json` 和 runtime 文件后再原子 rename；重名返回 409，非 ZIP 返回 415，包内诊断返回 422 `data.error.diagnostics[]`，首次导入失败不保留目标目录；覆盖失败恢复旧目录，恢复受阻时保留备份并报告位置。成功后沿用 `skills` reload 和 Agent 重组；解压与静态校验在 Catalog mutation 保护外，当前状态检查、目录发布及 reload 位于保护内；暂存与备份在技能根的同级目录。

普通技能删除先移入技能根同级备份，再执行 skills reload；reload 失败恢复整目录及 metadata，成功后清理备份。包内子技能可使用技能包专用删除接口；普通 Skill 删除与事务删除也复用包成员删除事务，同步更新成员清单，不能绕过引用检查。

技能中心采用以下目录结构：顶层 `skills-center/<skill>/SKILL.md` 是独立技能；`skills-center/<package>/package.json` 标识技能包，成员位于包内一层 `<skill>/SKILL.md`。`package.json` 为 JSON，要求非空合法 `name` 与 `skills` 对象数组，例如 `{"name":"office","skills":[{"id":"pdf"},{"id":"docx"},{"id":"xlsx"}]}`。每个成员至少保存包内单段 id，可携带扩展属性；扩展属性在导入、编辑和删除其他成员时保留，但成员 name、版本和展示元数据仍读取自身 SKILL.md；id 不允许重复（含大小写冲突）、目录穿越或多层路径。`displayName/description/version/triggers/metadata` 及多语言字段可选。成员清单决定加载范围和包内顺序，展示信息来自各自 SKILL.md；未声明目录不自动成为技能。空包使用 `skills:[]`，删除最后成员保留包信息。 声明成员的目录或 SKILL.md 缺失、符号链接或不可读取时，包记录保留该成员与 diagnostics，其他成员仍可用。新 ZIP 导入要求声明成员完整；编辑可先声明缺失成员再创建，已有成员路径仍须通过安全校验。

启动 Catalog 前对已安装且缺少 `skills` 的历史 package.json 执行一次成员补齐，原文件在技能根外保留备份；已有 `skills` 的清单不进入此兼容迁移，严格读取和新 ZIP 导入不隐式推断成员。旧 `.package` 迁移保留缺失成员声明与诊断。

`GET /api/admin/skills` 列表与详情的 `packageId` 仅用于分组。独立技能 ID 为 `<skill>`，包内 ID 为 `<package>/<skill>`，不能以短名替代包内 ID；同名技能可同时存在并独立选择、编辑和更新。`GET /api/admin/skill-packages` 按清单顺序返回声明成员与实际版本；缺少版本保持空值，不借用包版本。包展示文案复用请求语言解析规则，缺少展示名回退 name。列表读取不改写文件，也不维护 SQL 或第二份成员清单。声明成员文件缺失时保留成员条目并标记 incomplete/missingSkillIds，聊天选择列表只提供有效成员。新格式清单缺少 skills 或使用字符串数组视为无效；旧市场 ZIP 与旧 .package 迁移自动生成对象数组。

`POST /api/admin/skill-packages/import` 接收原始 ZIP，不接受文件系统路径。新 ZIP 为 package.json 和成员目录，可带一层包目录；历史市场 manifest.json ZIP 仅作导入兼容，由 Platform 转换为新布局。校验在隐藏 staging 完成，发布时只替换 `skills-center/<package>`；包外同名技能不参与冲突、覆盖或删除。普通技能安装只替换顶层对应技能，不能覆盖技能包根目录。新包移除的成员随本包更新一起移除。整个目录切换与 Catalog 重载失败会恢复旧包，临时 ZIP/staging/backup 成功后清理。

技能包信息编辑统一使用 `GET/PUT /api/admin/source`，GET 参数 `type=skill-package&id=office`，PUT 结构如下；原 `/api/admin/skill-packages/manifest` 路由已移除。

```json
{
  "target": {"type": "skill-package", "id": "office"},
  "content": "{\"name\":\"office\",\"skills\":[{\"id\":\"pdf\"}]}",
  "baseSha256": "读取响应中的 sha256"
}
```

返回统一 `target/source/content/encoding/sha256/size/updatedAt`。PUT 需要读取时的 `baseSha256`，并发修改返回 409；禁止修改 name 更换包身份、移除正在被 Agent 引用的成员。保存复用 Catalog 事务，重载失败恢复原文件。修改成员列表只修改归属，不隐式删除文件；新增成员可先声明 id，再通过既有 Skill 创建/导入接口填充成员内容，或整包导入。成员删除接口在同一事务内删除目录并更新 skills，失败同时回滚；整包卸载仍检查 Agent 使用情况。包内文件编辑继续使用 `type:skill` 和完整成员 ID。


`/api/admin/registries` 是列表接口，不返回 registry 文件绝对路径、完整 `diagnostics[]` 或文件大小；编辑器应通过 `/api/admin/registries/detail` 获取 `source`、完整诊断、`content`、`parsed` 与 `size`。

使用目录 `GET /api/connectors[?agentKey=...]` 返回 `data.connectors[]` 的 id/name、非空 description/iconUrl/mutuallyExclusiveWith、本地 readiness 和可选 mcp[]（agentKey/serverKey/status/toolCount，只含各 Agent 当前发布版本，每个组件最多一条）；指定 Agent 时返回顶层 agentKey/reloadPending，全局省略。读取使用既有快照，不执行 CLI 或主动探测上游；按当前 Agent 过滤全局与 mode 预置，省略 agentKey 时过滤全部预置并集。`/api/agent.connectors` 提供已保存的非预置关联 ID；`PUT /api/agents/connectors` 只返回 agentKey、非预置 connectorIds 和 reloadPending，旧 GET 保留兼容。管理目录 `GET /api/admin/connectors` 保留完整包清单、`hasMcp/hasCli/hasBin/skills`；`mcp[]` 包含 `serverKey/toolCount/status` 和可选同步时间、脱敏诊断。管理挂载 `GET/PUT /api/admin/agents/connectors` 保留 presetConnectorIds、declaredConnectorIds 和 activeConnectorIds。两套挂载 PUT 都拒绝预置切换（403 `preset_connector_readonly`），不改变运行时自动挂载。读取定义使用 `GET /api/admin/connectors/detail?id=...&file=...`，保存使用同路径 PUT（`id/file/content/baseSha256`，哈希必填，冲突 409）。先校验、原子替换、本地 reload，失败恢复；远端初始化继续后台执行，发送 `catalog.updated(reason=connectors)`。完整契约见 [连接器](连接器.md)。

带图标的连接器在管理目录另返回实际选中的 `icon`（例如 `assets/icon.svg`）、`iconSha256` 和 `iconUrl`（`/api/connectors/icon?id=<id>&v=<sha256>`）；使用目录仅返回 iconUrl，无可用图标时省略。显式 `connector.json.icon` 优先；未配置或为空时按 `assets/icon.svg` → `assets/icon.png` → `assets/<id>.svg` → `assets/<id>.png` 自动发现，`<id>` 为连接器 ID，不回写定义或修改版本。图标接口成功响应直接为 `image/svg+xml` 或 `image/png` 字节，失败沿用 JSON 错误包裹；仅允许读取该连接器显式配置或默认规则选中的图片，不能用 `file` 参数读取其他包文件。启用鉴权时必须携带有效认证。缓存使用 `private, max-age=0, must-revalidate` 和内容 SHA-256 ETag；`If-None-Match` 命中返回 304。客户端通过带认证请求获取 Blob，再用 `<img>` 显示，失败显示默认图标。

Registry 列表的 `summary` 按分类返回展示字段：provider 暴露 `baseUrl`；model 暴露 `provider/protocol/type/isVision/isReasoner/isFunction/maxInputTokens/maxOutputTokens/timeout`；provider 与 model 均通过 `summary.icon` 透传 YAML 中的非空图标标识，未配置时省略，前端使用默认图标。旧 viewport server 管理分类已删除。



### Chat

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/chats` | query: `lastRunId`、`agentKey`、`mode`、`pinned`、`primary`、`hasWorkspace`、`limit` | chat 摘要列表 |
| PUT | `/api/chats/primary` | body: `chatId` | 把已有 Chat 设为其 Agent 的主要聊天，返回 `agentKey`、`chatId`、`previousChatId`、`changed` |
| GET | `/api/chats/order` | 无 | 当前 `sortMode`、完整有序 `pinnedChats`、兼容投影 `pinnedOrder` 与可选 `updatedAt` |
| PUT | `/api/chats/order` | body: `set_mode`、`move` 或 `set_pinned` operation | 更新后的 `sortMode`、`pinnedOrder` 与 `updatedAt` |
| GET | `/api/chat` | query: `chatId`、`includeRawMessages`、`detail` | chat 详情，默认含 events；`detail` 见 [细节等级](#细节等级与-runactivity) |
| POST | `/api/chats/search` | body: `query`、`agentKey`、`limit` | 全局 chat 搜索结果 |
| POST | `/api/read` | body: `chatId` | 标记已读结果 |
| POST | `/api/feedback` | body: `chatId`、`runId`、`messageId`、`rating`、`reason` | feedback 写入结果 |
| POST | `/api/chat/delete` | body: `chatId` | 删除 chat 结果 |
| POST | `/api/chat/rename` | body: `chatId`、`chatName` | 重命名结果 |
| POST | `/api/chat/derive` | body: `sourceChatId`、`sourceRunId`、`chatId`、`chatName` | 从已完成 run 派生新 chat |
| POST | `/api/compact` | body: `requestId`、`chatId`、`trigger`、`level` | 历史或活动原生根 Run 的上下文压缩最终结果 |
| POST | `/api/chat/archive` | body: `chatId`、`reason` | 归档结果 |
| GET | `/api/chat/export` | query: `chatId`，可选 `format=markdown\|snapshot` | 默认 Markdown；`snapshot` 返回 V1 JSON；HTML 与未知格式返回 400 |
| GET | `/api/chat/jsonl` | query: `chatId` | 原始持久化 chat JSONL 文本；active 不存在时回退 archive，不得作为公开分享输入 |
| GET | `/api/chat/system-prompt` | query: `chatId`、`runId`、`agentKey` | 获取该 agent 在历史 run 中首次使用的持久化 system message；服务端从 run 的 system-init / step `systemRef` 解析快照 |
| GET | `/api/chat/llm-trace` | query: `file=<chatId>/.llm-records/<runId>_NNN.json` | 原始 LLM chat trace JSON 文本 |

`/api/chats/search` 的 HTTP 与 WebSocket 结果同时提供搜索命中的 `snippet` 和 Chat 摘要中的 `lastRunContent`。后者用于展示最新 Run 内容，即使命中来自较早的 Run；没有最新内容时返回空字符串，不用命中片段或对话标题替代。

`/api/chats`、`/api/agents?includeChats=...` 的 `chats[]` 和 `/api/chats/order` 的 `pinnedChats[]` 均省略 `usage` 与 `canContinue`，HTTP 与 WebSocket 一致。列表查询不计算续执行资格；`/api/chat` 详情提供 `canContinue`，用量仍持久化并通过详情读取完整统计。

`/api/chats` 和 `/api/chat` 的历史发现、owner 和回放不要求当前 Agent 配置有效；Agent 详情的 404 不代表 Chat 不存在。客户端应独立加载历史与当前执行配置，保留 Chat 自身的认证、缺失及损坏错误，不能通过历史读取恢复无效 Agent 的 query 能力。详见 [历史读取与当前 Agent 可用性](会话存储与回放.md#历史读取与当前-agent-可用性)。

`/api/chats` 的 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；只接受 `GENERAL`、`CODER`、`KBASE`、`TEAM`、`EXECUTOR`、`PROXY`、`CHANNEL`（大小写无关），已下线的 `PLAN-EXECUTE` 返回 400。通用类型的筛选输入只接受 `GENERAL`，传入 `REACT` 返回 400；`GENERAL` 同时匹配历史保存的 `REACT` 和新的 `GENERAL` chat。可选布尔 `hasWorkspace` 按 chat 所属 Agent 当前是否配置了具体项目目录筛选（未配置 Workspace 或使用 `@root` 都视为没有）：`true` 只返回项目型 Agent 的 chat；`false` 排除它们，同时保留 Agent 已不在 catalog 中的 chat。它与 `mode`、`pinned` 为 AND 关系，并在 `limit` 截断之前生效。HTTP 只接受单个 `true` 或 `false` 字符串，WebSocket 使用同名 JSON boolean，其他值返回 400；旧 `agentType` 参数或 payload 返回 400，调用方应改用 `hasWorkspace`。`pinned=true` 按置顶 ID 直接读取；`pinned=false` 且实例排序为 `recent` 时，`limit` 在读取阶段生效，只为返回的 chat 计算完整摘要。旧别名和未知值均返回 400。筛选与 agentKey、lastRunId 为 AND 关系，TEAM Chat 也遵循这些筛选。显式 agentKey 只返回该根 Agent 的 Chat。可选 `limit` 必须为正整数且不设上限；省略时返回全部匹配项，传入时必须在全部筛选和当前实例级排序后截断，不能先取最近记录再局部重排。`limit=0`、负数、空值或非整数返回 400；当前不支持 offset 或分页游标。WebSocket 的 `/api/chats` 请求使用等价的 `mode` 与 `limit` 字段（`limit` 未传为全部）。旧 `agentMode` 参数或 payload 会返回 400，调用方应改用 `mode`。

`/api/chats` 的可选 `pinned` 与其他筛选按 AND 组合：`true` 只取置顶组，`false` 只取未置顶组，省略则取全部且置顶组在前。HTTP 只接受单个 `true` / `false`，WebSocket 只接受 JSON boolean；非法值返回 400。筛选和各组排序均在 `limit` 截断之前完成。例如 `mode=GENERAL&pinned=false&limit=8` 返回最多 8 条未置顶的匹配记录，不会让置顶项占用这 8 个位置。获取完整跨 mode 置顶组使用 `/api/chats?pinned=true`，不传 `mode` 或 `limit`。

`/api/chats/order` 管理同一 Platform 实例的展示偏好，不按用户、Agent 或 mode 分开。没有保存排序配置时默认 `recent`，配置损坏时也回退到 `recent`；已有有效的 `manual` 偏好继续保留。`pinnedOrder` 是独立的有序 active Chat ID 数组，缺省为空；`sortMode` 只控制未置顶组。`recent` 按 `updatedAt DESC, chatId DESC`；`manual` 先把尚未进入保存序列的新建或恢复 Chat 按 `createdAt DESC, chatId DESC` 放在未置顶组前面，再接保存的 active Chat 顺序，已归档、删除或不存在的 ID 自动忽略。`updatedAt` 是两份展示偏好的较新修改时间，未发生修改时省略。

手动顺序首次初始化按 Chat 创建时间倒序，后续内容更新不改变位置；模式切换保留已有手动顺序，新建或恢复且未保存的 Chat 按创建时间倒序补到前面。recent 下直接拖动以当时全量 recent 顺序为基线应用移动并原子保存为 manual；已有手动顺序不因升级重置。

WebClient 与 Desktop 导航只通过一次 `/api/chats/order` 读取排序配置和完整跨 mode、跨 owner 的置顶摘要，不再探测能力或追加 `/api/chats?pinned=true`。读取响应的 `pinnedChats` 固定为数组（空列表为 `[]`），每项复用 `/api/chats` 摘要结构和活动 Run 投影，数组顺序即展示顺序；`pinnedOrder` 仅是同一列表的兼容 ID 投影。存储层在同一锁内读取偏好、置顶和持久化摘要；活动 Run 随后按既有规则补充，仍需实时 Push 校准。`updatedAt` 仅表示展示偏好修改时间，不能作为摘要整体的缓存版本。读取失败不得被客户端解释为空置顶列表。

PUT/WS mutation 继续返回轻量 `sortMode/pinnedOrder/updatedAt`，不附带 `pinnedChats`，避免保存成功后摘要读取失败造成写入结果歧义。成功后两端通过统一读取接口刷新，普通未置顶预览按需补位。

### 主要聊天

每个 Agent 最多有一个主要聊天，它的其他 active Chat 是历史聊天；归档不受影响。指针保存在 `chat-primary.json`（存储与损坏处理见 [会话存储与回放](会话存储与回放.md)），不进入 JSONL 或 `chats.db`，也不区分 mode。Chat 摘要、`/api/chats/order` 的 `pinnedChats[]` 与 `/api/chat` 详情顶层固定返回 `primary` boolean。

- 新建：`/api/query`（HTTP/WS）带 `primary:true`，且本次请求创建该 Chat（省略 `chatId`、`chatId` 尚不存在，或是只有上传、尚未受理首条 query 的占位 Chat）。Chat 建立后指针随即切换；`chatId` 指向已有 Chat 时返回 400，侧通道（`lane` 非空）同样返回 400。`primary` 是调用方原样输入，只作用于这一次请求，计划批准后的执行 Run 与恢复不会重放它。
- 设定：`PUT /api/chats/primary {chatId}`（WS 同路径同字段）把已发布的已有 Chat 设为其 owner Agent 的主要聊天；Chat 不存在返回 404，上传占位 Chat 或无 owner 的 Chat 返回 400；已是主要聊天时 `changed:false` 且不广播。
- 互斥：当前主要聊天有活动 Run（含等待用户提问、审批的 Run）时，新建与设定都返回 409 `primary_chat_busy`，`data` 带当前主要聊天的 `chatId` 与 `runIds`。query 在创建 Chat 之前检查，被拒绝时不会留下新 Chat。
- Mailbox 补缺：chat_send 省略 chatId/newChat 且主要指针缺失时，在会话 mutation 锁内补建，并以“指针仍缺失才设置”的条件写入设为主要聊天；恢复 accepting 时重新仲裁已有指针。既有指针不切换；`primary:true` 的 query 不取该锁，期间若它先设置了指针，信留在新建的会话中。
- 不切换指针的入口：普通 query、在历史聊天中续聊、Automation、`chat_start`、`/api/execute`、channel、derive 和归档恢复创建或继续的 Chat 都只是历史聊天。
- 生命周期：归档或删除主要聊天时先清除指针，客户端由既有 `chat.archived` / `chat.deleted` 得知；恢复归档不恢复指针。没有指针的 Agent 视为没有主要聊天，升级后不迁移已有 Chat。
- 列表：`/api/chats` 可选 `primary` 与其他筛选按 AND 组合，并在 `limit` 之前生效；`primary=true` 按指针 ID 直接读取，`agentKey=<key>&primary=false` 即该 Agent 的历史聊天。HTTP 只接受单个 `true`/`false`，WebSocket 使用 JSON boolean。
- 指针切换成功后广播 `chat.primary.changed`。

智能体可通过 `chat_manage` 的 `setPinned` 调用同一置顶业务入口，沿用 `chats.order.changed` 通知；工具契约与执行边界见 [Platform 控制工具设计](Platform控制工具设计.md#会话)。

`PUT /api/chats/order` 接受三种互斥 operation：

- `{"operation":"set_mode","sortMode":"recent"}`：`sortMode` 只接受 `recent | manual`。切回 recent 保留 manual 顺序，之后切回 manual 可恢复；置顶组顺序不变。
- `{"operation":"set_pinned","chatId":"chat-1","pinned":true}`：必须显式给出 boolean。首次置顶插入置顶组首位，重复设置相同状态不写文件、不调换位置、不广播；取消置顶后按原 recent/manual 规则回到未置顶组。置顶不存在的 Chat 返回 404，仅上传尚未正式命名的占位 Chat 返回 400；取消不存在的 Chat 的置顶是幂等清理。
- `{"operation":"move","chatId":"chat-1","beforeChatId":"chat-2"}`：`beforeChatId`、`afterChatId` 必须且只能指定一个；目标与锚点必须是 active Chat，不能自身锚定或跨置顶/未置顶组移动。组内置顶拖动只修改 `pinnedOrder`；未置顶组在 recent 下首次 move 建立全量 manual 基线并切换模式。

HTTP 与 WebSocket 使用相同字段和错误语义；WebSocket 空 payload 相当于 GET。成功 mutation 仅修改展示偏好，不修改 Chat `updatedAt`、owner、内容或已读状态；成功修改后广播 `chats.order.changed {updatedAt}`，客户端重读列表对账。归档或删除清理对应置顶 ID，并沿用既有 lifecycle Push；恢复归档不恢复置顶。


Chat 列表摘要、`/api/agents?includeChats` 中的摘要和 `/api/chat` 详情顶层固定返回 `pinned` boolean。chat 摘要会在新数据中返回可选 `mode`；`/api/chat.runs[]`、`/api/agents?includeChats` 及 archive detail 中的共享 `runs[]` 均返回每次 run 的可选 `mode`。普通 agent 持久化规范 API mode（例如 `GENERAL`、`CODER`、`KBASE`、`TEAM`、`EXECUTOR`、`PROXY`、`CHANNEL`）；TEAM Agent 保存 `TEAM`。历史 chat/run 不根据当前 catalog 回填或转换，原始 mode 仅用于历史读取，不能作为当前筛选或运行输入；唯一的例外是改名前保存的 `REACT`：存储行与 JSONL 保持原值不改写，响应中统一以 `GENERAL` 返回，并可被 `mode=GENERAL` 筛选命中；TEAM Chat 按同样的 mode 规则筛选。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 摘要，以及 `/api/chat` 详情顶层在新数据中可包含 `source`，表示 chat 首次创建来源。当前只记录 query 与 automation 两类：`query` / `query:<user>` 表示由 query 创建，`automation:<automationId>` 表示由 automation 创建。旧数据为空、上传创建或派生创建时省略。channel 远程用户调用本机智能体仍属于 query source；gateway 可在受信 channel 请求中传 `sourceUser`，否则服务端会从形如 `channel#single#user1#...` 的 chatId 中取远端用户段作为 `query:<user>`。`sourceChannel` 是 gateway/channel 路由标签，不承载 query / automation 语义。

`/api/chat` 详情固定返回顶层 `createdAt` 与 `updatedAt`，并与列表 summary 一样返回 owner `agentKey`、可选 `mode`、`lastRunId`、`lastRunContent` 和完整 `read { isRead, readAt?, readRunId? }`；客户端不得从 runs、events 或本机时间推断这些字段。该详情 summary 是外部路由直接打开未进入列表缓存的 Chat 时的权威 read 基线。每个 `runs[]` 的 `startedAt` 由注册时捕获并持久化；已完成 run 的 `completedAt` 必填，仍在执行的 run 则省略 `completedAt`（绝不输出 `0`）。`activeRun.startedAt` 与对应 push `run.started.startedAt` 是同一个已捕获时刻；push `run.finished.finishedAt` 与完成记录的 `completedAt` 相同。`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 以及 `/api/chat` 的 chat 详情，在存在当前进程的活动等待项时都包含顶层 `awaiting`：`awaitingId`、`runId`、`mode`、`status:"awaiting"`、`createdAt`。完整问题、审批项、表单和 planning 定义仍从 chat events 中的 `awaiting.ask` 获取；没有顶层 `awaiting` 的历史 ask 不可提交。Platform 重启统一取消全部未完成 Run，包括 question/planning/approval/form/wait，清除 pending，保留历史内容并补齐中断结果与取消记录；不会注册历史 activeRun，也不会自动执行已提交的答案或计划。浏览器重连仍可 attach 当前进程的 live Run。

单 Chat 已读由显示 Main Chat 内容的 WebClient 调用 `/api/read { chatId, runId? }` 发起；Platform 按现有单调 `readRunId` 规则先持久化，再广播字段完整的 `chat.read`。Run 完成准备发送 `chat.unread` 时，服务端必须重新读取当前 summary；如果 `readRunId` 已覆盖 `lastRunId`，不得发送与持久化状态矛盾的 unread。消费者用 `RunIDAfter(lastRunId, readRunId)` 与 `readAt/createdAt` 合并迟到 Push。Agent 级 `/api/read { agentKey }` 只用于用户显式“全部标为已读”，成功后广播 `chat.read_all`。

`POST /api/chat/derive` 只支持 active chat 存储，不从 archive 直接派生。`sourceRunId` 省略时使用 source chat 的 `lastRunId`；source chat 必须没有 active run 和 pending awaiting，且目标 source run 已完成。服务端会创建新的独立 `chatId`，复制截至 source run 的可回放 JSONL 历史与必要资源，并为复制出的历史 run 生成新的 runId；返回 `lastRunId` 是新 chat 中映射后的 runId。派生成功后客户端继续用新 `chatId` 调 `/api/query`，后续运行不会写回原 chat。

`/api/chat` 返回 active run 时，`activeRun.lastSeq` 是本次 chat detail 已返回历史 events 覆盖到的公开 live stream 游标，客户端应用这些 events 后可把它作为 `/api/attach.lastSeq`。它来自 `chatId.jsonl` 每行顶层 `liveSeq` 的 replay 结果，不是内存 run 当前最新 seq；内存最新 seq 只用于服务端运行状态。新的 Native / Team run 只在事件实际发布时递增该游标，内部事件复用最近公开游标；历史 run 的旧游标不迁移。对 `WAITING_SUBMIT` active run，该 attach 应在 submit 前建立并保持等待；submit 成功不应再创建第二个 attach，同一连接会从 `request.submit` / `awaiting.answer` 开始继续接收该 run 的后续事件。

`POST /api/compact` 的标准手动请求为 `{ "requestId":"...", "chatId":"...", "trigger":"manual", "level":"l1_tools"|"summary" }`，HTTP 与 WebSocket 字段一致。`l1_tools` 确定性清理可压缩轮次的 reasoning 与完整工具组，保护最近 N 轮完整模型调用及未完成交互，保留正文、引用和附件；通过原行 `_compact` 标记控制上下文，不复制或改写原文，不调用模型，也不产生 `compactionUsage`。`summary` 将符合条件的多个旧 Run 和活动 Run 已完成前缀合并成一个摘要，严格只调用一次摘要模型；摘要输入先做不落盘的 L1 结构化投影，完整规范化输入仍超出预算时返回 `summary_input_too_large`，绝不丢弃中间历史或拆成多次摘要调用。

压缩规划、L1 前后计算、L2 mandatory 检查和结果校验共用同一套多模态安全估算。普通文本和工具 Schema 使用同一文本 Token 估算器；`image_url` 的 Base64 正文不作为文本计数，可从有界图片头读取尺寸时按 `ceil(width×height/750)` 计算并限制在 256–32768 token，WebP、外部 URL、非法 Data URL 或无法识别尺寸时固定为 8192 token；尺寸检查不会完整解码或访问网络。活动 Run 有有效 provider prompt usage 时，自动触发取该 usage 增量估算与完整请求估算的较大值，压缩后保留保守校准系数。L1 完成后用同一口径重新计算，只有仍达到模型窗口 90% 才进入 L2；L1 不使用 60% 目标，最近 N 轮完整模型调用在 L1 中始终硬保留（默认按模型窗口取 5/7/10）。L2 摘要 prompt 会将候选图片正文临时投影为 MIME、字节大小、可读宽高和 payload SHA-256，不修改活动消息、Chat JSONL 或 checkpoint 中保留的原始图片消息。

没有活动 Run 时，Platform 持有 Chat maintenance lease 后原子更新历史。L1 只给已有行增加 `_compact:{level,id,keep?}`，keep 为 content/reasoning/tool 分类，无 keep 整行跳过，不改原文、不新增 L1 行。L2 使用独立 `compact.checkpoint`，插在保留记录之前，被覆盖记录标记为 L2。历史 L2 默认按已结束 Run 保留尾部，可按预算扩大总结范围。`no_compactable_tools` 与 `no_compactable_history` 分别表示 L1 无候选与 L2 无候选。maintenance lease 持有期间新 query 以 `409 compact_in_progress` 拒绝。

普通 Agent 或 Team 协调器的活动原生根 Run 同时支持 L1/L2，请求投递给 REACT 循环并阻塞等待最终结果。已启动模型或工具批次自然结束后，在下一次模型调用、HITL 等待或 Run 终态边界执行；未完成交互与当前指令受保护。压缩前先关闭已完成输出块，持久化原始消息，再原子提交原行标记及可选 L2 摘要。持久化失败返回 `compact_persist_failed` 并终止继续调用；私有校验消息不进入公共 SSE/WS。

手动 L2 的模型失败、空摘要和输入过大分别返回 `summary_model_failed`、`summary_empty`、`summary_input_too_large`，保持逻辑上下文不变且不创建 checkpoint；新压缩不再生成 deterministic fallback，已有旧 checkpoint 仍兼容回放。压缩不消耗 Agent `maxSteps`、工具轮次或普通 LLM 调用计数，L2 用量单独记录为 `compactionUsage`。自动压缩达到完整请求 90% 后执行 L1、重新估算，仍高于模型窗口 60% 时同一周期立即执行一次 L2；自动 L2 失败时不继续下一次模型调用：摘要模型请求失败或返回空内容时 `run.error` 为 `context_compact_failed`（retryable 跟随原因），message 带原始错误，`diagnostics` 含 compactId/level/detail、模型与窗口、摘要预算及 `cause`（原始结构化错误）；摘要输入超预算、无可压缩历史或压缩后仍超窗口才是 `context_window_uncompactable`，同样附带原因和估算。Provider 在未提交 Turn 返回标准 context-length 错误时自动压缩并只重试一次；再次溢出，或 system、工具 Schema、当前用户输入与不可拆分待处理组本身超窗口时，以 `context_window_uncompactable` 终止。

#### Compact 自动周期可选字段

公共完成响应与 `context.compact.start/complete/failed` 增加可选 `cycleId`、`cycleComplete`。自动 L1→L2 的两层使用不同 compactId、同一 cycleId；L1 后仍高于 60% 需 L2 时 cycleComplete 为 false，最终完成/失败为 true。旧事件无这些字段时按单次操作解释。skipped 不发布完成事件。

L1 不以 60% 停止清理，统一保护最近 N 轮完整模型调用；60% 只决定自动 L1 后是否升级 L2。N 默认按窗口 ≤200k、200k～1M、≥1M 分别为 5、7、10，可用模型 YAML `l1KeepRecentRounds: 5..10` 覆盖；未完成轮额外保护。无候选返回 no_compactable_tools。直接手动 L2 只执行一次摘要；请求禁用工具、思考和重试，篇幅目标 `summaryTargetTokens`（`min(4096, 窗口/10)`）写入提示词，不作为平台输出上限，协议必需的上限按窗口剩余量收紧；长度截断与异常结束的摘要不提交。L2 的 60% 是选材目标，不是成功硬门槛。

新 L1 不写独立记录，原行 `_compact` 不存正文副本。新 L2 使用独立摘要，不包含保留尾部的消息副本；摘要插入和覆盖标记原子提交。旧 version:2 逻辑快照和字符串 `_compact` 继续兼容读取。

相同 `requestId` 重试会加入原请求并取得同一结果；不同请求并发返回 `accepted:false/status:"busy"/detail:"compact_in_progress"/retryable:true`。客户端断开只结束当前 HTTP/WS 等待，已入队任务继续并可从事件回放恢复。Interrupt 会将尚未完成的请求解决为 `failed/run_interrupted`。ACP、PROXY、CHANNEL 和 BTW 活动 Run 返回 `unsupported_active_run`。成功响应包含 `runId`（仅 run scope）、`trigger`、`scope:"history"|"run"`、`level`、Token 估算、`remainingRatio`、`releasedRatio`、`tokensFreed`、工具统计，以及 L2 可选的 `compactionUsage`；失败/跳过通过 `status/detail/retryable` 表达。

`/api/chat/jsonl`、`/api/chat/system-prompt`、chat/archive replay、搜索结果与 `/api/chat/llm-trace` 都在读取前验证各自明确拥有的时间字段。JSONL 的 line `updatedAt`、event `timestamp`、`messages[].ts` 和 awaiting/submit 时间保持严格；trace 中 `sentAt`、`responseStartedAt`、`completedAt` 以及 `interrupt.interruptedAt` 均为 epoch milliseconds，对应的 `sentTime`、`responseStartedTime`、`completedTime`、`interrupt.interruptedTime` 为 RFC3339Nano 可读时间。字符串、秒、浮点、零值或缺少必填平台时间会返回 `422 time_contract_violation`；trace 中外部 request/response/tool payload 保持透明。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=N`附带的 chat 摘要，以及 WebSocket `/api/chats` 响应都会在存在运行中 run 时返回 `activeRun`。run 摘要带本次 Run 启动时的选择：`planningMode`（为 true 时返回）和解析后的 `editingMode:true|false`，方便客户端刷新或重连后还原；query 行顶层的 `editingMode` 用于同进程续接和计划批准后执行。这些摘要可能包含局部 `error`，用于展示单个 chat 的可恢复/可诊断异常而不让列表整体失败。当前 `multiple active runs found for chat` 会返回 `error: { "code": "active_run_conflict", "message": "multiple active runs found for chat", "chatId": "...", "runIds": ["..."] }`，此时该 chat 不包含 `activeRun`。

`/api/agent` 返回顶层 `modelKey`、`reasoningEffort`、可选 `serviceTier`。模型 key 原样反映配置，ACP 详情不访问上游模型列表、不自动回退到其他模型。思考读取 Agent 顶层 `modelConfig.reasoning`：显式 `enabled:false` 返回 `NONE`，否则返回规范化 `effort`，未配置回退 `MEDIUM`；不代表 stageSettings 或单次 query 的覆盖结果。未设置服务等级时省略 `serviceTier`。不返回 `model`、`selected*`、`modelConfig`、`modelOptions`，meta 不重复返回 modelKey/modelKeys/providerKey/protocol。

`tools`、`skills`、`connectors` 统一为 ID 字符串数组，空列表为 `[]`。tools/skills 按已发布运行时顺序返回（含自动挂载），connectors 读取已保存源配置并过滤预置，活动 Run 延后生效时保持最新开关。技能展示名、description、version、revision 由 `/api/skills` 等目录提供；`/api/agent` 不返回 toolBindings，管理 `/api/admin/agent` 及创建、修改、改名响应保留来源、removable、excluded、active。`meta.perAgentSkills` 已删除。

`POST /api/agent/model-config`（HTTP/WS 相同）仅接受 `agentKey` 必填，`modelKey`、`reasoningEffort`、`serviceTier` 至少一项。省略字段保持原值；modelKey 不允许空值；reasoningEffort 为 NONE/LOW/MEDIUM/HIGH/XHIGH/MAX，不接受空值/null；serviceTier 为非空字符串或 null，null 清除等级，STANDARD 也按清除处理，非标准等级仅限 ACP。未知字段（包括旧 key 别名）拒绝。更新会校验最终模型与 ACP 能力；响应为 `{agentKey,modelKey,reasoningEffort,serviceTier?}`。YAML 的 modelConfig.reasoning.enabled/effort 结构保持不变，API 通过 NONE 表达关闭。


`/api/agent` 返回 `greetings`、`introductions` 与 `wonders` 数组。`greetings` 用作新会话主标题，客户端随机选一条，没有有效项时显示“与 <agentName> 对话”；`introductions` 用作输入框自我介绍 placeholder，独立随机选择，没有有效项时使用默认输入提示。`wonders` 用于可直接提交的 query 示例。`/api/agents` 列表摘要不返回这些数组。`/api/agent` 是运行时详情接口，不返回 `definition`、`soulPrompt`、`agentsPrompt`、`source`；编辑器应使用 `/api/admin/agent` 获取这些字段，以及 `status`、`diagnostics`。

### Archive

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/archives` | query: `agentKey`、`limit`、`offset` | archive 摘要列表 |
| GET | `/api/archive` | query: `chatId` | archive 详情 |
| POST | `/api/archives/search` | body: `query`、`agentKey`、`limit` | archive 搜索结果 |
| POST | `/api/archive/delete` | body: `chatId` | 删除 archive 结果 |

Archive 摘要、详情和搜索结果都会返回时间字段：`createdAt` 为 chat 创建时间，`lastRunAt` 为最后一次 run 完成时间，`archivedAt` 为归档时间。`updatedAt` 保留为兼容字段，不应再作为 last run 时间使用。

### Automation

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/automations` | body: `tag` | automation 列表 |
| POST | `/api/automation` | body: `id` 或 `automationId` | automation 详情 |
| POST | `/api/automation/create` | body: `name`、`cron`、`query`，以及 `agentKey`；可选 `description`、`enabled`、`zoneId`、`remainingRuns` | 创建后的 automation 详情 |
| POST | `/api/automation/update` | body: `id` 或 `automationId`，以及可更新字段；`remainingRuns` 省略保持、正整数设置、`null` 清除次数限制 | 更新后的 automation 详情 |
| POST | `/api/automation/delete` | body: `id` 或 `automationId` | 删除结果 |
| POST | `/api/automation/toggle` | body: `id` 或 `automationId`、`enabled` | 启停后的 automation 详情 |
| POST | `/api/automation/trigger` | body: `id`（兼容 `automationId`） | 异步受理结果：`accepted`、`status`、`automationId`、`executionId` |
| POST | `/api/automation/executions` | body: `id` 或 `automationId`、`limit`、`offset` | execution history |
| POST | `/api/automation/execution` | body: `executionId`（兼容 `id`） | 单条 execution 与完整 query/result 内容 |

`query` 对象包含必填 `message`，以及可选 `chatId`、`hidden`、`accessLevel`、`references`、`params`；兼容接收 `role`，只保留供审计，执行时忽略且不校验角色枚举，自动化输入始终由平台记为 automation 来源。`hidden` 省略时按 `true` 执行，只隐藏 Chat 时间线里的 automation query 消息，不隐藏 chat、run 或模型回复，显式 `false` 可显示该 query。省略值在 Automation 详情中继续省略，结构化更新不会把计算后的默认值写回 YAML。

Automation 摘要和详情中的 `nextFireAt` 是下次触发时间的 epoch milliseconds；`lastExecution` 与 execution history 中的 `startedAt`、`completedAt` 同样是 epoch milliseconds。这些 `*At` 字段是排序、计算和客户端本地化的唯一权威时间。对应的 `nextFireTime`、`startedTime`、`completedTime` 均由 Platform 按 `automation.default-zone-id`（无效或未配置时回退进程 `time.Local`）转换为 `YYYY-MM-DD HH:mm:ss`，只用于阅读，不保留毫秒或时区信息。

Automation 的 `description` 和 `zoneId` 均可省略。Execution 的 `zoneId` 是创建 execution 时解析出的有效业务时区快照，解析顺序为 automation `environment.zoneId`、Platform `automation.default-zone-id`、进程 `time.Local`。它不会随 automation 后续修改或删除而变化，也不参与上述 `*Time` 展示转换。

Automation 列表和详情固定返回 `executionHistory:{available,state,message?}`，其中 `state` 为 `initializing|ready|degraded|unavailable`。History 不可读不影响 Automation 配置 API；`/api/automation/executions` 和 `/api/automation/execution` 此时返回 `503`。

Automation 创建、更新的 `query.accessLevel` 与详情中的同名字段支持 `ask_for_approval/auto_approve/full_access`，省略时执行权限为 `ask_for_approval`；更新省略整个 `query` 保留原值，提供 `query` 则整体替换，省略其中的档位恢复默认。非法值或错误类型返回 400。定时和手动触发均使用受理时的配置，不继承已有 Chat 的历史权限，也不经过 `chat_start` 的权限提升审批；目标交互准入及工具硬限制继续生效。

`POST /api/automation/trigger` 是 HTTP-only 的原生手动触发入口。每次请求都会创建不同的 Execution，并与 Cron 执行共用全局 Automation 并发池；暂停状态也可触发，但不会扣减 `remainingRuns`、持久化 Automation 或改变 `nextFireAt`。成功响应保持统一 200 包装，例如 `data:{"accepted":true,"status":"accepted","automationId":"daily-report","executionId":"exec_xxx"}`。该响应只表示已受理，Query 准入、模型执行、HITL 或停止取消等后续结果通过 execution history 和 `automation.execution.*` push 反映。

触发受理时会固定完整 Definition、有效 `zoneId` 和 `automation:<id>` Chat source；后续编辑或删除不影响该 Execution。`startedAt` 是受理时间，`runStartedAt` 是 Query 真正注册时间。History 初始化或不可用不阻止触发。无 ID 或 payload 格式错误返回 400，不存在返回 404，orchestrator 未配置、未启动或正在停止返回 503。该路由不注册为普通 WebSocket 管理 route。

Execution history item 与 `lastExecution` 可包含 `chatId`、`runId`、`finishReason`、`hasResult`、`resultPreview` 和 `runStartedAt`。`resultPreview` 由 Platform 从完整结果生成，最多约 240 字符；列表接口不读取或返回完整 `RESULT_CONTENT_`。`POST /api/automation/execution` 按需返回 `queryContent` 与 `resultContent`，其中结果来自对应 Run 持久化时的 `RunCompletion.AssistantText`，不是事后读取可能已变化的 Chat `lastRunContent`。

Automation 只配置 `agentKey`，TEAM 也按普通 Agent 准入执行。

### Run

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/execute` | body（JSON 或 multipart 的 `request` 字段）: `agentKey`、`message`、`outputSchema`、`input`、`requestId`、`accessLevel`；multipart 其余字段为输入文件 | 等待 Run 结束，返回 `result_submit` 的 `{chatId,status,output|reason}`；契约见 [智能体执行接口](智能体执行接口.md) |
| POST | `/api/query` | query: `detail`；body: `lane`（默认 `main`，可选 `btw`）、`sideId`（旁路分支续问）、`message`、`agentKey`、`chatId`、`runId`、`requestId`、`references`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`planningMode`、`editingMode`、`accessLevel`、`model`、`primary`（见 [主要聊天](#主要聊天)） | 默认 SSE stream；`stream:false` 时返回 JSON |
| GET | `/api/attach` | query: `runId`、根 `agentKey`、`lastSeq` | 按公开 owner 续接 run 的 SSE stream，档位为 Run 受理时冻结的 `detail`，不接受 `detail` 参数 |
| POST | `/api/submit` | body: 根 `agentKey`、`runId`、`awaitingId`，以及 `params`（question/approval 的数组）或 `param`（planning/form 的单个对象），二选一 | HITL submit ack |
| POST | `/api/steer` | body: 根 `agentKey`、`runId`、`message`、`requestId`、`chatId`、`steerId`、`references` | steer ack |
| POST | `/api/interrupt` | body: 根 `agentKey`、`runId`、`message`、`requestId`、`chatId` | interrupt ack |
| POST | `/api/access-level` | body: 根 `agentKey`、`runId`、`accessLevel`、`requestId`、`reason` | 动态更新 native run 的 accessLevel |
| POST | `/api/compact` | body: `requestId`、`chatId`、`trigger:"manual"`、`level:"summary"` | 无活动 Run 时压缩历史；活动 native root Run 时阻塞到安全点压缩结束 |

`POST /api/submit` 成功仍返回 200。已知终态返回 409，`msg` / `data.errorCode` 为 `awaiting_expired`、`awaiting_interrupted` 或 `already_resolved`；`data` 同时包含 `chatId`、`runId`、`awaitingId`、`status`、`detail` 与结构化 `error`。真正不存在或 awaiting 身份不匹配返回 400 `unknown_awaiting`。同一进程内的 submitId 重试保持幂等；Platform 重启后旧等待项返回 awaiting_interrupted，已完成的持久收据可回读但不重建执行者。

`/api/query` 的 `stream` 是 JSON body 字段；省略或传 `true` 时返回 SSE，结束帧为 `data: [DONE]`。传 `false` 时服务端仍执行完整 run、持久化 chat，并在结束后返回普通 JSON。默认只返回最终回答，响应示例见下文。

editingMode 只认顶层 JSON boolean，是 GENERAL/CODER/KBASE/TEAM 的通用 Workspace 编辑开关。显式 true/false 一致生效；省略时使用模板默认值（KBASE false，其他 true），规划 Run 始终只读。它只控制最终 runtimeConfig.workspaceRoot 的 mutation，不增加来源目录授权、不补工具、不改变 external 或当前 Chat 的通用权限，params.editingMode 不生效。JSONL 的原始 `query` 不补入默认值；query 行顶层的 `editingMode` 与 `request.query` 事件保存解析后的请求意图（含 false），用于普通续接和计划批准后的执行；activeRun 返回当前 Run 的实际值，规划 Run 为 false。该字段不写 Agent 配置、不从上一轮继承。

JSONL query 行的 `query` 只保存调用方原样给出的输入，平台不往里添加任何字段：HTTP body 与 WS payload（含省略、显式 false、null、空对象/数组、大整数，以及 `stream`、`detached`、`lane`、`sourceUser` 等传输字段；不承诺空白或键顺序），chat_start 的工具参数，`agent_invoke`/`agent_delegate` 中该任务的原始条目，Automation 定义里实际配置的 query 字段。平台的决定与平台算出的有效值作为 query 行顶层字段并列保存：`requestId`、`agentKey`、`lane`（主通道省略）、`origin`、`accessGrant`、`planRunId`、`accessLevel`、`hidden`（有效值为 true 时写入，含 Automation 缺省隐藏与旁路通道）、`planningMode`、`editingMode`、`model`、`references`；任务行另有 `taskId`、`taskName`（含缺省为成员名称的有效值）、`taskToolId`、`taskAgentKey`。平台缺省值在写入时存下有效值，读取时不按当前规则重算。没有调用方输入的行不写 `query`：Run 中途只更新 system 快照的行只有 `system`；计划批准后执行 Run 的首行带 `planRunId`，其可见文本按该行 `locale` 在回放时生成。`request.query` 实时事件、历史回放与执行续接都由同一投影从 `query` 的内容字段和顶层决定生成。读取不区分新旧布局：平台决定只从顶层读取，缺失即视为未设置；回放与客户端恢复需要的 `requestId`、`agentKey`、`references`、`hidden`、`accessLevel`、`model` 在顶层缺失时从 `query` 读取，来源在缺失时由旧的 `role`/`runOrigin` 推出。旧记录因此照常显示，历史文件不改写；旧 Run 不再恢复执行，在旧 Chat 继续对话会写新的 query 行。复制 Chat 时整行（含 query）一起重新绑定 Chat/Run。HTTP/WS 请求与 Automation API 的字段名必须与协议拼写完全一致，大小写变体（如 `Message`）按未知字段拒绝。

`origin` 记录输入是谁发起的，由平台判定，请求不能携带：`{type:"user",userId?}`（HTTP/WS；userId 取认证主体，受信 gateway 下取远端用户）、`{type:"agent",agentKey,chatId,runId,toolId}`（chat_start 与 chat_steer）、`{type:"automation",automationId}`。计划批准后的执行 Run 沿用规划 Run 的 origin。memory 只采集 `user` 来源的输入；`automation` 来源不进入会话搜索与导出，模型输入标注为自动化请求。HTTP/WS query 和 Automation YAML/管理 API query 中的 `role` 均接收并保留供审计，执行时忽略，不进入 `request.query` 事件，也不决定模型角色。`agent.yml` 的 `role` 仍为 Agent 描述字段，与请求角色无关。chat_start 的权限审计（父档位、版本、来源、审批摘要）写在顶层 `accessGrant`。

`references` 统一表示本条消息指定的资源。skill/kbase/chat 客户端只传 type 和 id，服务端补齐可信元数据；Chat 正文不随请求拼接，智能体通过 task-control 的 chat_query read/search 按需读取。引用按类型与规范化 ID 去重，整个 Run 累计上限为技能 32、知识库 8、对话 32；steer 超限整条拒绝且不留下授权。技能目录只读授权和脚本入口凭据、知识库可读性、Chat 身份与授权边界见 [引用协议](引用协议.md)。选择列表只进入用户消息，不进入 system prompt 或 fingerprint；固定绑定库 boundLibraryId 保留。

`GET /api/kbases?agentKey=...` 与主 WS `/api/kbases`（payload `{agentKey}`）返回 `{agentKey, capability:{available,reason?}, boundLibraryId?, libraries:[{id,name,description?,collections:[{name,description?}],readable,indexing,degraded,unavailableReason?}]}`。目标需挂载 builtin.kbase-control。库选择通过 `references:[{type:"kbase",id:"library-id"}]` 提交；不开启入口不影响既有检索工具。

HTTP/WS 的 query/steer 请求拒绝未知字段，包括值为 null 或空数组的字段；HTTP query 返回 unknown_field，其他入口同样给出未知字段说明。requiredSkillKeys 继续按既有删除字段协议拒绝。chat_start、agent_invoke、agent_delegate 与 Automation query 均使用 references；子任务只接受 skill/kbase，chat 拒绝。TEAM 总控支持三类定位引用；ACP/PROXY/CHANNEL 的 query 与 steer 均拒绝 skill/kbase/chat，只接受文件、选区与站点。

所有调用与控制只使用根 `agentKey`。已有 Chat 归属固定；成员事件上的 key 不授予根控制权限。TEAM 的成员若不存在、为 TEAM/ACP、不支持子任务或携带 agent_invoke，整个请求返回 503。活动 Run 使用冻结版本，同进程延迟批准重新校验当前版本；重启取消未完成 Run。query/steer 中的未声明字段拒绝；其他入口沿用各自 DTO 校验，不检测旧配置。


HTTP `POST /api/query` 的 `lane:"btw"` 用于“顺便问”：`chatId` 必须指向已有 active chat；不传 `sideId` 时从当前主 JSONL 创建隐藏快照并在响应头 `X-Side-Id` 与首个 `request.query.sideId` 返回分支 ID（`side_` 前缀），传 `sideId` 时继续该分支。BTW 固定继承父 chat 的 agent/team，按用户输入执行且关闭 planning mode。主 chat 的 active run、pending awaiting、摘要、未读、搜索和 JSONL 都不会被 BTW 更新。

BTW 与普通 query 使用同一 Agent/ReAct、模型协议、SSE assembler、attach/interrupt 和 StepWriter；`request.query` 额外包含 `lane`（`btw` 或 `explain`）、`sideId` 与 `hidden:true`，不新增 event type，也不发送 `chat.start` / `chat.updated`。分支存放在 `<chatId>/.btw/<sideId>.jsonl` 或 `<chatId>/.explain/<sideId>.jsonl`，文件名即分支身份，JSONL 行只记 `lane`。同一个 `sideId` 只允许一个 active run，父 chat 与不同 BTW 分支可以并行。

Desktop 使用 `main`、`btw`、`explain` 三条独立普通 WebSocket v2 lane，连接 source 分别为 `desktop-main`、`desktop-btw`、`desktop-explain`。三者发起 Run 统一发送 route `/api/query`：Platform 根据已认证连接身份将 main 送入普通 query，将 btw/explain 送入隐藏只读分支，payload 不能覆盖 lane。旁聊与划词解释都使用 BTW payload（含可选 `sideId`），都支持在同一分支续问，同一连接可先后创建、续问和 attach 不同分支 Run，但同一时刻只能观察一个 Run stream。HTTP 统一使用 `POST /api/query`，body 的 `lane` 缺省或 `main` 执行主聊天、`btw` 执行隐藏分支；`explain` 返回 403 `explain_ws_required`，其他值返回 400 `invalid_lane`，均不创建 Chat。网页仍使用 SSE，也支持 `stream:false` JSON。旧 HTTP/WS `/api/btw` 入口已删除，Platform、Desktop 与 WebClient 配套发布。attach、detach、steer、interrupt、access-level 在既有 agent/team owner 校验之外，执行下述 Run 控制归属校验。

以下控制归属限制不适用于 HITL Submit。Run 创建时由服务端冻结控制归属 `transport`、`lane`、认证 subject 与 WS device boundary，只保存在 Platform 进程内存中，不写入 `.state`、`chats.db` 或 Chat JSONL；请求不能通过 payload 修改归属。HTTP 创建的 Run 只接受 HTTP attach/steer/interrupt/access-level；WS 创建的 Run 只接受同身份、同设备边界、同 lane 的 WS 控制。HTTP 控制不另要求 body lane，HTTP BTW 仍通过自身 Run ID 和 agent/team owner 定位；HTTP 没有 detach endpoint，直接关闭 SSE。跨 transport 返回 403 `run_transport_mismatch`，跨 lane 返回 403 `run_lane_mismatch`，身份不同返回 403 `run_control_identity_mismatch`。缺少归属记录的旧 Run 返回 409 `run_control_identity_unavailable`，不自动认领或默认为 main。

归属不绑定 sessionId 或 surfaceId，同 lane 重连/关窗重开可按 lastSeq attach。归属在进程存活期间保留，Run 结束后仍可查到；planning 后续执行 Run 继承源 Run 归属，跨进程重启取消旧等待项；新 Query 建立新的控制归属。内部 Run 调度与控制走内部调用路径，不由客户端声明内部身份。

Automation 等内部 Run 的全空控制归属允许受信任 Desktop Main 通过独立授权执行上述五种 WS 控制，控制和观察不改写 Run 归属；该授权在 token 到期后立即失效，不等待心跳关闭连接。认证、设备和来源限制见[鉴权与安全边界](鉴权与安全边界.md)。

每条 WS 连接最多一个 Run stream（终端类订阅独立）；query 在准备 Chat/启动 Run 前原子预留名额，attach 同样申请名额。已有 stream 时返回 409 `active_stream_exists`（同一 Run 重复观察仍为 `duplicate_observe`）。冲突错误的结构化 diagnostics 返回所属 lane、连接诊断 ID、旧流的 Run/请求/stream 身份、占用起始时间和预留或已绑定状态；尚未生成 Run 时仍可按请求 ID 定位。切换 Run 必须先 detach 或等待旧 stream 终态；detach 不终止后台 Run。main/btw/explain 三条连接可以同时各有一个 stream。

后台启动使用 main lane 的 `/api/query` 并在 payload 传 `detached:true`：Platform 不预留、不创建 Run stream，Run 注册并开始执行后返回普通 `response`，`data` 为 `{accepted:true, status:"running", runId, chatId, agentKey, startedAt}`，启动失败返回与普通 query 相同的 error frame。它不受 `active_stream_exists` 约束，可以与本连接已有的 stream 以及其他 detached Run 并行；Run 控制归属仍绑定发起连接的 lane，后续状态由全局 `run.started` / `run.finished` / `chat.updated` Push 收敛，需要逐条输出时再 `/api/attach`。btw/explain lane 传 `detached:true` 返回 400 `invalid_request`，HTTP `POST /api/query` 传 `detached:true` 返回 400 `detached_ws_required`，均不创建 Chat。

发布配套要求：旧网页的“HTTP query + WS attach/interrupt”混用会被拒绝，必须改为 HTTP 全链路。旧 Desktop 在同一连接并发订阅多个 Run 会被拒绝，必须先 detach。这里只完成 Platform 协议与集成测试，真实三端联调尚待客户端配套验收。


BTW 发给 provider 的 system、tools、tool choice 和 cache key 与普通 chat 保持一致；只读说明放在本次 user message。平台内置查询工具可执行，写文件、Bash、memory mutation、plan mutation、agent invoke、artifact/image、desktop、frontend/action 等工具返回 `btw_tool_disabled` 且不会进入 HITL。MCP / agent-local / external 工具只有 `meta.readOnly:true` 时可执行；MCP `annotations.readOnlyHint:true` 会映射为该字段。proxy/channel/ACP coder 因工具执行不经过本地门禁，返回 `btw_backend_unsupported`。

`stream:false` 的 BTW 响应为：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "sideId": "side_xxx",
    "parentChatId": "chat_xxx",
    "runId": "run_xxx",
    "content": "最终回答"
  }
}
```

`references` 中的文件引用使用 `path` 表示当前目标智能体可直接访问的执行路径。当前 Chat 文件在 Host 为 Chat 绝对路径，在 Container Hub 为 `/chat/...`；Workspace 文件在 Host 为 Workspace 绝对路径，在 Container Hub 为 `/workspace/...`。跨 Agent 文件会先物化到 Chat；PROXY/CHANNEL/ACP 等远程 adapter 使用带 ticket 的 resource URL，由接收端在 30 秒、50 MiB 安全上限内下载到自己的 Chat，再生成本地执行路径。历史 path-only `/workspace` Chat 引用不再作为新 run 输入接受。

划词引用在 query/steer 中统一使用 `{ "id": "原始稳定ID", "type": "selection", "text": "引用原文", "annotation": "可选批注" }`。`text` 必须非空，批注可省略；只读取顶层 `text`，不支持 `meta.text`。规范化后仅保留原始 ID、type、text、annotation 与可选 annotationIndex，不再生成文件路径或保留文件元数据。首次 query 必须有非空 message；后续 query 和 steer 可只发送有效文件或选区引用。

仅模型提示词中的划词 ID 按本条消息映射为 `r1/r2/...`，避开其他类型引用的 ID，并同步替换用户正文中的 `#{原始ID}`；请求、事件与持久化引用保留原始 ID。普通与 advanced-user-prompt 格式共用精简条目：

```yaml
- id: r1
  type: selection
  text: 引用原文
  annotation: 可选批注
```

没有批注时不输出 annotation，多行原文/批注使用块文本。原文是引用材料，annotation 是用户针对该材料的指令，与顶层 message 同属用户指令。

Chat 与 Site 沿用同一 `references` 数组，但不按文件路径处理：

- Chat：客户端提交 `{ "type": "chat", "id": "chatId" }`，服务端补齐名称、Agent 和更新时间。只传定位信息，不读取历史正文或写 meta.context；拒绝自引用、失效 Chat 和跨 principal，匿名只能显式引用无归属 Chat。chat_query read/search 按本 Run 授权读取，管理操作权限不变。
- Site：`{ "type": "site", "id": "entryKey", "name": "站点名", "url": "https://...", "meta": { "kind": "website|webapp", "updatedAt": 0 } }`。服务端只注入经过字段白名单校验的名称、类型、入口标识和 HTTP(S) URL，不抓取页面内容。

文件引用的 `url` 只用于平台资源下载、ticket 与 gateway 数据面，不进入模型 prompt；只有 Site 引用的可信指针 URL 会作为引用元数据展示给模型。

Native Query 的 SSE、`stream:false` 和进程内阻塞调用共用同一执行核心，因此子 Agent/Team、usage 聚合和阶段处理不因返回方式不同而分叉。`content` 与持久化完成记录一致，`fullText` 保留内部模型轮次丢弃语义。Proxy 仍使用现有协议适配。

普通非流式 query 的默认响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "content": "最终回答"
  }
}
```

`includeUsage:true` 会在 `data` 中追加本轮用量；`includeFullText:true` 会追加面向阅读的全过程文本：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "content": "最终回答",
    "fullText": "Tool: datetime\n{}\n\nTool result: datetime\n...\n\nAnswer\n最终回答",
    "usage": {
      "promptTokens": 10,
      "completionTokens": 5,
      "totalTokens": 15
    }
  }
}
```

`steam` 不是支持字段；如果误传 `steam:false`，不会触发非流式响应。

实时 SSE / WS stream 中所有工具统一使用 `tool.start → tool.args × N → tool.end → tool.snapshot → tool.output × 0..N → tool.result` 生命周期，不再存在 `action.*` 事件。`tool.end` / `tool.snapshot` 只表示调用参数已经完整；只有唯一的 `tool.result` 收口执行结果。`tool.output` 是可选过程输出，当前只有 Native Host `bash` 发送，Container Hub `bash` 与 `bash_sandbox` 仍只返回最终结果；工具输入 Schema、模型参数和配置均未增加开关。

原生 `wait` 通过 `tool.wait` 和 `tool.wait.update` 发送等待期限、说明与条件进度，仍由唯一 `tool.result` 收口。用户输入（`/api/steer`）提前唤醒后在同一 Run 继续，没有专用的跳过接口；空白 steer 表示“继续”：照常入队、发布 `message` 为空的 `request.steer` 并持久化，模型输入补充继续指令。参数、断线快照、预算和重启取消规则见 [原生等待工具](原生等待工具.md)。

`tool.output` 的公开结构如下；`taskId` 只在 Team / Plan task 范围内携带：

```json
{
  "type": "tool.output",
  "seq": 123,
  "timestamp": 1788500000000,
  "runId": "run_123",
  "taskId": "task_123",
  "toolId": "call_123",
  "toolName": "bash",
  "stream": "stdout",
  "delta": "请扫描二维码：\n████████\n",
  "chunkIndex": 0
}
```

`stream` 只允许 `stdout` / `stderr`，`delta` 为非空有效 UTF-8；`chunkIndex` 从 `0` 开始并在同一 `toolId` 内跨两个 stream 单调递增。`seq` 仍由统一 Assembler 分配，事件进入 SSE、WebSocket 与 active Run 的 attach backlog。最终 `tool.result` 可以汇总、重组或概括过程输出，不要求机械拼接；Bash 进程非零退出时，`tool.result` 保留真实 `exitCode` 并作为可恢复的工具失败展示，不会自动升级为终止性的 `run.error`，成功但写入 stderr 的命令仍保持 `exitCode: 0`。

`tool.output` 不进入 Chat JSONL、非流式 `fullText` 或导出；冷回放仍只有 `tool.snapshot + tool.result`。大型 Bash 最终结果继续使用 64 KiB spill / preview / `resultRef`。持久化到 `chatId.jsonl` 时，同一 assistant turn 的多个工具调用会合并为一条 assistant message 的 `tool_calls[]`；如果该组存在 awaiting，确认前不会执行任何 sibling tool，确认后的所有结果写入同 `seq` 的 `_type:"react-tool"` continuation。

`budget.tool.maxCalls` 是平台硬限制。计数包含被拒绝的尝试，因此上限为 `60` 时，第 `61` 次调用不会进入 ToolRouter，而是先发送一次失败 `tool.result`。如果同一 assistant turn 在该调用之后还有 sibling，平台会先为所有确定尚未执行的 sibling 写入内部 `executed:false` 结果，保证持久化历史中的每个 `tool_call_id` 都有对应结果；这些内部结果不进入 SSE、WebSocket、attach、普通 Chat 回放或 full-text。串行和并行批次都完成上述收尾后，才发送唯一的 `run.error` 并结束，不再请求模型补答；已在预算内启动的并行 sibling 仍正常收尾。终止错误沿用公共错误结构：

```json
{
  "type": "run.error",
  "runId": "run_xxx",
  "error": {
    "code": "tool_calls_exceeded",
    "category": "tool",
    "scope": "tool",
    "retryable": false,
    "userSafeMessageKey": "tool_calls_exceeded",
    "diagnostics": {
      "toolCalls": 61,
      "limitValue": 60,
      "limitName": "budget.tool.maxCalls",
      "toolName": "bash"
    }
  }
}
```

该 `run.error` 是完成态事实：SSE、WebSocket、`/api/attach` 和 `chat_get_status` 返回同一错误，run summary 持久化为 `finishReason:"error"`，运行状态为 `FAILED`。首个终态获胜；平台确定错误后到达的 `/api/interrupt` 返回 `accepted:false, status:"unmatched"`，不能把失败覆盖为 cancel。`stream:false` 仍完成相同持久化，但 HTTP 返回错误 envelope。

预算错误闭合后的历史可以安全用于同一 Chat 的后续新 run。部署修复前已经缺少 tool result、且执行状态无法证明的历史不会自动修复，仍按 `chat_history_incomplete` 返回 `409`。

`reasoning.start` 与 `reasoning.snapshot` 的 `reasoningLabel` 是按查看者语言解析的显示文案。`en`、`zh-CN` 各有 30 条，文案及固定对应顺序以 `internal/i18n/reasoning.go` 为事实源；中文文案不含「中」和「正在」。对 `TrimSpace(reasoningId)` 的 UTF-8 字节做 32 位 FNV-1a 哈希，再 `% 30` 选择同一组双语文案，语言不参与哈希；空 ID 使用第一组 `Thinking / 思考`。HTTP 使用请求语言，WebSocket 使用连接当前语言；SSE、attach、Chat/Archive 回放与 Snapshot 导出复用该规则。协议只保留现有 `reasoningLabel`，不增加 key 或翻译表。标签只作等待文案，推理正文保持原样。

TEAM 总控的普通工具与 reasoning 沿普通 Agent 事件链路；内部 `agent_delegate` 不展示工具卡。成员输出继续使用现有 `task.*` 与 task-scoped `content.*`：成员事件带 `taskId`，携带成员 `agentKey`、`presentation:"task"`，并在 `actor` 中标记 `type:"agent"`。一项和多项委派使用相同终止规则，成员正文不会成为根回答；最终非流式 `content`、run summary 与 `AssistantText` 只取总控生成的唯一 Team 最终正文。

#### 细节等级与 run.activity

`/api/query` 与 `/api/chat` 接受细节等级 `detail`：HTTP 用 URL 参数（`?detail=full`），WS 用请求帧顶层字段（与 `type`、`id` 并列，见[帧类型](#帧类型)），不放进 query 请求体，因此不进入 JSONL 原始 query。显式取值为 `summary` 或 `full`（旧 `none` 不再支持），其他值返回 400 `invalid_detail`；WS 其他路由携带 `detail` 同样拒绝。

档位属于 Run：`/api/query` 受理时冻结（请求显式值 > 实际准入 Agent 的 `interactionConfig.defaultDetail` > summary，Agent 默认值来自 Agent 显式配置 > agent-settings.yml 对应 mode 配置），整个 Run 期间所有观察者收到同一档事件。没有调用方连接的 Run（`chat_start`、Automation、`/api/execute`）取 Agent 默认值；批准计划后启动的执行 Run 沿用规划 Run 的档位；TEAM 子任务与 steer 不改变档位。档位只在进程内保存，不落盘（Platform 重启本就取消未完成 Run）。

- `/api/attach` 不接受 `detail`：HTTP 带该参数（含空值）返回 400 `detail_not_allowed`，WS attach 帧带 `detail` 返回协议错误；续接一律按 Run 冻结的档位。
- `/api/chat`：Chat 有进行中的 Run 时，整个响应（含之前已结束 Run 的历史事件）按该 Run 的档位过滤，请求的 `detail` 忽略；没有进行中的 Run 时按请求档位读取，省略时按 Chat owner Agent 默认值。`activeRun.detail` 返回 Run 的档位。

`/api/chat` 返回 `display: {defaultDetail,effectiveDetail}`，前者为 Agent 有效默认值，后者为实际下发档位。流式 `run.start` 在下发时附加相同 display（不修改共享事件或落盘）；HTTP SSE 另提供 `X-Agent-Default-Detail` 和 `X-Event-Detail` 响应头。Agent 不存在时兜底 summary，不阻止历史读取。页面按 `effectiveDetail`（实际下发档位）展示。Desktop broker 不改写 `detail`：窗口请求省略时跟随 Agent，只有需要读取工具事件的 Desktop 主进程 Run（图片工作台）在自己的 query 上显式请求 full。

落盘始终完整，细节等级只决定下发哪些事件：

| 事件 | summary | full |
|---|---|---|
| `request.query/steer/submit`、`chat.start` | ✓ | ✓ |
| `run.start/complete/cancel/error` | ✓ | ✓ |
| `task.start/complete/cancel/error` | ✓ | ✓ |
| `content.*`（含 `content.view`、`content.snapshot`） | ✓ | ✓ |
| `planning.*`、`plan.create/update`、`artifact.publish`、`source.publish` | ✓ | ✓ |
| `awaiting.ask/answer` | ✓ | ✓ |
| 公示与清除 `run.activity`（`kind` 为 `announce` 或 `idle`） | ✓ | ✓ |
| 其他进度 `run.activity` | — | ✓ |
| 重试 `run.activity`（带 `retry`） | — | ✓ |
| `reasoning.*`、`tool.*`、`usage.snapshot`、`context.compact.*`、`debug.llmChat`、`memory.*` 及其他类型 | — | ✓ |

summary（精简）使用白名单，新增事件类型默认不进入；进度活动只保留模型公示和输出开始时的清除，自动生成的思考、工具、等待、子任务委派及整理上下文等一句话进度均不下发。提问和审批本身仍通过 `awaiting.ask/answer` 正常展示。full（详细）是全集；客户端的详细视图忽略进度活动，因为细节事件本身已经展示了进度。summary 不下发 `usage.snapshot`，用量读取 `run.complete/cancel/error` 的 `usage.run` 与 `usage.chat`：其中 `toolCallCount`、`llmChatCompletionCount` 固定输出（含 0），另有 `promptTokens`、`completionTokens`、`completionTokensDetails.reasoningTokens`、`estimatedCost` 等；full 仍实时下发 `usage.snapshot`。`/api/chat` 只按等级过滤 `events`，`runs[].usage` 与顶层 `usage` 不受影响。Proxy 上游查询固定请求 `full`，由本地按查看者等级过滤；gateway 与 Desktop 透传客户端的 `detail`，省略时跟随 Agent。

`run.activity` 用一句话说明 Run（或带 `taskId` 的子任务）现在在做什么。字段只有 `runId`、`chatId`、可选 `taskId`、`message`，进度活动另带 `kind`（`thinking`、`reasoning`、`tool_run`、`announce`、`subtask`、`awaiting`、`compacting`、`idle`），重试时另带 `retry`；`message` 按查看者语言渲染（HTTP 用请求语言、WS 用连接当前语言）。同一 `(runId, taskId)` 新的一条替换旧的一条；`message` 为空（`kind:"idle"`）表示该 scope 当前没有活动（用户正在看正文或计划），客户端隐藏活动行；`task.complete/cancel/error` 清除该子任务的活动，Run 终止事件清除全部。

- 进度活动（full 接收全部，summary 仅保留公示与清除）：模型请求开始时为“正在思考”；`reasoning.start` 时为“正在”加该段的 `reasoningLabel`（英文为 `reasoningLabel…`），不读取推理正文；`content.start` 与 `planning.start` 发出 `message` 为空的活动以清空活动行，正文本身就是进度；每个模型回合在参数完整后（`tool.end` 时、审批之前）至多发一条工具活动，覆盖审批等待和整批执行，`tool.start` 与执行开始不再单独发：本回合有公示工具（`action_announce`）时为公示，否则为第一个工具的“工具 label：说明”，说明取工具定义 `activityArgs` 中第一个非空参数，没有则为“正在调用「工具 label」”；`task.start` 在父级显示“正在委派子任务「taskName」”；`awaiting.ask` 按 mode 显示“等待你审批 / 回答 / 填写表单 / 确认计划”，`awaiting.answer` 后恢复等待前的那条活动；`context.compact.start` 为“正在整理上下文”。相同文案仍下发，由客户端决定是否刷新展示；`clientVisible:false` 工具不产生活动，公示除外：公示是模型写给用户的话，不是工具本身的展示。
- 公示（`kind:"announce"`）：`message` 是模型用用户语言写的一句话（下一步要做什么），原样展示、不翻译。summary 下持续显示到下一条公示、开始输出正文或计划（`idle`）或 Run 终止；不再设置保持秒数，旧 `announce-hold-seconds` 配置拒绝加载。full 的其他进度正常生成和下发，不被公示压住。
- attach 时服务端分别保存每个 scope 的最后一条公示或清除，以及当前完整进度；先补发公示状态，再补发不同的当前活动。summary 过滤后停在公示或清除，full 停在当前活动。清除状态在后续自动进度出现后仍保留，重连不恢复已清除的旧公示。
- 进度活动不占公共序号：`seq` 为 0，不进入 attach 回放日志、不落盘；attach 时服务端为每个仍在活动的 scope 补发当前那一条（同样 `seq` 为 0）。因此 full 的公共 `seq` 保持连续（忽略 `seq` 为 0 的事件）；summary 因过滤会出现跳号，客户端不能把跳号当作丢事件。
- 重试活动（仅 full）：模型请求可恢复重试时发出，`retry` 含 `runSeq`（模型轮次）、`attempt`（含首次请求）、`maxAttempts`、`reason`、`error`（公共结构化错误）、`timeoutSeconds`、`elapsedMs`，普通重试另带 `delayMs` 与 `retryAt`（epoch ms），客户端显示序号为 `attempt-1` / `maxAttempts-1`；等待前先发出，等待支持取消。summary 看不到重试，重试耗尽后各档都收到终止 `run.error`。

`run.activity` 不表示 run 失败；`run.error` 仍是终止事件，发出后不应再出现 content / reasoning / tool 等业务事件，后面只允许传输层 `[DONE]`。

native LLM loop 以平台内部的 model turn commit 作为唯一接受边界。provider 合法终止、流式块完整收尾、tool call 完成 materialize 且通过平台接纳检查后才 commit；该控制信号不进入 SSE / WebSocket。`usage`、`contextWindow`、provider `finish_reason` 和 tool result 都不是完成标记，其中 `usage` / `contextWindow` 是可选元数据，缺失不会阻止正常 turn 提交。

commit 前遇到 EOF、非法流帧、连接中断或可重试的 provider stream 错误，且尚未开始工具执行时，平台丢弃整个 attempt 并按模型 retry budget 重试。客户端先收到本次未提交输出段的失败结束事件：

```json
{
  "type": "reasoning.end",
  "reasoningId": "reasoning_1",
  "status": "failed",
  "error": {
    "code": "provider_stream_failed",
    "message": "模型响应流中断",
    "category": "model",
    "scope": "model",
    "status": 502,
    "retryable": true,
    "userSafeMessageKey": "provider_stream_failed"
  }
}
```

`reasoning.end`、`content.end`、`tool.end` 统一约定：正常结束保持原结构，不发送 `status` 或 `error`；异常增加 `status:"failed"` 与同源公共错误对象。失败段停止计时并保留内容，重试使用新 ID。一个未提交 attempt 内已经正常关闭的段也可能收到失败结束修正，因为段结束不等于 model turn commit；重复失败通知幂等，后到 snapshot/delta 不得覆盖失败状态。已提交轮次和已执行工具不受影响，`tool.end` 只表示参数流结束，不替代执行结果 `tool.result`。

失败结束事件另外携带原始 `startedAt`（epoch ms）、`runId/taskId`、节点展示元数据以及 `text` 或 `arguments`，作为独立 JSONL event 展示记录，实时与历史回放一致；不产生 assistant/tool messages，不进入模型上下文或成功回答摘要。重试进度只通过带 `retry` 的 `run.activity` 显示，不控制节点删除；不重试的丢弃只发失败结束事件。重试耗尽时在输出段结束之后发送 `run.error`。

model turn 与后续 tool batch 是两个独立事务边界：turn commit 后完整 tool call 保留，工具执行失败写正常失败 tool result。工具已经开始执行或可能产生副作用时，不通过回滚 turn 自动重试。异常诊断复用常开的 `llm_model_attempt_error`，不改变 Trace 文件结构。

旧会话历史如果存在无法安全判定工具是否执行过的末尾调用，HTTP 与 WebSocket `/api/chat` 以及后续 query 都返回 `409 chat_history_incomplete`，不会把有歧义的历史发给 provider。仅当 run 以 cancel 结束、末尾调用保留未关闭 awaiting、没有 submit/answer/result 冲突，且每个缺失调用都能映射到该 awaiting 时，读取逻辑视图才会在内存中补出 `run_interrupted` answer/result；原始 JSONL 与数据库不回写。已经开始执行而结果未知的调用不会标记为 `executed:false`。

可运行的 HTTP JSON 模式 curl：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false}'
```

带用量：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false,"includeUsage":true}'
```

带全过程：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/query \
  -H "Content-Type: application/json" \
  -d '{"message":"用一句话介绍 agent-platform","agentKey":"zenmi","stream":false,"includeFullText":true}'
```

`params` 原则上是业务透传对象，平台不写入，也不通过它授予工具、运行环境或其他权限。当前只有一个收紧型运行时例外：当 `agentKey=zenmi` 且 `params.desktop.source=copilot`、`params.desktop.action=image_studio` 时，平台把该次图片工坊任务的 `MaxToolCalls` 与 `MaxToolRounds` 都限制为 `1`，避免图片工具失败后由模型再次调用；其他 Zenmi 对话继续使用智能体原有预算。该标记只会减少本次 run 的能力，不能扩大调用方权限。

`hidden` 是可选的 `request.query` 时间线展示标记；省略时普通 query 不隐藏，Automation 调度会按自身默认值传入 `true`。

输入来源只由平台判定的 `origin` 表达（见上文），不决定 chat 摘要的 `source`；外部请求不能通过任何字段（包括 `role`）伪造 automation 来源。旧记录中 `role:"automation"/"system"` 的 query 继续不参与搜索和导出。普通 HTTP `/api/query` 传入 `sourceUser` 也不会改变 source；该字段只在受信 channel/gateway 上下文中作为远端用户提示使用。

Markdown 与 Snapshot 导出统一由 `Summary + LoadChat` 投影一次内部 `ConversationSnapshotV1`。它包含已绑定的可见根 query、reasoning/content、工具、来源、计划、子任务、运行终态，以及已发布产物和可见图片音视频的候选声明；不包含系统提示、任意原始 payload 或附件文件字节。构建过程只执行一次 JSON 序列化，同一份字节在导出出口按 `configs/runtime.yml` 的 `conversation-export.max-snapshot-mib`（默认 50，正整数，重启生效）校验并用于 `format=snapshot` 响应；Snapshot 响应携带 `X-Conversation-Export-Max-Bytes` 字节额度头，CORS 暴露该头。HTTP/工具每次导出只构建一次，普通会话读取只消费纯投影结构，不编码 JSON、不读取资源文件，也不应用导出额度。HTTP 导出的 manifest 读取失败时保留正文并记录遗漏；工具导出沿用严格失败语义。当前 Platform 对每个 turn 最多保留 2000 个节点。Markdown 仅消费同一 Snapshot 结构体，写出已完成轮次的用户问题和最后一个 assistant 正文回答。

Snapshot 资源发现独立于纯事件投影：只处理当前 Chat 已发布产物、query/人类 steer 引用、成功 `image_generate` 输出和实际 Markdown 媒体；HTML 只解析 img/audio/video/source 的直接 src/srcset/poster，不递归扫描目录、Workspace、CSS 或 JS。普通媒体候选返回 stat 大小，未知 hash 省略；Desktop 冻结成功后补齐最终 size/hash。已发布附件始终保留 manifest 声明，不因文件暂时缺失或变更而在快照收集阶段删除；HTML 完整性校验或依赖发现失败时保留主附件候选，实际读取继续按 manifest 校验。节点可选 `attachmentIds`、HTML 描述符可选 `mediaRefs`，根级可选 `omittedResources` 保存逐项失败原因；规范 sourceRef 的 SHA-256 前 12 字节作为稳定 ID，成功和省略不重叠。同次导出复用一份 manifest 快照；既有已发布清单仍适用于非媒体下载，实际资源读取按当次请求重新执行 manifest/hash 校验。

`POST /api/chat/export/resources/read` 只服务 Desktop app/device scope 且带本地主体的 token，正文为 `{chatId, sourceRef}`。sourceRef 必须是当前 Chat 规范相对引用；普通文件限图片/音频/视频，路径与 MIME 检查复用 chatresource/documentmeta。端点只返回原字节，不扩展通用资源读取权限。已登记资源必须通过产物校验，失败不降级；未登记的当前 Chat 媒体允许作为 HTML 相邻依赖读取。文件读取归 chatresource，快照层只发现引用和装配描述。错误映射：400 invalid、401/403 forbidden、404 not_found、409 changed、415 unsupported；响应不泄漏本地绝对路径。

`GET /api/chat/export` 的空 `format` 与 `format=markdown` 返回 `text/markdown`；`format=snapshot` 返回 `application/json` 和 `<title>.snapshot.json` 文件名。响应的 `Content-Disposition` 使用标准媒体类型参数序列化：ASCII 文件名使用 `filename`，含非 ASCII 字符的文件名使用 RFC 编码的 `filename*`，响应头不会包含裸 Unicode。客户端必须按标准媒体类型参数解析文件名。Platform 不提供 `format=html`、模板加载、资源域名 Header 或创建、列表、撤销分享 API，也不接收 Tunnel site token。WebClient 拥有 HTML 模板和运行时，Desktop Worker 负责本地 HTML 生成，Tunnel 协议、RFC3339 分享元数据和链接生命周期由 Desktop/Tunnel 边界负责。

`model` 可做本次 run 的模型覆盖：

```json
{
  "agentKey": "coder",
  "message": "实现这个改动",
  "accessLevel": "auto_approve",
  "model": {
    "key": "qwen3-coder",
    "modelId": "qwen3-coder",
    "reasoningEffort": "HIGH"
  }
}
```

PROXY/CHANNEL/ACP CODER 的 query 不接受用户 `params.cwd`（出现即返回 400，包括空值/null）。仅受信任本机 ACP bridge 的上游 query 由 Platform 注入 canonical `runtimeConfig.workspaceRoot` 为 `params.cwd`；此字段不加入原始公共 query，普通远端 PROXY/CHANNEL 不注入。路由配置与边界见 [ACP 工作目录契约](智能体配置说明.md#本机-acp-工作目录契约)。

对于 native agent，`model.key` 必须存在于 model registry；`model.modelId` 由后端转发给 ACP CODER 上游时补齐，优先来自 model registry 的 `modelId`，为空时回退到 key；`model.reasoningEffort` 统一接受 `NONE`、`LOW`、`MEDIUM`、`HIGH`、`XHIGH`、`MAX`，其中 `NONE` 用于关闭本次 run 的 reasoning。输入兼容别名 `EXTRA_HIGH` 会归一为 `XHIGH`，不会通过 API 返回或持久化。PROXY agent 会把 `model` 对象原样透传给上游，platform 不做本地 model registry 校验，也不写入本地 session/stage settings。该配置只影响当前 run，不写回 agent 配置。

`accessLevel` 在 `/api/query` 中作为 run 初始值；运行中可通过 `/api/access-level` 调整：

```json
{
  "agentKey": "default_agent",
  "runId": "run-id",
  "accessLevel": "auto_approve",
  "reason": "user toggled permission"
}
```

响应包含 `accepted`、`status`、`runId`、`previousAccessLevel`、`accessLevel`、`version`、`detail`。更新只影响后续 host bash 与 file tools 的 access-policy 判断；已经开始执行的工具不会被中断。若 run 正在等待 access-policy approval，权限提升后会重新评估当前等待项，满足新权限时自动清理 awaiting 并继续执行。PROXY / ACP CODER run 当前返回 `status=unsupported`，不隐式透传。

#### 工具内模型调用用量

工具内部调用模型（目前为 `image_generate`、`vision_recognize`、`speech_generate`）且上游返回 usage 或计费字符数时，平台在该工具的 `tool.result` 之后发出一个 `usage.snapshot`：`usage.current` 为这次工具调用的用量，额外带 `source:"tool"`、`toolId`、`toolName`、`operation`（`image.generation` / `image.edit` / `image.inpainting` / `vision.recognize` / `speech.generation`），按字符计费的调用另带 `characters`（token 字段为 0），有上游请求 ID 时带 `upstreamRequestId`，`modelKey` 为工具实际使用的模型，`estimatedCost` 按该模型价格计算，不带 `reasoningEffort` 与 `llmChatCompletionCount`；`usage.run`、`usage.chat` 已包含这次用量。`contextWindow` 与 `model` 仍描述 Agent 主模型，客户端无需区分来源即可按原逻辑显示。TEAM 成员快照带 `taskId`，`toolId` 使用成员命名空间。

Chat JSONL 步骤行用可选数组 `toolUsage` 保存这些调用（字段同上，不含 `source`），与该步骤主模型的 `usage` 分开；回放把它们计入 Run/Chat 的 token 与费用，但不计入模型调用次数、计时、上下文窗口或步骤 `modelKey`。旧记录没有该字段时按 0 处理。

#### CODER model options

`GET /api/model-options` 返回聊天输入区运行时可选项。前端按当前 agent `mode` 自行决定是否展示该控件：

- `models`: 当前 model registry 中可展示的聊天模型。Native 选项仅返回 `key/name/icon/provider` 与非空的 `serviceTiers`，不返回 `modelId/protocol/isReasoner/isVision/contextWindow/timeout` 或模型级 `reasoningEfforts`。ACP 透传模型保留原有字段与 bridge 声明的能力信息，包括显式 boolean `isReasoner/isVision`、模型级 `reasoningEfforts/serviceTiers`；内部发现与 Query 校验不受 Native 响应精简影响。`icon` 是可选的模型图标标识；ACP 透传模型仅在上游 `/api/models` 返回该字段时携带。普通模型要求 `type: chat`、provider 存在且 `apiKey` 非空；`protocol: ACP_PASSTHROUGH` 的 ACP 透传模型不要求 provider。`type: embedding`、`type: image`、`type: vl` 与 `type: tts` 均不会出现在聊天模型选项中。
- `reasoningEfforts`: 保留 `{key,label}` 对象数组，支持中文等显示标签。native model options 固定为 `NONE`、`LOW`、`MEDIUM`、`HIGH`、`XHIGH`、`MAX`，其中 `NONE` 表示关闭思考深度；ACP CODER 仍按 bridge 的模型发现结果生成
- `serviceTiers`: 可选服务等级（ACP 按 bridge 能力解析，包含恢复标准等级的 STANDARD 选项）

该接口仅表达可选能力，不返回 `defaultModelKey/defaultReasoningEffort/defaultServiceTier`。当前选择由 `/api/agent` 顶层 `modelKey/reasoningEffort/serviceTier` 提供，刷新选项不改变 Agent 的选择；无可配置服务等级时省略 serviceTiers。

`GET /api/admin/agents/editor-options` 的 `models` 仅返回 `type: chat` 的模型（未声明 `type` 时按 `chat` 兼容），供 Agent 创建与编辑选择；`embedding`、`image`、`vl`、`tts` 不进入选项，支持看图的 `chat` 模型仍保留。该接口无需类型过滤参数。reasoner model 同样返回 `reasoningEfforts: [LOW, MEDIUM, HIGH, XHIGH, MAX]`。这里及聊天、usage、回放中记录的均为用户选择的逻辑档位；provider 实际映射值不会作为额外字段回显。

Agent 编辑选项与 ACP 模型选项中的 `contextWindow` 是 API 响应字段名；model registry YAML 中对应配置字段为 `maxInputTokens`。Native 聊天模型选项不返回该字段；用量显示读取 usage 数据。

HITL 三态细节见 [HITL协议](HITL协议.md)。真流式、heartbeat、attach backlog 与 H2A 缓冲见 [真流式和H2A](真流式和H2A.md)。

### KBASE

库管理 REST `/api/admin/kbases` 与当前 Agent 的检索 status 分开，契约见 [知识库](知识库.md#http-契约)。库 GET 返回配置内容修订 baseRevision；更新在 JSON 中必传，删除在 query 中必传，过期修订返回 409。库管理 status 在首次索引前也返回 state/error/indexedAt/degraded，就绪后附加 statistics；模型通过 builtin.platform-control 的 kbase_library_query/manage 使用同一服务，管理工具的参数和审批顺序见随包 Skill 手册。

KBASE status API 接受有 libraryId 绑定的 Native Agent；未绑定或未知 Agent 返回 404。四个只读工具 search/files/read/status 复用中心库。source ID 为 kbase:<libraryId>/<collection>/<relativePath>，事件保留 libraryId 和 agentKey。`POST /api/chat/sources/read` 接收 chatId/sourceId/offset/limit，`POST /api/chat/sources/file` 接收 chatId/sourceId 下载当前源文件；检查 Chat 权限、实际发布记录和当前库绑定，不借 Workspace `/api/resource` 权限。

启用 KBASE capability 的 Agent 在运行时调用 `kbase_search` 且召回到内容时，会额外通过 live stream 发布 `source.publish` 事件。事件包含 `kind: "kbase"`、`query`、`sourceCount`、`chunkCount` 与按检索来源聚合的 `sources[].chunks[]`，chunk 可携带 `path`、行号、页码、slide、`sourceType`、`matchType`、`score` 等定位字段；chat JSONL 会把该事件作为对应 `react-tool` step 的顶层 `sources.items[]` sidecar 持久化，`/api/chat` replay 时再合成 `source.publish` 事件并保留原始 `liveSeq`，供时间线与 `/api/attach.lastSeq` 使用。当前 `_type:"event"` 的 `source.publish` 也保持可回放。

`artifact_publish` 仅在整个批次文件物化且 `<chatId>/.tools/artifacts.json` 原子写入成功后发布 `artifact.publish`。事件包含合法 epoch-millisecond `timestamp`、`chatId`、`runId`、`toolId`、`artifactCount`、`artifacts`，子任务有明确归属时额外包含 `taskId`；每个 `artifacts[]` 项至少包含 `artifactId/name/mimeType/sizeBytes/sha256/url`。发布器从物化后的真实文件计算 SHA、大小和统一文档 MIME；manifest 未声明或旧逻辑会声明为 `application/octet-stream` 的安全 UTF-8 文本，在新产物写入时即规范化，不批量迁移历史 manifest。JSONL 的对应 `react-tool.artifacts.items[]` 只是该次调用的审计记录；`GET /api/chat` 的 `data.artifact = { items: [...] }` 只从 manifest 恢复，每个 item 返回 `publishedAt`（Unix epoch 毫秒），表示该条产物记录的发布时间；已有 manifest 中的时间直接返回，无需迁移。

`image_generate.images[].path` 与 `artifact_publish.artifacts[].path` 是工具间传递的内部文件系统字段，可以是当前 Host 的绝对路径，但不得进入 Markdown 或用户可见正文。`image_generate.images[].url` 指向 Chat 根目录中的生成文件；发布时复制到 `artifacts/<runId>/<filename>`，成功后的 `publishedArtifacts[].url` 必须指向该发布副本（形如 `@chat/artifacts/<runId>/<filename>`，字面路径不编码），并优先于生成源 URL。工具若没有返回合法 `url`，模型必须明确报告物化/发布失败，不能伪造图片或下载链接。

路径分类契约如下：

| 输入形式 | `image_generate` | `artifact_publish.path` | Markdown / 工具返回 `url` | `/api/resource?file=` |
|---|---|---|---|---|
| Workspace 相对路径 | 不适用 | 接受，按当前 Workspace 解析 | 不作为本地 Markdown 地址 | 不作为资源键 |
| 当前 Chat 内的 Host 绝对路径 | 内部 `path` 可返回 | 接受 | 禁止展示；改用 ChatScope `url` | 拒绝；使用 ChatScope 逻辑键读取 |
| 普通 Agent Workspace 内的 POSIX 绝对路径 | 内部 `path` 可返回 | 接受，canonical 后仍须属于当前根 | 允许实时引用 | 必须同时传当前 `chatId` 与 Bearer/Cookie |
| 冻结临时根内的实际 Host 绝对路径 | 不适用 | 接受，按最终 canonical 目标校验 | Agent（含 TEAM）允许实时引用 | 必须同时传当前 `chatId` 与 Bearer/Cookie；symlink/junction 逃逸拒绝 |
| `@chat`、`@workspace`、`@temp`、Container `/chat`、`/workspace` | 不适用 | 接受并映射到当前受控根 | 禁止展示 `@*`；临时文件应使用实际 Host 绝对路径 | 禁止语义根；只接受 ChatScope 或实际绝对路径 |
| 其他 chat 或 Workspace/冻结临时根之外的绝对路径 | 不适用 | 拒绝 | 禁止 | 拒绝 |
| `file://`、当前 Host 不支持的异平台/UNC 绝对路径 | 不适用 | 拒绝 | 禁止 | 拒绝 |
| `http://`、`https://`、`data:`、`blob:` | provider 图片响应先下载并物化到当前 Chat | 拒绝作为发布源 | 允许原样引用 | 不作为资源键 |
| `relative/path` ChatScope 引用 | 作为 `url` 返回 | 不作为 `path` | 当前 Chat 稳定资源格式 | adapter 加当前 `chatId` 后接受 |
| `<currentChatId>/relative/path` | 不再生成 | 不作为 `path` | 禁止 | 仅可作为隐藏 HTTP 逻辑键 |
| 历史 `/api/resource?file=...` | 不再生成 | 不作为 `path` | 不迁移、不预览 | endpoint 本身继续作为内部数据面 |

KBASE 工具读取当前 Agent 的 KBX active 索引内容。`kbase_search.method` 为 query（缺省混合）/search（全文）/vsearch（向量）/gsearch（图关系）；支持 pathPrefix/pathGlob/type/filter，过滤在召回前与库 collection 策略取交集。query/search/vsearch 另支持 exclude/intent/minScore/candidateLimit/recencyWeight/recencyHalfLifeDays，query 可用 noGraph 关闭图召回；gsearch 支持 entities/relations/direction/maxHops 并保留 graph 关系路径与边证据。不适用所选方法的参数明确拒绝；offset/cursor 不支持，不返回伪造的 matchCount 或分页完成标志。结果返回 method、实际召回通道和降级；仅有预算 trace 的方法返回 candidateBudgetExhausted。vsearch/gsearch 要求对应能力可用，不自动回退；Platform 尚不自动建图。详细参数见 [KBX 接入](KBX接入.md#查询路径与引用)。`kbase_files` 浏览 active 文件清单，支持 path/pattern/type、files/tree、depth/headLimit/offset；`kbase_read.chunkId` 接受 chunkId/evidenceId/nextEvidence 精确回读，或使用 path 与一基行号分页，拒绝跨库、越界和被排除的内容。

专用 KBASE 只有一个 main stage（editingMode 不切换 stage 或 cache key），使用 Agent 有效工具集合，没有固定文件工具集。Workspace 固定为本 run 冻结的 runtimeConfig.workspaceRoot，相对路径从 Workspace 解析，当前 Chat 目录通过 @chat 使用。未开启 editing 时 Workspace mutation 与其他 mode 一样返回 file_write_path_blocked / file_edit_path_blocked；hostAccess、writeRoots、approval 和 full_access 不能替代这一 gate。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/kbase/{agentKey}/status` | 无 | 绑定库 libraryId、state、indexing/stale、文件数、全文及向量 readiness；chunksKnown=false，省略未知的 chunks |

status 字段节选：

```json
{
  "libraryId": "research",
  "engine": "kbx",
  "state": "ready",
  "indexing": false,
  "stale": false,
  "files": 27,
  "chunksKnown": false,
  "indexes": {
    "fts": {"type": "fts", "ready": true},
    "vector": {"type": "vector", "ready": false, "pendingContentUnits": 0}
  }
}
```

`lastIndexedAt` 是可选 Unix epoch 毫秒时间点。首次扫描完成前不可读；普通内容维护允许读取 KBX 已提交内容，embedding 失败在全文完成时标记 degraded；未知部分失败禁读，重启与周期对账自动重试。详见 [KBX 接入](KBX接入.md)。

免鉴权 `GET /healthz` 检查 Go HTTP runtime，并报告受管 KBX 的维护能力。单个库不可用或绑定悬空不使平台健康返回 503，`data.kbase.required` 为 false。Run 开始与知识工具调用独立检查当前库就绪状态。

### Memory

Memory 已替换为纯 Markdown 文件管理，只提供 HTTP `/api/memory/file`（GET/PUT/DELETE）、`/api/memory/daily`（GET）和 `/api/memory/search`（GET）。文件使用固定 kind/date 标识和 revision 乐观锁，冲突 409。详情见 [记忆系统](记忆系统.md#手工触发与内置工具)。旧 Memory meta/scope/record/history/context-preview 与 learn 接口不注册，无数据库兼容模式。

### View / Resource

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/file` | query: `agentKey`、`path`、`response` 可选 | agent workspace 文件；默认 JSON metadata/text，`response=content` 返回文件内容流 |
| GET | `/api/project/git` | query: `agentKey` | 实际 Workspace 的只读 Git 快照，与 Agent 列表/详情独立 |
| GET | `/api/project/git/branches` | query: `agentKey` | 本地分支列表、Git 快照及写操作边界 |
| POST | `/api/project/git/branches` | body: `agentKey,operation,branch,expectedRevision` | 切换或新建并切换后返回最新 Git 快照 |
| GET | `/api/project/tree` | query: `agentKey`、`path`、`limit`、`cursor` | CODER/KBASE Workspace 单层目录树，目录优先稳定排序 |
| GET | `/api/project/changes` | query: `agentKey`、`chatId`、可选 `runId/limit/cursor` | 当前 Chat 的 Run 文件历史列表 |
| GET | `/api/project/diff` | query: `agentKey`、`chatId`、`runId`、`path`、可选 `encoding` | 单个 Run 快照的原始/当前文本 |
| GET | `/api/view` | query: `source,key`；connector 另需 `chatId,connectorId,hash`，可选 `usage` | 统一返回 `{view,html?,qlc?,entry?,assets?}`；connector 缺 hash 返回 400 `view_snapshot_required`；未知内置 key 或 native 文档返回 404 |
| POST | `/api/view/query` | body: `chatId,runId,view:{connectorId,key,hash},query,params` | `{data,executedAt}`；只执行快照声明的只读查询（native/cli/mcp），按查看者授权，`runId` 须属于该 Chat |
| GET | `/api/resource` | query: `file`、`chatId`、`t`、`download` | ChatScope 或普通 Agent Workspace/冻结临时根资源字节；绝对路径必须传 `chatId` |
| GET | `/api/tool-result` | query: `chatId`、`path`、`t` | `.tools/results/<toolId>.json` 完整工具结果；`t` 为可选 resource ticket |
| POST | `/api/upload` | multipart: `requestId`、`chatId`、`name`、`file` | upload ticket；文件保存为 `<chatId>/<name>` |
| POST | `/api/document/commit` | body: 来源判别联合、`mode`、`expectedRevision`、MIME 与文本/二进制 payload | 覆盖原文档或生成新 Artifact 的身份、类型与 revision |
| POST | `/api/resource/image/commit` | body: `operation=resource.image.commit`、`profile`、`agentKey`、`chatId`、`resourceId`、`relativePath`、`mode`、`expectedRevision`、`mimeType`、`dataBase64` | 覆盖原 Artifact 或生成新 Artifact 的身份与 revision |

`/api/view` 统一 builtin 与 connector 来源。前者只读取内嵌 HTML，后者沿用 Chat 鉴权，只读取事件已发布 hash 的 Chat 快照，不按当前挂载现场解析；source 必填。旧 `/api/viewport` 已删除。

`/api/upload` 的 `chatId` 与 `name` 均可省略。无 `chatId` 时平台会先分配会话；同时无 `name` 时，该会话以 `<default>` 标记为尚未正式命名。仅完成上传、尚未接受首条正式 query 的占位会话仍可通过 `chatId` 继续使用，但不进入 `/api/chats` 历史列表。上传文件保持既有契约，落入 `<chatId>/<name>`，公开 `url` 为不带 `chatId` 的 `<name>`。首条正式 query 在会话尚无历史 run 时会用 message 生成 `chatName`，并广播 `chat.renamed`；已命名或已有历史的会话不会被覆盖。

`/api/file` 与 `/api/agent/open-directory` 的 `directoryType:"workspace"` 都使用 `runtimeConfig.workspaceRoot`。`path` 可以是 Workspace 相对路径，也可以是宿主机绝对路径；绝对路径经 canonical 解析后必须分类为 Workspace，进入整个 ChatsRoot 会返回 `path_crosses_chat_root`，`..` 与 symlink escape 会返回 forbidden。默认响应使用统一 JSON 包裹，文本文件内联 `content`，二进制/PDF/图片只返回 metadata 与 `contentUrl`；`response=content` 时直接返回文件字节流，不使用 JSON 包裹。文件 JSON 与内容流共用文档元数据解析器，返回一致的 `documentKind/contentKind/mimeType/revision/sizeBytes`；Markdown 与普通文本 MIME 明确声明 UTF-8。该接口不读取 KBASE 索引库，也不扩大 `hostAccess.readRoots`。

Project 的 tree/changes/diff 三个端点是只读 HTTP 数据面，对服务端从 `agentKey` 解析出的任何具有真实 Workspace 的 Agent 可用，不按 mode 限制，也不接受客户端 `workspaceRoot`；没有 Workspace 时返回 `invalid_request`。所有 `path` 都是 Workspace 相对 POSIX 路径：拒绝绝对路径、反斜杠、`..`、ChatsRoot、symlink 逃逸和 device file。KBASE 不要求 `editingMode:true`。

`GET /api/project/git?agentKey=...` 是独立的只读 HTTP/WS 查询，不扩展 `/api/agents`、`/api/agent`，不扫描工作区改动。仅按 catalog 中真实 `Workspace.Root` 解析 canonical 目录及 ChatsRoot 边界，不按 CODER/KBASE mode 限制；没有目录也不会退回 Agent 配置目录或当前进程目录。

成功响应 `data` 为 `{agentKey,status,branch?,commit?,reason?,revision?}`：

| status | 含义与字段 |
|---|---|
| `branch` | 实际分支名 `branch`；HEAD 有提交时包含完整 SHA `commit`，空仓库可省略 |
| `detached` | 游离 HEAD，只返回 `commit`，不虚构分支名 |
| `not_repository` | 普通非 Git 目录 |
| `no_workspace` | Agent 未配置稳定项目目录 |
| `unavailable` | 目录或 Git 读取失败；`reason` 为 `workspace_unavailable`、`git_unavailable`、`probe_failed` 或 `probe_timeout` |

参数错误、未知 Agent、文件系统权限拒绝仍走已有 HTTP 错误包裹。需要宿主 PATH 中可用的 Git；Git 不可用不会阻塞 Agent 列表/详情。探测限时 3 秒，清除继承的 Git 路由/配置环境变量，支持仓库子目录与 worktree；损坏 `.git` 不作为普通非 Git 目录。接口不执行分支切换、创建、checkout、索引刷新或项目写入，响应 `Cache-Control: no-store`。`projectConfig.git.expectedBranch` 是对所有 mode 生效的运行约束，既不决定探测资格，也不作为实际分支的回退值。

`GET /api/project/git/branches` 按需返回 `{git,branches,canChange,blockedReason?,expectedBranch?}`。`branches` 为本地分支名数组，包含空仓库当前尚未产生提交的分支；不 fetch、不列远端分支。`git.revision` 绑定解析后的项目目录、当前 HEAD 状态、分支及提交，是客户端操作前置条件。`expectedBranch`（任何 mode 配置后都会返回）仅提示既有运行约束，不会被写操作修改。

`POST /api/project/git/branches` 请求 `{agentKey,operation:"switch"|"create",branch,expectedRevision}`。`switch` 仅切换已有本地分支；`create` 从当前 HEAD 创建并切换（空仓库可创建新的初始分支）。分支名须通过 Git ref 格式校验，拒绝选项注入及不合法名称。请求缺少 revision、非法操作或名称返回 400；HEAD/目录已变返回 409 `revision_conflict`，同仓库并行操作返回 `git_busy`，重复名称返回 `branch_exists`，目标不存在返回 `branch_not_found`。成功返回新的 Git 快照。

切换影响整个 worktree，因此只有 Workspace 正好覆盖仓库工作树根且不包含 ChatsRoot 时 `canChange:true`；子目录仍可读分支，mutation 返回 403 `workspace_not_repo_root`，包含聊天存储返回 `workspace_contains_chats`，bare/不可访问 worktree 返回 `worktree_unavailable`。这是用户直接项目操作，不借用 KBASE run 的 editingMode，也不扩大普通文件工具授权。

操作在进程内按 canonical Git common-dir 串行，使用 Git 自有锁及未提交/未跟踪/被忽略文件保护，不 force、stash、clean，不自动猜测远端分支、不递归切换 submodule、不执行 checkout hooks。其他 worktree 占用目标分支时由 Git 拒绝，返回 409 `git_switch_rejected` 及截断后的原因。操作最长 30 秒；超时或执行后无法确认返回 503 `git_operation_uncertain`，客户端必须刷新确认后再决定是否重试，不承诺命令被中止后自动回滚。外部 Git 进程不参与 Platform 进程内锁；用户应避免同一工作树同时进行其他 Git 操作或运行文件写入任务。

`/api/project/tree` 每次只枚举一个目录，默认 `limit=200`、最大 1000；目录优先并按名称稳定排序。游标绑定响应 `revision`，继续分页前目录发生变化会返回 HTTP 409，错误码为 `data.error.code=directory_changed`。目录 symlink 不允许展开；指向 Workspace 内普通文件的 symlink 可交给 `/api/file` 预览，逃逸、断链、目录目标或非普通文件保留 `kind:"symlink"` 但返回 `accessible:false`。Workspace 为文件系统根时仍屏蔽整个 ChatsRoot。

`/api/project/changes` 验证 Chat owner 与 Agent 一致；指定 `runId` 时还会验证 Run 属于该 Chat。不指定 Run 时按每个 Run 的独立基线返回现有 file-history，不跨 Run 聚合；已离开当前 Workspace 的历史条目不回显。空目录和空历史分别固定返回 `entries:[]`、`runs:[]`、`items:[]`，不会返回 `null`。`/api/project/diff` 从同一历史存储一次返回 `original/current` 两侧；新增和删除由各侧 `exists` 表达。任一快照超过 `file-tools.max-read-bytes` 返回 413 `diff_too_large`，二进制、含二进制控制内容、无法按请求编码解码的快照返回 415 `unsupported_media_type`。Bash、ACP 或外部程序绕过 FileTools 的写入没有历史快照，只能通过 `/api/file` 读取实时内容，不会生成伪 Diff。

`/api/resource` 的 ChatScope 数据面使用 `file=<chatId>/<relativePath>` 逻辑资源键。Markdown 的 `relativePath` 每段先做 URI path 编码，adapter 再把整个逻辑键做 query 编码；服务端逐段只解码一次并执行 canonical/symlink 边界检查，拒绝路径穿越、其他 chat 所有权和 `.tools`/`.btw` 内部目录。active 文件不存在时会在同一 chat 的 archive 副本中查找，使生成和发布资源可稳定回放。GET 与 HEAD 共用文档元数据解析器，以 canonical `relativePath` basename 为语义文件名，内部落盘或缓存文件名只作兜底；两者一致返回 `X-Document-Kind`、`X-Document-Revision`、`Content-Type` 与 `Content-Length`。`application/octet-stream` 仅是未知 MIME，扩展名只用来选择候选文档类型，Markdown/Text 还必须通过安全 UTF-8 文本校验；固定 512 字节样本额外读取最多 `utf8.UTFMax-1` 字节，只用于验证跨越样本边界的 3/4 字节 UTF-8 字符，不放宽样本中间的非法字节、完整内容末尾的不完整字符或样本内 NUL。远端缓存必须保留源资源 revision，不得用缓存文件自身 mtime 替代。

`POST /api/document/commit` 是统一文档提交数据面。Workspace File 只允许带 expected revision 原位覆盖；Artifact 默认创建新 Artifact，也可带 revision 明确覆盖；Reference 只允许创建新 Artifact。revision 对客户端不透明，冲突统一返回 409 `revision_conflict`。Platform 在每次提交中重新验证 Agent/Chat 所有权、Workspace 根、ChatScope profile、canonical path、普通文件、symlink 和 ChatsRoot 边界；文件与 Artifact manifest 通过同目录 staging 和平台显式原子替换提交。

`POST /api/resource/image/commit` 保留为旧 Desktop 图片编辑器的兼容适配器，并委托统一 document committer。它继续保持原请求、响应和 PNG/JPEG/WebP 约束，不得形成第二套路径、revision 或 manifest 写入语义。

绝对路径数据面使用 `file=<hostAbsolutePath>&chatId=<currentChatId>`，仅接受 Bearer/Cookie 主体，resource ticket 不能授权。服务端从 chat owner 解析 `agentKey` 和当前 `AgentDefinition.workspaceRoot`：普通 Agent 允许 canonical 后仍在 Workspace 或冻结临时根的路径，当前/其他 Chat 的绝对路径必须改用 ChatScope，TEAM Chat 同样按自己的 Workspace 校验。临时根由进程启动时的统一解析器冻结：Unix/macOS 纳入 `os.TempDir()` 与 canonical `/tmp`，macOS 自动把 `/tmp`、`/private/tmp` 视为同一根；Windows 只纳入 `os.TempDir()`，不硬编码 `C:\Windows\Temp`。所有临时请求按最终 canonical 目标校验，symlink/junction 逃逸与 `..` 逃逸拒绝。Workspace 和临时绝对路径都是实时引用，文件变化或删除会直接影响回放。两种读取均返回原始字节和准确 `Content-Type`；图片默认 `Content-Disposition: inline`，`download=true` 改为 `attachment`。

resource ticket、JWT 与 CORS 见 [鉴权与安全边界](鉴权与安全边界.md)。

### Monitor

监控接口是 HTTP polling snapshot，不使用 WebSocket 实时订阅；鉴权沿用普通 `/api/*` 链路。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/monitor` | query: `messageLimit` 可选，默认 5，范围 1..50 | 总览与 WS 摘要 |
| GET | `/api/monitor/ws/connections` | query: `limit` 默认 100，范围 1..500；`sessionId`、`source`、`deviceId` 可选 | 当前/最近 WS 连接列表 |
| GET | `/api/monitor/ws/messages` | query: `limit` 默认 5，范围 1..50；`sessionId`、`source`、`deviceId` 可选 | 最近 WS 消息列表 |

`/api/monitor` 返回：

```json
{
  "generatedAt": 1710000000000,
  "runtime": {
    "mode": "desktop",
    "actionTransport": "reverse-websocket"
  },
  "ws": {
    "connectionCount": 1,
    "latestConnection": {},
    "recentMessages": []
  }
}
```

`/monitor` 页面标题旁按 `runtime.mode` 显示“运行形态 · Desktop 服务”或“运行形态 · 独立服务”，总览同时显示 Action Transport；总览加载失败时显示“运行形态未知”，不默认成 Standalone。

连接项包含 `protocolVersion`、`generation`、`sessionId`、`kind`、`active`、`subject`、`gatewayId`、`channel`、`source`、`deviceId`、`remoteAddr`、`userAgent`、`connectedAt`、`closedAt`、`closeReason`、`lastSeenAt`、`lastMessageAt`、`lastInboundAt`、`lastPingAt`、`lastPongAt`、`lastHeartbeatAt`、`receivedMessages`、`sentMessages`、`errors`、`inflightRequests`、`activeStreams`、`writeQueueDepth`。

消息项包含 `seq`、`timestamp`、`sessionId`、`source`、`deviceId`、`direction`、`frame`、`type`、`id`、`sizeBytes`、`payloadPreview`、`truncated`、`error`。`payloadPreview` 只保存脱敏后的截断摘要，最多 512 字符；不会记录完整 payload，不记录 ping/pong/control frame，并跳过 `push.heartbeat`。

## WebSocket 定义

### 入口与鉴权

- 入口：`GET /ws`，HTTP upgrade 为 WebSocket。
- 鉴权：复用 HTTP token 校验链路。
- Desktop 旁聊连接必须验证 `connected.data.lane` 与预期 `btw`/`explain` 一致后，才发送 `/api/query`。缺少或不匹配时明确拒绝，不回退到主聊天或重发。普通连接返回 `main`，该字段不接受 payload 覆盖。
- token 可通过 `Sec-WebSocket-Protocol: bearer.<token>`、`Authorization: Bearer <token>` 请求头或 query token 传递，按此顺序取第一个；服务端会在握手成功时回写匹配的 subprotocol。URL 会进入代理与访问日志，能设置请求头或子协议的客户端不应使用 query token；PROXY 上游连接只使用请求头与子协议。
- `runtime.yml` 的 `auth.audience`、`auth.allowed-subjects`、`auth.require-expiry` 可把实例绑定到指定的 `aud` 与 `sub` 并要求 `exp`，对 HTTP 与 WebSocket（含 `auth.refresh`）同样生效，不满足时按未认证处理；默认不启用。`resource.public-base-url` 配置后，资源地址使用该固定值，不读取请求的 `Host` 与 `X-Forwarded-*` 头。
- 客户端可通过 query 自报监控元数据：`source` 与 `deviceId`，例如 `/ws?source=webclient&deviceId=device-123`。普通 source 转小写后只用于监控和日志展示，不参与权限或能力声明；Desktop 的 `desktop-main` / `desktop-btw` / `desktop-explain` 是三个受限 lane 标识，但仍必须同时通过 app scope、JWT device claim 与握手 deviceId 校验，`source` 本身绝不构成授权。
- WebClient 控制连接额外携带 `surfaceId`，推荐形式为 `/ws?source=WebClient&deviceId=device-123&surfaceId=surface-123`。Platform 在握手时记录该元数据，不需要注册帧；同一 client boundary 与 `surfaceId` 的新连接替换旧连接。Desktop Broker 不再按 Main Chat、Copilot 或 WebView surface 创建连接：同一设备只登记一个 `desktop-main`，并在首次使用对应能力时分别登记 `desktop-btw`、`desktop-explain`，随后跨 Chat/Run 复用。
- `desktop-main` 是唯一 Desktop Main 默认 target，接收全局 Push 和反向 Desktop Action/CDP；`desktop-btw` / `desktop-explain` 不参与默认 target replacement，不接收这些 Push/request。第二条相同 Desktop lane 连接只替换该 lane 的旧连接，main、btw、explain 可以同时存活。
- WebSocket 控制面常开；没有单独的关闭开关。

普通 `/ws` 使用唯一的协议版本 2。Upgrade 成功后服务端必须先发送 `push.connected`；客户端校验该帧后才能把连接视为可用。版本缺失/不匹配、首帧不是 connected、字段缺失或存活参数越界都属于协议错误，应立即关闭而不是继续收发业务帧。握手等待上限为 10 秒。

WebSocket 存活配置与 SSE heartbeat 完全独立。服务端默认每 30 秒发送 RFC WebSocket Ping，Pong 等待 60 秒；应用 heartbeat 默认每 30 秒发送一次，向客户端协商 100 秒静默阈值。`heartbeatIntervalMs` 只允许 5～120 秒；`silenceTimeoutMs` 至少为 `2 × heartbeatIntervalMs + 10 秒` 且不超过 10 分钟。客户端以任意合法入站帧刷新 `lastInboundAt`，单独记录 `lastHeartbeatAt` 仅用于诊断，不能在业务帧持续到达时因未见 heartbeat 而误断。

### 帧类型

客户端请求帧：

```json
{
  "frame": "request",
  "type": "/api/agents",
  "id": "req-1",
  "payload": {}
}
```

`/api/query`、`/api/chat` 的请求帧可在顶层携带 `detail`（显式 `summary` 或 `full`，省略跟随 Agent），它是下发选项而非 payload；attach 帧不接受，规则见[细节等级](#细节等级与-runactivity)：

```json
{"frame":"request","type":"/api/query","id":"req-2","detail":"full","payload":{"message":"hello"}}
```

Desktop Action 反向请求直接使用具体 Action 名作为 `type`，可信上下文位于帧顶层，payload 只包含该 Action 参数：

```json
{"frame":"request","type":"desktop.workpanel.getState","id":"dsa-123","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{}}
{"frame":"request","type":"desktop.display","id":"dsa-124","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{"kind":"effect","effect":"fireworks","durationMs":8000}}
{"frame":"request","type":"desktop.awcp.manual","id":"dam-124","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{"surfaceId":"surface-1"}}
{"frame":"request","type":"desktop.awcp.manual","id":"dam-125","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{"surfaceId":"surface-1","section":"orders.select","revision":"opaque-revision"}}
{"frame":"request","type":"desktop.awcp.invoke","id":"daw-126","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{"surfaceId":"surface-1","revision":"opaque-revision","action":"orders.select","args":{"ids":["order-1"]}}}
{"frame":"request","type":"desktop.cdp.call","id":"dsc-123","payload":{"requestId":"dsc-123","method":"Runtime.evaluate","params":{"expression":"document.title"},"surfaceId":"surface-1","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"}}}
```

Desktop 模式由 Main Broker 处理普通 `desktop.*` request type、AWCP 专用 wire action `desktop.awcp.manual` / `desktop.awcp.invoke` 和 `desktop.cdp.call`；Standalone 只由当前根 agent-webclient 处理七个 `desktop.workpanel.*` 与 `desktop.display`。Desktop-only 的 `desktop.workpanel.openLocalFile` 在 Standalone 返回 `desktop_action_unsupported_runtime`；`surface_*` 与 `awcp_*` 工具在 Standalone 不提供给模型，直接调用返回 `desktop_cdp_unsupported_runtime`，不转发给 agent-webclient。普通 Action 的调用方可选 request ID 继续映射为帧 `id`；AWCP 的帧 `id` 只由 Platform 生成。响应必须保持同 `id`、同 request `type`；旧统一 envelope 不提供兼容入口。模型通过 `awcp_manual` / `awcp_invoke` 两个静态工具分别映射到 manual/invoke wire；`awcp_manual` 只带 `surfaceId` 时返回轻量目录，读取章节时必须同时给出 `section` 与 `revision`。Platform 把工具字段组装为 wire payload 并附带可选 `surfaceId`；不投影或缓存页面合同。`awcp_invoke` 只接收 `revision`、`action`、`args`，wire payload 可另带 `surfaceId`，运行核心不保存 revision、Action 集合或重试状态，不编译业务 Schema、不改写工具 Schema，目标和来源仅由可信 Run 决定；合法 AWCP `ok:false` 保持普通 response data 并让工具失败，不统一添加执行阶段标记，页面字段错误也不提升为宿主证据。宿主错误仍使用 error frame，可信预检只投影 `manual_required/page_changed/stale_revision/action_not_found`。目标必须来自当前 run，缺失或断连立即失败，不选择其他连接。大 JSON 使用 `desktop.bridge.response.delta`，CDP 截图使用 `desktop.cdp.screenshot.delta`；stream event 包含 `seq/type/timestamp/encoding/chunk`，终态 response manifest 包含 `streamed/streamId/encoding/chunkCount/totalBytes`。超时或取消时 Platform 发送 `{"frame":"push","type":"desktop.bridge.cancel","payload":{"requestId":"..."}}`，各层都不自动重放。

Desktop Action 的执行器错误由 Desktop Broker 转换为统一 error frame，外层 `frame/type/id/code/msg/data` 不变。`type/msg` 承载原始错误类型和消息，`data` 只保存 `{action, details?}`，不再嵌套完整 Action result 或 `error`。Platform 从 `data.details` 提取有界的 `issues`（最多 16 项，每项 `path/code/expected/actual` 字符串最多 256 字节）与恢复提示；`actual` 只表示类型或缺失，不返回输入值。参数错误在模型工具结果中保留 `invalid_args`，其他 provider 错误保留既有分类。此边界由 Desktop/Platform 配套发布；不猜测历史嵌套格式，不改变 CDP/AWCP 各自的诊断协议。

服务端响应帧：

```json
{
  "frame": "response",
  "type": "/api/agents",
  "id": "req-1",
  "code": 0,
  "msg": "success",
  "data": {}
}
```

反向 request 成功时返回同 `id`、同 request `type` 的 response；未知 request 返回 `unknown_request_type`，参数错误返回 `invalid_request`，当前页面无法执行返回 `unsupported_in_current_view`。同一连接内重复使用 request `id` 返回 `duplicate_id`。

实时流帧：

```json
{
  "frame": "stream",
  "id": "req-1",
  "streamId": "run-id",
  "event": {},
  "lastSeq": 12
}
```

推送帧与错误帧：

```json
{"frame":"push","type":"connected","data":{"protocolVersion":2,"sessionId":"ws_123","serverTime":1787281659000,"liveness":{"heartbeatIntervalMs":30000,"silenceTimeoutMs":100000}}}
{"frame":"push","type":"heartbeat","data":{"sessionId":"ws_123","sequence":17,"timestamp":1787281689000}}
{"frame":"error","type":"invalid_request","id":"req-1","code":400,"msg":"...","data":{}}
{"frame":"error","type":"active_run_conflict","id":"req-1","code":409,"msg":"multiple active runs found for chat","data":{"code":"active_run_conflict","message":"multiple active runs found for chat","chatId":"chat-id","runIds":["run-1","run-2"]}}
```

当前 platform 主动发送的 `push.type`：

| Push type | data |
|---|---|
| `connected` | `protocolVersion=2`、`sessionId`、`serverTime`、`liveness.heartbeatIntervalMs`、`liveness.silenceTimeoutMs` |
| `heartbeat` | `sessionId`、单调递增 `sequence`、`timestamp` |
| `auth.expiring` | `expiresAt` |
| `run.started` | `runId`、`chatId`、`agentKey`、必填 `startedAt`；带 requestId 的 execute 另含 requestId，仅用于取消关联，不去重 |
| `run.finished` | `runId`、`chatId`、`status`、`finishReason`、必填 `finishedAt` |
| `chat.created` | `chatId`、`chatName`、`agentKey`、`createdAt`、`source` |
| `chat.updated` | `chatId`、`lastRunId`、`lastRunContent`、`updatedAt` |
| `chat.unread` | `chatId`、`agentKey`、`lastRunId`、`createdAt`（等于本次 run 完成后写入的 chat `updatedAt`）、`readRunId`、`agentUnreadCount` |
| `chat.read` | `chatId`、`agentKey`、`lastRunId`、`readAt`、`readRunId`、`agentUnreadCount` |
| `chat.read_all` | `agentKey`、`updatedCount`、`agentUnreadCount` |
| `chat.deleted` | `chatId` |
| `chat.renamed` | `chatId`、`chatName`、`agentKey` |
| `chat.archived` | `chatId`、`agentKey` |
| `archive.restored` | `chatId`、`agentKey`、`summary` |
| `archive.deleted` | `chatId` |
| `catalog.updated` | `reason`、`updatedAt` |
| `chats.order.changed` | `updatedAt`；列表展示偏好修改后刷新，时间不代表 Chat 内容变化 |
| `chat.primary.changed` | `agentKey`、`chatId`、可选 `previousChatId`；Agent 的主要聊天指针切换 |
| `awaiting.asking` | `chatId`、`runId`、根 `agentKey`、`awaitingId`、`mode`、`createdAt`、可选 `timeout` / `view` |
| `awaiting.answered` | `chatId`、`runId`、根 `agentKey`、`awaitingId`、`mode`、`status`、`answeredAt`、可选 `errorCode` / `submitId` / `durationMs` |
| `artifact.published` | `chatId`、`runId`、`artifactId`、`name`、`type`、`mimeType`、`sizeBytes`、`sha256`、`url`、`publishedAt`；仅已认证 Desktop Main |
| `resource.pushed` | `chatId`、`artifactId`、`name`、`mimeType`、`sha256`、`sizeBytes`、`pushedAt` |

上述全局 Push 广播排除 `desktop-btw` 和 `desktop-explain` 连接。旁聊 lane 只接收握手、heartbeat、本连接请求的 response/error 与 Run stream；Desktop Primary 可以按全局唯一 runId 使用 `run.finished` 收敛 BTW RunChannel。

`artifact.published` 是 Main 专属产物发布通知：本地成功发布并持久化 manifest 后，由实时 `artifact.publish` 生命周期触发；代理收到实时发布事件并写入本地记录后使用同一转换逻辑。每个产物一条 push，字段平铺在 `data` 中，不携带 `artifacts`、`artifactCount`、`toolId` 或 `taskId`。`url` 是相对于 `chatId` 的发布资源引用，`publishedAt` 是发布事件的 epoch 毫秒时间。它只投递给当前已认证注册的 `desktop-main`，所有 BTW、Explain、WebClient、Gateway 及其他 WS 均不接收；Main 离线不转投、不缓存通知。通知不依赖网关、当前选中 Chat 或 Run stream 订阅，attach/历史回放不重发；客户端以 `(chatId, runId, artifactId)` 幂等更新，断线后通过持久化产物清单对账。

`resource.pushed` 仅表示文件已成功上传网关；没有网关配置/路由或上传失败不发送，代理不再把 `artifact.publish` 转换成它。`artifact.publish` 仍保留为原有批次 Run stream 事件。

除 Main 专属 `artifact.published` 外，上述全局 Push 广播排除 `desktop-btw` 和 `desktop-explain` 连接。BTW lane 只接收握手、heartbeat、本连接请求的 response/error 与 Run stream；Desktop Primary 可以按全局唯一 runId 使用 `run.finished` 收敛 BTW RunChannel。

除 `heartbeat.timestamp` 外，platform 主动发送的 push payload 不使用 `timestamp`；它们用上表的业务语义时间字段。这是硬切换，不会双写旧字段，前端与服务端需要同版本发布。SSE 与 WebSocket `frame:"stream"` 的 `event.timestamp` 仍是每个业务流事件必填的 epoch milliseconds。`auth.refresh` response 在 JWT 存在 `exp` 时才返回 `expiresAt = exp * 1000`；没有 `exp` 时省略字段。`auth.expiring.expiresAt` 同样始终是 epoch milliseconds。客户端不得把缺失 `readAt` / `expiresAt` 解释为 1970 或当前时间。

`run.finished` 是 run 退出 active 状态的终态通知：`finishReason` 只允许 `complete | error | cancel`，对应的 `status` 分别是 `completed | failed | interrupted`。前端必须以 `status` 判断结果，不得仅因收到 `run.finished` 就当作成功；完整错误内容仍从同一 run 的 stream `run.error` 获取。

`awaiting.asking.timeout` 与 stream 中的 `awaiting.ask.timeout` 语义一致：对普通 HITL 等待项，`0` 表示无限等待、不自动超时；大于 `0` 时由后端按真实时间独立倒计时，observer / attach / detach 状态不会暂停或延长后端超时。planning confirmation 使用 `mode:"planning"` 和同时包含 `planningId + planningFile` 的 `planning` payload，永远省略 `timeout`，表示永久等待；它不同于 `plan_*` / plan-tasks 的执行任务计划。awaiting mode 只允许 `question | approval | form | planning`。

stream `awaiting.answer` 的 `error.code == "timeout"` 时，`error.message` 会显示超时秒数和原因；`error` 可附带 `timeoutSeconds`、`elapsedSeconds`、`reason:"submit_not_received_before_timeout"`。

字段说明：

| 字段 | 适用帧 | 说明 |
|---|---|---|
| `frame` | 全部 | `request` / `response` / `stream` / `push` / `error` |
| `type` | request / response / push / error | route 或 push/error 类型 |
| `id` | request / response / stream / error | 客户端请求 id，用于关联响应和流 |
| `payload` | request | route payload，通常对应 HTTP query/body 的 JSON 化形态 |
| `code` / `msg` / `data` | response / error | 与 HTTP JSON envelope 对齐 |
| `streamId` | stream | runId 或流 id |
| `event` | stream | `stream.EventData` |
| `reason` | stream | stream 结束或中断原因 |
| `lastSeq` | stream | 已发送事件序号，可用于 attach |

当 `POST /api/query` 使用 SSE 时，WebClient 同时保持 `/ws` 控制连接，并在 query 与后续 `GET /api/attach` 中发送 `X-Agent-WebClient-Device-Id`、`X-Agent-WebClient-Surface-Id`。前者与 `/ws?deviceId=...` 使用同一个 localStorage device 标识；认证 JWT 已含 device claim 时以 claim 为准。WebSocket query/attach 则直接使用发起请求的连接。每次成功且具有有效 target 的 attach 都把该连接或逻辑 surface 设为 run 的最新反向 Action target；失败 attach 和不带 target headers 的普通 HTTP attach 不改变原绑定。统一 Desktop Action 与 CDP 都使用当前 run 的通用 `budget.tool.timeout`，没有独立 Desktop 超时配置。

回放事件的 `seq` 是展示序号。`chatId.jsonl` 使用每行顶层 `liveSeq` 记录该行覆盖到的公开 live stream 游标；replay 时会把它注入到对应事件 payload，供 attach cursor 使用。新的 Native / Team run 对外事件序号严格连续，`llm.request`、内部 snapshot、隐藏工具等不发布事件不占号；PROXY / CHANNEL 保持上游序号语义。

### WS Route

`/ws` 可转发的 route 由 `internal/server/ws_routes.go` 注册。除 `/api/query` 与 `/api/attach` 外，大多数 route 返回一次 `response` frame。

| Route | Payload | 返回 |
|---|---|---|
| `/api/agents` | `includeChats`、`chatsPinned`、`scope`、`mode`、`hasWorkspace` | `response` |
| `/api/agent` | `agentKey` | `response` |
| `/api/skills` | 可选 `agentKey` 读取；`id/pinned` 写入 | `response`；data 与 HTTP `/api/skills` 完全一致 |
| `/api/connectors` | 可选 `agentKey` 读取 | `response`；与 HTTP 使用目录相同的精简 DTO，展示文本使用连接语言 |
| `/api/agents/connectors` | `agentKey/connectorId/enabled` 写入；旧 `agentKey` 读取保留兼容 | `response`；data 与 HTTP 使用挂载一致，失败返回原业务 error |
| `/api/agent/model-config` | `agentKey`、可选 `modelKey/reasoningEffort/serviceTier` | `response` |
| `/api/model-options` | 无 | `response` |
| `/api/chats` | `lastRunId`、`agentKey`、`mode`、`pinned`、`primary`、`hasWorkspace`、`limit` | `response` |
| `/api/chats/order` | 空 payload 读取；或 `operation` 与对应字段更新 | `response` |
| `/api/chats/primary` | `chatId` | `response`；与 HTTP PUT 相同 |
| `/api/chat` | `chatId`、`includeRawMessages` | `response` |
| `/api/read` | `chatId` | `response` |
| `/api/feedback` | feedback 字段 | `response` |
| `/api/chat/delete` | `chatId` | `response` |
| `/api/chat/rename` | `chatId`、`chatName` | `response` |
| `/api/chat/archive` | `chatId`、`reason` | `response` |
| `/api/chat/jsonl` | `chatId` | `response`，data 为原始 JSONL 字符串；HTTP 仍返回 text/plain |
| `/api/chat/system-prompt` | `chatId`、`runId`、`agentKey` | `response`，data 与 HTTP 成功响应完全一致 |
| `/api/chat/llm-trace` | `file` | `response`，data 为原始 LLM trace JSON 字符串；HTTP 返回 application/json |
| `/api/archives` | `agentKey`、`limit`、`offset` | `response` |
| `/api/archive` | `chatId` | `response` |
| `/api/archives/search` | `query`、`agentKey`、`limit` | `response` |
| `/api/archive/delete` | `chatId` | `response` |
| `/api/automations` | `tag` | `response` |
| `/api/automation` | `id` 或 `automationId` | `response` |
| `/api/automation/executions` | `id` 或 `automationId`、`limit`、`offset` | `response` |
| `/api/automation/execution` | `executionId` 或 `id` | `response` |
| `/api/chats/search` | `query`、`agentKey`、`limit` | `response` |
| `/api/query` | main: `QueryRequest`；btw/explain: BTW query payload | `stream`，执行语义由已认证连接 lane 决定 |
| `/api/attach` | `runId`、根 `agentKey`、`lastSeq` | `stream` |
| `/api/detach` | `runId`、根 `agentKey`、`reason` | `response`；关闭当前 WS 连接上该 run 的 observer，不中断 run |
| `/api/terminal/open` | `agentKey`、可选 `terminalKey`、`cols`、`rows` | `stream`；agent scope attach-or-create；兼容传入的 `chatId` 会被忽略 |
| `/api/terminal/input` | `terminalId`、`data` | `response` |
| `/api/terminal/resize` | `terminalId`、`cols`、`rows` | `response` |
| `/api/terminal/detach` | `streamRequestId`、可选 `terminalId` | `response`；只释放当前 WS terminal stream，不关闭 PTY |
| `/api/terminal/close` | `terminalId`，或 `streamRequestId` | `response`；关闭 PTY；`streamRequestId` 用于 open 尚未返回 `terminal.opened` 的预取消 |
| `/api/terminal/status` | 无 | `stream`；当前 owner boundary 下所有 agent-scoped terminal 快照 |
| `/api/terminal/status/detach` | `streamRequestId` | `response` |
| `/api/submit` | `SubmitRequest` | `response` |
| `/api/steer` | `SteerRequest` | `response` |
| `/api/interrupt` | `InterruptRequest` | `response` |
| `/api/compact` | `requestId`、`chatId`、`trigger`、`level` | `response`；活动 native root Run 时等待最终 completed/failed/skipped |
| `/api/file` | `agentKey`、`path`、可选 `encoding`、可选 `response=json` | `response`；data 为 agent workspace 文件 metadata，文本文件包含 `content` |
| `/api/view` | `source,key`；connector 另带 `chatId,connectorId,hash`，可选 `usage` | `response`；来源与错误语义同 HTTP |
| `/api/view/query` | `chatId,runId,view:{connectorId,key,hash},query,params` | `response`；data 为 `{data,executedAt}`，错误语义同 HTTP |
| `/api/resource` | `file`、`pushURL` | `response` |
| `/api/upload` | gateway upload metadata | `response` |

### Channel WebSocket

`/ws/channel` 是 platform / adaptor / peer platform 专用入口，普通 UI 与浏览器客户端继续使用 `/ws`。连接时必须带 `channelId`（或兼容别名 `channel`），并且该 channel 在 `configs/channels.yml` 中必须是 `mode: server`：

```text
ws://127.0.0.1:11949/ws/channel?channelId=public-entry
```

Channel WS 复用标准 `platform-ws` 帧：`request`、`response`、`stream`、`push`、`error`。外部调用导出的本地 agent 时，payload 中使用导出名：

```json
{"frame":"request","type":"/api/query","id":"req-1","payload":{"externalAgentKey":"assistant","message":"hello"}}
```

服务端会按本地 agent 的 `channelConfig.exports` 将 `externalAgentKey`（如未显式配置则默认等于本地 agent 的 `key`）映射为本地 `agentKey`，并检查该 channel 的 `allow.query / submit / steer / interrupt / fileTransfer` 权限。`/api/file` 是例外：它直接使用 `agentKey` 读取该 agent workspace，当前不要求 export 或 `fileTransfer` 授权。

当本地 `mode: CHANNEL` agent 引用 `mode: server` channel 时，运行时会复用该 channel 已接入的 `/ws/channel?channelId=...` 连接向对端发送 `request` 帧，并按相同 `id` 收回 `stream / response / error` 帧。`mode: client` 与 `mode: server` 只表示连接建立方向，不表示 agent 的拥有方；server channel 未连接时会返回 `503 channel <channelId> is not connected`。

### Channel Agent 接出注册 v1

Channel 有两个互不替代的维度：`channel.mode` 只决定 WebSocket 由哪一方建立；本地 `mode: CHANNEL` agent 是静态接入远端 Agent，普通 agent 的 `channelConfig.exports` 才是把本地 Agent 接出给对端。本节协议只管理接出，不接收对端动态注册，也不动态创建本地 Agent。

无论 WebSocket 由谁发起，只要本 Platform 有接出项，本 Platform 都是 `agent.register / agent.unregister / agent.list` 的请求发送方：

- `mode: client`：Platform 主动连接对端，收到对端 v1 `push connected` 后开始对账。
- `mode: server`：对端连接 `/ws/channel?channelId=...`。Platform 不在该连接上发送普通 `/ws` 使用的 `push connected`，但仍发送 WebSocket ping；收到对端 v1 `push connected` 后开始对账。
- 普通 `/ws` 的 connected、heartbeat 和鉴权行为保持不变。
- 同一 `mode: server` channel 的每条活跃连接是独立 Session，各自注册、解除和查询；相同 `platformKey` 不合并。

对端声明示例：

```json
{
  "frame": "push",
  "type": "connected",
  "data": {
    "sessionId": "2f43b83d-82f1-4d8d-95b3-508a90fdb481",
    "platformKey": "desktop-standard-ws",
    "registrationMode": "MULTI_EMPLOYEE",
    "status": "ACTIVE",
    "agentRegistration": {
      "version": "1",
      "maxAgentsPerPlatformChannel": 100,
      "supportedCapabilities": ["query", "steer", "interrupt", "hitl"]
    },
    "timestamp": 1786502400000
  }
}
```

应用层 connected 等待使用 channel 的 `reconnect.handshakeTimeout`，默认 10 秒。超时只把接出注册标为 `error`，不关闭连接；迟到的有效声明仍可恢复。内容相同的重复声明会被忽略；同一连接的 `platformKey`、注册版本或 `registrationMode` 发生冲突时，该 Session 停止注册对账，其他 channel 功能不受影响。

注册对象只来自普通 Agent 上匹配 channel、且 `allow.query: true` 的 export；`mode: CHANNEL` Agent 不会被注册。请求一次只处理一个 Agent，payload 只包含 `agentKey / name / role / description / capabilities`：

```json
{
  "frame": "request",
  "type": "agent.register",
  "id": "register-agent-001",
  "payload": {
    "agentKey": "support-agent",
    "name": "售后数字分身",
    "role": "售后支持",
    "description": "协助处理售后问题",
    "capabilities": ["all"]
  }
}
```

`agentKey` 使用 `externalAgentKey`，省略时回退本地 Agent key；`name` 为空时也回退本地 key。`role`、`description` 为空时省略。能力按 export allow 映射：`query <- query`、`steer <- steer`、`interrupt <- interrupt`、`hitl <- submit`。`fileTransfer`、Skill、Tool、Tag 和 KBASE 隐式能力均不注册，也不会因注册协议扩大现有文件权限。四项全部启用且对端 v1 正好支持四项稳定能力时发送 `["all"]`；否则按 `query, steer, interrupt, hitl` 固定顺序发送显式列表。Gateway 在响应和 list 中返回展开后的能力。

每轮对账先调用 `agent.list`，其结果是当前认证连接 `platformKey` 下所有活跃 Session 的注册。Platform 只读取和操作 `ownedByCurrentSession:true` 的项目：先解除本 Session 已持有但本地不再有效的 Agent，再注册缺失项或完整更新字段不同的项，最后再次 list 验证。初始 list 失败时不执行变更；`NOT_REGISTERED` 按幂等成功处理。Catalog 热重载以 2 秒 debounce 触发新一轮对账并取消旧 generation；断线取消所有请求并依赖 Gateway 清理本 Session 路由。

请求等待 10 秒；网络错误和 5xx 最多尝试三轮，约按 2 秒、4 秒抖动退避，4xx 不自动重试。每条 Session 最多并发 4 个注册或解除请求，`SINGLE_EMPLOYEE` 固定为 1；若本地存在多个有效 export，整条 Session 标为配置错误，不会任意选择一个。

`GET /api/admin/channels` 与 `GET /api/monitor/channels` 继续在每个 export 上使用兼容字段 `cardStatus`，状态值为 `error / rejected / retrying / pending / accepted / offline`。server channel 多 Session 按最严重状态聚合；只有所有具备有效 v1 connected 的活跃 Session 均经最终 list 确认一致时才是 `accepted`，没有活跃 Session 时为 `offline`。

Channel 注册支持 Agent 接出注册、解除、查询和对账。注册成功的 `registrationId` 会保存，但尚不用于 Query/Run 路由；新版 Query Stream、Run TTL、HITL Schema、Steer/Interrupt/Submit 控制路由均未实现。注册帧与对端契约见 [Gateway Agent 接出注册协议](Gateway-Agent注册与调用协议.md)。

## 约束与注意事项

- HTTP query 参数在 WS payload 中通常以同名 JSON 字段传入。
- `GET /api/attach`、WS `/api/detach`、`POST /api/submit`、`POST /api/steer`、`POST /api/interrupt` 按 run 的公开 owner 校验 根 `agentKey`；二者不能用隐藏执行身份互相替代。
- WS 客户端离开一个 active run 时应发送 `/api/detach`；detach 只释放当前连接上的订阅流，不停止后台 run。Desktop Broker 按每个 RunChannel 串行化 detach/attach：detach 尚未写入时可由新 observer 取消，已经写入时新 attach 等其完成后再从 lastSeq 恢复，不要求不同 Run 之间建立全局切换门禁。
- WS `/api/resource` 要求 `file + pushURL`，用于将本地资源推给 gateway；`pushURL` 是 gateway HTTP 目的地址，通常为 `/api/push/...`，WS `/api/push` 不存在；HTTP `/api/resource` 直接返回文件字节。
- WS `/api/file` 接受 `agentKey`、`path` 和可选 `encoding`；省略 `response` 或传 `response: "json"` 时，文本内容在 `response.data.content` 返回，读取上限为 `file-tools.max-read-bytes`（默认 1 MiB），超出时标记 `truncated: true`。二进制文件只返回 metadata 和 `contentUrl`；`response: "content"` 仅适用于 HTTP，WS 会返回 `400 invalid_request`。
- `/ws/channel` 也允许 `/api/file`，直接按 payload 的 `agentKey` 读取工作区；它不经 `externalAgentKey` 映射，也不检查 agent export 或 `fileTransfer`。
- `.tools` 是隐藏工具内部目录，不通过 `/api/resource` 或 WS `/api/resource` 暴露；HTTP `/api/tool-result` 接受 `.tools/results/<toolId>.json`。
- `configs/channels.yml` 只接受 canonical channel：`mode`、`transport`、`protocol`、`endpoint`、`auth`、`heartbeat`、`reconnect`；旧 `type/default-agent/agents/gateway` 会使配置加载失败。
- 完整 DTO 字段以 `internal/api/*.go` 为事实源。

### Agent Terminal

Agent 终端只复用主 `/ws` 连接，不提供独立 `/ws/terminal`，也不新增顶层 `frame` 类型。终端协议仍使用 `frame:"request"` / `frame:"stream"` / `frame:"response"` / `frame:"error"`。

`/api/terminal/open` 是长生命周期 stream，语义是 Agent 级 `attach-or-create`。正式请求字段是 `agentKey`、可选 `terminalKey`、`cols`、`rows`；兼容客户端可以继续发送 `chatId`，但 Platform 不校验、不创建 Chat 目录，也不让它参与终端身份、环境或事件。`terminalKey` 是同一 Agent 内的稳定 tab key，未传时默认为 `"main"`；同一 owner boundary 下的同一 `agentKey + terminalKey` 会复用同一个 PTY，切换 Chat 不会创建新 PTY。owner boundary 由 WS 鉴权主体确定：只有同时具备 `subject + deviceId` 时才按该二元组跨 WS 连接复用；缺少 `deviceId` 或缺少 `subject` 时按当前 WS 连接隔离，因此这类连接不承诺跨 WS 重连复用。

`terminalKey` 只接受不超过 64 字节的 ASCII 字母、数字、`-`、`_`、`.`、`:`。后端会限制单 owner + agent 的 terminal 数量以及进程内总 terminal 数量，避免恶意创建大量长期存活 PTY。

```json
{"frame":"request","type":"/api/terminal/open","id":"term-1","payload":{"agentKey":"coder","terminalKey":"main","cols":120,"rows":32}}
```

open 成功后先返回 `terminal.opened`，再返回可选 replay output，之后进入 live output。所有 terminal 事件都包含 `scope:"agent"`，不包含 `chatId`；`reused:true` 表示复用了同一 Agent 的已有 PTY，`replay:true` 表示该条 `terminal.output` 来自 terminal manager 的短期回放 buffer。

```json
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.opened","seq":1,"terminalId":"term_xxx","agentKey":"coder","terminalKey":"main","scope":"agent","cwd":"/absolute/project","shell":"/bin/zsh","reused":true}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.output","seq":2,"terminalId":"term_xxx","terminalKey":"main","scope":"agent","data":"...","replay":true}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.exit","seq":3,"terminalId":"term_xxx","terminalKey":"main","scope":"agent","exitCode":0}}
{"frame":"stream","id":"term-1","streamId":"term_xxx","reason":"exit","lastSeq":3}
```

键盘输入、窗口大小变化、detach 和关闭使用普通 request/response：

```json
{"frame":"request","type":"/api/terminal/input","id":"term-input-1","payload":{"terminalId":"term_xxx","data":"ls\r"}}
{"frame":"request","type":"/api/terminal/resize","id":"term-resize-1","payload":{"terminalId":"term_xxx","cols":120,"rows":32}}
{"frame":"request","type":"/api/terminal/detach","id":"term-detach-1","payload":{"terminalId":"term_xxx","streamRequestId":"term-1"}}
{"frame":"request","type":"/api/terminal/close","id":"term-close-1","payload":{"terminalId":"term_xxx"}}
```

`detach` 只释放当前 WS 连接上的 terminal subscriber；Agent 的 PTY、cwd 与输出回放 buffer 保持不变。`streamRequestId` 必须指向当前 WS 连接上的 terminal stream；如果同时传入 `terminalId`，后端会校验两者绑定关系。浏览器隐藏 terminal 面板、SPA 切换 Chat、组件卸载都应使用 `detach`，之后用同一 `agentKey + terminalKey` open 会复用原 PTY。如果 open 请求已发出但尚未收到 `terminal.opened`，前端可只传 `streamRequestId` 进行预取消。只有用户关闭 terminal tab 时才调用 `/api/terminal/close`，该操作会结束对应 Agent 的 PTY；同样支持在 `terminal.opened` 前仅传 `streamRequestId` 做关闭预取消。

该接口定义为 Workspace Terminal。macOS/Linux 使用 Unix PTY，Windows 使用 ConPTY / PowerShell PTY；cwd 只由 Platform 从 Agent 的最终 Workspace 解析，不信任前端 cwd，也不会回退 Chat。没有 Workspace、Workspace 不存在或不是目录时拒绝打开。terminal 只冻结 `AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/.config` 与 `AP_WORKSPACE_DIR=<workspace>`，不注入 `AP_CHAT_DIR`。

## 相关文件

- `internal/server/server.go`
- `internal/server/ws_routes.go`
- `internal/server/ws_query_routes.go`
- `internal/server/ws_resource_routes.go`
- `internal/api/types.go`
- `internal/api/types_automation.go`
- `internal/ws/protocol.go`
- `docs/手工测试用例.md`

## VIEW 连接器 API

HTTP GET 和 WS `/api/view` 对 connector 接受 `chatId/connectorId/key/hash/usage?`，hash 必填，返回 Chat 快照中的 HTML/QLC 文档和声明资源。工具结果与表单事件携带 `view/viewError`，业务结果与提交协议不变。

助手正文中的 Markdown VIEW 块由 Run 在块闭合时绑定：`content.view` 携带 `{contentId,runId,taskId?,index,view?,viewError?}`，`index` 为该 content 内已闭合 VIEW 块的序号；`content.snapshot` 以 `views:[{index,view?,viewError?}]` 汇总并持久化到 assistant message，冷回放的 `content.snapshot` 原样返回。原文不改写，绑定不进入模型上下文。`viewError` 取 `invalid_view`、`view_not_found`、`view_unavailable` 或 `view_data_invalid`。

`POST /api/view/query`（WS 同名）执行 VIEW 快照声明的只读查询：请求 `{chatId,runId,view:{connectorId,key,hash},query,params}`，响应 `{data,executedAt}`（executedAt 为 Unix 毫秒）。查询来源为登记的原生操作或声明包自身的 CLI/MCP，三者结果形状一致；按查看者的 Chat 权限授权，不经过工具准入，不复查挂载；连接器失败保留 `connector_*` 错误码且不返回 401/403；`runId` 必须属于该 Chat，其根 agentKey 进入查询上下文。完整定义、隔离和迁移步骤见 [VIEW连接器](VIEW连接器.md#view-查询)。

技能候选的 `icon` 使用 `/api/skills/icon?id=...`，读取技能中心图标，不因 Agent 改变。缺图省略 `icon`；HTTP/WS 共享字段，图片校验与 ETag 缓存保留。旧带 `agentKey` 的图标请求仍按 Agent 运行副本解析，不影响新全局目录。


## 用户技能置顶

`GET /api/skills` 在技能列表旁返回当前用户的 `pinned:string[]`，技能包、独立技能与包成员共用这一有序数组：包使用包 ID，独立技能使用自身 id，包成员使用 `<package>/<skill>`。例如 `["office","pdf","office/export"]` 中三个置顶项互相独立；置顶或取消整个包只更新 `office`，不会展开、新增或移除成员的置顶，也不能从成员全部置顶推断包已置顶。新置顶在前，重复设置保持原位置。

`PUT /api/skills` 接收 `{id:"office",pinned:true|false}`，返回 `{agentKey:"",skills:[],pinned:[...],packages?}`；沿用列表的包展示投影与请求语言，不暴露存储的 version/updatedAt。写入不需要 agentKey。非法 id 或缺少 boolean pinned 返回 400；包必须是当前目录扫描识别且元数据有效的已安装技能包，不存在或包元数据损坏时置顶返回 404。普通技能仍沿用已安装技能的准入规则，取消置顶允许清理已删除或损坏条目的旧偏好。锁内单条合并并原子保存，存储格式不变。旧 `/api/skills/order` HTTP/WS 路由已删除。

Platform WebSocket 注册同一路径：空 payload `{}` 或 `{agentKey}` 对应 GET，`{id,pinned}` 对应 PUT，出现 id 或 pinned 即按写入校验，不完整写入返回 400；复用相同存储及错误语义。HTTP 不缓存用户置顶响应。用户身份只取已验证 Principal 的 subject，忽略客户端指定的 userKey；认证开启但没有用户身份时拒绝。认证关闭的本地部署使用独立 `local` 记录。

唯一持久化位置是 `<runtime>/skills-center/order.json`，具体根目录复用 `Config.Paths.SkillsCenterDir`。文件格式：

```json
{
  "version": 1,
  "users": {
    "user:<subject>": {
      "order": ["office", "pdf", "office/export"],
      "updatedAt": 1788912000000
    }
  }
}
```

同一用户的全部 Agent 共用一份 order，不存在 agentKey 维度；不同用户的记录互相隔离，接口只返回当前用户的记录。旧 version 1 文件及现存技能、成员 id 原样保留，不迁移为包 ID，也不从成员偏好合成包置顶；包 ID 直接加入同一字符串数组。未写入前 GET 返回空列表，不创建文件；重启后读取原文件，损坏或未知版本不自动覆写。技能中心只加载技能目录，order.json 与原子写临时文件均不触发技能/Agent 重载。置顶只影响可见条目的展示顺序，不改变 Agent 技能配置、Query skill 引用 或技能权限；包 ID 不进入 SkillDefinition，也不能作为 skill 引用执行，选择包执行时仍须展开为有效成员的完整 id。

### 连接器中心置顶顺序

`GET /api/connectors/order` 返回当前用户的 `{version:1,order:[connectorId],updatedAt?}`。`PUT /api/connectors/order` 接收 `{id:connectorId,pinned:boolean}`；WebSocket 同路径的空 payload 读取、非空 `{id,pinned}` 更新。HTTP 和 WebSocket 共用服务端认证主体，忽略客户端用户与 Agent 路由提示，未启用鉴权时使用本地用户。

偏好原子保存到 `runtime/connectors-center/order.json`，文件结构与技能中心一致，按 `users` 隔离，同一用户所有 Agent 共享。新置顶排在最前，重复请求幂等，取消后保留其他项的顺序；未安装的连接器不能新增置顶，已移除的连接器仍可取消置顶。内置只读连接器同样可以置顶，偏好不修改包内容、挂载、配置或登录凭证。两个中心的顺序彼此独立，根目录下的偏好及临时文件不会触发 Agent 热重载。连接器目录扫描跳过外部中心根目录下的普通 `order.json` 偏好文件与隐藏临时文件，写入置顶后列表、导入校验和运行时装配仍读取真实连接器包；同名目录、符号链接及其他异常包条目继续按包规则校验。

技能中心列表也读取 `/api/skills` 的 `pinned`，与 Composer 共用用户置顶顺序；已安装但无效或禁用的技能可在管理列表置顶，置顶不会改变其可运行状态或令其进入 Composer 可用候选。

### Agent 发起的 steer 来源

builtin.task-control 的 chat_steer 接收 `{runId,message}`。公开 request.steer 在 SSE、WS 与 `/api/chat` events 中带可选 `origin:{type:"agent",agentKey,chatId,runId,toolId}`，与 query 行的 `origin` 同一结构；这些是发起方身份，事件顶层 chatId/runId 是目标身份。role 仍为 user，message 与模型输入不添加来源标注。HTTP/WS `/api/steer` 传入 origin 按未知字段拒绝；普通人类 steer 不产生 origin。JSONL 将 origin 保存在 steer 对象中；改名前写入 `source` 的旧记录照常读取，回放时以 `origin` 输出。Agent 发起的 steer 不作为 memory 证据。

```json
{
  "type": "request.steer",
  "timestamp": 1791700000000,
  "chatId": "chat_A",
  "runId": "run_A",
  "steerId": "steer_3f9a1c7e2b4d",
  "message": "优先完成后端权限校验和越权测试；暂缓管理界面。",
  "role": "user",
  "origin": {
    "type": "agent",
    "agentKey": "initiator-agent",
    "chatId": "chat_P",
    "runId": "run_P",
    "toolId": "call_7"
  }
}
```

chat_steer 不审批、不比较双方权限档位，保留目标原有工具授权。调用身份派生的 steerId 与目标事件相同；首次结果（含未受理）在同进程同调用内冻结，参数变化返回 idempotency_conflict。收据随调用方 Run 结束清理，在途调用完成后释放；重放等待可取消，且不覆盖首个调用结果。WS 未入队、发送前校验失败与上游明确拒绝返回 executionState:not_started；SSE 传输失败、超时或响应无法解析返回 executionState:unknown。来源不传上游，同次调用不重发，未知结果不自动补发。完整所有权、等待和调用边界见 [子智能体调度](子智能体调度.md#chat_steer)。

### planning 期间的 steer

活动原生 planning Run（GENERAL/CODER/KBASE）的一轮规划结束不会关闭 Run 的 steer 队列。生成候选计划期间入队的 steer 会在当前模型调用结束后使该计划失效，并在同一 Run 中生成新 revision；等待确认期间收到 steer 也会立即使旧确认失效并唤醒规划。旧计划失效统一发布 `planning.superseded`，字段为 `planningId`、`planningFile`、`awaitingId`、`reason:"steer"`；客户端应将该计划标为已失效，不再允许执行。已有确认框还会收到 `awaiting.answer(status:"error", error.code:"planning_superseded")`，没有确认框时不会补造 `awaiting.ask` 或 `request.submit`。

steer 与 approve 原子确定先后：steer 先入队时，旧确认的 submit 返回 `409 already_resolved`；approve 先被接受并创建 execution continuation 时，旧 Run 的 steer 返回 `accepted:false,status:"unmatched"`。后续指令应发往新 execution Run。新计划仍需重新确认。此行为适用于仍有 planning 执行者的活动 Run，跨进程重启统一取消等待项，不通过 submit 恢复；完整时序见 [HITL协议](HITL协议.md)。

### 附件 steer

`POST /api/steer` 和普通 WebSocket `/api/steer` 共用 Runtime 入口。图片和普通文件先通过 `/api/upload` 上传到当前 Chat，随后将返回引用放入可选 `references: Reference[]`；`message` 可为空，也可以完全空白（无文字、无引用）表示继续；同一主 Chat 的首次 query 要求非空正文，后续 query 可只带有效文件、对话或选区引用，仅最后一次主 Run 明确异常结束或取消后允许完全空白表示继续；空 query 的 message 保持为空，仅模型输入补充英文继续指令，steer 接受完全空白，表示继续（公开 message 为空，模型输入补充独立的“立即进行下一步”指令）。`chatId` 缺省时从 Run 补齐，提供时必须匹配。skill/kbase/chat/site 经 Runtime 类型准入；文件仅接受当前 Chat 的资源相对 URL。技能或知识库引用不能单独充当空正文的可发送内容。Host/Container 路径由冻结的 Run 环境重新解析，不信任客户端 path/MIME。格式及单图 20 MiB 上限复用多模态 loader。

普通 native Agent 与 Team 协调器在原有安全点接收纯图片、纯普通文件或混合附件。HTML/MD 等普通文件作为经校验的引用供工具按需读取，不要求视觉模型；不会自动执行 HTML。图片在实际文件类型检查后走多模态 loader；视觉模型接收图片块，非视觉模型仅接收图片文件引用，供已配置的图片识别工具按需读取，不因缺少原生视觉能力拒绝 steer。任一资源不可用时整条拒绝（ack `accepted:false,status:invalid_reference`）；未支持的远端 PROXY/CHANNEL 附件路径返回 `unsupported`。校验期间 Run 已结束返回 `unmatched`。视觉模型的图片在准入时读取并冻结，入队后同名文件修改不会替换图片输入；普通文件以及非视觉模型的图片仅将引用元数据与路径放入模型上下文，工具读取时获得文件的当时内容。`accepted:true` 表示已入队；实际消费仍以 `request.steer` 事件确认，不新增已消费或持久队列保证。

公开 `request.steer` 携带 `references`，不携带图片 Base64。新 steer JSONL 保存 message/references 及 Agent 调用的可选 source，不产生内部 `request.steer.snapshot`，不写 `messages`，不重复进入后续 step 的 `inputMessages`。续聊按当前模型能力从 Chat 资源重建附件输入；图片不保留历史版本，失效附件提示不可用，不阻断续聊。旧纯文本与已有 messages 快照继续读取。公开协议保持不变。


## Office 在线预览 HTTP API

以下接口仅使用普通 HTTP 与现有 Platform 鉴权，不通过 Run、WebSocket action 或 Agent 工具执行；响应沿用 `ApiResponse`，`Cache-Control: no-store`。

`GET /api/document/preview/capabilities` 的 `data`：

```json
{"enabled":true,"supportedExtensions":["docx","pptx","xlsx"],"maxFileBytes":52428800,"openMode":"iframe"}
```

能力响应不暴露服务内部地址或凭据。`POST /api/document/preview` 的两类请求体：

```json
{"requestId":"preview-unique-id","source":{"kind":"workspace-file","agentKey":"coder","path":"reports/summary.xlsx"}}
```

```json
{"requestId":"preview-unique-id","source":{"kind":"chat-resource","chatId":"chat-123","relativePath":"artifacts/report.pptx"}}
```

`relativePath` 是解码后的 ChatScope 相对路径，支持 Artifact、Reference、上传文件、可读取的 Team 与归档资源。不得混用两类 source 字段，也不接收任意外链。Platform 复用 `/api/file` 和 `/api/resource` 的定位、越界及读取权限检查，每次请求（包括缓存命中）重新鉴权并读取普通文件快照，校验后缀、Office 包结构与大小。

成功 `data`：

```json
{"previewId":"opaque-id","sourceRevision":"sha256:content-hash","openMode":"iframe","url":"http://127.0.0.1:8090/s/opaque-share-token","expiresAt":1789084800000}
```

`expiresAt` 为毫秒 Unix 时间。成功表示上传及只读链接准备完成，渲染由 document-hub/ONLYOFFICE 页面执行，无转换轮询接口。固定分享策略为 `expiry: "1d"`、无密码、禁止下载/打印、允许复制；原文件下载继续走 Platform。

相同用户、服务/认证作用域、文件身份及内容 SHA-256 复用副本，并发请求合并上传与分享准备。同一 `requestId` 绑定文件版本；变更文件必须使用新 ID，否则返回 409。文件修改上传新副本；链接到期前一分钟内重新创建链接；远端副本已删除则重新上传。前端刷新重新请求检查版本，不应把 URL 持久化为文件身份。

错误使用 HTTP 状态、`msg` 和可用时的 `data.code`：400 非法请求/来源，403 无读取权限，404 文件或副本缺失，409 版本变化或 requestId 冲突，413 超限，415 不支持/无效 Office，503 未配置或凭据不可读，502 上游失败/响应不合法/上传结果未知，504 请求等待超时。上传接口无幂等键，结果未知时不自动重试；并发等待者共享失败，用户重新发起新 requestId 后才再尝试。已有远端 ID 会在创建分享前落盘，分享失败可复用该副本重试。

本地读取权限变化只阻止后续预览请求，不会实时撤销已签发的公开分享；链接仍受一天有效期约束。配置和副本回收规则见 [配置化说明](配置化说明.md#office-在线预览服务)。

### 连接器 ZIP 导入身份校验

`POST /api/admin/connectors/import` 的 multipart `file` 接收单个 ZIP；可选 `overwrite`、`expectedId`、`expectedVersion`。后两者与包内清单不符时返回 HTTP 409，`data.code: connector_identity_mismatch`，不发布、不准备 CLI。导入目标由 Platform 固定为 `connectors-center/<id>/`。Agent 的 Skill/Connector ZIP 导入复用这些发布机制，通过 `catalog_manage.import {resourceType,archivePath,overwrite?}` 调用；审批、文件权限及进程内收据见 [ZIP 包导入](Platform控制工具设计.md#zip-包导入)。

### CLI 独立准备

`/api/admin/connectors/prepare?id=<id>` 支持 GET 查询、POST 准备/重试和 DELETE 取消。状态为 pending/preparing/ready/failed/canceled，与 `/api/admin/connectors/auth` 登录状态独立；准备时来源 mutation/登录返回 409。ZIP 导入响应保留 installed（仅表示包已发布），追加 preparation，CLI 初始化异步执行。详情见 [连接器安装与授权](连接器安装与授权.md#cli-准备与隔离)。

### Desktop 普通技能事务与并发保护

`POST /api/admin/skills/transaction` 使用现有管理接口鉴权。请求字段为 `key`、`operation`（`snapshot` / `replace` / `delete`），变更必须提交 `expectedRevision`；`replace` 另携带 ZIP 的 `archiveBase64`。响应为 `{key, exists, revision}`，`snapshot` 在资源存在时额外返回完整 `archiveBase64`。不存在的版本为 `missing`；存在资源的版本是固定时间戳、排序目录与文件、保留权限的完整 ZIP SHA-256。

Platform 在同一 Catalog 保护区内取得快照、比较版本、替换或删除、重载并返回已提交版本。替换 ZIP 先在保护区外解压校验，进入后重新比较版本。只读 snapshot 不暂停监听或触发 reload；目录变更只暂停技能根的 watcher，恢复时按内容差异补偿检查，避免 API 与 watcher 重复重载。版本不匹配返回 409 `revision_conflict`，不改变资源。Desktop 只能用首次快照归档和本次成功提交的版本执行条件恢复；超时或丢失响应时不得自动反向操作。删除仍遵循 Agent 使用保护及技能包归属约束，包内子技能删除继续使用既有技能包 API。

快照包含隐藏运行配置和元数据，不校验旧技能正文是否有效，不跟随符号链接或归档特殊文件。ZIP/单文件上限 32 MiB，展开总量上限 256 MiB，最多 4096 项；快照超限则阻止变更。快照是管理端敏感数据，不可广播或写入公共日志。归档恢复仍经过导入校验，旧无效内容可能无法自动恢复，此时调用方应保留恢复归档并报告失败。

事务保护协调 Platform 内部管理写入与 watcher；不能锁住用户编辑器或其他外部进程。既有导入/删除接口保持兼容。

纯文本选区 steer 使用 `references:[{type:"selection",text:"选中文本",annotation:"可选批注"}]`；`text` 必须为非空字符串，`annotation` 为可选字符串。选区在准入时冻结并作为文本注入，不要求视觉模型、不授予客户端 path/URL 文件访问权限。消费后的原始 message/references 进入 steer JSONL，用于回放和续聊重建；btw/explain 仅写各自隐藏分支。selection 可以与当前 Chat 的文件引用混合使用，HTTP/WS 控制归属规则保持一致。

## 网页 Surface 契约

每个网页是一个独立 Surface，由 `surfaceId` 标识；导航和刷新保留身份，关闭重开使旧身份失效。网页控制工具以 `surface_list` 发现本次 Run 已授权的网页，`surface_state` 省略 `surfaceId` 时读取所属应用的当前页；所有页面操作只使用 `surfaceId`。Desktop 协议中的 `containerId` 与 WorkPanel 条目 ID 不出现在模型工具的参数和结果里。`surface_*` 工具在 Platform 内映射为 `desktop.cdp.call` 的 Surface.list / Surface.getCurrent / Surface.getState / Surface.goBack / Surface.close 等 Desktop 方法和受限 Chromium 方法，由 Desktop 定位到精确 webContents 后执行。

普通 Chat 用 `workpanel_open` 打开网址，返回 `surfaceId` 与状态；这是打开新页面的唯一入口，Website 内的多个标签在 `surface_list` 中是多个独立 Surface。Website/WebApp Copilot 沿用所属应用 Run grant。发现与操作使用相同授权范围，后台页面不因隐藏失效，其他 Chat、文件预览与任意应用不可借此访问。`awcp_manual` / `awcp_invoke` 接受可选 `surfaceId`，只能在已有应用 grant 内选页；省略时沿用该应用活动页，不改变页面桥权限。Platform、Desktop 与技能必须配套发布。

划词可携带正整数 `annotationIndex`，独立于 Reference ID，页面气泡编号与模型称呼 `Annotation N` 均使用该值。没有批注文字时仍保留编号；编辑、删除其他引用不重排编号。编号随 query/steer 引用持久化，未提供编号时不生成编号字段。

主 Chat 的纯引用后续 query 在 HTTP/SSE 与 WebSocket 共用准入校验：以服务端主 Chat 摘要或已保存的 request.query 判断历史，预分配 chatId 和上传创建的空 Chat 不算已发送。引用继续执行既有校验和模型输入转换，不添加默认正文。BTW/解读的正文要求及传输方式保持现状；chat_start 工具入口仍要求文字。

完全空 query 额外要求服务端 `canContinue:true`。该布尔值仅由 `/api/chat` 详情返回，导航列表省略；客户端从详情与运行事件维护当前会话的续执行资格。服务端由最后一次持久化主 Run 计算：只允许已结束的 `error/cancel`，兼容 `cancelled/canceled/interrupted`；正常完成、未知终态、新 Run 已启动但尚未结束、等待人工交互以及无历史均为 false。不能回退到更早一次失败或取消，也不因浏览器断线推断异常中断。已有历史但不满足该条件时返回 HTTP 400、`empty_query_not_allowed`。原始 message 保持为空，模型输入补充 `Continue based on the current conversation context.`，纯附件 query 不受该额外限制。


### HITL 提交与创建通道分离

`/api/submit` 不要求创建与提交的 transport、device boundary 或 lane 相同，允许桌面创建、手机审批及 HTTP/WS 交叉提交。HTTP/WS 的既有认证、Agent/Team owner、等待项和提交参数校验及重复提交仲裁保持不变；其他 Run 控制入口仍执行原通道归属检查。

`chat_start` 工具只接受 `agentKey` 作为目标字段，省略时从可信调用上下文补齐当前 Agent；目标使用精确 Agent key。显式 `agentKey` 必须为非空字符串，续聊仍校验 Chat owner，审批与幂等使用补齐后的目标。`chat_start` 由服务端读取可信父 Run 的控制记录，继承连接归属（含 transport/lane），执行生命周期仍使用独立后台 context。创建来源和父级关系保存在 query 行的 `origin`（type agent）；派生链始终继承最初 HTTP/WS 入口的 transport/lane；父级记录缺失或不是 HTTP/WS 时明确失败，不创建空来源的新 Run。不迁移或重写已有 Run 的控制记录。

## 通用智能体根目录与项目目录

`runtimeConfig.workspaceRoot: "@root"` 显式表示通用根目录智能体。Catalog 保留该意图，同时把运行时 Workspace 解析为本机绝对根目录：macOS/Linux 为 `/`，Windows 为 Platform 当前驱动器根目录。工具相对路径、`@workspace`、运行上下文和目录权限使用解析后的真实路径；该值不增加跨盘权限，不绕过 readonly、审批、KBASE 根目录限制或根目录扫描禁令。

公共 HTTP/WebSocket `/api/agents`与 Admin 摘要的 `workspaceDir` 只表达项目目录：`@root` 省略该字段，普通绝对目录（包括显式 `/`）继续返回解析后的目录，未配置时也省略。Admin 原始 definition 保存 `"@root"`，编辑、导入、重载不得把它改写为实际根路径。Desktop 据 `workspaceDir` 是否非空区分 Projects，不按 mode 或路径形状猜测通用身份。现有通用智能体的 `/` 配置需显式改为 `"@root"`；不自动迁移所有根路径，以免改变显式项目配置的语义。

### Project Git WebSocket 控制面

`/api/project/git` 的 WS payload 为 `{agentKey}`。`/api/project/git/branches` 读取 payload 为 `{agentKey}`；存在 `operation`、`branch` 或 `expectedRevision` 任一字段时按写请求处理，完整写 payload 与 HTTP POST 相同。两种传输复用 Project Service、目录边界、分支保护和 revision 冲突校验；错误保留 HTTP 状态码和领域 code。WS 写请求失败不自动重试或回退 HTTP。

WebClient 先检查有效 `workspaceDir`，没有 Workspace 不查询；有 Workspace 首次按需探测，不要求 `expectedBranch`。快照采用短期内存缓存，分支列表按需加载，写入成功复用返回快照，写前仍由服务端实时校验。

### 连接器配置状态接口

连接器配置使用 `configured`，不提供独立启用开关。HTTP `GET /api/connectors/connection[?id=...]` 读取本地快照；`POST /api/connectors/connect?id=...[&component=...]` 开始既有认证流程；Token 通过既有管理端 credentials 提交；`POST /api/connectors/check?id=...[&component=...]` 显式验证；`POST /api/connectors/disconnect?id=...` 清理本地授权并保留私有设置/数据。不新增 WS 对应路由。详情见 [连接器](连接器.md#配置完成与本地授权) 与 [连接器安装与授权](连接器安装与授权.md#token-保存验证与状态读取)。

### L1 推理清理统计

层级标识继续使用 `l1_tools`。完成响应和实时 `context.compact.complete` 可附带 `reasoningCleared`，表示排除推理的 assistant 消息数；`toolsCleared/toolsKept` 统计工具，`tokensFreed` 统计合计收益。自动 L1 统一由完整输入达到 90% 触发，不再设置独立 reasoning 阈值。


## 技能展示字段与语言

技能对象新增 `displayName/version/revision`；名称和描述按 HTTP 请求语言或 WebSocket 连接语言解析，API 不返回 i18n 表。技能对象只返回 `id/displayName`，不返回 `name`；缺少显示名称时用 SKILL.md 的 name 回退，语言切换后客户端须重新请求。字段、版本兼容规则及完整示例见 [技能展示元数据](技能展示元数据.md)。


## 已发布产物读取

这些接口属于 Chat 资源能力，不属于连接器。均为 POST，即使 `auth.enabled=false` 也要求有效 JWT 和非空 subject；每次请求读取当前 Chat 摘要并复用 principal 的 Chat 引用权限。`query:<subject>` Chat 限该主体访问，旧无 owner 或非 query 来源沿用现有引用规则。唯一例外是 Desktop 运行形态下的受信 app principal（非空 subject、`scope=app` 和 device claim），可跨 owner 调用严格路由 `/api/chat/artifacts/read` 并且只能使用 `sourceRef`。该例外不扩展到 list/get、`artifactId` read、Standalone 或通用 `/api/resource`。

| 路径 | 请求 | 返回 |
| --- | --- | --- |
| `/api/chat/artifacts/list` | `{chatId,runId?,cursor?,limit?}` | `{items,nextCursor?}`，默认 50、最多 100 |
| `/api/chat/artifacts/get` | `{chatId,artifactId,runId?}` | 产物元数据 |
| `/api/chat/artifacts/read` | `{chatId,artifactId,runId?}` 或 `{chatId,sourceRef}`，定位参数二选一 | 文件字节，失败为 JSON 错误 |

元数据包含 `chatId/runId/artifactId/publishedAt/name/mimeType/sizeBytes/sha256`，不包含内部路径。`sourceRef` 只接受规范的 `artifacts/<runId>/<file>`，必须精确命中当前 Chat 的发布 manifest；同一 `sourceRef` 多次发布时以 manifest 中最后一条为准。仅查询 active Chat 的发布 manifest，无全局 artifactId 查询和任意路径读取；歧义返回 `artifact_ambiguous`，内容或摘要变化返回 `artifact_changed`，请求取消关闭读取文件。输入严格拒绝未知字段。

旧 `/api/webapp/artifact/*` 路由及其 410 拒绝处理已删除。客户端切换到上述接口，使用自身可信 JWT，继续在客户端校验其内部访问范围并通过请求取消终止读取；不得把 JWT 暴露给不可信调用方。迁移需与连接器旧传输退役同批发布。


### 无认证连接器

连接器 `authMode: "none"` 无需登录或配置完成标记。connection 接口返回 `configurationRequired=false`、`configured=false`、`authentication.status="no_auth"` 和 `canConnect/canDisconnect/canCheck=false`；无需准备或准备完成时 readiness 为 no_auth，并不代表客户端在线。客户端显示“无需配置”，隐藏认证操作。connect/disconnect/check 返回 HTTP 409 和 `connector_auth_not_required`。Desktop 已使用此模式；Agent 挂载、执行授权与客户端能力检查仍有效。完整约束见 [连接器安装与授权](连接器安装与授权.md#no_auth-状态与客户端接入)。

技能目录 WebSocket `/api/skills` 的列表和置顶写响应统一使用连接语言。通过 `/api/locale` 切换连接语言，不向业务 payload 添加 `locale`；不支持的语言由 `/api/locale` 返回 400 `invalid_locale`。

技能包图标：包根目录可放 `icon.svg` 或 `icon.png`，SVG 优先，不读取包根 `assets/`，不向子技能继承。管理包列表及 `/api/skills` 的 `packages` 返回可选 `icon` URL：`GET /api/skill-packages/icon?id=<package>`。图标接口沿用 API 鉴权、私有 ETag 缓存与图片校验，非法 id 返回 400，包或图标不可用返回 404；SVG 拒绝脚本及外部资源，符号链接图标不提供。独立技能和包成员仍从各自 `assets/` 读取图标。


### Skill ID 字段统一

技能列表、Agent 技能、私有技能、连接器技能摘要和技能管理/文件/transaction 请求响应统一使用 `id`；query 与 multipart 参数同样为 `id`。`PUT /api/skills` 使用 `{id,pinned}`，图标参数为 `id`。`/api/admin/source` 的 skill/skill-package target 使用 `id`，agent/automation 仍使用 `key`。技能包导入与删除使用包 `id`；成员删除继续使用 `{packageId,skillId}`。连接器技能详情使用 `?id=<connectorId>&skillId=<skillId>`。旧包清单与 ZIP 身份的读取兼容见 [技能展示元数据](技能展示元数据.md#技能标识命名)。

### 技能管理目录与置顶 HTTP 契约

`GET /api/admin/skills` 的 `data` 为 `{skills:[],packages:[],pinned:[]}`，三个字段必返且不为 null。技能与包由同批目录记录投影，在现有 catalog mutation coordinator 内读取；置顶是独立的用户偏好快照，不声明跨存储事务。目录或偏好读取失败整体返回错误，无部分成功。管理列表与置顶接口设置 `Cache-Control: no-store`，按请求语言投影技能和包展示字段。

`PUT /api/admin/skills/pin {id,pinned}` 仅支持 HTTP，与 `/api/skills` 共用同一个用户级 `skillOrder` store 和 `skills-center/order.json`。身份来自认证上下文，关闭认证时沿用 local 用户。新增置顶只接受管理目录里的独立技能或技能包；取消置顶允许清理合法但已失效的 ID（包括旧成员键）。pinned 必须显式提供，重复置顶幂等且不移动位置，取消后再置顶才移到最前。写入成功直接返回 `{pinned}`，不再读取目录，不触发 catalog reload。

使用端 `/api/skills` 的 HTTP/WS 目录、配置标记与置顶契约不变。管理页不得借用此接口或建立业务 WS。旧 `GET /api/admin/skill-packages` 保留供 Desktop，包 import/delete/skills/delete 和 source 编辑接口保留。管理列表使用对象响应，客户端必须按该结构解析。

## Mailbox 会话投递

Mailbox 默认关闭；在 `configs/runtime.yml` 设置 `mail.enabled: true` 并重启后启用。关闭时邮件列表与动作接口返回 503，不创建或打开 `mail.db`，详见 [启用与关闭](Mailbox.md#启用与关闭)。

会话投递增加 GET /api/mails 与 POST /api/mails/action，广播 mail.updated；request.query/request.steer 在顶层记录 mailId、mailKind、mailAttempt、mailHop，reply 另有 outcome；submit 可带平台生成的 origin/accessGrant 审计。字段不能由公共输入伪造，JSON 仍使用统一包裹。具体参数与权限见 [Mailbox](Mailbox.md#http-与展示)。列表及动作的 JSON 字段名不随 MAILS 数据库列名变化；purgedAt 只表示邮件正文已清理，创建会话、父邮件和回信关系继续保留，HTTP/JSONL 协议不变。


## Agent 默认权限与来源

Agent YAML 的 accessConfig.default 是默认值，使用目录 /api/agents、/api/agent 及对应 WS 返回有效 defaultAccessLevel。普通 /api/query、/api/execute 与 BTW/explain 在省略 accessLevel 时取目标冻结定义的默认，未配置为 ask_for_approval；显式请求优先。关闭 interactionConfig.accessLevel 时仅接受省略或等于 Agent 默认的显式值。活动 Run 当前档位与 Agent 默认分别展示，运行中切档不写回 YAML。

Agent 发起 chat_start/chat_send 时，省略采用目标默认且可高于父 Run；显式高于父当前档位返回 run_start_access_level_exceeded，executionState=not_started。目标准备与准入快照变化返回可重试的 run_start_target_changed。Automation 省略仍固定 ask_for_approval。

JSONL query 原始输入不补默认。顶层 accessLevel 保存有效值，accessGrant.source 可为 explicit/agent_default/platform_default/automation_config/automation_default/continuation，目标 revision 与父基线在适用时一起冻结。旧 inherited/approved 历史继续读取，不按新配置重算。

计划批准的 param 可带 executionAccessLevel（三个档位之一），用于确认等待期间升高的 Agent 默认。未确认升高返回 HTTP 409/plan_access_level_changed，data.executionAccessLevel 返回当前值，等待项不消费；客户端先刷新展示，再由用户重新确认。它不替代原始显式请求或规划期间用户切档。


### 默认权限选择与具体档位

Query、execute、side query 和子执行器的输入 `accessLevel: default`（或省略）表示由后台解析目标 Agent 的 `accessConfig.default`；Agent 未配置时使用 `ask_for_approval`。`default` 不再表示请求审批，也不是执行权限档位。用户显式选择使用 `ask_for_approval`（Ask for approval）、`auto_approve`（Auto-approve）或 `full_access`（Full access）。运行中的切档接口只接受这三个具体档位。

Runtime 受理时冻结具体权限与来源，JSONL 顶层 `accessLevel`、Run 状态和权限事件使用具体档位；原始 query 保留请求中的 `default` 或省略，不改写。旧 JSONL 顶层 `default` 是旧版已冻结的请求审批决定，回读为 `ask_for_approval`，不依据当前 Agent 配置重算；旧规划记录中显式请求审批的执行续接也保留原权限。

Automation 保持独立的场景默认：省略或 `default` 使用 `ask_for_approval`，不采用 Agent/Chat 的高权限默认。Gateway 与 Desktop WebApp execute 的固定低权限调用明确发送 `ask_for_approval`。Platform、WebClient、Desktop 和 Gateway 应同步升级，旧调用方若要明确请求审批，必须改发 `ask_for_approval`。

## Linux Compose WebApp 前置诊断

`GET /api/admin/webapps/host` 仅在 Linux standalone、显式 `--webapp-host=compose` 且 `runtime.yml` 的 `webapps.enabled: true` 时注册。启用还要求 JWT issuer/audience/expiry 和唯一 allowed-subjects owner；接口不接受 WebApp/连接器执行 token。未启用不注册路由；认证后 subject 非 owner 返回 403，宿主状态未装配返回 503；响应 no-store，包裹为 `{code,msg,data}`。

`data` 含 host/os/arch、nodeVersion/nodeReady、supervisorReady、landlockAbi/isolationReady、backendPrerequisitesReady、hostingAvailable、stage 和 issues。当前 stage 为 foundation，hostingAvailable 固定 false；它只报告运行前提，不提供应用安装、访问或 Node 启停能力。

启用时，Host 分类先于平台 API/CORS/WS。未知或应用内容 Host 不回落平台路由；仅 loopback peer 的精确 loopback:平台端口 GET /healthz 保留健康检查。内容域 Origin 禁止进入平台 API/主 WS，不使用 X-Forwarded-Host 推断平台身份。完整范围及限制见 [WebApp 托管](WebApp托管.md)。
