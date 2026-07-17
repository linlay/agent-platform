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

## 统一时间契约（破坏性）

platform 自己定义和拥有的 API、JSONL、SSE、WebSocket 与 trace 生命周期时间点，统一使用未加引号的 Unix epoch milliseconds JSON 整数（Go `int64`、客户端 `number`）。可接受范围固定为 `1000000000000..9007199254740991`：这既拒绝十位 Unix 秒，也保证 JavaScript number 精确表示。

- 已声明的平台字段（例如 chat/run 的 `createdAt`、`updatedAt`、`startedAt`、`completedAt`，stream envelope 的 `timestamp`，以及 `expiresAt`）必须是 epoch-ms。可选字段缺失时必须省略；不得输出 `0`、`null`、数字字符串、ISO 字符串或浮点数。
- 已声明的可读时间（`*Time` 或 `iso`）必须是带 `Z` 或 offset 的 RFC3339 / RFC3339Nano；若协议声明它与 epoch-ms 字段配对，两者必须表示同一毫秒时刻。
- 名字不是契约：外部 tool result、MCP content、Desktop action result、trace request/response/tool payload 的 `createdAt`、`timestamp`、`iso` 等业务字段不会因名称被平台推断为时间。
- 工具结果只有在其可选 `outputSchema` 显式声明时才校验时间：`x-platform-time: "epoch-ms"` 表示严格毫秒整数，`format: "date-time"` 表示 RFC3339 可读字符串，`x-platform-time-pair` 表示显式配对。未声明 `outputSchema` 的工具结果是透明 JSON。
- 任何 producer、历史 JSONL/archive、trace 或上游 child/proxy stream 违反此契约，HTTP/WS 返回 `422`，其 `data.error` 固定含有 `code:"time_contract_violation"`、`field`、`location`、`expected:"epoch_ms_int64"`。已开始的 stream 会先发送平台本地 `run.error` 后结束；服务端绝不以当前时间、run ID 或完成时间修补原事件。
- 这是立即生效的破坏性切换：不迁移旧记录，也不提供双读、feature flag 或宽松解析。旧不合规 chat/archive/trace 不可读取，旧上游使用字符串、秒值或浮点 timestamp 的流会失败。

JWT `exp` / `iat` 与 resource ticket payload 的 `e` 仍是 token 内部的 NumericDate 秒值；`durationMs`、`timeout`、cron、本地日期拆分、ID、文件名和日志前缀不是结构化时间点。

## 核心流程

```text
普通 JSON API -> ApiResponse envelope
POST /api/query -> SSE message events -> data: [DONE]
POST /api/btw -> hidden read-only branch -> same SSE message events
GET /ws -> request / response / stream / push / error frames
文件上传下载 -> HTTP 数据面
```

文件传输按“HTTP 数据面 + WebSocket 控制面”划分：浏览器上传走 `POST /api/upload`，下载走 `GET /api/resource`；WebSocket `/api/upload` 用于 gateway 发送 `url + metadata` 下载通知，由 platform 按 metadata 中的 URL 自己通过 HTTP 拉取并校验（该 URL 可指向 gateway 的 `/api/pull/...`）。反向推送本地资源走 WS `/api/resource`，platform 再把文件字节 HTTP POST 到 gateway 的 `pushURL`（通常是 `/api/push/...`）；WS `/api/push` 不存在。

## HTTP API 定义

参数位置说明：`query` 表示 URL query，`body` 表示 JSON body，`multipart` 表示 multipart form。

