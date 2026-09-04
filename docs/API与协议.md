# API与协议

## 当前状态

运行时提供 HTTP REST、SSE 与 WebSocket 三类协议入口。REST 承载 catalog、chat、automation、memory、resource 等请求；`POST /api/query` 使用 SSE 返回实时 run stream；`GET /ws` 是 WebSocket 控制面，复用一批 `/api/*` route，并用 `stream` frame 承载实时事件。

所有非 SSE HTTP JSON 接口统一返回：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

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

Chat JSONL 每条物理行只允许一个 JSON object，`_type` 必填且只允许 `query`、`react`、`react-tool`、`event`、`steer`、`submit`、`compact.checkpoint`、`compact.run.checkpoint`、`compact.tool`。空行、多行 object、同行多个 JSON 值、数组、标量、语法错误、非法 `_type` 及非法 system/planning/awaiting 结构统一返回 HTTP/WS `422 chat_storage_schema_violation`。

`_type:"steer"` 的持久化行使用专用 `steer` object，不使用通用 `event`：顶层 `updatedAt/liveSeq` 分别保存事件时间与公开 live cursor，`steer` 只保存 `requestId/chatId/runId/steerId/message/role` 业务字段，且 `requestId` 为空时省略。回放时重新合成扁平 `type:"request.steer"` 与 `timestamp`；SSE、WebSocket stream 和 `/api/chat.events[]` 的对外事件结构不变。旧 `_type:"steer" + event` 以及 `_type:"event" + event.type:"request.steer"` 均属于不支持的存储 schema，不兼容读取或迁移。

HTTP 的 `data.error` 与 WebSocket error frame 的 `data` 包含 `code`、`field`、`location`、`expected`、可选 `actual`、`status:422`、`retryable:false`。`location` 使用 1-based 物理行号；响应不会携带完整 JSONL 行或 system prompt。时间字段不合法仍使用 `time_contract_violation`。

## 核心流程

`platform_control` 是 run 内的 system tool，不是 HTTP 协议扩展。动态环境变量由普通 native root Agent 在当前 run 中调用 `run.env.set/unset` 修改；`POST /api/query`、前端和 Chat API 都不传 `documentId`、run env 或 platform-control 授权字段。

```text
普通 JSON API -> ApiResponse envelope
POST /api/query -> SSE message events -> data: [DONE]
POST /api/btw -> hidden read-only branch -> same SSE message events
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
| GET | `/api/agents` | query: `includeChats`、`includeTeam`、`scope`、`mode` | agent 列表；可选混入 Team 与最近 chat 摘要 |
| GET/PUT | `/api/agents/order` | PUT body: `order` | 全部有效 runtime Agent 的 catalog 顺序 |
| GET | `/api/agent` | query: `agentKey` | 单个运行时 agent 详情，不返回编辑专用字段 |
| GET | `/api/skills` | query: `agentKey` | 有效技能中心 Skill 与该 Agent 已配置 Skill 的并集 |
| POST | `/api/agent/model-config` | body: `agentKey`/`key`、`modelKey`、`reasoningEffort` | 更新 CODER agent 的运行时默认模型配置 |
| POST | `/api/agent/open-directory` | body: `agentKey`、`directoryType` | 打开 Agent 工作目录或配置目录 |
| GET | `/api/teams` | 无 | 目录式 Team 列表 |
| GET | `/api/skill-candidates` | query: `agentKey` | skill candidate 列表 |
| GET | `/api/model-options` | 无 | 聊天运行时可选模型与思考深度 |

`/api/agents` 的 `scope` 可取 `nav`、`copilot`、`invoke`、`internal`、`all`，省略时为 `all`；`includeChats` 为 `0..50`，省略时不附带 chat。可选 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；只接受 `REACT`、`CODER`、`KBASE`、`PLAN-EXECUTE`、`PROXY`、`CHANNEL`（大小写无关）。`PLAN_EXECUTE`、`ONESHOT`、ACP 别名、`TEAM` 和未知值均返回 400。`mode` 与 `scope` 为 AND，筛选普通 agent catalog 自身的 `mode`，不改变 `includeChats` 按 agentKey 获取 chat 的规则。

`GET /api/agents/order` 返回所有有效 runtime Agent 的完整 catalog 顺序，不接受 `scope` 或 `mode` 过滤，也不暴露 invalid Agent。`PUT` 接受 `{ "order": ["agent-b", "agent-a"] }`：key 会裁剪空白并校验为空、重复、数量上限和当前有效 catalog 成员；请求未携带的当前有效 Agent 按现有 catalog 顺序追加。Platform 再把这份有效顺序替换进完整 admin 序列的有效 Agent 槽位，invalid Agent 的位置和相对顺序保持不变，并原子写入既有 `agent-order.json`、reload catalog、发布一次 `catalog.updated`。该接口仅提供 HTTP；`/api/admin/agents/order` 继续面向管理台，允许完整 admin catalog 与 invalid Agent，两者共享同一顺序文件且不迁移已有数据。

`GET /api/skills` 是 WebClient slash 技能选择器的只读接口，要求精确的 `agentKey`。响应 `data` 固定为 `{ "agentKey": "...", "skills": [...] }`，每个 Skill 只包含 `key`、`name`、可选 `description` 与布尔值 `agentHasSkill`；不使用 `items`，也不返回 `meta` 或运行时选择来源。Agent 已配置 Skill 先按 Agent 配置顺序返回并标记为 `true`，其中包括只存在于 Agent 本地 `skills/` 的 Skill；随后按当前有效 skills-center catalog 的稳定顺序追加其余 Skill并标记为 `false`。两组按 key 大小写不敏感去重，`skills` 无结果时仍返回空数组。缺少 `agentKey` 返回 400 `agent_key_required`，Agent 不存在返回 404 `agent_not_found`，已配置 Skill 无法从稳定 Agent runtime 解析时返回 503 `skill_catalog_unavailable`。

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

`includeTeam` 是可选布尔 query，省略或 `false` 时响应保持原有的 agent 列表和排序。设为 `true` 时，响应改为扁平联合列表，每项带 `kind:"agent" | "team"`：agent 项保留原有摘要字段；team 项返回 `teamId`、`name`、可选 `description/icon`、`agentKeys`、`meta`，并和 agent 一样包含 `stats` 与可选 `chats`，但绝不返回虚拟 `key`、`mode`、`runtimeMode`、`workspaceDir`、`agentConfigDir` 或模型配置。此时 `scope` 与 `mode` 只过滤 agent，所有 Team 均保留；`mode=TEAM` 会返回 400。混合项按各自最新 chat 的 `lastRunId` 降序排列，无 chat 的项置后；同值按名称、kind 与稳定身份字段确定顺序。`includeChats=N` 对 Team 也按 `teamId` 返回最近 N 条 Team-owned chat。WebSocket `/api/agents` 使用等价的 `scope`、`includeChats`、`includeTeam`、`mode` 字段，其中 `includeTeam` 为 JSON boolean。

### Admin

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/admin/agents` | 无 | admin agent 列表，包含 invalid agent 诊断 |
| GET | `/api/admin/agents/detail` | query: `agentKey` | admin agent 详情，包含编辑配置、来源和诊断 |
| GET/PUT/DELETE | `/api/admin/source` | GET query: `type`、`key`、`path`、`category`、`file`；PUT body: `target`、`content`、`baseSha256`；DELETE body: `target`、`baseSha256` | 读取或保存受控的 Agent、Skill、Automation、Registry 文本 source；删除目前仅允许 `registry/mcp-servers`；mutation 使用可选哈希防止覆盖并发修改 |
| GET/PUT | `/api/admin/agents/order` | PUT body: `order` | agent 展示顺序 |
| POST | `/api/admin/agents/create` | body: `key`、`definition`、`soulPrompt`、`agentsPrompt` | 创建后的 agent 详情 |
| POST | `/api/admin/agents/import` | multipart: `file`、可选 `overwrite` | 导入完整 Agent ZIP，返回包含 `status` 与 `diagnostics` 的 admin agent 详情 |
| POST | `/api/admin/agents/update` | body: `key`/`agentKey`、`definition`、`soulPrompt`、`agentsPrompt` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/update-name` | body: `key`/`agentKey`、`name` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/delete` | body: `key`/`agentKey` | 删除结果 |
| POST | `/api/admin/agents/skills/import` | multipart: `agentKey`、`file`；兼容可选 `key` | 为一个目录型 Agent 导入并启用专属 ZIP Skill，返回更新后的 admin agent detail |
| POST | `/api/admin/agents/skills/delete` | body: `agentKey`、`key` | 删除该 Agent 的专属 Skill 与其配置引用，返回更新后的 admin agent detail |
| GET | `/api/admin/agents/editor-options` | 无 | agent 编辑器可选项 |
| GET | `/api/admin/skills` | 无 | skills-center skill 列表，包含状态、图标 URL、可选 `version`、摘要诊断、更新时间、大小与引用 agent |
| GET | `/api/admin/skills/detail` | query: `key`、`openPath` | skill 详情，返回 `fileManifest.entries[]` 与可选 `openedFile` |
| POST | `/api/admin/skills/create` | body: `key`、`skillMd`、`files[]` | 创建后的 skill 详情 |
| POST | `/api/admin/skills/import` | multipart: `key`、`file` | 原子校验并导入完整 ZIP，返回创建后的 skill 详情 |
| POST | `/api/admin/skills/delete` | body: `key` | 删除结果；仍被 agent 引用时返回 409 和 `usedByAgents` |
| GET | `/api/admin/skill-packages` | 无 | 返回 Platform 已安装技能包及其子技能 ID、版本和包摘要 |
| POST | `/api/admin/skill-packages/import` | query: `key`、`version`；raw ZIP body | 原子校验并安装或更新技能包，返回包状态与实际安装的子技能 |
| POST | `/api/admin/skill-packages/delete` | body: `key` | 原子卸载技能包及其子技能，返回删除的子技能列表 |
| POST | `/api/admin/skill-packages/skills/delete` | body: `packageId`、`skillId` | 原子删除包内单个子技能并更新包状态 |
| GET/PUT | `/api/admin/skills/file` | query/body: `key`、`path`、`content`、`baseSha256` | 读取或保存 UTF-8 文本文件 |
| POST | `/api/admin/skills/file/create` | body: `key`、`path`、`content` | 创建文本文件 |
| POST | `/api/admin/skills/file/delete` | body: `key`、`path`、`recursive`、`baseSha256` | 删除 skill 内文件或目录 |
| POST | `/api/admin/skills/file/mkdir` | body: `key`、`path` | 创建 skill 内目录 |
| POST | `/api/admin/skills/file/rename` | body: `key`、`fromPath`、`toPath`、`overwrite` | 重命名 skill 内文件或目录 |
| POST | `/api/admin/skills/file/upload` | multipart: `key`、`path`、`overwrite`、`file` | 上传 skill 内二进制或大文件 |
| GET | `/api/admin/skills/file/download` | query: `key`、`path` | 下载 skill 内非目录文件 |
| GET | `/api/admin/skills/download` | query: `key` | 下载 skill 的安全分发 ZIP 包 |
| POST | `/api/admin/skills/validate` | body/query: `key` | 重新加载并返回该 skill 当前校验结果 |
| GET | `/api/admin/tools` | 无 | tool 列表，含扁平化工具来源字段 |
| GET | `/api/admin/registries` | 无 | registry 文件列表摘要，含状态、脱敏 summary、首条诊断摘要与诊断数量 |
| GET/PUT | `/api/admin/registries/detail` | query/body: `category`、`file`、`content` | registry 文件详情或保存结果 |
| POST | `/api/admin/registries/validate` | body: `category`、`file`、`content` | registry 内容校验结果 |

`POST /api/admin/agents/import` 不接受单独的 Agent Key；Key 固定读取 ZIP 根目录或唯一一层包装目录内的 `agent.yml` / `agent.yaml`。导入会保留 Agent 目录的全部普通文件，包括 prompt、专属 Skills、`.config`、知识文件与其他资源。ZIP 校验拒绝路径逃逸、反斜杠路径、symlink、非普通文件、大小写冲突、重复路径及文件/目录冲突，并忽略 `__MACOSX` 与 `.DS_Store`。限制为 32 MiB 上传、32 MiB 单文件、256 MiB 解压总量、4096 个条目和 1 MiB UTF-8 Agent YAML。

同 Key 已存在且 `overwrite` 省略或为 `false` 时返回 409，`data.error` 包含 `code`、`agentKey`、`existingName` 与 `overwriteRequired:true`。确认后以同一 ZIP 和 `overwrite=true` 重试会整目录替换旧来源；目录型 Agent 原位替换，平铺 YAML Agent 转换为规范目录来源，不合并或保留旧 `.config`、专属 Skills 或资源。导入使用隐藏 staging/backup 完成原子切换；catalog 硬重载失败时恢复旧来源，回滚失败返回明确的 500 诊断。ZIP 布局、YAML、Key 或公共 mode 等结构性错误返回 422 和文件级 diagnostics，非 ZIP 返回 415，超限返回 413。若 catalog 可以重载、但该 Agent 因本机模型、工具、Workspace、KBASE 或 Skill 缺失而为 `invalid`，导入结果仍保留并以 200 返回无效状态与 diagnostics。

`/api/admin/source` 的 target 是逻辑标识而不是文件系统路径：`agent` 与 `automation` 使用 `key`，`skill` 使用 `key` 与相对 `path`，`registry` 使用 `category` 与 `file`。GET 响应固定返回 target、实际受控来源、原始 `content`、`encoding`、`sha256`、`size` 和 `updatedAt`；文本必须为 UTF-8 且不超过 1 MiB。Agent 保存只允许该 agent 的 `agent.yml`，并 reload agent catalog；Skill 与 Automation 保存分别 reload 对应 catalog / orchestrator。`registry/mcp-servers` 的 PUT 保存与 DELETE 删除都在成功响应前完成本地 Registry 校验、发布和对应工具快照清理，远端初始化与 tool sync 随后在后台执行；DELETE 成功返回 `target` 与 `deleted: true`，本地 reload 硬失败时会恢复原 YAML。旧的 Skill 文件结构、二进制上传下载接口，以及 Registry detail 接口仍保留兼容。

`/api/admin/tools` 中 `kind` 表示调用方式（如 `backend`、`frontend`、`action`），`sourceType` 表示定义来源类型（如 `local`、`agent-local`、`mcp`），`sourceCategory` 表示来源分类：`platform` 为 runtime 自带工具，`external` 可用于 `paths.tools-dir` 下普通 frontend/action/agent-local YAML 的来源分类，`mcp` 为 MCP registry 同步工具。`external` 不再表示子进程调用协议。MCP 工具额外返回 `serverKey`。列表响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 tool definition `meta`；接口不接收 query 过滤参数。

`/api/admin/skills` 只编辑 `paths.skills-center-dir` 下的共享 skill 目录，不直接编辑 agent 本地 `skills/` 同步副本。文件路径必须是相对路径，服务端拒绝目录逃逸和 symlink 跟随；JSON 文本读写限制为 UTF-8 且不超过 1 MiB，二进制或大文件通过 upload/download 接口处理。保存、上传、删除或重命名 skill 文件后会触发 `skills` reload 并级联 reload `agents`，使声明该 skill 的 agent 本地副本重新同步。

`POST /api/admin/agents/skills/import` 只面向目录型普通 Agent。它沿用共享 Skill ZIP 的校验和限额，但将内容写入 `<agents>/<agentKey>/skills/<key>/`，并原子地把 key 加入该 Agent 的 `skillConfig.skills` 后 reload agents；导入失败或 reload 失败都会恢复 Agent YAML 并清理本地目录。未传 `key` 时，服务端从 ZIP 的 `SKILL.md` frontmatter 读取 `key`，否则读取 `name` 作为 Skill Key；旧调用方仍可显式传 Key。专属 Skill 不会出现在 `/api/admin/skills`，也不能由共享 Skill 删除接口删除。导入准入不查询 skills-center；若两者 Key 相同，该专属版本只对当前 Agent 优先，其他 Agent 仍可使用技能中心版本。Admin Agent Detail 的 `privateSkills[]` 返回本地摘要、是否启用及 `overridesCenter`，不返回本地路径或文件内容。专属删除同样只在 Agent 路由执行，并同时删目录和配置引用。

`/api/admin/skills` 管理 Skill 的结构和二进制文件操作；可编辑文本内容可通过 `/api/admin/source` 的 Skill target 读取和保存。`detail` 不内联全量文件内容，而返回轻量 `fileManifest`：`revision`、`defaultOpenPath`、文件统计和预排序扁平 `entries[]`。每个 entry 使用完整相对 `path` 作为稳定 ID，并带 `parentPath/depth/order/contentKind/language/role/editable/downloadable/uploadable/renamable/deletable`。`openPath` 指向可编辑 UTF-8 文本文件时，`detail` 额外返回 `openedFile`；二进制或过大文件只返回 metadata。保存使用 `baseSha256` 做并发保护，冲突返回 409。创建、删除、重命名、上传和 mkdir 的 mutation 响应会返回新的 `fileManifest` 与 `selectedPath`，方便前端直接刷新文件树。列表和详情摘要会在 skill 目录存在 regular、非 symlink 的 `assets/<skill-id>.png` 时返回 `icon` 下载 URL；未提供图标时省略字段，由客户端负责默认图。skill 摘要从 `SKILL.md` frontmatter 提取可选 `version`：顶层 `version` 优先，缺失或空白时回退 `metadata.version`；两者皆无或空白时省略字段。`file/download` 只下载单一文件；`download` 返回 ZIP，包含安全的普通 skill 文件、跳过 symlink 与 `.runtime-env.json`，并限制未压缩内容为 256 MiB。

`POST /api/admin/skills/import` 只接受单个不超过 32 MiB 的 ZIP，Skill Key 必须尚不存在。ZIP 可直接以 `SKILL.md` 为根，也可只有一层包装目录；`__MACOSX` 与 `.DS_Store` 被忽略。服务端拒绝目录逃逸、反斜杠路径、symlink、非普通文件、重复或大小写冲突路径、文件/目录冲突，并限制单文件 32 MiB、未压缩总量 256 MiB、最多 4096 个 entry。解包先进入 catalog 与 watcher 都忽略的隐藏 staging，完整验证 `SKILL.md`、`.runtime-env.json` 和 runtime 文件后再原子 rename；重名返回 409，非 ZIP 返回 415，包内诊断返回 422 `data.error.diagnostics[]`，所有失败都不保留目标目录。成功后沿用 `skills` reload 和 Agent 重组；reload 失败会删除刚导入的目录并恢复旧 catalog。

`GET /api/admin/skill-packages` 是 `.package` 状态的只读投影，供 Desktop 将包内子技能识别为已安装并使用 manifest 版本比较；它不返回本地路径或 ZIP。`POST /api/admin/skill-packages/import` 是 Desktop Market 的技能包交付入口，不接受文件系统路径或 multipart。调用方以 `application/zip` 原样传入动态包，Platform 将请求体写入进程临时文件，完成 ZIP 安全检查、`manifest.json` 包身份与版本校验、必选子技能存在性以及每个子技能的标准 Skill 校验后，才在同一事务中替换 `skills-center/<skill-id>/`。无技能包归属的同名独立 Skill 会被包内版本原子替换并纳入新包管理，事务失败时恢复原目录；已归属其他技能包的同名 Skill 仍返回 409。更新会同时移除新版本不再包含的旧子技能；删除仍被 Agent 使用的子技能或通过普通 Skill 接口绕过包状态同样返回 409。包内子技能可以通过专用删除接口单独卸载，该操作会同步移除子技能目录和包记录中的成员；删除最后一个子技能时一并删除包状态。任一校验、文件切换或 Catalog 重载失败时，子技能目录和包状态全部恢复。成功后只保留子技能目录与 `skills-center/.package/<package-id>.json`，请求临时 ZIP、staging 和 backup 均删除；`.package` 不进入 Skill Catalog。整包卸载端点使用同一事务删除该包记录与剩余子技能，Catalog 重载失败同样回滚。

`/api/admin/registries` 是列表接口，不返回 registry 文件绝对路径、完整 `diagnostics[]` 或文件大小；编辑器应通过 `/api/admin/registries/detail` 获取 `source`、完整诊断、`content`、`parsed` 与 `size`。

MCP Server 列表项的 `summary` 额外包含 `toolCount` 与 `syncStatus`。`syncStatus` 取值为 `pending`、`syncing`、`ready`、`unavailable` 或 `disabled`；可选的 `lastSyncAttemptAt`、`lastSyncSuccessAt` 使用 epoch milliseconds，可选 `syncDiagnostic` 只返回脱敏后的 `severity/code/message`。顶层 `status` 仍只表示 YAML 配置状态。通过通用 `PUT /api/admin/source`、`DELETE /api/admin/source` 或兼容的 `PUT /api/admin/registries/detail` 变更 `category=mcp-servers` 时，成功响应只保证合法本地配置已经发布：新建或连接变化的 Server 此时可以是 `pending`/`syncing`，随后变为 `ready` 或 `unavailable` 并发送 `catalog.updated(reason=mcp-servers)`。远端不可达不会回滚合法配置；删除或禁用 Server 会在响应前清理对应工具。

Registry 列表的 `summary` 按分类返回展示字段：provider 暴露 `baseUrl`；model 暴露 `provider/protocol/type/isVision/isReasoner/isFunction/maxInputTokens/maxOutputTokens/timeout`；MCP server 暴露 `transport/toolCount`，其中 HTTP 项另有 `baseUrl`，stdio 项不返回 `baseUrl`、`command`、`args` 或 `env`，`toolCount` 是当前已同步注册的 MCP 工具数量；viewport server 仅暴露 `baseUrl`，当前不返回 viewport 数量。

`/api/teams` 每项返回 `teamId`、`name`、可选 `description/icon`、`agentKeys` 与安全摘要 `meta`。`meta` 包含 `validAgentKeys`、`invalidAgentKeys`、`orchestrated:true` 与 `maxParallel`；不再返回 `runtimeMode` 或任何 legacy runtime metadata。接口不会返回隐藏总控 key、总控模型配置、system prompt、`SOUL.md/AGENTS.md` 内容或 internal-only `agent_delegate` 定义；`/api/admin/tools` 同样不列出该工具。