### Catalog

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/agents` | query: `includeChats`、`includeTeam`、`scope`、`mode` | agent 列表；可选混入 Team 与最近 chat 摘要 |
| GET | `/api/agent` | query: `agentKey` | 单个运行时 agent 详情，不返回编辑专用字段 |
| POST | `/api/agent/model-config` | body: `agentKey`/`key`、`modelKey`、`reasoningEffort` | 更新 CODER agent 的运行时默认模型配置 |
| GET | `/api/teams` | 无 | team 列表，区分 `runtimeMode: legacy | orchestrated` |
| GET | `/api/skill-candidates` | query: `agentKey` | skill candidate 列表 |
| GET | `/api/model-options` | 无 | 聊天运行时可选模型与思考深度 |

`/api/agents` 的 `scope` 可取 `nav`、`copilot`、`invoke`、`internal`、`all`，省略时为 `all`；`includeChats` 为 `0..50`，省略时不附带 chat。可选 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；大小写无关，`PLAN_EXECUTE` 会归一为 `PLAN-EXECUTE`。`mode` 与 `scope` 为 AND，筛选普通 agent catalog 自身的 `mode`，不改变 `includeChats` 按 agentKey 获取 chat 的规则；未知 mode（包括普通 agent catalog 中不存在的 `TEAM`）返回空列表。

`includeTeam` 是可选布尔 query，省略或 `false` 时响应保持原有的 agent 列表和排序。设为 `true` 时，响应改为扁平联合列表，每项带 `kind:"agent" | "team"`：agent 项保留原有摘要字段；team 项返回 `teamId`、`name`、可选 `description/icon`、`runtimeMode`、`agentKeys`、`meta`，并和 agent 一样包含 `stats` 与可选 `chats`，但绝不返回虚拟 `key`、`mode`、workspace 或模型配置。此时 `scope` 与 `mode` 只过滤 agent，所有 Team 均保留；`mode=TEAM` 因而只会返回 Team。混合项按各自最新 chat 的 `lastRunId` 降序排列，无 chat 的项置后；同值按名称、kind 与稳定身份字段确定顺序。`includeChats=N` 对 Team 也按 `teamId` 返回最近 N 条 Team-owned chat。WebSocket `/api/agents` 使用等价的 `scope`、`includeChats`、`includeTeam`、`mode` 字段，其中 `includeTeam` 为 JSON boolean。

### Admin

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/admin/agents` | 无 | admin agent 列表，包含 invalid agent 诊断 |
| GET | `/api/admin/agents/detail` | query: `agentKey` | admin agent 详情，包含编辑配置、来源和诊断 |
| GET/PUT | `/api/admin/agents/order` | PUT body: `order` | agent 展示顺序 |
| POST | `/api/admin/agents/create` | body: `key`、`definition`、`soulPrompt`、`agentsPrompt` | 创建后的 agent 详情 |
| POST | `/api/admin/agents/update` | body: `key`/`agentKey`、`definition`、`soulPrompt`、`agentsPrompt` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/update-name` | body: `key`/`agentKey`、`name` | 更新后的 agent 详情 |
| POST | `/api/admin/agents/delete` | body: `key`/`agentKey` | 删除结果 |
| GET | `/api/admin/agents/editor-options` | 无 | agent 编辑器可选项 |
| GET | `/api/admin/skills` | 无 | skills-market skill 列表，包含状态、摘要诊断、更新时间、大小与引用 agent |
| GET | `/api/admin/skills/detail` | query: `key`/`skillKey`（`key` 为规范参数） | skill 详情，包含 `SKILL.md`、目录文件树、完整诊断和来源 |
| POST | `/api/admin/skills/create` | body: `key`、`skillMd`、`files[]` | 创建后的 skill 详情 |
| POST | `/api/admin/skills/delete` | body: `key` | 删除结果；仍被 agent 引用时返回 409 和 `usedByAgents` |
| GET/PUT | `/api/admin/skills/file` | query/body: `key`、`path`、`content`、`baseSha256` | 读取或保存 UTF-8 文本文件 |
| POST | `/api/admin/skills/file/delete` | body: `key`、`path`、`recursive`、`baseSha256` | 删除 skill 内文件或目录 |
| POST | `/api/admin/skills/file/mkdir` | body: `key`、`path` | 创建 skill 内目录 |
| POST | `/api/admin/skills/file/rename` | body: `key`、`fromPath`、`toPath`、`overwrite` | 重命名 skill 内文件或目录 |
| POST | `/api/admin/skills/file/upload` | multipart: `key`、`path`、`overwrite`、`file` | 上传 skill 内二进制或大文件 |
| GET | `/api/admin/skills/file/download` | query: `key`、`path` | 下载 skill 内非目录文件 |
| GET | `/api/admin/skills/v2` | 无 | v2 skill 列表摘要 |
| GET | `/api/admin/skills/v2/detail` | query: `key`、`openPath` | v2 skill 详情，返回 `fileManifest.entries[]` 与可选 `openedFile` |
| GET/PUT | `/api/admin/skills/v2/file` | query/body: `key`、`path`、`content`、`baseSha256` | v2 文本文件读取或保存 |
| POST | `/api/admin/skills/v2/file/create` | body: `key`、`path`、`content` | v2 创建文本文件 |
| POST | `/api/admin/skills/v2/file/mkdir` | body: `key`、`path` | v2 创建目录 |
| POST | `/api/admin/skills/v2/file/rename` | body: `key`、`fromPath`、`toPath`、`overwrite` | v2 重命名文件或目录 |
| POST | `/api/admin/skills/v2/file/delete` | body: `key`、`path`、`recursive`、`baseSha256` | v2 删除文件或目录 |
| POST | `/api/admin/skills/v2/file/upload` | multipart: `key`、`path`、`overwrite`、`file` | v2 上传或替换二进制/大文件 |
| GET | `/api/admin/skills/v2/file/download` | query: `key`、`path` | v2 下载非目录文件 |
| POST | `/api/admin/skills/v2/validate` | body/query: `key` | v2 重新加载并返回该 skill 当前校验结果 |
| GET | `/api/admin/tools` | 无 | tool 列表，含扁平化工具来源字段 |
| GET | `/api/admin/registries` | 无 | registry 文件列表摘要，含状态、脱敏 summary、首条诊断摘要与诊断数量 |
| GET/PUT | `/api/admin/registries/detail` | query/body: `category`、`file`、`content` | registry 文件详情或保存结果 |
| POST | `/api/admin/registries/validate` | body: `category`、`file`、`content` | registry 内容校验结果 |

`/api/admin/tools` 中 `kind` 表示调用方式（如 `backend`、`frontend`、`action`、`external`），`sourceType` 表示定义来源类型（如 `local`、`agent-local`、`mcp`），`sourceCategory` 表示来源分类：`platform` 为 runtime 自带工具，`external` 为 `paths.tools-dir` 下通过 RPC / YAML 接入的外部工具，`mcp` 为 MCP registry 同步工具。MCP 工具额外返回 `serverKey`。列表响应只返回 `key`、`name`、`label`、`description`、`kind`、`sourceType`、`sourceCategory`、`serverKey`，不透出内部 tool definition `meta`；接口不接收 query 过滤参数。

`/api/admin/skills` 只编辑 `paths.skills-market-dir` 下的共享 skill 目录，不直接编辑 agent 本地 `skills/` 同步副本。文件路径必须是相对路径，服务端拒绝目录逃逸和 symlink 跟随；JSON 文本读写限制为 UTF-8 且不超过 1 MiB，二进制或大文件通过 upload/download 接口处理。保存、上传、删除或重命名 skill 文件后会触发 `skills` reload 并级联 reload `agents`，使声明该 skill 的 agent 本地副本重新同步。

`/api/admin/skills/v2` 是面向文件编辑器的 canonical 接口。`detail` 不内联全量文件内容，而返回轻量 `fileManifest`：`revision`、`defaultOpenPath`、文件统计和预排序扁平 `entries[]`。每个 entry 使用完整相对 `path` 作为稳定 ID，并带 `parentPath/depth/order/contentKind/language/role/editable/downloadable/uploadable/renamable/deletable`。`openPath` 指向可编辑 UTF-8 文本文件时，`detail` 额外返回 `openedFile`；二进制或过大文件只返回 metadata。v2 保存使用 `baseSha256` 做并发保护，冲突返回 409。创建、删除、重命名、上传和 mkdir 的 mutation 响应会返回新的 `fileManifest` 与 `selectedPath`，方便前端直接刷新文件树。

`/api/admin/registries` 是列表接口，不返回 registry 文件绝对路径、完整 `diagnostics[]` 或文件大小；编辑器应通过 `/api/admin/registries/detail` 获取 `source`、完整诊断、`content`、`parsed` 与 `size`。

Registry 列表的 `summary` 按分类返回展示字段：provider 暴露 `baseUrl`；model 暴露 `provider/protocol/type/isVision/isReasoner/isFunction/maxInputTokens/maxOutputTokens/timeout`；MCP server 暴露 `baseUrl/toolCount`，其中 `toolCount` 是当前已同步注册的 MCP 工具数量；viewport server 仅暴露 `baseUrl`，当前不返回 viewport 数量。

`/api/teams` 每项返回 `teamId`、`name`、可选 `description/icon`、`runtimeMode`、`agentKeys` 与安全摘要 `meta`。`meta` 包含 `validAgentKeys`、`invalidAgentKeys`、`orchestrated`；legacy Team 额外返回 `defaultAgentKey/defaultAgentKeyValid`，orchestrated Team 只额外返回 `maxParallel`。接口不会返回隐藏总控 key、总控模型配置、system prompt、`SOUL.md/AGENTS.md` 内容或 internal-only `agent_delegate` 定义；`/api/admin/tools` 同样不列出该工具。

### Chat

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/chats` | query: `lastRunId`、`agentKey`、`mode`、`limit` | chat 摘要列表 |
| GET | `/api/chat` | query: `chatId`、`includeRawMessages` | chat 详情，默认含 events |
| POST | `/api/chats/search` | body: `query`、`agentKey`、`teamId`、`limit` | 全局 chat 搜索结果 |
| POST | `/api/read` | body: `chatId` | 标记已读结果 |
| POST | `/api/feedback` | body: `chatId`、`runId`、`messageId`、`rating`、`reason` | feedback 写入结果 |
| POST | `/api/chat/delete` | body: `chatId` | 删除 chat 结果 |
| POST | `/api/chat/rename` | body: `chatId`、`chatName` | 重命名结果 |
| POST | `/api/chat/derive` | body: `sourceChatId`、`sourceRunId`、`chatId`、`chatName` | 从已完成 run 派生新 chat |
| POST | `/api/chat/archive` | body: `chatId`、`reason` | 归档结果 |
| GET | `/api/chat/export` | query: `chatId` | Markdown 导出 |
| GET | `/api/chat/jsonl` | query: `chatId` | 原始 chat JSONL 文本；active 不存在时回退 archive |
| GET | `/api/chat/system-prompt` | query: `chatId`、`runId`、`agentKey` | 获取该 agent 在历史 run 中首次使用的持久化 system message；服务端从 run 的 system-init / step `systemRef` 解析快照 |
| GET | `/api/chat/llm-trace` | query: `file=<chatId>/.llm-records/<runId>_NNN.json` | 原始 LLM chat trace JSON 文本 |