### Chat

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/chats` | query: `lastRunId`、`agentKey`、`mode`、`limit` | chat 摘要列表 |
| GET | `/api/chats/order` | 无 | 当前 `sortMode` 与可选 `updatedAt` |
| PUT | `/api/chats/order` | body: `set_mode` 或 `move` operation | 更新后的 `sortMode` 与 `updatedAt` |
| GET | `/api/chat` | query: `chatId`、`includeRawMessages` | chat 详情，默认含 events |
| POST | `/api/chats/search` | body: `query`、`agentKey`、`teamId`、`limit` | 全局 chat 搜索结果 |
| POST | `/api/read` | body: `chatId` | 标记已读结果 |
| POST | `/api/feedback` | body: `chatId`、`runId`、`messageId`、`rating`、`reason` | feedback 写入结果 |
| POST | `/api/chat/delete` | body: `chatId` | 删除 chat 结果 |
| POST | `/api/chat/rename` | body: `chatId`、`chatName` | 重命名结果 |
| POST | `/api/chat/derive` | body: `sourceChatId`、`sourceRunId`、`chatId`、`chatName` | 从已完成 run 派生新 chat |
| POST | `/api/compact` | body: `requestId`、`chatId`、`trigger`、`level` | 历史或活动原生根 Run 的上下文压缩最终结果 |
| POST | `/api/chat/archive` | body: `chatId`、`reason` | 归档结果 |
| GET | `/api/chat/export` | query: `chatId`，可选 `format=markdown\|html` | 默认 Markdown；`html` 返回完整静态只读文档；`sse` 与未知格式返回 400 |
| GET | `/api/chat/jsonl` | query: `chatId` | 原始持久化 chat JSONL 文本；active 不存在时回退 archive，不得作为公开分享输入 |
| GET | `/api/chat/system-prompt` | query: `chatId`、`runId`、`agentKey` | 获取该 agent 在历史 run 中首次使用的持久化 system message；服务端从 run 的 system-init / step `systemRef` 解析快照 |
| GET | `/api/chat/llm-trace` | query: `file=<chatId>/.llm-records/<runId>_NNN.json` | 原始 LLM chat trace JSON 文本 |

`/api/chats` 的 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；只接受 `REACT`、`CODER`、`KBASE`、`PLAN-EXECUTE`、`PROXY`、`CHANNEL`（大小写无关）。旧别名、`TEAM` 和未知值均返回 400。它筛选 Agent-owned chat，并与 `agentKey`、`lastRunId` 为 AND 关系；Team-owned chat 天然包含在全局列表中，不受合法 `mode` 影响。显式 `agentKey` 仍只返回该 agent 的 chat，不会匹配 Team。可选 `limit` 必须为正整数且不设上限；省略时返回全部匹配项，传入时必须在全部筛选和当前实例级排序后截断，不能先取最近记录再局部重排。`limit=0`、负数、空值或非整数返回 400；当前不支持 offset 或分页游标。WebSocket 的 `/api/chats` 请求使用等价的 `mode` 与 `limit` 字段（`limit` 未传为全部）。旧 `agentMode` 参数或 payload 会返回 400，调用方应改用 `mode`。

`/api/chats/order` 管理同一 Platform 实例的 Chat 列表顺序。缺省 `recent` 按 `updatedAt DESC, chatId DESC`；`manual` 先把尚未进入保存序列的新建或恢复 Chat 按 recent 置顶，再接保存的全量 active Chat 顺序，已经归档、删除或不存在的 ID 自动忽略。`PUT` 的 `set_mode` 接受 `sortMode:"recent" | "manual"`；`move` 必须给出 `chatId`，并且只能给 `beforeChatId`、`afterChatId` 之一，目标和锚点都必须是当前 active Chat，不能自身锚定。recent 下首次 move 会以当时的全量 recent 顺序建立 manual 基线并原子切换模式；切回 recent 保留 manual 顺序，之后切回 manual 可恢复。HTTP 与 WebSocket 使用相同 operation 和错误语义；WebSocket 空 payload 相当于 GET。`/api/chats/order` 的成功 mutation 只改变展示顺序，不修改 Chat `updatedAt`。

chat 摘要会在新数据中返回可选 `mode`；`/api/chat.runs[]`、`/api/agents?includeChats` 及 archive detail 中的共享 `runs[]` 均返回每次 run 的可选 `mode`。普通 agent 持久化规范 API mode（例如 `REACT`、`CODER`、`KBASE`、`PLAN-EXECUTE`、`PROXY`、`CHANNEL`）；Team 固定为 `TEAM`，不会暴露隐藏协调器 key。历史 chat/run 不根据当前 catalog 回填或转换，原始 mode 仅用于历史读取，不能作为当前筛选或运行输入；Team-owned chat 在合法 `/api/chats` mode 查询中始终保留。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 摘要，以及 `/api/chat` 详情顶层在新数据中可包含 `source`，表示 chat 首次创建来源。当前只记录 query 与 automation 两类：`query` / `query:<user>` 表示由 query 创建，`automation:<automationId>` 表示由 automation 创建。旧数据为空、上传创建或派生创建时省略。channel 远程用户调用本机智能体仍属于 query source；gateway 可在受信 channel 请求中传 `sourceUser`，否则服务端会从形如 `wecom#single#user1#...` 的 chatId 中取远端用户段作为 `query:<user>`。`sourceChannel` 是 gateway/channel 路由标签，不承载 query / automation 语义。

`/api/chat` 详情固定返回顶层 `createdAt` 与 `updatedAt`，并与列表 summary 一样返回 owner `agentKey`/`teamId`、可选 `mode`、`lastRunId`、`lastRunContent` 和完整 `read { isRead, readAt?, readRunId? }`；客户端不得从 runs、events 或本机时间推断这些字段。该详情 summary 是外部路由直接打开未进入列表缓存的 Chat 时的权威 read 基线。每个 `runs[]` 的 `startedAt` 由注册时捕获并持久化；已完成 run 的 `completedAt` 必填，仍在执行的 run 则省略 `completedAt`（绝不输出 `0`）。`activeRun.startedAt` 与对应 push `run.started.startedAt` 是同一个已捕获时刻；push `run.finished.finishedAt` 与完成记录的 `completedAt` 相同。`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 以及 `/api/chat` 的 chat 详情，在存在可恢复等待项时都包含顶层 `awaiting`：`awaitingId`、`runId`、`mode`、`status:"awaiting"`、`createdAt`。完整问题、审批项、表单和 planning 定义仍从 chat events 中的 `awaiting.ask` 获取；没有顶层 `awaiting` 的历史 ask 不可提交。Platform 重启时，未超时/无限等待的 question 与永久 planning 可恢复，approval/form 会按 timeout 或 runtime restart 原因终态化。可恢复 question/planning 还会同时返回同一 `runId` 的 `activeRun`，其 `state:"WAITING_SUBMIT"`、`startedAt` 保留原 run 时刻，且对应 Platform 内已真实注册的 suspended run，不是 API 层合成摘要。

单 Chat 已读由显示 Main Chat 内容的 WebClient 调用 `/api/read { chatId, runId? }` 发起；Platform 按现有单调 `readRunId` 规则先持久化，再广播字段完整的 `chat.read`。Run 完成准备发送 `chat.unread` 时，服务端必须重新读取当前 summary；如果 `readRunId` 已覆盖 `lastRunId`，不得发送与持久化状态矛盾的 unread。消费者用 `RunIDAfter(lastRunId, readRunId)` 与 `readAt/createdAt` 合并迟到 Push。Agent 级 `/api/read { agentKey }` 只用于用户显式“全部标为已读”，成功后广播 `chat.read_all`。

`POST /api/chat/derive` 只支持 active chat 存储，不从 archive 直接派生。`sourceRunId` 省略时使用 source chat 的 `lastRunId`；source chat 必须没有 active run 和 pending awaiting，且目标 source run 已完成。服务端会创建新的独立 `chatId`，复制截至 source run 的可回放 JSONL 历史与必要资源，并为复制出的历史 run 生成新的 runId；返回 `lastRunId` 是新 chat 中映射后的 runId。派生成功后客户端继续用新 `chatId` 调 `/api/query`，后续运行不会写回原 chat。

`/api/chat` 返回 active run 时，`activeRun.lastSeq` 是本次 chat detail 已返回历史 events 覆盖到的公开 live stream 游标，客户端应用这些 events 后可把它作为 `/api/attach.lastSeq`。它来自 `chatId.jsonl` 每行顶层 `liveSeq` 的 replay 结果，不是内存 run 当前最新 seq；内存最新 seq 只用于服务端运行状态。新的 Native / Team run 只在事件实际发布时递增该游标，内部事件复用最近公开游标；历史 run 的旧游标不迁移。对 `WAITING_SUBMIT` active run，该 attach 应在 submit 前建立并保持等待；submit 成功不应再创建第二个 attach，同一连接会从 `request.submit` / `awaiting.answer` 开始继续接收该 run 的后续事件。

`POST /api/compact` 的标准手动请求为 `{ "requestId":"...", "chatId":"...", "trigger":"manual", "level":"l1_tools"|"summary" }`，HTTP 与 WebSocket 字段一致。`l1_tools` 只确定性压缩白名单内已经完成、配对完整的 assistant tool call/result，普通 user/assistant/system、引用、附件、未完成工具与 HITL 原样保留；它不调用模型，也不产生 `compactionUsage`。`summary` 将符合条件的多个旧 Run 和活动 Run 已完成前缀合并成一个摘要，严格只调用一次摘要模型；摘要输入先做不落盘的 L1 结构化投影，完整规范化输入仍超出预算时返回 `summary_input_too_large`，绝不丢弃中间历史或拆成多次摘要调用。

压缩规划、L1 前后计算、L2 mandatory 检查和结果校验共用同一套多模态安全估算。普通文本和工具 Schema 继续按现有字符口径估算；`image_url` 的 Base64 正文不作为文本计数，可从有界图片头读取尺寸时按 `ceil(width×height/750)` 计算并限制在 256–32768 token，WebP、外部 URL、非法 Data URL 或无法识别尺寸时固定为 8192 token；尺寸检查不会完整解码或访问网络。活动 Run 有有效 provider prompt usage 时，自动触发优先以该 usage 加上 assistant 之后新增消息估算；否则估算完整上下文。L1 完成后用同一口径重新计算，只有仍高于模型窗口 60% 目标才进入 L2。L2 摘要 prompt 会将候选图片正文临时投影为 MIME、字节大小、可读宽高和 payload SHA-256，不修改活动消息、Chat JSONL 或 checkpoint 中保留的原始图片消息。

没有活动 Run 时，Platform 持有 Chat maintenance lease 后原子改写历史。L1 使用 `_type:"compact.tool"` 和 `_compact` 标记；L2 使用 `_type:"compact.checkpoint"`。L2 对一个终态 Run 可整体压缩，两个压缩第一个，三个及以上默认保留最后两个；保留尾部仍超过模型窗口 60% 目标时逐步减少到一个或零个。`no_compactable_tools` 与 `no_compactable_history` 分别表示不存在可做 L1 的工具组和不存在尚未压缩的终态历史。maintenance lease 持有期间新 query 以 `409 compact_in_progress` 拒绝，不会与 JSONL 替换竞态。

普通 Agent 或 Team 协调器的活动原生根 Run 同时支持 L1/L2，不改写活动 JSONL，而是把请求投递给 REACT 循环并阻塞等待最终结果。已启动的模型 Turn 或工具批次自然结束后，在下一次模型调用、HITL 等待或 run 终态边界的安全点压缩；已启动工具不取消也不重复。question/approval/form/planning 的未完成 tool group 与 `awaitingId` 保留，压缩后回到同一等待。安全点依次发布 `context.compact.start`、持久化 `_type:"compact.run.checkpoint"`、再发布 `context.compact.complete`；checkpoint 失败时不发布 compact complete，而是发布 `context.compact.failed(detail:"compact_persist_failed")` 和终态 `run.error`，不发起下一次模型调用。

手动 L2 的模型失败、空摘要和输入过大分别返回 `summary_model_failed`、`summary_empty`、`summary_input_too_large`，保持逻辑上下文不变且不创建 checkpoint；新压缩不再生成 deterministic fallback，已有旧 checkpoint 仍兼容回放。压缩不消耗 Agent `maxSteps`、工具轮次或普通 LLM 调用计数，L2 用量单独记录为 `compactionUsage`。自动压缩达到阈值后固定执行 L1、重新估算，仍高于模型窗口 60% 目标才执行一次 L2；自动 L2 失败时不继续下一次模型调用。Provider 在未提交 Turn 返回标准 context-length 错误时自动压缩并只重试一次；再次溢出，或 system、工具 Schema、当前用户输入与不可拆分待处理组本身超窗口时，以 `context_window_uncompactable` 终止。

相同 `requestId` 重试会加入原请求并取得同一结果；不同请求并发返回 `accepted:false/status:"busy"/detail:"compact_in_progress"/retryable:true`。客户端断开只结束当前 HTTP/WS 等待，已入队任务继续并可从事件回放恢复。Interrupt 会将尚未完成的请求解决为 `failed/run_interrupted`。ACP、PROXY、CHANNEL 和 BTW 活动 Run 返回 `unsupported_active_run`。成功响应包含 `runId`（仅 run scope）、`trigger`、`scope:"history"|"run"`、`level`、Token 估算、`remainingRatio`、`releasedRatio`、`tokensFreed`、工具统计，以及 L2 可选的 `compactionUsage`；失败/跳过通过 `status/detail/retryable` 表达。

`/api/chat/jsonl`、`/api/chat/system-prompt`、chat/archive replay、搜索结果与 `/api/chat/llm-trace` 都在读取前验证各自明确拥有的时间字段。JSONL 的 line `updatedAt`、event `timestamp`、`messages[].ts` 和 awaiting/submit 时间保持严格；trace 中 `sentAt`、`responseStartedAt`、`completedAt` 以及 `interrupt.interruptedAt` 均为 epoch milliseconds，对应的 `sentTime`、`responseStartedTime`、`completedTime`、`interrupt.interruptedTime` 为 RFC3339Nano 可读时间。字符串、秒、浮点、零值或缺少必填平台时间会返回 `422 time_contract_violation`；trace 中外部 request/response/tool payload 保持透明。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=N`（包括 `includeTeam=true`）附带的 chat 摘要，以及 WebSocket `/api/chats` 响应都会在存在运行中 run 时返回 `activeRun`。KBASE editing run 的摘要带可选 `editingMode:true`，方便客户端重连后恢复 badge；false 时省略。这些摘要可能包含局部 `error`，用于展示单个 chat 的可恢复/可诊断异常而不让列表整体失败。当前 `multiple active runs found for chat` 会返回 `error: { "code": "active_run_conflict", "message": "multiple active runs found for chat", "chatId": "...", "runIds": ["..."] }`，此时该 chat 不包含 `activeRun`。

`/api/agent` 会返回 agent 配置中的 `greetings` 与 `wonders` 数组。客户端可将 `greetings` 作为开场/占位介绍，并随机挑选一条显示在聊天输入框 placeholder 或空状态里；`wonders` 用于展示可直接提交的具体 query 示例。`/api/agents` 是列表摘要接口，不返回 `greetings` 或 `wonders`。`/api/agent` 是运行时详情接口，不返回 `definition`、`soulPrompt`、`agentsPrompt`、`source`；编辑器应使用 `/api/admin/agents/detail` 获取这些字段，以及 `status`、`diagnostics`。

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
| POST | `/api/automation/create` | body: `name`、`cron`、`query`，以及 `agentKey` / `teamId` 二选一；可选 `description`、`enabled`、`zoneId`、`remainingRuns` | 创建后的 automation 详情 |
| POST | `/api/automation/update` | body: `id` 或 `automationId`，以及可更新字段 | 更新后的 automation 详情 |
| POST | `/api/automation/delete` | body: `id` 或 `automationId` | 删除结果 |
| POST | `/api/automation/toggle` | body: `id` 或 `automationId`、`enabled` | 启停后的 automation 详情 |
| POST | `/api/automation/trigger` | body: `id`（兼容 `automationId`） | 异步受理结果：`accepted`、`status`、`automationId`、`executionId` |
| POST | `/api/automation/executions` | body: `id` 或 `automationId`、`limit`、`offset` | execution history |
| POST | `/api/automation/execution` | body: `executionId`（兼容 `id`） | 单条 execution 与完整 query/result 内容 |

`query` 对象包含必填 `message`，以及可选 `chatId`、`role`、`hidden`、`params`。`role` 可选值为 `user`、`assistant`、`automation`、`system`，省略时按 `automation` 执行；`hidden` 省略时按 `true` 执行，只隐藏 Chat 时间线里的 automation query 消息，不隐藏 chat、run 或模型回复，显式 `false` 可显示该 query。省略值在 Automation 详情中继续省略，结构化更新不会把计算后的默认值写回 YAML。

Automation 摘要和详情中的 `nextFireAt` 是下次触发时间的 epoch milliseconds；`lastExecution` 与 execution history 中的 `startedAt`、`completedAt` 同样是 epoch milliseconds。这些 `*At` 字段是排序、计算和客户端本地化的唯一权威时间。对应的 `nextFireTime`、`startedTime`、`completedTime` 均由 Platform 按 `automation.default-zone-id`（无效或未配置时回退进程 `time.Local`）转换为 `YYYY-MM-DD HH:mm:ss`，只用于阅读，不保留毫秒或时区信息。

Automation 的 `description` 和 `zoneId` 均可省略。Execution 的 `zoneId` 是创建 execution 时解析出的有效业务时区快照，解析顺序为 automation `environment.zoneId`、Platform `automation.default-zone-id`、进程 `time.Local`。它不会随 automation 后续修改或删除而变化，也不参与上述 `*Time` 展示转换。

Automation 列表和详情固定返回 `executionHistory:{available,state,message?}`，其中 `state` 为 `initializing|ready|degraded|unavailable`。History 不可读不影响 Automation 配置 API；`/api/automation/executions` 和 `/api/automation/execution` 此时返回 `503`。

`POST /api/automation/trigger` 是 HTTP-only 的原生手动触发入口。每次请求都会创建不同的 Execution，并与 Cron 执行共用全局 Automation 并发池；暂停状态也可触发，但不会扣减 `remainingRuns`、持久化 Automation 或改变 `nextFireAt`。成功响应保持统一 200 包装，例如 `data:{"accepted":true,"status":"accepted","automationId":"daily-report","executionId":"exec_xxx"}`。该响应只表示已受理，Query 准入、模型执行、HITL 或停止取消等后续结果通过 execution history 和 `automation.execution.*` push 反映。

触发受理时会固定完整 Definition、有效 `zoneId` 和 `automation:<id>` Chat source；后续编辑或删除不影响该 Execution。`startedAt` 是受理时间，`runStartedAt` 是 Query 真正注册时间。History 初始化或不可用不阻止触发。无 ID 或 payload 格式错误返回 400，不存在返回 404，orchestrator 未配置、未启动或正在停止返回 503。该路由不注册为普通 WebSocket 管理 route。

Execution history item 与 `lastExecution` 可包含 `chatId`、`runId`、`finishReason`、`hasResult`、`resultPreview` 和 `runStartedAt`。`resultPreview` 由 Platform 从完整结果生成，最多约 240 字符；列表接口不读取或返回完整 `RESULT_CONTENT_`。`POST /api/automation/execution` 按需返回 `queryContent` 与 `resultContent`，其中结果来自对应 Run 持久化时的 `RunCompletion.AssistantText`，不是事后读取可能已变化的 Chat `lastRunContent`。

Automation 的 Team 身份规则与 query 一致：只配置 `teamId`，同时传 `agentKey` 会被拒绝。触发时由隐藏协调器接管，不会选择或回显虚拟 Agent key。