`/api/chats` 的 `mode` 支持逗号分隔和重复 query 参数，所有非空值组成 OR 集合；大小写无关，`PLAN_EXECUTE` 会归一为 `PLAN-EXECUTE`。它只筛选 Agent-owned chat，并与 `agentKey`、`lastRunId` 为 AND 关系；Team-owned chat 天然包含在全局列表中，不受 `mode`（包括未知 mode）影响。显式 `agentKey` 仍只返回该 agent 的 chat，不会匹配 Team。可选 `limit` 必须为正整数且不设上限；省略时返回全部匹配项，传入时在全部筛选和固定排序 `updatedAt DESC, chatId DESC` 后截断结果。`limit=0`、负数、空值或非整数返回 400；当前不支持 offset、分页游标或自定义排序。WebSocket 的 `/api/chats` 请求使用等价的 `mode` 与 `limit` 字段（`limit` 未传为全部）。旧 `agentMode` 参数或 payload 会返回 400，调用方应改用 `mode`。

chat 摘要会在新数据中返回可选 `mode`；`/api/chat.runs[]`、`/api/agents?includeChats` 及 archive detail 中的共享 `runs[]` 均返回每次 run 的可选 `mode`。普通 agent 持久化规范 API mode（例如 `REACT`、`CODER`、`KBASE`、`PLAN-EXECUTE`、`PROXY`、`CHANNEL`）；orchestrated Team 固定为 `TEAM`，不会暴露隐藏协调器 key；legacy Team 仍记录实际成员的 mode。历史 chat/run 不根据当前 catalog 回填，mode 保持为空且不会命中 Agent mode 筛选；Team-owned chat 在 `/api/chats` 的 mode 查询中始终保留。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 摘要，以及 `/api/chat` 详情顶层在新数据中可包含 `source`，表示 chat 首次创建来源。当前只记录 query 与 automation 两类：`query` / `query:<user>` 表示由 query 创建，`automation:<automationId>` 表示由 automation 创建。旧数据为空、上传创建或派生创建时省略。channel 远程用户调用本机智能体仍属于 query source；gateway 可在受信 channel 请求中传 `sourceUser`，否则服务端会从形如 `wecom#single#user1#...` 的 chatId 中取远端用户段作为 `query:<user>`。`sourceChannel` 是 gateway/channel 路由标签，不承载 query / automation 语义。

`/api/chat` 详情固定返回顶层 `createdAt` 与 `updatedAt`；Desktop 不得从 runs、events 或本机时间推断它们。每个 `runs[]` 的 `startedAt` 由注册时捕获并持久化；已完成 run 的 `completedAt` 必填，仍在执行的 run 则省略 `completedAt`（绝不输出 `0`）。`activeRun.startedAt` 与对应 push `run.started.startedAt` 是同一个已捕获时刻；push `run.finished.finishedAt` 与完成记录的 `completedAt` 相同。`/api/chats` 的 chat 摘要、`/api/agents?includeChats=...` 的 `chats[]` 以及 `/api/chat` 的 chat 详情，在存在可恢复等待项时都包含顶层 `awaiting`：`awaitingId`、`runId`、`mode`、`status:"awaiting"`、`createdAt`。完整问题、审批项、表单和 planning 定义仍从 chat events 中的 `awaiting.ask` 获取。

`POST /api/chat/derive` 只支持 active chat 存储，不从 archive 直接派生。`sourceRunId` 省略时使用 source chat 的 `lastRunId`；source chat 必须没有 active run 和 pending awaiting，且目标 source run 已完成。服务端会创建新的独立 `chatId`，复制截至 source run 的可回放 JSONL 历史与必要资源，并为复制出的历史 run 生成新的 runId；返回 `lastRunId` 是新 chat 中映射后的 runId。派生成功后客户端继续用新 `chatId` 调 `/api/query`，后续运行不会写回原 chat。

`/api/chat` 返回 active run 时，`activeRun.lastSeq` 是本次 chat detail 已返回历史 events 覆盖到的 live stream 游标，客户端应用这些 events 后可把它作为 `/api/attach.lastSeq`。它来自 `chatId.jsonl` 每行顶层 `liveSeq` 的 replay 结果，不是内存 run 当前最新 seq；内存最新 seq 只用于服务端运行状态。

`/api/chat/jsonl`、`/api/chat/system-prompt`、chat/archive replay、搜索结果与 `/api/chat/llm-trace` 都在读取前验证各自明确拥有的时间字段。JSONL 的 line `updatedAt`、event `timestamp`、`messages[].ts` 和 awaiting/submit 时间仍严格；新写入的 trace 中 `sentAt`、`responseStartedAt`、`completedAt` 以及 `interrupt.interruptedAt` 均为 epoch milliseconds，对应的 `sentTime`、`responseStartedTime`、`completedTime`、`interrupt.interruptedTime` 为 RFC3339Nano 可读时间。历史 trace 或 JSONL 不迁移；其中字符串、秒、浮点、零值或缺少必填平台时间会返回 `422 time_contract_violation`，不会原样透传或补值；trace 中外部 request/response/tool payload 保持透明。

`/api/chats` 的 chat 摘要、`/api/agents?includeChats=N`（包括 `includeTeam=true`）附带的 chat 摘要，以及 WebSocket `/api/chats` 响应都会在存在运行中 run 时返回 `activeRun`（不包含详情专属的 `planningMode` 或重算后的 `lastSeq`）。这些摘要可能包含局部 `error`，用于展示单个 chat 的可恢复/可诊断异常而不让列表整体失败。当前 `multiple active runs found for chat` 会返回 `error: { "code": "active_run_conflict", "message": "multiple active runs found for chat", "chatId": "...", "runIds": ["..."] }`，此时该 chat 不包含 `activeRun`。

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
| POST | `/api/automation/create` | body: `name`、`description`、`cron`、`agentKey`、`enabled`、`teamId`、`zoneId`、`remainingRuns`、`query` | 创建后的 automation 详情 |
| POST | `/api/automation/update` | body: `id` 或 `automationId`，以及可更新字段 | 更新后的 automation 详情 |
| POST | `/api/automation/delete` | body: `id` 或 `automationId` | 删除结果 |
| POST | `/api/automation/toggle` | body: `id` 或 `automationId`、`enabled` | 启停后的 automation 详情 |
| POST | `/api/automation/executions` | body: `id` 或 `automationId`、`limit`、`offset` | execution history |

`query` 对象包含 `message`、`chatId`、`role`、`params`。`role` 可选值为 `user`、`assistant`、`automation`、`system`；automation 未显式配置时默认为 `automation`。

Automation 摘要和详情中的 `nextFireAt` 是下次触发时间的 epoch milliseconds；`nextFireTime` 是按 automation `zoneId` 格式化的 RFC3339 展示时间。`lastExecution` 与 execution history 中的 `startedAt`、`completedAt` 为 epoch milliseconds；对应的 `startedTime`、`completedTime` 为按 automation 时区格式化的 RFC3339Nano 可读时间。

Automation 的 Team 身份规则与 query 一致：legacy Team 配置 `teamId + agentKey`；orchestrated Team 只配置 `teamId`，同时传 `agentKey` 会被拒绝。后者触发时由隐藏协调器接管，不会选择或回显虚拟 Agent key。

### Run

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| POST | `/api/query` | body: `message`、`agentKey`、`teamId`、`chatId`、`runId`、`requestId`、`role`、`references`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`planningMode`、`accessLevel`、`model` | 默认 SSE stream；`stream:false` 时返回 JSON |
| POST | `/api/btw` | body: `chatId`、`message`、可选 `btwId`、`runId`、`requestId`、`references`、`params`、`scene`、`stream`、`includeUsage`、`includeFullText`、`accessLevel`、`model` | 创建或继续隐藏只读分支；复用 query SSE，`stream:false` 返回带 `btwId` 的 JSON |
| GET | `/api/attach` | query: `runId`、`agentKey` 或 `teamId`、`lastSeq` | 按公开 owner 续接 run 的 SSE stream |
| POST | `/api/submit` | body: `agentKey` 或 `teamId`、`runId`、`awaitingId`、`params` | HITL submit ack |
| POST | `/api/steer` | body: `agentKey` 或 `teamId`、`runId`、`message`、`requestId`、`chatId`、`steerId` | steer ack |
| POST | `/api/interrupt` | body: `agentKey` 或 `teamId`、`runId`、`message`、`requestId`、`chatId` | interrupt ack |
| POST | `/api/access-level` | body: `agentKey` 或 `teamId`、`runId`、`accessLevel`、`requestId`、`reason` | 动态更新 native run 的 accessLevel |

`/api/query` 的 `stream` 是 JSON body 字段；省略或传 `true` 时返回 SSE，结束帧为 `data: [DONE]`。传 `false` 时服务端仍执行完整 run、持久化 chat，并在结束后返回普通 JSON。默认只返回最终回答，响应示例见下文。

`teamId` 的 HTTP、WebSocket、Automation、submit continuation 与子智能体准入共享同一 resolver。chat 创建后 `teamId` 固定，但两种 Team 的请求身份不同：

- legacy Team：公开 owner 是所选成员，query 使用 `teamId + agentKey`；同一 Team chat 内可切换有效成员。
- orchestrated Team：公开 owner 是 Team，query 只使用 `teamId`；运行时在 run 内合成内部 `TEAM` 协调器，任何 `agentKey` 都会被视为绕过调度器。

| 场景 | HTTP 结果 |
|---|---|
| 新请求使用未知 `teamId` | 400 |
| 已有 Team chat 对应的 Team 已不存在 | 503 |
| legacy Team 的 `agentKey` 不属于 Team | 403 |
| orchestrated Team 同时传入 `agentKey` | 400 |
| 已有 chat 传入不同 Team；包括为无 Team chat 补传 Team | 409 |
| legacy Team 默认/当前成员无效，或 orchestrated Team 成员为空/存在失效成员 | 503 |

WebSocket 使用现有错误 envelope 表达相同语义。Team 无效时不会回退全局或 channel 默认 agent；run 开始后使用已解析的成员、成员 `AgentDefinition`、协调器配置与 prompt 快照，不受本轮 catalog 热重载影响。需要启动新执行 run 的 active/deferred submit 会在消费 awaiting 前重新准入，失败时保留 awaiting。

run 控制接口从 `agentKey/teamId` 推导互斥身份：Agent-owned run 必须传 `agentKey`；orchestrated Team run 必须只传 `teamId`，漏传返回 400，错 Team 返回 403，同时传 `agentKey` 也返回 400。legacy Team 保留成员 `agentKey + teamId`。orchestrated Team 的 `request.query` 与 `run.start` 携带 `teamId` 且 `agentKey` 为空；chat/run summary 同样使用这一身份对表达公开归属。虚拟协调器 key 不是公共 API 身份。