### Run

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/query` | body: `message`、`agentKey`、`teamId`、`chatId`、`runId`、`requestId`、`role`、`references`、`mustUseSkills`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`planningMode`、`editingMode`、`accessLevel`、`model` | 默认 SSE stream；`stream:false` 时返回 JSON |
| POST | `/api/btw` | body: `chatId`、`message`、可选 `btwId`、`runId`、`requestId`、`references`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`accessLevel`、`model` | 创建或继续隐藏只读分支；复用 query SSE，`stream:false` 返回带 `btwId` 的 JSON |
| GET | `/api/attach` | query: `runId`、`agentKey` 或 `teamId`、`lastSeq` | 按公开 owner 续接 run 的 SSE stream |
| POST | `/api/submit` | body: `agentKey` 或 `teamId`、`runId`、`awaitingId`、`params` | HITL submit ack |
| POST | `/api/steer` | body: `agentKey` 或 `teamId`、`runId`、`message`、`requestId`、`chatId`、`steerId` | steer ack |
| POST | `/api/interrupt` | body: `agentKey` 或 `teamId`、`runId`、`message`、`requestId`、`chatId` | interrupt ack |
| POST | `/api/access-level` | body: `agentKey` 或 `teamId`、`runId`、`accessLevel`、`requestId`、`reason` | 动态更新 native run 的 accessLevel |
| POST | `/api/compact` | body: `requestId`、`chatId`、`trigger:"manual"`、`level:"summary"` | 无活动 Run 时压缩历史；活动 native root Run 时阻塞到安全点压缩结束 |

`POST /api/submit` 成功仍返回 200。已知终态返回 409，`msg` / `data.errorCode` 为 `awaiting_expired`、`awaiting_interrupted` 或 `already_resolved`；`data` 同时包含 `chatId`、`runId`、`awaitingId`、`status`、`detail` 与结构化 `error`。真正不存在或 awaiting 身份不匹配返回 400 `unknown_awaiting`。同一 `submitId` 在 answer 已持久化但 continuation 尚未恢复完成的崩溃窗口内仍可作为幂等重试继续完成原提交，不会重复写 submit/answer。

`/api/query` 的 `stream` 是 JSON body 字段；省略或传 `true` 时返回 SSE，结束帧为 `data: [DONE]`。传 `false` 时服务端仍执行完整 run、持久化 chat，并在结束后返回普通 JSON。默认只返回最终回答，响应示例见下文。

`editingMode` 只认顶层 JSON boolean。仅 `editingMode:true` 且目标为专用 `mode: KBASE` 时生效；普通 Agent 附加 KBASE capability、CODER、PROXY、CHANNEL 和 Team 返回 HTTP 400，`msg=editing_mode_unsupported`。专用 KBASE 在 true/false 两种状态下都以最终 `runtimeConfig.workspaceRoot` 作为 Workspace，并提供相同的五个文件工具；`false` 或省略只表示 Workspace mutation 未授权，Workspace 仍可读，当前 Chat 目录仍可读写。`params.editingMode` 不生效。开启时，`request.query` live event、chat JSONL、replay/export 和运行中 `activeRun` 保留 `editingMode:true`；false 时省略。它是单次 run 授权，不写 Agent 配置，也不会从上一轮继承。

`mustUseSkills` 是单次 run 的强制 Skill 数组。服务端对各项 trim、忽略空值、按大小写不敏感去重并保留首次出现顺序，不设置额外数量上限。已经配置在目标 Agent 的 Skill 从 `ru-agents/<agentKey>/skills/<key>` 解析，模型看到的指令路径为 `@skills/<key>/SKILL.md`；未配置的额外 Skill 必须存在于当前有效 skills-center catalog，并从共享技能中心解析为 `@skills-center/<key>/SKILL.md`。任一项缺失、无合法 `SKILL.md` 或当前无法解析时，整个请求在 run 启动前以 HTTP 400、`msg=must_use_skill_unavailable` 失败，不会部分执行或静默降级。

每个 must-use Skill（包括已配置 Skill）都会解析为最终 canonical 目录，并在当前 run 建立 trusted read + readonly roots：选中目录内的 `SKILL.md`、scripts、references 与 assets 免读路径 HITL，未选中的 skills-center 兄弟目录不随之开放，symlink 逃逸按最终目标重新判权；run readonly 不能被 writeRoots、hostAccess、`full_access` 或 exact/rule approval 放宽。存在额外 Skill 时，Container session 仍只追加一次整个 skills-center 的只读挂载 `/skills-center`，已有显式 `platform: skills-center` 挂载会去重并按只读使用；整个 mount 只表示容器可见性。系统 Prompt 会列出每个 Skill 的精确 `instructionsPath`，并要求全部读取和遵循。额外 Skill 不参与 Agent `.config`、`.runtime-env.json` 或 `.bash-hooks` 合并，不增加 Tool、MCP、Agent hostAccess、其他 mount 或 `accessLevel`；平台也不生成内容快照，continuation 会按当前 catalog 和磁盘内容重新验证。

规范化后的 `mustUseSkills` 会进入 session、system-init fingerprint、live/persist/replay 的 `request.query`、synthetic query，以及 Proxy/Channel 转发 payload。Proxy、Channel 与 ACP 路由入口只负责规范化和透传，不用本机 catalog 代替远端判定；真正执行 query 的 Platform 按上述规则解析、挂载和失败。orchestrated Team 的非空数组返回 HTTP 400、`must_use_skills_unsupported`。旧字段 `requiredSkillKeys` 已删除；HTTP 与 WebSocket query 入口只要出现该字段（即使为空）都会返回 `required_skill_keys_removed`，不会按未知字段忽略。

`teamId` 的 HTTP、WebSocket、Automation、submit continuation 与子智能体准入共享同一 resolver。chat 创建后 `teamId` 固定；Team 的公开 owner 是 Team，query 只使用 `teamId`。运行时在 run 内合成内部 `TEAM` 协调器，任何 `agentKey` 都会被视为绕过调度器。

| 场景 | HTTP 结果 |
|---|---|
| 新请求使用未知 `teamId` | 400 |
| 已有 Team chat 对应的 Team 已不存在 | 503 |
| Team 同时传入 `agentKey` | 400 |
| 已有 chat 传入不同 Team；包括为无 Team chat 补传 Team | 409 |
| Team 成员为空或存在失效成员 | 503 |

WebSocket 使用现有错误 envelope 表达相同语义。Team 无效时不会回退全局或 channel 默认 agent；run 开始后使用已解析的成员、成员 `AgentDefinition`、协调器配置与 prompt 快照，不受本轮 catalog 热重载影响。需要启动新执行 run 的 active/deferred submit 会在消费 awaiting 前重新准入，失败时保留 awaiting。

run 控制接口从 `agentKey/teamId` 推导互斥身份：Agent-owned run 必须传 `agentKey`；Team run 必须只传 `teamId`，漏传返回 400，错 Team 返回 403，同时传 `agentKey` 也返回 400。Team 的 `request.query` 与 `run.start` 携带 `teamId` 且 `agentKey` 为空；chat/run summary 同样使用这一身份对表达公开归属。虚拟协调器 key 不是公共 API 身份。

`/api/btw` 用于“顺便问”：`chatId` 必须指向已有 active chat；不传 `btwId` 时从当前主 JSONL 创建隐藏快照并在响应头 `X-Btw-Id` 与首个 `request.query.btwId` 返回分支 ID，传 `btwId` 时继续该分支。BTW 固定继承父 chat 的 agent/team，固定 `role:user` 且关闭 planning mode。主 chat 的 active run、pending awaiting、摘要、未读、搜索、自动 learn 和 JSONL 都不会被 BTW 更新。

BTW 与普通 query 使用同一 Agent/ReAct、模型协议、SSE assembler、attach/interrupt 和 StepWriter；`request.query` 额外包含 `kind:"btw"`、`btwId`、`parentChatId`、`hidden:true`，不新增 event type，也不发送 `chat.start` / `chat.updated`。同一个 `btwId` 只允许一个 active run，父 chat 与不同 BTW 分支可以并行。

Desktop 的 BTW 实时入口是普通 WebSocket v2 上的 route `/api/btw`，但只接受已认证且握手 metadata 为 `source=desktop-btw` 的连接；普通 `/api/query` 在该 lane 固定拒绝。一个 BTW 连接可以并发创建、继续和 attach 多个 BTW Run，detach、submit、steer 与 interrupt 沿用现有 run owner 校验。HTTP `POST /api/btw` 保留为 Platform 公共接口，但 Desktop 不把它作为旧协议 fallback。

BTW 发给 provider 的 system、tools、tool choice 和 cache key 与普通 chat 保持一致；只读说明放在本次 user message。平台内置查询工具可执行，写文件、Bash、memory mutation、plan mutation、agent invoke、artifact/image、desktop、frontend/action 等工具返回 `btw_tool_disabled` 且不会进入 HITL。MCP / agent-local / external 工具只有 `meta.readOnly:true` 时可执行；MCP `annotations.readOnlyHint:true` 会映射为该字段。proxy/channel/ACP coder 因工具执行不经过本地门禁，返回 `btw_backend_unsupported`。

`stream:false` 的 BTW 响应为：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "btwId": "btw_xxx",
    "parentChatId": "chat_xxx",
    "runId": "run_xxx",
    "content": "最终回答"
  }
}
```

`references` 中的文件引用使用 `path` 表示当前目标智能体可直接访问的执行路径。当前 Chat 文件在 Host 为 Chat 绝对路径，在 Container Hub 为 `/chat/...`；Workspace 文件在 Host 为 Workspace 绝对路径，在 Container Hub 为 `/workspace/...`。跨 Agent 文件会先物化到 Chat；PROXY/CHANNEL/ACP 等远程 adapter 使用带 ticket 的 resource URL，由接收端在 30 秒、50 MiB 安全上限内下载到自己的 Chat，再生成本地执行路径。历史 path-only `/workspace` Chat 引用不再作为新 run 输入接受。

Chat 与 Site 沿用同一 `references` 数组，但不按文件路径处理：

- Chat：`{ "type": "chat", "id": "chatId", "name": "会话名", "meta": { "agentKey": "...", "teamId": "...", "updatedAt": 0 } }`。服务端忽略客户端提供的上下文，按可信 chatId 重新读取 compact 摘要和最近 12 条用户/助手消息；拒绝当前 chat 自引用、失效 chat 和跨 query principal 访问。
- Site：`{ "type": "site", "id": "entryKey", "name": "站点名", "url": "https://...", "meta": { "kind": "website|webapp", "updatedAt": 0 } }`。服务端只注入经过字段白名单校验的名称、类型、入口标识和 HTTP(S) URL，不抓取页面内容。

文件引用的 `url` 只用于平台资源下载、ticket 与 gateway 数据面，不进入模型 prompt；只有 Site 引用的可信指针 URL 会作为引用元数据展示给模型。

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

实时 SSE / WS stream 中所有工具统一发送 `tool.start`、`tool.args`、`tool.end`、`tool.snapshot`、`tool.result`，不再存在 `action.*` 事件。Bash 进程非零退出时，`tool.result` 保留真实 `exitCode` 并作为可恢复的工具失败展示，不会自动升级为终止性的 `run.error`；成功但写入 stderr 的命令仍保持 `exitCode: 0`。持久化到 `chatId.jsonl` 时，同一 assistant turn 的多个工具调用会合并为一条 assistant message 的 `tool_calls[]`；如果该组存在 awaiting，确认前不会执行任何 sibling tool，确认后的所有结果写入同 `seq` 的 `_type:"react-tool"` continuation。

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

该 `run.error` 是完成态事实：SSE、WebSocket、`/api/attach` 和 `run_status` 返回同一错误，run summary 持久化为 `finishReason:"error"`，运行状态为 `FAILED`。首个终态获胜；平台确定错误后到达的 `/api/interrupt` 返回 `accepted:false, status:"unmatched"`，不能把失败覆盖为 cancel。`stream:false` 仍完成相同持久化，但 HTTP 返回错误 envelope。

预算错误闭合后的历史可以安全用于同一 Chat 的后续新 run。部署修复前已经缺少 tool result、且执行状态无法证明的历史不会自动修复，仍按 `chat_history_incomplete` 返回 `409`。

orchestrated Team 的总控 reasoning 和 `agent_delegate` 工具事件会被过滤，不进入客户端事件流。成员输出继续使用现有 `task.*` 与 task-scoped `content.*`：成员事件带 `taskId`，可带 `teamId`、成员 `agentKey`、`presentation:"task"`，并在 `actor` 中标记 `type:"agent"`。一项和多项委派使用相同终止规则，成员正文不会成为根回答；最终非流式 `content`、run summary 与 `AssistantText` 只取总控生成的唯一 Team 最终正文。

`run.activity` 是运行中的非终止状态事件，用于展示当前 run 正在等待、运行、重试或完成某个活动阶段。基础字段为 `runId`、`chatId`、`phase`、`status`；可选字段包括 `taskId`、`backend`、`key`、`message`，以及按场景嵌套的 `retry` / `recovery` / `degradation` 对象。当前 native 模型调用使用 `phase:"model_call"`，可恢复重试使用 `status:"retrying"` 且把 `attempt`、`maxAttempts`、`reason`、`timeoutSeconds`、`elapsedMs` 放入 `retry`。`run.activity` 不表示 run 失败；`run.error` 仍是终止事件，发出后不应再出现 content / reasoning / tool 等业务事件，后面只允许传输层 `[DONE]`。`run.activity` 只用于 live / attach，默认不进入 `/api/chat` 历史回放。