`/api/btw` 用于“顺便问”：`chatId` 必须指向已有 active chat；不传 `btwId` 时从当前主 JSONL 创建隐藏快照并在响应头 `X-Btw-Id` 与首个 `request.query.btwId` 返回分支 ID，传 `btwId` 时继续该分支。BTW 固定继承父 chat 的 agent/team，固定 `role:user` 且关闭 planning mode。主 chat 的 active run、pending awaiting、摘要、未读、搜索、自动 learn 和 JSONL 都不会被 BTW 更新。

BTW 与普通 query 使用同一 Agent/ReAct、模型协议、SSE assembler、attach/interrupt 和 StepWriter；`request.query` 额外包含 `kind:"btw"`、`btwId`、`parentChatId`、`hidden:true`，不新增 event type，也不发送 `chat.start` / `chat.updated`。同一个 `btwId` 只允许一个 active run，父 chat 与不同 BTW 分支可以并行。

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

`references` 中的文件引用使用 `path` 表示当前目标智能体可直接访问的执行路径。服务端会按 agent 运行位置生成或归一化该字段：本地运行时为宿主机绝对路径，容器运行时为 `/workspace/...`。`url` 只用于平台资源下载、ticket 与 gateway 数据面，不进入模型 prompt。

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

实时 SSE / WS stream 的工具事件形状不变：仍按单个工具发送 `tool.snapshot`、`tool.result`、`action.snapshot`、`action.result`。持久化到 `chatId.jsonl` 时，同一 assistant turn 的多个工具调用会合并为一条 assistant message 的 `tool_calls[]`；如果该组存在 awaiting，确认前不会执行任何 sibling tool，确认后的所有结果写入同 `seq` 的 `_type:"react-tool"` continuation。

orchestrated Team 的总控 reasoning 和 `agent_delegate` 工具事件会被过滤，不进入客户端事件流。成员输出继续使用现有 `task.*` 与 task-scoped `content.*`：成员事件带 `taskId`，可带 `teamId`、成员 `agentKey`、`presentation:"task"`，并在 `actor` 中标记 `type:"agent"`。一项和多项委派使用相同终止规则，成员正文不会成为根回答；最终非流式 `content`、run summary 与 `AssistantText` 只取总控生成的唯一 Team 最终正文。

`run.activity` 是运行中的非终止状态事件，用于展示当前 run 正在等待、运行、重试或完成某个活动阶段。基础字段为 `runId`、`chatId`、`phase`、`status`；可选字段包括 `taskId`、`backend`、`key`、`message`，以及按场景嵌套的 `retry` / `recovery` / `degradation` 对象。当前 native 模型调用使用 `phase:"model_call"`，可恢复重试使用 `status:"retrying"` 且把 `attempt`、`maxAttempts`、`reason`、`timeoutSeconds`、`elapsedMs` 放入 `retry`。`run.activity` 不表示 run 失败；`run.error` 仍是终止事件，发出后不应再出现 content / reasoning / tool 等业务事件，后面只允许传输层 `[DONE]`。`run.activity` 只用于 live / attach，默认不进入 `/api/chat` 历史回放。

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

`params` 是业务透传对象，平台不读取、不写入、不约定内部 key。

`role` 可选值为 `user`、`assistant`、`automation`、`system`，普通 query 缺省为 `user`。`automation` / `system` 的 `request.query` 会保留在 trace 中，但不会作为可见用户消息参与搜索或 Markdown 导出。`role` 只影响本次 query 展示语义，不决定 chat 摘要的 `source`；外部请求不能通过 `role=automation` 或传入 `source` 伪造 automation 创建来源。普通 HTTP `/api/query` 传入 `sourceUser` 也不会改变 source；该字段只在受信 channel/gateway 上下文中作为远端用户提示使用。

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

对于 native agent，`model.key` 必须存在于 model registry；`model.modelId` 由后端转发给 ACP CODER 上游时补齐，优先来自 model registry 的 `modelId`，为空时回退到 key；`model.reasoningEffort` 一般可取 `LOW`、`MEDIUM`、`HIGH`，CODER agent 额外支持 `NONE` 用于关闭本次 run 的 reasoning。PROXY agent 会把 `model` 对象原样透传给上游，platform 不做本地 model registry 校验，也不写入本地 session/stage settings。该配置只影响当前 run，不写回 agent 配置。

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

- `models`: 当前 model registry 中可展示的聊天模型，字段为 `key/provider/modelId/protocol/isReasoner/isVision/contextWindow`。普通模型要求 `type: chat`、provider 存在且 `apiKey` 非空；`protocol: ACP_PASSTHROUGH` 的 ACP 透传模型不要求 provider。`type: embedding` 与 `type: image-generation` 不会出现在聊天模型选项中。
- `reasoningEfforts`: 固定为 `NONE`、`LOW`、`MEDIUM`、`HIGH`，其中 `NONE` 表示关闭思考深度
- `defaultModelKey`: 可展示模型中的默认模型；优先普通可调用模型，没有时可回退到 ACP 透传模型，无默认模型时为空
- `defaultReasoningEffort`: 固定为 `MEDIUM`

其中 `contextWindow` 是 API 响应字段名；model registry YAML 中对应配置字段为 `maxInputTokens`。

HITL 三态细节见 [HITL协议](HITL协议.md)。真流式、heartbeat、attach backlog 与 H2A 缓冲见 [真流式和H2A](真流式和H2A.md)。

### KBASE

KBASE API 只接受 `mode: KBASE` agent；非 KBASE agent 会返回 forbidden/unsupported。手工 refresh 与运行时工具 `kbase_refresh` 调用同一个后端入口。KBASE 的 search/files/read/status 工具声明为只读，BTW/read-only policy 下仍可使用；refresh 是变更索引状态的操作，在只读 policy 下禁用。五个 KBASE tool 名称、REST 路径、`SearchHit`、chunk ID 和 `source.publish` 契约固定由 LanceDB 路径提供。agent catalog 热重载完成后会立即重绑相应 workspace watcher；agent 删除或 workspace/config 变化不会继续沿用旧 watcher，周期 reconcile 仅作为兜底。

KBASE agent 在运行时调用 `kbase_search` 且召回到内容时，会额外通过 live stream 发布 `source.publish` 事件。事件包含 `kind: "kbase"`、`query`、`sourceCount`、`chunkCount` 与按 source 聚合的 `sources[].chunks[]`，chunk 可携带 `path`、行号、页码、slide、`sourceType`、`matchType`、`score` 等定位字段；新写入的 chat JSONL 会把该事件作为对应 `react-tool` step 的顶层 `sources.items[]` sidecar 持久化，`/api/chat` replay 时再合成 `source.publish` 事件并保留原始 `liveSeq`，供时间线与 `/api/attach.lastSeq` 使用。历史 JSONL 中独立 `_type:"event"` 的 `source.publish` 仍保持可回放。

`artifact_publish` 仅在整个批次文件物化且 `<chatId>/.tools/artifacts.json` 原子写入成功后发布 `artifact.publish`。事件包含 `chatId`、`runId`、`toolId`、`artifactCount`、`artifacts`，子任务有明确归属时额外包含 `taskId`。JSONL 的对应 `react-tool.artifacts.items[]` 只是该次调用的审计记录；`GET /api/chat` 的 `data.artifact = { items: [...] }` 只从 manifest 恢复，旧 JSONL artifact 快照不会影响返回值。

KBASE 工具只读取 active 索引库，不直接访问宿主文件系统。`kbase_search` 支持 `pathPrefix`、`pathGlob`、`type` 与 `offset` 做 scoped retrieval；`kbase_files` 支持按 `path`、`pattern`、`status`、`type`、`mode=files|tree`、`depth`、`head_limit`、`offset` 浏览已索引/已扫描文件元数据。Lance 路径并行取 vector 与 FTS 候选并使用加权 RRF 融合；`matchType` 为 `vector|fts|hybrid`，score 归一化到 `[0,1]`。`matchCount` 是受 candidate 上限约束的两路去重并集数，不是全库总命中数。

| Method | Path | 参数 | 响应 |
|---|---|---|---|
| GET | `/api/kbase/{agentKey}/status` | 无 | 当前 Lance 索引状态；包含 `engine/schemaVersion/generation/indexes/sidecar/pendingRecoveryOperations/pendingChanges/storageDiskUsage`；FTS/vector index 状态包含未索引行数 |
| POST | `/api/kbase/{agentKey}/refresh` | body: `force` 可选 | 手工 refresh 始终做完整文件对账；结果在原字段外增加 `scope/candidatePaths/newFiles/modifiedFiles/metadataOnlyFiles/unchangedFiles/embeddedChunks/reusedChunks/pendingChanges`；`force=true` 构建新 generation |

status 中 Lance 字段是可选扩展，旧客户端可忽略：

```json
{
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

容器与本地进程探活使用免鉴权 `GET /healthz`。它不返回用户数据：始终检查 Go HTTP runtime；存在 KBASE agent 时，还通过 Go 内部持有的 Bearer token 检查 sidecar protocol handshake。sidecar 必需但不可用时返回 HTTP 503。

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
| GET | `/api/viewport` | query: `viewportKey`、`viewportType` | viewport 模板或 fallback |
| GET | `/api/resource` | query: `file`、`t` | chat 资源文件；`t` 为可选 resource ticket |
| GET | `/api/tool-result` | query: `chatId`、`path`、`t` | `.tools/results/<toolId>.json` 完整工具结果；`t` 为可选 resource ticket |
| POST | `/api/upload` | multipart: `requestId`、`chatId`、`file` | upload ticket 与资源访问信息 |

`/api/file` 读取当前 agent 的真实 `runtimeConfig.workspaceRoot` 文件，供 CODER / KBASE 等界面预览 Markdown、PDF、图片或普通文件。`path` 可以是 workspace 相对路径，也可以是宿主机绝对路径；绝对路径经 canonical 解析后必须仍位于当前 agent workspace 内，`..` 与 symlink escape 会返回 forbidden。默认响应使用统一 JSON 包裹，文本文件内联 `content`，二进制/PDF/图片只返回 metadata 与 `contentUrl`；`response=content` 时直接返回文件字节流，不使用 JSON 包裹。该接口不读取 KBASE 索引库，也不暴露 `hostAccess.readRoots` 之外的文件。

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
  "ws": {
    "connectionCount": 1,
    "latestConnection": {},
    "recentMessages": []
  }
}
```

连接项包含 `sessionId`、`kind`、`active`、`subject`、`gatewayId`、`channel`、`source`、`deviceId`、`remoteAddr`、`userAgent`、`connectedAt`、`closedAt`、`closeReason`、`lastSeenAt`、`lastMessageAt`、`receivedMessages`、`sentMessages`、`errors`、`inflightRequests`、`activeStreams`、`writeQueueDepth`。

消息项包含 `seq`、`timestamp`、`sessionId`、`source`、`deviceId`、`direction`、`frame`、`type`、`id`、`sizeBytes`、`payloadPreview`、`truncated`、`error`。`payloadPreview` 只保存脱敏后的截断摘要，最多 512 字符；不会记录完整 payload，不记录 ping/pong/control frame，并跳过 `push.heartbeat`。

## WebSocket 定义

### 入口与鉴权