native LLM loop 以平台内部的 model turn commit 作为唯一接受边界。provider 合法终止、流式块完整收尾、tool call 完成 materialize 且通过平台接纳检查后才 commit；该控制信号不进入 SSE / WebSocket。`usage`、`contextWindow`、provider `finish_reason` 和 tool result 都不是完成标记，其中 `usage` / `contextWindow` 是可选元数据，缺失不会阻止正常 turn 提交。

commit 前遇到 EOF、非法流帧、连接中断或可重试的 provider stream 错误，且尚未开始工具执行时，平台丢弃整个 attempt 并按模型 retry budget 重试。客户端会收到：

```json
{
  "type": "run.activity",
  "phase": "model_call",
  "status": "retrying",
  "retry": {
    "attempt": 2,
    "maxAttempts": 3,
    "reason": "provider stream ended unexpectedly",
    "timeoutSeconds": 60,
    "elapsedMs": 60123
  },
  "recovery": {
    "action": "discard_incomplete_model_turn",
    "runSeq": 1,
    "reasoningIds": ["reasoning_1"],
    "contentIds": ["content_1"],
    "toolIds": ["call_1"]
  }
}
```

客户端收到该 recovery 后应按给出的 id 移除已经展示的半截 reasoning、content 或 tool。重试耗尽时平台发送 `run.error`，未提交 attempt 不进入 JSONL 或 run summary。model turn 与后续 tool batch 是两个独立事务边界：turn commit 后，完整 tool call 会保留；工具执行失败写正常失败 tool result。工具已经开始执行或可能产生副作用时，平台不会通过回滚 turn 自动重试，避免重复执行。

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

`role` 可选值为 `user`、`assistant`、`automation`、`system`，普通 query 缺省为 `user`。`automation` / `system` 的 `request.query` 会保留在 trace 中，但不会作为可见用户消息参与搜索或对话导出。`role` 只影响本次 query 展示语义，不决定 chat 摘要的 `source`；外部请求不能通过 `role=automation` 或传入 `source` 伪造 automation 创建来源。普通 HTTP `/api/query` 传入 `sourceUser` 也不会改变 source；该字段只在受信 channel/gateway 上下文中作为远端用户提示使用。

Markdown 与 Snapshot 导出统一由 `Summary + LoadChat` 投影一次内部 `ConversationSnapshotV1`。它只包含已绑定的可见根 query、reasoning/content snapshot 和运行终态，不包含子任务、工具、系统提示、附件、内部 ID 或原始 payload。构建过程只执行一次 JSON 序列化，同一份字节用于 20 MiB 校验和 `format=snapshot` 响应；消息总数最多 2000 条，不限制单条消息字节数。Markdown 仅消费 Snapshot 结构体，写出已完成轮次的用户问题和最后一个 assistant 回答。

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

#### CODER model options

`GET /api/model-options` 返回聊天输入区运行时可选项。前端按当前 agent `mode` 自行决定是否展示该控件：

- `models`: 当前 model registry 中可展示的聊天模型，字段为 `key/name/icon/provider/modelId/protocol/isReasoner/isVision/contextWindow/reasoningEfforts`。native reasoner model 的 `reasoningEfforts` 固定为五个启用档位 `LOW/MEDIUM/HIGH/XHIGH/MAX`；ACP 透传模型继续使用 bridge 声明。`icon` 是可选的模型图标标识；ACP 透传模型仅在上游 `/api/models` 返回该字段时携带。普通模型要求 `type: chat`、provider 存在且 `apiKey` 非空；`protocol: ACP_PASSTHROUGH` 的 ACP 透传模型不要求 provider。`type: embedding`、`type: image-generation` 与 `type: vl` 均不会出现在聊天模型选项中。
- `reasoningEfforts`: native model options 固定为 `NONE`、`LOW`、`MEDIUM`、`HIGH`、`XHIGH`、`MAX`，其中 `NONE` 表示关闭思考深度；ACP CODER 仍按 bridge 的模型发现结果生成
- `defaultModelKey`: 可展示模型中的默认模型；优先普通可调用模型，没有时可回退到 ACP 透传模型，无默认模型时为空
- `defaultReasoningEffort`: 固定为 `MEDIUM`

`GET /api/admin/agents/editor-options` 的 reasoner model 同样返回 `reasoningEfforts: [LOW, MEDIUM, HIGH, XHIGH, MAX]`。这里及聊天、usage、回放中记录的均为用户选择的逻辑档位；provider 实际映射值不会作为额外字段回显。

其中 `contextWindow` 是 API 响应字段名；model registry YAML 中对应配置字段为 `maxInputTokens`。

HITL 三态细节见 [HITL协议](HITL协议.md)。真流式、heartbeat、attach backlog 与 H2A 缓冲见 [真流式和H2A](真流式和H2A.md)。

### KBASE

KBASE API 接受所有 `kbaseConfig.enabled: true` 的 Agent，包括专用 `mode: KBASE` 和挂载公共 capability 的普通 Agent；存在但未启用 KBASE 的 Agent 与未知 Agent 均返回 `404`，Manager 不保留 disabled capability。手工 refresh 与运行时工具 `kbase_refresh` 调用同一个后端入口。KBASE 的 search/files/read/status 工具声明为只读，BTW/read-only policy 下仍可使用；refresh 是变更索引状态的操作，在只读 policy 下禁用。五个 KBASE tool 名称、REST 路径、`SearchHit`、chunk ID 和 `source.publish` 契约固定由 LanceDB 路径提供。agent catalog 热重载完成后会立即重绑所有 enabled Workspace watcher；Agent 删除、禁用或 Workspace/config 变化不会继续沿用旧 watcher，周期 reconcile 仅作为兜底。

启用 KBASE capability 的 Agent 在运行时调用 `kbase_search` 且召回到内容时，会额外通过 live stream 发布 `source.publish` 事件。事件包含 `kind: "kbase"`、`query`、`sourceCount`、`chunkCount` 与按检索来源聚合的 `sources[].chunks[]`，chunk 可携带 `path`、行号、页码、slide、`sourceType`、`matchType`、`score` 等定位字段；chat JSONL 会把该事件作为对应 `react-tool` step 的顶层 `sources.items[]` sidecar 持久化，`/api/chat` replay 时再合成 `source.publish` 事件并保留原始 `liveSeq`，供时间线与 `/api/attach.lastSeq` 使用。当前 `_type:"event"` 的 `source.publish` 也保持可回放。

`artifact_publish` 仅在整个批次文件物化且 `<chatId>/.tools/artifacts.json` 原子写入成功后发布 `artifact.publish`。事件包含合法 epoch-millisecond `timestamp`、`chatId`、`runId`、`toolId`、`artifactCount`、`artifacts`，子任务有明确归属时额外包含 `taskId`；每个 `artifacts[]` 项至少包含 `artifactId/name/mimeType/sizeBytes/sha256/url`。发布器从物化后的真实文件计算 SHA、大小和统一文档 MIME；manifest 未声明或旧逻辑会声明为 `application/octet-stream` 的安全 UTF-8 文本，在新产物写入时即规范化，不批量迁移历史 manifest。JSONL 的对应 `react-tool.artifacts.items[]` 只是该次调用的审计记录；`GET /api/chat` 的 `data.artifact = { items: [...] }` 只从 manifest 恢复。

`image_generate.images[].path` 与 `artifact_publish.artifacts[].path` 是工具间传递的内部文件系统字段，可以是当前 Host 的绝对路径，但不得进入 Markdown 或用户可见正文。`image_generate.images[].url` 指向 Chat 根目录中的生成文件；发布时复制到 `artifacts/<runId>/<filename>`，成功后的 `publishedArtifacts[].url` 必须指向该发布副本，并优先于生成源 URL。工具若没有返回合法 `url`，模型必须明确报告物化/发布失败，不能伪造图片或下载链接。

路径分类契约如下：

| 输入形式 | `image_generate` | `artifact_publish.path` | Markdown / 工具返回 `url` | `/api/resource?file=` |
|---|---|---|---|---|
| Workspace 相对路径 | 不适用 | 接受，按当前 Workspace 解析 | 不作为本地 Markdown 地址 | 不作为资源键 |
| 当前 Chat 内的 Host 绝对路径 | 内部 `path` 可返回 | 接受 | 禁止展示；改用 ChatScope `url` | 拒绝；使用 ChatScope 逻辑键读取 |
| 普通 Agent Workspace 内的 POSIX 绝对路径 | 内部 `path` 可返回 | 接受，canonical 后仍须属于当前根 | 允许实时引用 | 必须同时传当前 `chatId` 与 Bearer/Cookie |
| 冻结临时根内的实际 Host 绝对路径 | 不适用 | 接受，按最终 canonical 目标校验 | 普通 Agent 允许实时引用；Team 禁止 | 必须同时传当前 `chatId` 与 Bearer/Cookie；symlink/junction 逃逸拒绝 |
| `@chat`、`@workspace`、`@temp`、Container `/chat`、`/workspace` | 不适用 | 接受并映射到当前受控根 | 禁止展示 `@*`；临时文件应使用实际 Host 绝对路径 | 禁止语义根；只接受 ChatScope 或实际绝对路径 |
| 其他 chat 或 Workspace/冻结临时根之外的绝对路径 | 不适用 | 拒绝 | 禁止 | 拒绝 |
| `file://`、当前 Host 不支持的异平台/UNC 绝对路径 | 不适用 | 拒绝 | 禁止 | 拒绝 |
| `http://`、`https://`、`data:`、`blob:` | provider 图片响应先下载并物化到当前 Chat | 拒绝作为发布源 | 允许原样引用 | 不作为资源键 |
| `relative/path` ChatScope 引用 | 作为 `url` 返回 | 不作为 `path` | 当前 Chat 稳定资源格式 | adapter 加当前 `chatId` 后接受 |
| `<currentChatId>/relative/path` | 不再生成 | 不作为 `path` | 禁止 | 仅可作为隐藏 HTTP 逻辑键 |
| 历史 `/api/resource?file=...` | 不再生成 | 不作为 `path` | 不迁移、不预览 | endpoint 本身继续作为内部数据面 |

KBASE 工具只读取 active 索引库，不直接访问宿主文件系统。`kbase_search` 支持 `pathPrefix`、`pathGlob`、`type` 与 `offset` 做 scoped retrieval；`kbase_files` 支持按 `path`、`pattern`、`status`、`type`、`mode=files|tree`、`depth`、`head_limit`、`offset` 浏览已索引/已扫描文件元数据。Lance 路径并行取 vector 与 FTS 候选并使用加权 RRF 融合；`matchType` 为 `vector|fts|hybrid`，score 归一化到 `[0,1]`。`matchCount` 是受 candidate 上限约束的两路去重并集数，不是全库总命中数。

专用 KBASE 的 main/editing stage 都挂载 `file_read/file_glob/file_grep/file_write/file_edit`，两者工具 schema 相同，Workspace 固定为本 run 冻结的 `runtimeConfig.workspaceRoot`；相对路径从该 Workspace 解析，当前 `chatId` 的 Chat 目录只保存在 `ChatDir` 并通过 `@chat` 使用。所有获准目录统一先服从 AccessPolicy。未开启 editing 时 Workspace write/edit 返回 `kbase_editing_mode_required`；`hostAccess`、writeRoots、approval 和 `full_access` 不能替代该 Workspace mutation gate。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/kbase/{agentKey}/status` | 无 | 当前 Lance 索引状态；`workspaceRoot` 是唯一内容根，不再返回 `sourceRoot`；包含 `degraded/error/engine/schemaVersion/generation/indexes/sidecar/pendingRecoveryOperations/pendingChanges/storageDiskUsage`；FTS/vector index 状态包含未索引行数 |
| POST | `/api/kbase/{agentKey}/refresh` | body: `force` 可选 | 手工 refresh 始终做完整文件对账；结果在原字段外增加 `scope/candidatePaths/newFiles/modifiedFiles/metadataOnlyFiles/unchangedFiles/embeddedChunks/reusedChunks/pendingChanges`；`force=true` 构建新 generation |

status 中 Lance 字段是可选扩展，旧客户端可忽略：

```json
{
  "workspaceRoot": "/absolute/docs",
  "stale": false,
  "engine": "lancedb",
  "schemaVersion": "4",
  "generation": {
    "id": "kbg_...",
    "state": "active",
    "tableVersion": 12,
    "createdAt": 1700000000000
  },
  "indexes": {
    "fts": {"type": "FTS/ICU", "ready": true},
    "vector": {"type": "flat", "ready": true, "unindexedRows": 0}
  },
  "sidecar": {
    "available": true,
    "protocolVersion": 2,
    "engineVersion": "2.0.0",
    "lancedbVersion": "0.30.0"
  },
  "pendingRecoveryOperations": 0,
  "pendingChanges": 0,
  "storageDiskUsage": 0
}
```

`lastIndexedAt`、`indexes.lastOptimizedAt` 以及尚未完成的 `lastRun.finishedAt` 都是可选时间点：尚未发生时省略，control DB 中字符串 metadata 只在映射为公开 status 时严格校验，不能透出 `0` 或原始字符串。

KBASE 固定使用 LanceDB；没有 active generation 时 status/search 均标记 stale，search 会触发 refresh。sidecar 不可用时显式返回 unavailable，绝不回退 SQLite。generation 构建、建索引或验证失败时 active generation 不会被替换。watcher 的 change-set 路径不会通过 REST/tool 暴露，外部普通 refresh 仍是完整对账。当前没有新增公开的 generation rollback REST 路由，Lance generation 原子回滚能力保留在 KBASE Manager 内部。

容器与本地进程探活使用免鉴权 `GET /healthz`。它不返回用户数据：始终检查 Go HTTP runtime；存在 enabled KBASE capability 时，还通过 Go 内部持有的 Bearer token 检查 sidecar protocol handshake。至少一个专用 `mode: KBASE` 将 sidecar 标为 required，不可用时返回 HTTP 503；只有普通 Agent optional capability 时仍返回 HTTP 200，并在 `data.kbase` 中报告 `degraded` 与错误。

refresh 示例：

```bash
curl -sS -X POST http://127.0.0.1:11949/api/kbase/docs_kbase/refresh \
  -H "Content-Type: application/json" \
  -d '{"force":false}'
```

### Memory

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/learn` | body: `requestId`、`chatId`、`subjectKey` | learn / auto memory 结果 |
| GET | `/api/memory/meta` | 无 | memory category/type/scope/status 元数据 |
| POST | `/api/memory/context-preview` | body: `chatId`、`message` | memory context 预览 |
| GET | `/api/memory/scope/list` | query: `agentKey` | scope 列表 |
| GET | `/api/memory/scope/detail` | query: `agentKey`、`scopeType`、`scopeKey` | scope 详情 |
| POST | `/api/memory/scope/save` | body: `agentKey`、`scopeType`、`scopeKey`、`mode`、`markdown`、`records`、`archiveMissing` | scope 保存结果 |
| POST | `/api/memory/scope/validate` | body: `agentKey`、`scopeType`、`markdown` | scope markdown 校验结果 |
| GET | `/api/memory/record/list` | query: `agentKey`、`scopeType`、`scopeKey`、`category`、`status`、`limit`、`cursor` | memory record 列表 |
| GET | `/api/memory/history` | query: `agentKey`、`memoryId`、`limit`、`cursor` | memory history |
| GET | `/api/memory/record/detail` | query: `id` | memory record 详情 |
| GET | `/api/memory/record/timeline` | query: `id`、`limit` | memory record timeline |