- 入口：`GET /ws`，HTTP upgrade 为 WebSocket。
- 鉴权：复用 HTTP token 校验链路。
- token 可通过 `Sec-WebSocket-Protocol: bearer.<token>` 或 query token 传递；服务端会在握手成功时回写匹配的 subprotocol。
- 客户端可通过 query 自报监控元数据：`source` 与 `deviceId`，例如 `/ws?source=webclient&deviceId=device-123`；`source` 转小写后展示，缺省时可从 JWT claim `deviceId` 兜底。
- WebSocket 控制面常开；没有单独的关闭开关。

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
{"frame":"push","type":"connected","data":{}}
{"frame":"error","type":"invalid_request","id":"req-1","code":400,"msg":"...","data":{}}
{"frame":"error","type":"active_run_conflict","id":"req-1","code":409,"msg":"multiple active runs found for chat","data":{"code":"active_run_conflict","message":"multiple active runs found for chat","chatId":"chat-id","runIds":["run-1","run-2"]}}
```

当前 platform 主动发送的 `push.type`：

| Push type | data |
|---|---|
| `connected` | `sessionId` |
| `heartbeat` | `timestamp` |
| `auth.expiring` | `expiresAt` |
| `run.started` | `runId`、`chatId`、`agentKey`、必填 `startedAt` |
| `run.finished` | `runId`、`chatId`、必填 `finishedAt` |
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

除 `heartbeat.timestamp` 外，platform 主动发送的 push payload 不使用 `timestamp`；它们用上表的业务语义时间字段。这是硬切换，不会双写旧字段，前端与服务端需要同版本发布。SSE 与 WebSocket `frame:"stream"` 的 `event.timestamp` 仍是每个业务流事件必填的 epoch milliseconds。`auth.refresh` response 在 JWT 存在 `exp` 时才返回 `expiresAt = exp * 1000`；没有 `exp` 时省略字段。`auth.expiring.expiresAt` 同样始终是 epoch milliseconds。客户端不得把缺失 `readAt` / `expiresAt` 解释为 1970 或当前时间。

`awaiting.asking.timeout` 与 stream 中的 `awaiting.ask.timeout` 语义一致：对普通 HITL 等待项，`0` 表示无限等待、不自动超时；大于 `0` 时由后端按真实时间独立倒计时，observer / attach / detach 状态不会暂停或延长后端超时。CODER planning confirmation 使用 `mode:"planning"` 和 `planning` payload，永远省略 `timeout`，表示永久等待；它不同于 `plan_*` / plan-tasks 的执行任务计划。旧 `mode:"plan"` planning 协议不兼容。

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

历史重建事件的 `seq` 是展示序号。`chatId.jsonl` 使用每行顶层 `liveSeq` 记录该行覆盖到的原始 live stream 序号；replay 时会把它注入到对应的历史事件 payload，供 attach cursor 使用。

### WS Route

`/ws` 可转发的 route 由 `internal/server/ws_routes.go` 注册。除 `/api/query` 与 `/api/attach` 外，大多数 route 返回一次 `response` frame。

| Route | Payload | 返回 |
|---|---|---|
| `/api/agents` | `includeChats`、`includeTeam`、`scope`、`mode` | `response` |
| `/api/agent` | `agentKey` | `response` |
| `/api/agent/model-config` | `agentKey`/`key`、`modelKey`、`reasoningEffort` | `response` |
| `/api/model-options` | 无 | `response` |
| `/api/teams` | 无 | `response` |
| `/api/chats` | `lastRunId`、`agentKey`、`mode`、`limit` | `response` |
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
| `/api/chats/search` | `query`、`agentKey`、`teamId`、`limit` | `response` |
| `/api/query` | `QueryRequest` | `stream` |
| `/api/attach` | `runId`、`agentKey` 或 `teamId`、`lastSeq` | `stream` |
| `/api/detach` | `runId`、`agentKey` 或 `teamId`、`reason` | `response`；关闭当前 WS 连接上该 run 的 observer，不中断 run |
| `/api/terminal/open` | `agentKey`、可选 `terminalKey`、`cols`、`rows` | `stream`；agent scope attach-or-create |
| `/api/terminal/input` | `terminalId`、`data` | `response` |
| `/api/terminal/resize` | `terminalId`、`cols`、`rows` | `response` |
| `/api/terminal/detach` | `streamRequestId`、可选 `terminalId` | `response`；只释放当前 WS terminal stream，不关闭 PTY |
| `/api/terminal/close` | `terminalId`，或 `streamRequestId` | `response`；关闭 PTY；`streamRequestId` 用于 open 尚未返回 `terminal.opened` 的预取消 |
| `/api/submit` | `SubmitRequest` | `response` |
| `/api/steer` | `SteerRequest` | `response` |
| `/api/interrupt` | `InterruptRequest` | `response` |
| `/api/learn` | `LearnRequest` | `response` |
| `/api/memory/meta` | 无 | `response` |
| `/api/memory/context-preview` | `chatId`、`message` | `response` |
| `/api/memory/scope/list` | `agentKey` | `response` |
| `/api/memory/scope/detail` | `agentKey`、`scopeType`、`scopeKey` | `response` |
| `/api/memory/scope/save` | scope 保存字段 | `response` |
| `/api/memory/scope/validate` | `agentKey`、`scopeType`、`markdown` | `response` |
| `/api/memory/record/list` | memory record 过滤字段 | `response` |
| `/api/memory/record/detail` | `id` | `response` |
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

服务端会按本地 agent 的 `channelConfig.exports` 将 `externalAgentKey`（如未显式配置则默认等于本地 agent 的 `key`）映射为本地 `agentKey`，并检查该 channel 的 `allow.query / submit / steer / interrupt / fileTransfer` 权限。

当本地 `mode: CHANNEL` agent 引用 `mode: server` channel 时，运行时会复用该 channel 已接入的 `/ws/channel?channelId=...` 连接向对端发送 `request` 帧，并按相同 `id` 收回 `stream / response / error` 帧。`mode: client` 与 `mode: server` 只表示连接建立方向，不表示 agent 的拥有方；server channel 未连接时会返回 `503 channel <channelId> is not connected`。

### Gateway Agent Card 申报

platform 主动连接 `mode: client` gateway 后，会通过现有 `platform-ws` request/response 申报当前 channel 的 Agent Card。只有本地 agent 中显式声明了匹配 `channelConfig.exports` 且 `allow.query: true` 的导出项会被申报，线上 `agentKey` 使用 `externalAgentKey`；旧 gateway 配置中的 `agents: "*"` 不构成申报授权。`mode: server` 的 `/ws/channel` 连接本期不做反向申报。

请求使用 `id` 作为唯一关联 ID，payload 不重复携带 `requestId`：

```json
{
  "frame": "request",
  "type": "agent.card.update",
  "id": "card_01J...",
  "payload": {
    "agentKey": "support-agent",
    "agentCard": {
      "name": "售后数字分身",
      "description": "擅长售后工单、客户投诉、退款记录分析。",
      "skills": [
        {
          "id": "kb.query.support-agent",
          "name": "知识库问答",
          "description": "查询该智能体的本地知识库。",
          "tags": ["售后", "工单", "退款"]
        }
      ],
      "tools": [
        {
          "id": "kbase_search",
          "name": "知识库搜索",
          "description": "搜索已索引的本地知识库。",
          "tags": ["knowledge-base", "search"]
        }
      ]
    }
  }
}
```

gateway 必须用相同的 `id` 和 `type` 回应，并回显 `agentKey`：

```json
{
  "frame": "response",
  "type": "agent.card.update",
  "id": "card_01J...",
  "code": 0,
  "msg": "success",
  "data": {
    "agentKey": "support-agent",
    "accepted": true
  }
}
```

业务拒绝仍返回成功送达的 `response`，设置 `accepted: false`，并可在 `data.reason` 给出脱敏原因；格式错误等协议问题沿用 `error` frame。不使用 `push agent.card.ack`。

连接成功和每次重连后会立即全量申报。Agent、Skill、Tool、MCP 或 KBASE 标签成功重载后，platform 以 2 秒 debounce 合并变化并重新全量申报；每轮最多并发 4 张卡，每张等待响应 10 秒。超时、断流或 5xx 最多尝试 3 次并按约 2 秒、4 秒退避；`accepted: false` 与 4xx 不自动重试，只等待配置变化或重连。断线会取消旧连接上的待处理请求。

`skills` 和 `tools` 始终为数组，每项只包含 `id/name/description/tags`。卡片不会读取或发送 prompt、workspace/path、知识库原文、工具参数 schema、环境变量或认证配置；发送前还会拒绝明显的绝对路径和凭证模式。字段超长、条目过多、声明无法解析或总帧超限时整张卡在本地标记为 invalid/error，不发送残缺卡片。名称、描述和标签属于公开配置，维护者必须预先完成客户隐私脱敏。

本结构仅借用 A2A Agent Skill 的展示模型，是 gateway 的简化卡；未包含[完整 A2A 1.0 Agent Card 定义](https://github.com/a2aproject/A2A/blob/main/specification/a2a.proto)要求的接口、版本、capabilities、安全与输入输出模式等字段，`tools` 也是平台自定义扩展，不宣称完整兼容 A2A Agent Card。

`GET /api/admin/channels` 与 `GET /api/monitor/channels` 的每个 export 可包含 `cardStatus`，字段包括 `status`、`requestId`、`attempt`、`updatedAt`、可选 `acceptedAt` 和脱敏 `reason`。状态值为 `pending / accepted / rejected / retrying / error / offline`。

## 约束与注意事项

- HTTP query 参数在 WS payload 中通常以同名 JSON 字段传入。
- `GET /api/attach`、WS `/api/detach`、`POST /api/submit`、`POST /api/steer`、`POST /api/interrupt` 按 run 的公开 owner 校验 `agentKey` 或 `teamId`；二者不能用隐藏执行身份互相替代。
- WS 客户端切换 current chat 时，应先对原 chat 的 active run 发送 `/api/detach`，再对新 chat 的 active run 发送 `/api/attach`；detach 只释放当前 WS 连接上的订阅流，不停止后台 run。
- WS `/api/resource` 要求 `file + pushURL`，用于将本地资源推给 gateway；`pushURL` 是 gateway HTTP 目的地址，通常为 `/api/push/...`，WS `/api/push` 不存在；HTTP `/api/resource` 直接返回文件字节。
- `.tools` 是隐藏工具内部目录，不通过 `/api/resource` 或 WS `/api/resource` 暴露；HTTP `/api/tool-result` 接受 `.tools/results/<toolId>.json`。
- 旧反向 gateway 配置仍在 `configs/channels.yml` 兼容解析；新的 platform/adaptor 接入优先使用 channel `mode: client | server` 与 agent `channelConfig`。
- 完整 DTO 字段以 `internal/api/*.go` 为事实源。

### Agent Terminal

Agent 终端只复用主 `/ws` 连接，不提供独立 `/ws/terminal`，也不新增顶层 `frame` 类型。终端协议仍使用 `frame:"request"` / `frame:"stream"` / `frame:"response"` / `frame:"error"`。

`/api/terminal/open` 是长生命周期 stream，语义是 agent 级 `attach-or-create`。`terminalKey` 是同一 agent 内的稳定 tab key，未传时默认为 `"main"`；同一 owner boundary 下的同一 `agentKey + terminalKey` 会复用同一个 PTY，不因 chat 切换、面板隐藏或组件卸载而重新启动 shell。owner boundary 由 WS 鉴权主体确定：只有同时具备 `subject + deviceId` 时才按该二元组跨 WS 连接复用；缺少 `deviceId` 或缺少 `subject` 时按当前 WS 连接隔离，因此这类连接不承诺跨 WS 重连复用。

`terminalKey` 只接受不超过 64 字节的 ASCII 字母、数字、`-`、`_`、`.`、`:`。后端会限制单 owner + agent 的 terminal 数量以及进程内总 terminal 数量，避免恶意创建大量长期存活 PTY。

```json
{"frame":"request","type":"/api/terminal/open","id":"term-1","payload":{"agentKey":"coder","terminalKey":"main","cols":120,"rows":32}}
```

open 成功后先返回 `terminal.opened`，再返回可选 replay output，之后进入 live output。事件 payload 中 `scope:"agent"` 表示该 terminal 不绑定 chat；`reused:true` 表示复用了已有 PTY；`replay:true` 表示该条 `terminal.output` 来自 terminal manager 的短期回放 buffer。

```json
{"frame":"stream","id":"term-1","streamId":"term_xxx","event":{"type":"terminal.opened","seq":1,"terminalId":"term_xxx","agentKey":"coder","terminalKey":"main","scope":"agent","cwd":"/workspace","shell":"/bin/zsh","reused":true}}
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

`detach` 只释放当前 WS 连接上的 terminal subscriber；PTY、cwd 与输出回放 buffer 保持不变。`streamRequestId` 必须指向当前 WS 连接上的 terminal stream；如果同时传入 `terminalId`，后端会校验两者绑定关系。浏览器隐藏 terminal 面板、SPA 内切换 chat、组件卸载都应使用 `detach`。如果 open 请求已发出但尚未收到 `terminal.opened`，前端可只传 `streamRequestId` 进行预取消。只有用户关闭 terminal tab 时才调用 `/api/terminal/close`，该操作会结束远端 PTY；同样支持在 `terminal.opened` 前仅传 `streamRequestId` 做关闭预取消。

Agent 级终端对所有 agent 使用同一套本地 PTY 逻辑，不按 `mode`、ACP backend 或 sandbox runtime 做差异化禁用。macOS/Linux 使用 Unix PTY，Windows 使用 ConPTY / PowerShell PTY；Windows 需要 ConPTY 可用的系统版本（Windows 10 1809 / Windows Server 2019 及以上），旧系统会返回 `unsupported`。cwd 只由 platform 根据 agent workspace 反查，不信任前端传入任意 cwd；agent 配置了 `runtimeConfig.workspaceRoot` 时使用该目录，未配置稳定 workspace 或显式配置 `@chat` 时使用 platform 进程启动目录作为固定 cwd，避免同一 agent 跨 chat 共享 terminal 时目录随 chat 变化。缺失 agent、不可访问 workspace、非目录 workspace 会拒绝。终端输入与输出不会写入 chat/event log，也不进入 raw messages 或 events replay；只保存在 terminal manager 的短期 ring buffer，且 replay 只在相同 owner boundary 下可见。WS monitor 只记录 terminal 输入/输出的类型、id 与字节数，不记录原始 preview。错误沿用现有 error frame，`type` 为 `invalid_request`、`forbidden`、`terminal_not_found`、`unsupported`、`conflict`、`too_many_requests` 或 `internal_error`。

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