### Viewport / Resource

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/file` | query: `agentKey`、`path`、`response` 可选 | agent workspace 文件；默认 JSON metadata/text，`response=content` 返回文件内容流 |
| GET | `/api/project/tree` | query: `agentKey`、`path`、`limit`、`cursor` | CODER/KBASE Workspace 单层目录树，目录优先稳定排序 |
| GET | `/api/project/changes` | query: `agentKey`、`chatId`、可选 `runId/limit/cursor` | 当前 Chat 的 Run 文件历史列表 |
| GET | `/api/project/diff` | query: `agentKey`、`chatId`、`runId`、`path`、可选 `encoding` | 单个 Run 快照的原始/当前文本 |
| GET | `/api/viewport` | query: `viewportKey`、`viewportType` | viewport 模板或 fallback |
| GET | `/api/resource` | query: `file`、`chatId`、`t`、`download` | ChatScope 或普通 Agent Workspace/冻结临时根资源字节；绝对路径必须传 `chatId` |
| GET | `/api/tool-result` | query: `chatId`、`path`、`t` | `.tools/results/<toolId>.json` 完整工具结果；`t` 为可选 resource ticket |
| POST | `/api/upload` | multipart: `requestId`、`chatId`、`name`、`file` | upload ticket；文件保存为 `<chatId>/<name>` |
| POST | `/api/document/commit` | body: 来源判别联合、`mode`、`expectedRevision`、MIME 与文本/二进制 payload | 覆盖原文档或生成新 Artifact 的身份、类型与 revision |
| POST | `/api/resource/image/commit` | body: `operation=resource.image.commit`、`profile`、`agentKey`、`chatId`、`resourceId`、`relativePath`、`mode`、`expectedRevision`、`mimeType`、`dataBase64` | 覆盖原 Artifact 或生成新 Artifact 的身份与 revision |

`/api/upload` 的 `chatId` 与 `name` 均可省略。无 `chatId` 时平台会先分配会话；同时无 `name` 时，该会话以 `<default>` 标记为尚未正式命名。仅完成上传、尚未接受首条正式 query 的占位会话仍可通过 `chatId` 继续使用，但不进入 `/api/chats` 历史列表。上传文件保持既有契约，落入 `<chatId>/<name>`，公开 `url` 为不带 `chatId` 的 `<name>`。首条正式 query 在会话尚无历史 run 时会用 message 生成 `chatName`，并广播 `chat.renamed`；已命名或已有历史的会话不会被覆盖。

`/api/file` 与 `/api/agent/open-directory` 的 `directoryType:"workspace"` 都使用 `runtimeConfig.workspaceRoot`。`path` 可以是 Workspace 相对路径，也可以是宿主机绝对路径；绝对路径经 canonical 解析后必须分类为 Workspace，进入整个 ChatsRoot 会返回 `path_crosses_chat_root`，`..` 与 symlink escape 会返回 forbidden。默认响应使用统一 JSON 包裹，文本文件内联 `content`，二进制/PDF/图片只返回 metadata 与 `contentUrl`；`response=content` 时直接返回文件字节流，不使用 JSON 包裹。文件 JSON 与内容流共用文档元数据解析器，返回一致的 `documentKind/contentKind/mimeType/revision/sizeBytes`；Markdown 与普通文本 MIME 明确声明 UTF-8。该接口不读取 KBASE 索引库，也不扩大 `hostAccess.readRoots`。

Project 三个端点是只读 HTTP 数据面，只接受服务端从 `agentKey` 解析出的精确 `mode: CODER|KBASE`，不接受 Team、其他 mode 或客户端 `workspaceRoot`。所有 `path` 都是 Workspace 相对 POSIX 路径：拒绝绝对路径、反斜杠、`..`、ChatsRoot、symlink 逃逸和 device file。KBASE 不要求 `editingMode:true`。

`/api/project/tree` 每次只枚举一个目录，默认 `limit=200`、最大 1000；目录优先并按名称稳定排序。游标绑定响应 `revision`，继续分页前目录发生变化会返回 HTTP 409，错误码为 `data.error.code=directory_changed`。目录 symlink 不允许展开；指向 Workspace 内普通文件的 symlink 可交给 `/api/file` 预览，逃逸、断链、目录目标或非普通文件保留 `kind:"symlink"` 但返回 `accessible:false`。Workspace 为文件系统根时仍屏蔽整个 ChatsRoot。

`/api/project/changes` 验证 Chat owner 与 Agent 一致；指定 `runId` 时还会验证 Run 属于该 Chat。不指定 Run 时按每个 Run 的独立基线返回现有 file-history，不跨 Run 聚合；已离开当前 Workspace 的历史条目不回显。空目录和空历史分别固定返回 `entries:[]`、`runs:[]`、`items:[]`，不会返回 `null`。`/api/project/diff` 从同一历史存储一次返回 `original/current` 两侧；新增和删除由各侧 `exists` 表达。任一快照超过 `file-tools.max-read-bytes` 返回 413 `diff_too_large`，二进制、含二进制控制内容、无法按请求编码解码的快照返回 415 `unsupported_media_type`。Bash、ACP 或外部程序绕过 FileTools 的写入没有历史快照，只能通过 `/api/file` 读取实时内容，不会生成伪 Diff。

`/api/resource` 的 ChatScope 数据面使用 `file=<chatId>/<relativePath>` 逻辑资源键。Markdown 的 `relativePath` 每段先做 URI path 编码，adapter 再把整个逻辑键做 query 编码；服务端逐段只解码一次并执行 canonical/symlink 边界检查，拒绝路径穿越、其他 chat 所有权和 `.tools`/`.btw` 内部目录。active 文件不存在时会在同一 chat 的 archive 副本中查找，使生成和发布资源可稳定回放。GET 与 HEAD 共用文档元数据解析器，以 canonical `relativePath` basename 为语义文件名，内部落盘或缓存文件名只作兜底；两者一致返回 `X-Document-Kind`、`X-Document-Revision`、`Content-Type` 与 `Content-Length`。`application/octet-stream` 仅是未知 MIME，扩展名只用来选择候选文档类型，Markdown/Text 还必须通过安全 UTF-8 文本校验；固定 512 字节样本额外读取最多 `utf8.UTFMax-1` 字节，只用于验证跨越样本边界的 3/4 字节 UTF-8 字符，不放宽样本中间的非法字节、完整内容末尾的不完整字符或样本内 NUL。远端缓存必须保留源资源 revision，不得用缓存文件自身 mtime 替代。

`POST /api/document/commit` 是统一文档提交数据面。Workspace File 只允许带 expected revision 原位覆盖；Artifact 默认创建新 Artifact，也可带 revision 明确覆盖；Reference 只允许创建新 Artifact。revision 对客户端不透明，冲突统一返回 409 `revision_conflict`。Platform 在每次提交中重新验证 Agent/Chat 所有权、Workspace 根、ChatScope profile、canonical path、普通文件、symlink 和 ChatsRoot 边界；文件与 Artifact manifest 通过同目录 staging 和平台显式原子替换提交。

`POST /api/resource/image/commit` 保留为旧 Desktop 图片编辑器的兼容适配器，并委托统一 document committer。它继续保持原请求、响应和 PNG/JPEG/WebP 约束，不得形成第二套路径、revision 或 manifest 写入语义。

绝对路径数据面使用 `file=<hostAbsolutePath>&chatId=<currentChatId>`，仅接受 Bearer/Cookie 主体，resource ticket 不能授权。服务端从 chat owner 解析 `agentKey` 和当前 `AgentDefinition.workspaceRoot`：普通 Agent 允许 canonical 后仍在 Workspace 或冻结临时根的路径，当前/其他 Chat 的绝对路径必须改用 ChatScope，Team chat 一律拒绝绝对路径。临时根由进程启动时的统一解析器冻结：Unix/macOS 纳入 `os.TempDir()` 与 canonical `/tmp`，macOS 自动把 `/tmp`、`/private/tmp` 视为同一根；Windows 只纳入 `os.TempDir()`，不硬编码 `C:\Windows\Temp`。所有临时请求按最终 canonical 目标校验，symlink/junction 逃逸与 `..` 逃逸拒绝。Workspace 和临时绝对路径都是实时引用，文件变化或删除会直接影响回放。两种读取均返回原始字节和准确 `Content-Type`；图片默认 `Content-Disposition: inline`，`download=true` 改为 `attachment`。

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
- token 可通过 `Sec-WebSocket-Protocol: bearer.<token>` 或 query token 传递；服务端会在握手成功时回写匹配的 subprotocol。
- 客户端可通过 query 自报监控元数据：`source` 与 `deviceId`，例如 `/ws?source=webclient&deviceId=device-123`。普通 source 转小写后只用于监控和日志展示，不参与权限或能力声明；Desktop 的 `desktop-main` / `desktop-btw` 是两个受限 lane 标识，但仍必须同时通过 app scope、JWT device claim 与握手 deviceId 校验，`source` 本身绝不构成授权。
- WebClient 控制连接额外携带 `surfaceId`，推荐形式为 `/ws?source=WebClient&deviceId=device-123&surfaceId=surface-123`。Platform 在握手时记录该元数据，不需要注册帧；同一 client boundary 与 `surfaceId` 的新连接替换旧连接。Desktop Broker 不再按 Main Chat、Copilot 或 WebView surface 创建连接：同一设备只登记一个 `desktop-main`，并在首次 BTW 时登记一个 `desktop-btw`。
- `desktop-main` 是唯一 Desktop Main 默认 target，接收全局 Push 和反向 Desktop Action/CDP；`desktop-btw` 不参与默认 target replacement，不接收这些 Push/request。第二条相同 Desktop lane 连接只替换该 lane 的旧连接，Primary 与 BTW 可以同时存活。
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

Desktop Action 反向请求直接使用具体 Action 名作为 `type`，可信上下文位于帧顶层，payload 只包含该 Action 参数：

```json
{"frame":"request","type":"desktop.workpanel.getState","id":"dsa-123","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{}}
{"frame":"request","type":"desktop.display","id":"dsa-124","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"},"payload":{"kind":"effect","effect":"fireworks","durationMs":8000}}
{"frame":"request","type":"desktop.cdp.call","id":"dsc-123","payload":{"requestId":"dsc-123","method":"Runtime.evaluate","params":{"expression":"document.title"},"targetId":"target-1","source":{"runId":"run-1","chatId":"chat-1","agentKey":"agent-1"}}}
```

Desktop 模式由 Main Broker 按正式 Action 注册表处理 83 个具体 `desktop.*` request type；Standalone 只由当前根 agent-webclient 处理七个 `desktop.workpanel.*` 与 `desktop.display`。Desktop-only 的 `desktop.workpanel.openLocalFile` 在 Standalone 由 Platform 直接返回 `desktop_action_unsupported_runtime`，不转发给 agent-webclient。Action 的 `id` 同时是工具请求标识，响应必须保持同 `id`、同 Action `type`；旧统一 envelope 不提供兼容入口。目标必须来自当前 run，缺失或断连立即失败，不选择其他连接。大 JSON 使用 `desktop.bridge.response.delta`，CDP 截图使用 `desktop.cdp.screenshot.delta`；stream event 包含 `seq/type/timestamp/encoding/chunk`，终态 response manifest 包含 `streamed/streamId/encoding/chunkCount/totalBytes`。超时或取消时 Platform 发送 `{"frame":"push","type":"desktop.bridge.cancel","payload":{"requestId":"..."}}`。

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
| `run.started` | `runId`、`chatId`、`agentKey`、必填 `startedAt` |
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
| `awaiting.asking` | `chatId`、`runId`、`agentKey` 或 `teamId`、`awaitingId`、`mode`、`createdAt`、可选 `timeout` / `viewportType` / `viewportKey` |
| `awaiting.answered` | `chatId`、`runId`、`agentKey` 或 `teamId`、`awaitingId`、`mode`、`status`、`answeredAt`、可选 `errorCode` / `submitId` / `durationMs` |
| `resource.pushed` | `chatId`、`artifactId`、`name`、`mimeType`、`sha256`、`sizeBytes`、`pushedAt` |

上述全局 Push 广播排除 `desktop-btw` 连接。BTW lane 只接收握手、heartbeat、本连接请求的 response/error 与 Run stream；Desktop Primary 可以按全局唯一 runId 使用 `run.finished` 收敛 BTW RunChannel。

除 `heartbeat.timestamp` 外，platform 主动发送的 push payload 不使用 `timestamp`；它们用上表的业务语义时间字段。这是硬切换，不会双写旧字段，前端与服务端需要同版本发布。SSE 与 WebSocket `frame:"stream"` 的 `event.timestamp` 仍是每个业务流事件必填的 epoch milliseconds。`auth.refresh` response 在 JWT 存在 `exp` 时才返回 `expiresAt = exp * 1000`；没有 `exp` 时省略字段。`auth.expiring.expiresAt` 同样始终是 epoch milliseconds。客户端不得把缺失 `readAt` / `expiresAt` 解释为 1970 或当前时间。

`run.finished` 是 run 退出 active 状态的终态通知：`finishReason` 只允许 `complete | error | cancel`，对应的 `status` 分别是 `completed | failed | interrupted`。前端必须以 `status` 判断结果，不得仅因收到 `run.finished` 就当作成功；完整错误内容仍从同一 run 的 stream `run.error` 获取。

`awaiting.asking.timeout` 与 stream 中的 `awaiting.ask.timeout` 语义一致：对普通 HITL 等待项，`0` 表示无限等待、不自动超时；大于 `0` 时由后端按真实时间独立倒计时，observer / attach / detach 状态不会暂停或延长后端超时。CODER planning confirmation 使用 `mode:"planning"` 和同时包含 `planningId + planningFile` 的 `planning` payload，永远省略 `timeout`，表示永久等待；它不同于 `plan_*` / plan-tasks 的执行任务计划。awaiting mode 只允许 `question | approval | form | planning`。

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
| `/api/agents` | `includeChats`、`includeTeam`、`scope`、`mode` | `response` |
| `/api/agent` | `agentKey` | `response` |
| `/api/skills` | `agentKey` | `response`；data 与 HTTP `/api/skills` 完全一致 |
| `/api/agent/model-config` | `agentKey`/`key`、`modelKey`、`reasoningEffort` | `response` |
| `/api/model-options` | 无 | `response` |
| `/api/teams` | 无 | `response` |
| `/api/chats` | `lastRunId`、`agentKey`、`mode`、`limit` | `response` |
| `/api/chats/order` | 空 payload 读取；或 `operation` 与对应字段更新 | `response` |
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
| `/api/chats/search` | `query`、`agentKey`、`teamId`、`limit` | `response` |
| `/api/query` | `QueryRequest` | `stream` |
| `/api/btw` | BTW query payload | `stream`；仅 `source=desktop-btw` 的已认证 Desktop lane |
| `/api/attach` | `runId`、`agentKey` 或 `teamId`、`lastSeq` | `stream` |
| `/api/detach` | `runId`、`agentKey` 或 `teamId`、`reason` | `response`；关闭当前 WS 连接上该 run 的 observer，不中断 run |
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
| `/api/learn` | `LearnRequest` | `response` |
| `/api/memory/meta` | 无 | `response` |
| `/api/memory/context-preview` | `chatId`、`message` | `response` |
| `/api/memory/scope/list` | `agentKey` | `response` |
| `/api/memory/scope/detail` | `agentKey`、`scopeType`、`scopeKey` | `response` |
| `/api/memory/scope/save` | scope 保存字段 | `response` |
| `/api/memory/scope/validate` | `agentKey`、`scopeType`、`markdown` | `response` |
| `/api/memory/record/list` | memory record 过滤字段 | `response` |
| `/api/memory/record/detail` | `id` | `response` |
| `/api/file` | `agentKey`、`path`、可选 `encoding`、可选 `response=json` | `response`；data 为 agent workspace 文件 metadata，文本文件包含 `content` |
| `/api/viewport` | `viewportKey`、`viewportType` | `response` |
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

本次升级只实现 Agent 接出注册、解除、查询和对账。注册成功的 `registrationId` 会保存，但尚不用于 Query/Run 路由；新版 Query Stream、Run TTL、HITL Schema、Steer/Interrupt/Submit 控制路由均未实现。完整 Gateway 帧定义见 [Gateway Agent 注册与调用协议](Gateway-Agent注册与调用协议.md)。

## 约束与注意事项

- HTTP query 参数在 WS payload 中通常以同名 JSON 字段传入。
- `GET /api/attach`、WS `/api/detach`、`POST /api/submit`、`POST /api/steer`、`POST /api/interrupt` 按 run 的公开 owner 校验 `agentKey` 或 `teamId`；二者不能用隐藏执行身份互相替代。
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

该接口定义为 Workspace Terminal。macOS/Linux 使用 Unix PTY，Windows 使用 ConPTY / PowerShell PTY；cwd 只由 Platform 从 Agent 的最终 Workspace 解析，不信任前端 cwd，也不会回退 Chat。没有 Workspace、Workspace 不存在或不是目录时拒绝打开。terminal 只冻结 `AP_AGENT_CONFIG_HOME=<ru-agents>/<agentKey>/.config` 与 `AP_WORKSPACE_DIR=<workspace>`，不注入 `AP_CHAT_DIR`。如果未来需要 Chat Terminal，将使用独立显式类型，不复用本接口或隐式 fallback。

## 相关文件

- `internal/server/server.go`
- `internal/server/ws_routes.go`
- `internal/server/ws_query_routes.go`
- `internal/server/ws_resource_routes.go`
- `internal/api/types.go`
- `internal/api/types_automation.go`
- `internal/api/types_memory_console.go`
- `internal/ws/protocol.go`
- `docs/手工测试用例.md`
