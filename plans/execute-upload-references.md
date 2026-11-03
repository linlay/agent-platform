# CuteJ `/api/execute`：复用上传 Chat 与 references

## 背景

CuteJ 工作流节点需要「先上传附件、再在同一 Chat 里执行并交卷」：图片进入模型视觉输入，业务 `outputSchema` 绑定到 `result_submit`，由平台校验。现有 `/api/execute` 每次新建 Chat，且不接受 `chatId` / `references`；附件只能走 multipart `request` + `$file`，进入的是 Task input 字符串，不会构造 `image_url`。

本文记录现状、已定需求、核对到的缺口、两种附件来源的组合选项、改动面与验收，以及待定问题。行号以写作时的 main（`1ce8a7ec`，v0.5.37）为准。本轮只写计划，不改业务代码、不重启服务、不发起真实模型请求、不 push。

相关文档：`docs/智能体执行接口.md`（当前契约）、`docs/API与协议.md`（upload / query / references）、WebClient `docs/64-页面能力-Execute执行测试.md`（前端 Execute 测试页）。无既有同名 plan；与 `plans/webapp-node-hosting.md` 无实现依赖。

## 现状

### `/api/upload`

- 路由：`internal/server/server_routes.go:372` → `handleUpload`（`internal/server/handler_resource.go:344`）。
- 可省略 `chatId`：省略时 `newChatID()`（`:363-366`）；`EnsureChatWithSource` 可创建仅上传、尚未正式 query 的占位 Chat（`:372`；文档 `docs/API与协议.md:897`、`docs/会话存储与回放.md:255-260`）。
- 响应：`api.UploadResponse{requestId, chatId, upload}`（`internal/api/types_search_upload.go:30-45`）。`upload` 即 ticket：`id/type/name/path/mimeType/sizeBytes/url/sha256`；`url` 为不含 chatId 的 `@chat/<relativePath>` 或相对名。
- WebClient 把 `upload` 展开成一条 file reference：`agent-webclient/src/shared/data/api/requests/uploads.ts:16-42`（`extractUploadReferences`）、`:57-61`（`extractUploadChatId`）。

### `/api/execute` 请求与解码

- 公开 DTO：`internal/api/execute.go:7-18` — 仅有 `requestId`、`agentKey`、`message`、`input`、`outputSchema`、`accessLevel`；**没有** `chatId`、`references`。
- 严格解码：`handler_execute.go:65-69` 用 `DecodeStrictJSON`；未知字段 → 400 `unknown_field`。
- 现测明确把 `chatId` 当成非法字段：`handler_execute_test.go:330`（`"unknown field": ... "chatId":"c1"`）。
- 文档写死拒绝：`docs/智能体执行接口.md:26` — 「`chatId`、…、`references`、…都不接受：每次执行都新建 Chat」。
- Runtime 命令：`internal/runtime/types/execute.go:8-36` — 同样无 `ChatID`/`References`；有 `FileParts` + `StoreFiles`（multipart 存入**新** Chat）。
- 入口：`handler_execute.go:22-34` → `Runtime.Execute`；multipart 的 `request` 字段 + 文件字段（`:36-100`、`:102-126`）；`storeExecuteFiles`（`:128-151`）对给定 chatId `EnsureChatWithSource` 后按 upload 规则落盘。

### Execute 运行链路（始终新 Chat、无 references）

- `internal/runtime/query/execute_result.go:28-116`：
  - `:53` **`chatID := newChatID()`**，忽略任何调用方 Chat。
  - multipart 时 `StoreFiles(chatID)`，再把 `input` 里的 `{"$file":...}` 换成 `"@chat/<文件名>"` 字符串（`:54-63`、`:281-301`）。
  - 建 `ResultContract{OutputSchema, Input}`（`:72`），组装 `QueryCommand` 时带上 `ChatID`、`ResultContract`，**不设置 `References`**（`:74-87`）。
  - 随后 `prepare` → `RegisterPreparedQuery` → `executePreparedLocalQuery`；已接受的 `result_submit` 从 contract 读出返回（`:112-113`）。
- ResultContract 挂到 session：admission 完成后 `configureResultContractSession`（`execute_result.go:140-167`；调用点 `admission.go:686-687`）按次生成带业务 Schema 的 `result_submit` 工具定义。
- 交卷语义：`internal/contracts/result_contract.go`、`internal/resultsubmit/`、`internal/llm/run_stream_result_submit.go`（打回 3 次、提醒一次、审批即失败、重复交卷拒绝）。**本需求不改变这些语义。**

### Query 侧已有、Execute 未接通的能力

| 能力 | 位置 | Execute 现状 |
|---|---|---|
| 可选 `chatId`（空则新建） | `admission.go:372-375`；`api.QueryRequest.ChatID`（`types.go:253`） | Execute 在进 prepare 前强制 `newChatID()` |
| `references` 解析与授权 | `admission.go:506-510` → `runtime/reference/prepare.go:29`；file 校验当前 Chat 资源（`:79-97`）；chat 引用走 `QueryPrincipalCanReferenceChat`（`:244-255`） | Execute 的 `QueryCommand.References` 恒为空 |
| Agent 归属 | `CompleteQueryPreparation`：`target_owner_mismatch`（`admission.go:601-606`） | 新 Chat 无冲突；复用上传 Chat 时需走同一检查 |
| 运行冲突 | `RegisterPreparedQuery` → `active_run_conflict`（`query_gate.go:222-288`）；另有 awaiting gate（`admission.go:431` → `query_gate.go:60`） | 新 Chat 通常无活动 Run；复用时需同一路径 |
| 多模态 `image_url` | `session_builder.go:425-445` → `querymessages.BuildMessagesWithOptions`；图片块 `builder.go:216-256` + `multimodal/images.go:125-126`；**仅当模型 `IsVision` 且 references 含图片 MIME** | multipart `$file` 只进 Task input 字符串，**不会**产生 `image_url` |
| Identity | `queryContext` 可由 `Caller.Subject` 构造（`start.go:30-39`）；execute handler 已填 `Caller.Subject`（`handler_execute.go:83-85`） | 复用 Chat 时 references 授权可沿用，无需另造身份通道 |

### WebClient

- DTO：`agent-webclient/src/shared/data/api/dto/execute.ts` — 无 `chatId`/`references`。
- 请求：`requests/execute.ts` — 无附件 JSON，有附件 multipart `request` + 字段文件；注释写明不走 WS、不重试。
- Execute 测试页：`docs/64-页面能力-Execute执行测试.md:7` — 「文件选择不会先调用上传接口」；表单校验只认 `$file`（`features/execute/lib/executeForm.ts`）。
- Composer 上传→references 链路已成熟（`uploads.ts` + `composerAttachments.ts`），Execute 页尚未复用。

### 费用类 App 约束（需求原文）

不放宽结果校验：不得靠转换字符串布尔值、包装 `records.item`、或解析助手正文来「修」输出。平台继续只按调用方 `outputSchema` 做 `result_submit` 校验（`docs/智能体执行接口.md:50`）。

## 目标功能（已定需求）

1. **完整流程**：`POST /api/upload` 取得 `chatId` + upload ticket → `POST /api/execute` 传入该 `chatId`、`references`、`outputSchema` → 同一 Chat 内一次 Run：图片进视觉输入，业务 Schema 绑定 `result_submit`，平台校验交卷。
2. **`ExecuteRequest` 增加可选 `chatId`、`references`**（形状对齐 Query 的 Reference，不另造类型）。
3. **未传 `chatId`**：保留现有「新建 Chat」行为（含现有 multipart `$file` 存入新 Chat）。
4. **传入 `chatId`**：复用上传所属 Chat；按现有 Query 规则校验访问权限（principal / Chat source）、Agent 归属（`target_owner_mismatch`）、运行冲突（`active_run_conflict` / awaiting gate）。
5. **`references`**：复用现有 Query 的附件解析、授权与多模态消息构造（`References.Prepare` + `BuildCurrentMessages`）；不另造上传或识图服务。
6. **保留** JSON 与 multipart `request` + `$file`；**明确**两种附件来源的组合规则（见下节选项，待定后写入契约）。
7. **同一次 Run** 必须能同时具备图片输入（经 references → `image_url`，在视觉模型下）与 `ResultContract`；保留 Schema 校验、重复交卷保护、失败交卷语义。
8. **同步** WebClient DTO、接口文档与必要测试。
9. **不放宽**费用 App 结果校验；不靠字符串布尔转换、`records.item` 包装、解析助手正文修复输出。
10. **实现约束**：先核对再做最小修改，接通运行链路，不只加请求字段；本轮实现时不改真实 Agent/Skill/凭据/业务数据，不重启服务，不发起真实模型请求（验收用模拟 Provider）。

## 发现的问题 / 缺口

### P1. 只加 DTO 字段会停在严格解码之外或无法复用 Chat

今天 `chatId` 被测成 `unknown_field`。即便放进 `ExecuteRequest`，`execute_result.go:53` 仍 `newChatID()`，上传 Chat 与 Execute Chat 仍是两个。必须改 Runtime：有 `cmd.ChatID` 时用之，否则再 `newChatID()`，再交给现有 `prepare`。

### P2. multipart `$file` ≠ 视觉 `image_url`

`$file` 替换结果只出现在 `[Task input]` / `<task_input>` 字符串里（`querymessages` 的 `TaskInput`）。`image_url` 只来自 **references** 且模型 `IsVision`（`session_builder.go:426-432`、`builder.go:216-256`）。CuteJ「图片进视觉」必须走 references，不能指望只改 multipart。

### P3. 复用 Chat 时 multipart `StoreFiles` 与占位 Chat 的时序

现逻辑：先定 chatID → 有文件则 `StoreFiles` → 再 `prepare`。复用上传 Chat 时：`StoreFiles` 应写入**该** Chat（`storeExecuteFiles` 已按参数 chatId 落盘）；`EnsureChatWithSource` / `EnsureChatWithInitialName` 对已存在占位 Chat 的行为需与 Query 首条正式请求对齐（名称 promote、`chat.renamed`）。若组合规则禁止「复用 chatId 时再带 multipart」，则 StoreFiles 在该分支为空，缺口缩小。

### P4. 现有拒绝用例与文档会反转

- 测试：`handler_execute_test.go:330` 需从「拒绝 chatId」改为「合法可选字段」类用例，并补复用 / 冲突 / references 用例。
- 文档：`docs/智能体执行接口.md:26`、`docs/API与协议.md` 执行接口行、`AGENTS.md` 中 execute 摘要、WebClient `docs/64-...` 需改「每次新建、不接受 references」的表述。

### P5. 非视觉模型时验收句「模型请求含 image_url」不成立

`BuildCurrentMessages` 在 `!IsVision` 时不会附 `image_url`（图片会落在 references 文本侧）。模拟 Provider 验收需使用 **IsVision=true** 的 fixture 模型，或在待定问题中明确非视觉时的降级语义（仍发 file reference 文本、不发明识图服务）。

### P6. Execute 与 Query 对「空 message + 仅附件」不一致

Execute 仍要求非空 `message`（`execute_result.go:33-35`）。上传后仅带 references、无正文是否允许：**待定**（需求示例仍有任务说明 + schema，默认倾向继续要求 message）。

### P7. references 类型范围

Query 的 `Prepare` 接受 skill/kbase/chat/site/file/selection。Execute 是否只允许 **file**（及图片），还是与 Query 全量一致：**待定**。需求场景是上传附件/图片；放宽到 skill/kbase 会牵动 interactionConfig 与 EXECUTOR 默认 attachment 开关。

### P8. WebClient Execute 测试页与工作流调用方

平台接通后，测试页若仍只发 multipart、不先 `/api/upload`，则无法手工点出「同 Chat + image_url」路径。DTO/请求层至少要能带 `chatId`/`references`；UI 是否改为「可先上传」：**待定**（可先 DTO + 文档示例，UI 随后）。

### P9. Identity / DeviceID 完整度

execute 目前主要写 `Caller.Subject`；`buildAuthIdentity` 的 DeviceID/Scope/时间戳未在 execute handler 侧组装。`queryContext` 用 Subject 已能驱动 `QueryPrincipalCanReferenceChat`。是否要把 execute 的 Identity 补到与 Query 同级：**待定**（功能上 Subject 可能已够，审计字段不一致）。

## 两种附件来源的组合规则（选项，未定）

来源 A：**multipart** `request` + 文件字段，`input` 内 `{"$file":"<part>"}`（现有）。  
来源 B：**先 `/api/upload`**，Execute 带 `chatId` + `references`（本次新增）。

| 选项 | 规则 | 利 | 弊 |
|---|---|---|---|
| **C1. 互斥** | 同一请求不能同时有 FileParts 与非空 references；有 chatId+references 时禁止多余 multipart 文件 | 语义清晰；避免同名文件两次写入；测试矩阵小 | CuteJ 若既要 Task input 文件路径又要视觉图，需两次概念或把图只放 references |
| **C2. 允许并存、职责分离** | `$file` 只服务 Task input 字符串；references 只服务多模态/引用块；二者都可指向同 Chat 内文件（references 通常指向已 upload 的 url） | 一次 Run 可同时「结构化 input 文件」+「视觉图」；最贴需求「同一次 Run 必须同时具有图片输入和 ResultContract」 | 同名覆盖、重复上传、input 与 references 是否必须指向同一批文件需再定；测试更多 |
| **C3. 并存但禁止 FileParts 指向已在 references 中的 basename** | 在 C2 上加冲突检测 | 减少静默覆盖 | 规则难解释；跨 `$file` 替换名与 reference.name 对齐成本高 |
| **C4. 有 chatId 时禁止 multipart 文件** | 复用 Chat 只走 references；新建 Chat 仍可用 multipart | 复用路径与 Composer 一致；新建路径兼容旧客户端 | 调用方无法在「已 upload 的 Chat」上再附带一次性 `$file` input |

需求要求「保留现有 JSON/multipart，并明确组合规则」——上表供定夺；**未定前不在实现里静默选一种**。

与 chatId 的交叉（亦待定）：

- 未传 chatId + 仅 multipart：现状。
- 未传 chatId + 仅 references：references 的 file url 需要已有 Chat 资源还是禁止（无 Chat 无法 ResolveResource）→ 实质上 **file references 几乎必须带 chatId**（或先 upload 拿到 chatId）。
- 传 chatId + 无 references + 无 multipart：纯文本 execute，复用该 Chat（若允许）。
- 传 chatId 但 Chat 不存在：走 Ensure/Summary 现有错误，还是 404：**待定**（Query 对不存在 id 会创建；Execute 复用语义更倾向「必须已是 upload 所属 Chat」）。

## 改动面（实现时最小接通，本计划不改代码）

### Platform

| 区域 | 路径 | 方向 |
|---|---|---|
| 公开 DTO | `internal/api/execute.go` | 可选 `ChatID string`、`References []Reference`（`api.Reference` / `queryinput.Reference`） |
| Runtime 命令 | `internal/runtime/types/execute.go` | 同步可选 `ChatID`、`References`；注释改为「默认可新建，可复用」 |
| HTTP 解码 | `internal/server/handler_execute.go` | 把字段拷入 command；**不**在 transport 层自造 Prepare |
| Execute 编排 | `internal/runtime/query/execute_result.go` | `chatID := trim(cmd.ChatID); if empty { newChatID() }`；`QueryCommand.References = cmd.References`；其余仍设 `ResultContract`、StoreFiles、prepare/register |
| 组合规则 | `execute_result.go` / `handler_execute.go` | 按选定的 C1–C4 在进 Run 前校验并 400 |
| 复用校验 | 不新写一套 | 依赖现有 admission（归属、awaiting）+ `RegisterPreparedQuery`（active run）+ `References.Prepare`（资源与 principal） |
| ResultContract / result_submit | `internal/resultsubmit/*`、`contracts/result_contract.go`、`llm/run_stream_result_submit.go` | **不改语义**；仅保证复用 Chat 路径仍调用 `configureResultContractSession` |
| 测试 | `internal/server/handler_execute_test.go`（及必要时 `execute_result` 单测） | 见「验收与测试」；模拟 Provider / 现有 SSE fixture |
| 文档 | `docs/智能体执行接口.md`、`docs/API与协议.md`（execute 行）、必要时 `AGENTS.md`、`docs/手工测试用例.md` | 契约与示例 |

### WebClient（`/workspace/projects/agent-webclient`）

| 区域 | 路径 | 方向 |
|---|---|---|
| DTO | `src/shared/data/api/dto/execute.ts` | 可选 `chatId?`、`references?`（复用现有 Reference 类型，勿另造） |
| 请求 | `src/shared/data/api/requests/execute.ts` | JSON/multipart 序列化带上新字段；仍禁止 WS/重试 |
| 测试页 / 表单 | `src/features/execute/**`、`docs/64-页面能力-Execute执行测试.md` | 按待定：至少文档与类型；UI 是否先 upload 另定 |
| 上传复用 | `src/shared/data/api/requests/uploads.ts` | 已有 extract helpers，Execute 流程可直接用 |

### 明确不改

- 真实 Agent / Skill / 凭据 / 业务数据、费用 App Schema 放宽逻辑。
- 新建上传服务、新建识图服务、解析助手正文当 output。
- 本计划阶段：不改业务代码、不重启、不真实模型请求、不 push。

## 验收与测试（模拟 Provider）

沿用 `handler_execute_test.go` 的 `newExecuteFixture` + SSE mock；图片用例需 fixture 模型 **IsVision=true**，并准备可被 `LoadImageFile` 读取的上传样例。

| # | 场景 | 期望 |
|---|---|---|
| A | upload → execute(chatId, references 含图片, outputSchema) | 返回的 `data.chatId` **等于** upload 的 chatId |
| B | 同上，抓第一轮模型请求 | user content 含对应 **`image_url`** |
| C | 同上 | `tools` 里 `result_submit.parameters.properties.output` 含本次业务 Schema |
| D | 模型交非法 output | 打回；用尽次数 → 422 `output_invalid`（现有语义） |
| E | 模型交合法 output / failure | 200 + success/output 或 failure/reason |
| F | 无附件 JSON execute | 与现有兼容（仍新建 Chat） |
| G | multipart `$file` | 与现有兼容（Task input 替换、落盘） |
| H | 未传 chatId 且带 `chatId` 旧客户端字段 | 字段合法后不再 `unknown_field`；行为按「有值复用 / 无值新建」 |
| I | 复用 chatId 但 Agent 与 Chat 归属不符 | `target_owner_mismatch`（或现有等价码） |
| J | 目标 Chat 已有活动 Run | `active_run_conflict` |
| K | 组合规则（选定 C* 后） | 违规 400，不建 Run / 或按规定不新建多余 Chat |

最终实现说明应包含：修改位置、请求示例、测试结果、**尚未验证的部署差异**（见下）。

### 请求示例（契约草稿，组合规则未定）

```http
POST /api/upload
Content-Type: multipart/form-data
; file=receipt.png&agentKey=exec-agent
→ { "chatId":"chat_xxx", "upload":{ "id":"...", "type":"file", "name":"receipt.png", "url":"@chat/receipt.png", "mimeType":"image/png", ... } }
```

```http
POST /api/execute
Content-Type: application/json

{
  "agentKey": "exec-agent",
  "chatId": "chat_xxx",
  "message": "识别发票并结构化",
  "references": [
    {
      "id": "<upload.id>",
      "type": "file",
      "name": "receipt.png",
      "url": "@chat/receipt.png",
      "mimeType": "image/png"
    }
  ],
  "outputSchema": {
    "type": "object",
    "properties": {
      "amount": { "type": "number" },
      "vendor": { "type": "string" }
    },
    "required": ["amount", "vendor"],
    "additionalProperties": false
  }
}
```

旧 multipart 示例保持 `docs/智能体执行接口.md` 现有写法不变。

## 待定问题

1. 两种附件来源组合选 **C1 / C2 / C3 / C4** 哪一个？
2. 传入的 `chatId` 若不存在：按 Query 创建新 Chat，还是 404/400「必须先 upload」？
3. Execute 的 `references` 是否仅允许 `type=file`（图片/普通文件），还是与 Query 全量一致？
4. 非视觉模型：references 图片是否只保留文本引用（现状），验收是否要求必须配视觉模型？
5. 是否继续强制非空 `message`（即使已有 references）？
6. WebClient Execute 测试页是否本轮改为「可先 upload + 填 references」，还是只改 DTO/文档？
7. execute handler 是否补齐与 Query 同级的 `AuthIdentity`（DeviceID/Scope/iat/exp）？
8. 复用 Chat 时，upload 时的 `agentKey` 为空、execute 再带 agentKey：是否允许「占位 Chat 首次绑定」，还是 upload 就必须带同一 agentKey？
9. 费用 App / CuteJ 调用方是否还有除 `outputSchema` 外的额外交卷校验（平台侧仍不放宽；若有需在应用侧，不进本计划）。

## 需要更新的文档

- `docs/智能体执行接口.md`：请求字段表、删除「不接受 chatId/references / 每次新建」绝对表述；补充 upload→execute 流程、组合规则、示例。
- `docs/API与协议.md`：`/api/execute` 行补 `chatId`、`references`。
- `AGENTS.md` 中 `/api/execute` 摘要（若仍写「新 Chat」）。
- `agent-webclient/docs/64-页面能力-Execute执行测试.md`：与 DTO/流程对齐。
- 必要时 `docs/手工测试用例.md` 增补模拟/联调步骤。

## 尚未验证的部署差异（实现后仍可能存在）

- 生产/组合环境的默认 EXECUTOR 模型是否 `IsVision`；非视觉时只有 references 文本、无 `image_url`。
- 反向代理/网关对「先 upload 再长连接 execute」的超时与 body 大小；与单次 multipart execute 的差异。
- 多副本/粘滞：upload 与 execute 必须打到同一可访问该 Chat 目录的实例（现有 Chat 存储假设）。
- Desktop / 工作流引擎（CuteJ）客户端是否已按新字段发请求；旧客户端仅不传字段时应保持新建 Chat 兼容。
- 鉴权关闭 vs 开启时，占位 Chat 的 `source` 与 `QueryPrincipalCanReferenceChat` 行为差异（匿名可读未归属 Chat 的现有规则）。

## 本轮交付

- 本文件：`plans/cutej-execute-upload-references.md`
- 本地 git commit（仅此 plan），**不 push**

## 补充：评审意见（2026-11-03）

范围更正：本方案与 CuteJ 无关，是 Platform 通用 Execute 能力。原文中「CuteJ」字样（标题、背景「CuteJ 工作流节点」、P2「CuteJ『图片进视觉』」、C1 弊端列、待定问题 9、部署差异「工作流引擎（CuteJ）」）均按「通用 `/api/execute` 调用方」理解；「费用类 App 约束」保留为需求原文中的结果校验约束，不代表本方案服务特定应用。代码中 `cutej` 字样仅见 `internal/tools/tool_web_control_test.go:292` 的测试 URL，与 Execute 链路无关。原文未修改；文件名是否更名见待定问题 10。

以下核对基于本地 HEAD `727febd1`（其父提交即原文所写 main `1ce8a7ec`）。

### 1. 原文核实结果

| 原文位置 | 原文表述 | 结论 | 核对依据 |
|---|---|---|---|
| 现状 / upload | 路由 `server_routes.go:372`、`handleUpload` `handler_resource.go:344`、省略 chatId `:363-366`、占位 Chat `:372` | 准确 | 同行号 |
| 现状 / upload | 响应 `types_search_upload.go:30-45`，ticket 字段 id/type/name/path/mimeType/sizeBytes/url/sha256 | 准确；补充：ticket 无过期、一次性或归属字段 | `internal/api/types_search_upload.go:30-45` |
| 现状 / execute 解码 | `execute.go:7-18` 无 chatId/references；`handler_execute.go:65-69` 严格解码；测试 `:330`；文档 `:26` | 准确 | 同行号 |
| 现状 / 运行链路 | `execute_result.go:53` newChatID、`:54-63` StoreFiles、`:72` ResultContract、`:74-87` 不设 References、`:112-113` 读交卷 | 准确；补充：StoreFiles 早于 `prepare`（`:89`），见 R4 | `internal/runtime/query/execute_result.go:53-113` |
| 现状 / ResultContract | `configureResultContractSession` `:140-167`，调用点 `admission.go:686-687` | 准确 | 同行号 |
| 能力表 / 可选 chatId | `admission.go:372-375`、`types.go:253` | 准确 | 同行号 |
| 能力表 / references | 「file 校验当前 Chat 资源（`prepare.go:79-97`）」 | 不完整：`ResourceFileParamForChat` 返回空时引用原样返回（`prepare.go:81-84`）；绝对 http(s) URL 由服务端下载落入 `references/`（`prepare.go:99-102`、`:105-181`，上限 50 MiB，远端 `Content-Type` 覆盖 mimeType `:176-178`） | `internal/runtime/reference/prepare.go:79-180` |
| 能力表 / Agent 归属 | `target_owner_mismatch` `admission.go:601-606` | 准确；补充：`summary.AgentKey=="" && LastRunID==""` 的占位 Chat 不触发，随后 `UpdateAgentIdentity` 绑定（`admission.go:601`、`:609-615`） | `internal/runtime/query/admission.go:597-618` |
| 能力表 / 运行冲突 | `query_gate.go:222-288`、awaiting `admission.go:431` → `query_gate.go:60` | 准确；补充：另有 `ReserveChatQuery` 与 `compact_in_progress` 409（`admission.go:382-399`） | 同文件 |
| 能力表 / 多模态 | `session_builder.go:425-445`、`builder.go:216-256`、`images.go:125-126`，仅 IsVision 时产生 image_url | 准确 | 同行号；`internal/server/steer_images_test.go:62-64`、`:166-174`（非视觉场景断言无 image_url、文件名仍在） |
| 能力表 / Identity | 「复用 Chat 时 references 授权可沿用，无需另造身份通道」 | 部分准确：principal 只参与 chat 类型引用（`prepare.go:218`）；file 引用只在 `currentChatID` 内解析，不做 principal 判断，其授权实际取决于目标 Chat 是否属于调用方，见 R1 | `internal/runtime/reference/prepare.go:79-97`、`:218` |
| 目标功能 4 | 「按现有 Query 规则校验访问权限（principal / Chat source）」 | 已定需求不变；核实：Query admission 对目标 chatId 未找到 principal / source 比对（`admission.go:344-451`、`:583-620` 中无此检查；`QueryPrincipalCanReferenceChat` 调用点仅 `prepare.go:218` 与 server 资源类 handler，如 `handler_resource.go:74,101,220-236`）。「现有规则」在本链路是否存在：待确认 | 同左 |
| P2 | `session_builder.go:426-432` | 准确 | 同行号 |
| P3 | 「storeExecuteFiles 已按参数 chatId 落盘」 | 准确；遗漏写入时机早于归属与冲突检查（R4） | `execute_result.go:54-63` vs `:89`、`:99` |
| P5 | 非视觉时图片落在 references 文本侧 | 准确 | `steer_images_test.go:166-174` |
| P6 | 非空 message `execute_result.go:33-35` | 准确；「默认倾向继续要求 message」为倾向性表述，不作结论 | 同行号 |
| P9 | execute handler 仅填 Subject，DeviceID/Scope 未组装 | 准确：`queryContext` 会复制 `Caller.DeviceID/Scope`（`start.go:35-36`），但 `handler_execute.go:83-85` 只赋 Subject；「功能上 Subject 可能已够」为推断，待确认 | 同行号 |
| 组合规则 / chatId 交叉 | 「Query 对不存在 id 会创建」 | 准确：`EnsureChatWithInitialName` 对不存在的 id 新建（`chat/summary_repo.go:39-65`）；对已归档 id 的行为待确认 | 同行号 |
| 改动面 / 复用校验 | 「不新写一套，依赖现有 admission + Register + Prepare（资源与 principal）」 | 有缺口：Prepare 不覆盖目标 Chat 的 principal 判断（R1）；StoreFiles 不经 admission（R4） | 见上 |
| 验收 / 前提 | fixture 需 IsVision=true | 准确；可行性已有先例：测试向模型文件追加 `isVision`（`steer_images_test.go:62-64`）；`executeModelRequest` 能取到 Messages/Tools（`handler_execute_test.go:105-113`），已有对 result_submit output schema 的断言（`:157-164`） | 同行号 |
| 背景 | 「CuteJ 工作流节点」 | 错误（范围更正） | Frank 反馈 |

### 2. 发现的问题与风险

- **R1. 复用 chatId 无调用方归属检查（跨用户）**：`/api/upload` 带 chatId 时直接 `EnsureChatWithSource`，已存在的 Chat 原样返回，不比对 source（`handler_resource.go:363-372`；`chat/summary_repo.go:49-61`）。Query admission 同样未见比对（见核实表「目标功能 4」）。Execute 接受 chatId 后，持有他人 chatId 的已认证调用方可能在该 Chat 写文件、读取其历史作为上下文、引用其中 file。是否有其他层（网关、Desktop 单用户假设）限制：待确认。
- **R2. 跨 Agent 复用**：归属检查只比较 `summary.AgentKey`（`chat_tools_facade.go:18-24`）；upload 时 agentKey 为空且无 Run 的占位 Chat 可被任意有 result_submit 的 Agent 首次绑定（`admission.go:601`、`:609-615`）。普通 Query Chat（同 Agent 有历史）也可被 Execute 复用；是否允许待定（待定问题 11）。
- **R3. 复用带历史的 Chat 会把历史带进 ResultContract Run**：`IncludeHistory: !created`（`admission.go:651`），窗口 20 个 Run（`chat/defaults.go:3`，`session_builder.go:71-85`）。历史中可能含旧 Query 对话、旧 Execute 的 task input、旧 `result_submit` 调用与不同 Schema 的 output；历史技能凭据会重建（`AGENTS.md:136`）。本 Run 的工具定义仍是按 Run 生成（`execute_result.go:140-167`），历史中旧 tool_call 的回放形态是否原样进入模型请求：待确认。纯「upload 占位 Chat → 首次 execute」无历史 Run，不受影响。
- **R4. multipart 文件早于准入检查落盘**：`StoreFiles(chatID)` 在 `prepare` 之前执行（`execute_result.go:54-63` vs `:89`），复用 chatId 时，即便随后 `target_owner_mismatch` / `active_run_conflict` / awaiting / `compact_in_progress` 失败，文件已写入目标 Chat；`saveUploadedFile` 用 `os.Create` 按基名覆盖同名文件（`handler_resource.go:482-499`）。
- **R5. 重复交卷保护的隔离范围**：`ResultContract` 每次 Execute 新建（`execute_result.go:72`），`Accept` 只保留本实例首个提交（`contracts/result_contract.go`），打回计数在本 Run 的 stream 上（`llm/run_stream_result_submit.go:29,37`），按 Run 隔离。代码层面未见跨 Run 串扰；R3 的历史内容对模型行为的影响不属于该保护范围。
- **R6. upload ticket 无生命周期语义**：ticket 无过期、一次性、绑定 requestId/调用方的字段（`types_search_upload.go:30-45`）；Prepare 只按 `url` 解析当前 Chat 资源，不校验 `id`（`prepare.go:79-97`）。同一 ticket 可在多次 execute 中重复引用；同 Chat 内任何已存在文件都可按 url 引用。
- **R7. 远端 URL 引用经 Execute 暴露服务端下载**：绝对 http(s) URL 会被平台下载（`prepare.go:99-102`、`:105-181`，`httpclient.NewClient(0)` 进程级代理），远端 `Content-Type` 覆盖 mimeType（`:176-178`）。是否拦截内网地址：待确认。Execute 是否允许此类引用见待定问题 12。
- **R8. 路径与 MIME 信任**：当前 Chat 资源引用的 Path 由 url 重算（`session/prompt_context.go:146-176`），客户端 path 不被采信；path-only `/workspace` 引用拒绝（`:156-161`）。mimeType 仍取自客户端（或远端头），图片块按 MIME 白名单筛选（`querymessages/builder.go:216-256`）；`media.LoadImageFile` 是否按内容嗅探：待确认。
- **R9. 数量、大小与去重**：`NormalizeResourceReferences` 只对 skill/kbase/chat 去重（`queryinput/references.go:48-74`），file 引用不去重；单 Run 数量上限只覆盖 skill/kbase/chat（`session/references.go:33-37`）。单图大小上限 `DefaultMaxImageBytes`（`multimodal/images.go:23,52`），图片张数上限未找到：待确认。multipart 表单上限 32 MiB（`handler_execute.go:42`）。`$file` 文件名与 references 指向同一基名时无交叉检测（C2/C3 相关）。
- **R10. interactionConfig 对 references 生效，对 `$file` 不生效**：references 经 `ValidateInteractionInput`（`query_interaction.go:26-30`）与 `PrepareRunReferences`（`session/references.go:39-58`），file 类型受 `attachment.localFiles` 控制（`interaction/config.go:154-158`）；EXECUTOR 默认全部开启（`interaction/config.go:32-52`，EXECUTOR 不在各 switch 分支）。Agent 显式关闭 localFiles 时 references 被 400 `interaction_disabled`，multipart `$file` 仍可用。远端执行后端对 chat/kbase/skill 引用不支持（`session/references.go:51-55`）。
- **R11. 并发**：同 chatId 两次 Execute 由 `ReserveChatQuery`（`admission.go:382-399`）与 `RegisterPreparedQuery`（`query_gate.go:222-288`）拒绝后者，但 R4 的落盘已发生。`/api/upload` 不检查活动 Run（`handler_resource.go:344-372`），Run 进行中可覆盖同名文件；Query 当前消息的图片读取时机与历史重放读到被替换文件的行为（steer 场景见 `steer_images_test.go:139-145`、`:175-180`「附件已不可用」）在 Execute 路径上是否一致：待确认。
- **R12. 错误码与 `data.chatId` 契约**：`docs/智能体执行接口.md:63-72` 的错误表未列复用路径会出现的 `target_owner_mismatch`、`active_run_conflict`、awaiting 相关码、`compact_in_progress`、`interaction_disabled`、`reference_unavailable`、`resource_reference_*`（含 413/502）、`chat_reference_*`。409 同时承载 `cancelled` 与冲突类码。表中「不创建 Chat」与「已创建 Chat 时 data.chatId」的表述在复用场景含义变化；`prepare` 返回的错误不经 `executeRequestError`，不带 `data.chatId`（`execute_result.go:89-91` vs `:198-204`）。
- **R13. Chat 名称与事件**：复用占位 Chat 时 `PromotePendingChatName` 用 execute 的 message 命名并广播 `chat.renamed`，新建时广播 `chat.created`（`admission.go:617-640`）。占位 Chat 在首次正式请求前不出现在列表（`docs/API与协议.md:897`），execute 后出现。若复用的是 Agent 主要聊天，execute 不切换指针，但其活动 Run 会使 Mailbox 投递 409（`AGENTS.md:103`）。
- **R14. 审计与可观测**：JSONL query 行原样保存请求体，平台决定的 references 写在行顶层（`AGENTS.md:105`），execute 的 chatId/references 会随之落盘；带 requestId 的 execute Run 在 `run.started` Push 中附带 requestId 与 chatId（`docs/智能体执行接口.md:102`），复用场景下该 chatId 为调用方传入值。Execute 专用的指标/日志字段是否需要区分「新建 / 复用」：待确认。
- **R15. 向后兼容**：旧客户端不传新字段行为不变（原文 F/G/H）。曾依赖 `chatId` 被 400 `unknown_field` 拒绝的调用方或测试（`handler_execute_test.go:330`）语义反转。WebClient 已有 `references?: unknown[]`（`agent-webclient/src/shared/data/api/dto/commands.ts:19`）与 `ReferenceData`（`src/shared/contracts/agentEvents.ts:169-175`）两种表示，DTO 复用哪一种：待确认（待定问题 15）。
- **R16. 鉴权关闭 / 匿名**：principal 为空时 `QueryPrincipalCanReferenceChat` 只拒绝 `query:` 前缀来源的 Chat（`prepare.go:244-248`），资源 handler 对匿名直接放行（`handler_resource.go:220-223`）。与原文部署差异第 5 条同一问题，此处补充具体行号。

### 3. 补充缺口（续 P9）

- **P10. 目标 Chat 访问判断缺位**：对应 R1/R16。实现需求 4 时，「principal / Chat source」判断在 Query 主链路无可复用实现，需决定判断放在 Execute 专用路径还是 Query 通用准入（后者影响 `/api/query` 与 upload 现有行为）。见待定问题 13。
- **P11. 复用分支的落盘时机**：对应 R4。复用 chatId 且带 multipart 时，文件写入与准入检查的先后需在契约中明确（与 C4 相关）。
- **P12. 历史上下文**：对应 R3。复用带历史的 Chat 时是否加载历史、是否限定只能复用「仅上传、无 Run」的占位 Chat，需定义。见待定问题 11、14。
- **P13. 远端引用与引用类型白名单**：对应 R7、原文 P7/待定 3。file 类型内部又分「当前 Chat 资源」「绝对 http(s) URL」「解析为空原样返回」三种（`prepare.go:79-103`），白名单需细化到此层。
- **P14. 错误契约更新**：对应 R12。`docs/智能体执行接口.md` 错误表与 `data.chatId` 规则。
- **P15. 限额**：对应 R9。file 引用个数、图片张数、总字节、与 multipart 合计是否设上限。

### 4. 组合规则补充选项（续 C4，未定）

| 选项 | 规则 | 利 | 弊 |
|---|---|---|---|
| **C5. 复用 chatId 仅限占位 Chat** | 目标 Chat 必须无 `LastRunID`（仅 upload 产生）；否则 409/400 | 规避 R2/R3 历史与跨 Agent 问题；贴合 upload→execute 流程 | 不能在已有对话的 Chat 上 execute；需新错误码 |
| **C6. 复用时 multipart 延后落盘** | 带 chatId 时 StoreFiles 移到 admission/注册之后 | 消除 R4 的副作用 | 改动 Execute 编排顺序；失败路径需清理语义 |
| **C7. references 仅接受当前 Chat 资源 URL** | 拒绝绝对 http(s) 与解析为空的 file 引用 | 规避 R7；与「先 upload」流程一致 | 与 Query references 能力不一致 |

### 5. 验收补充（续 K）

| # | 场景 | 期望 |
|---|---|---|
| L | 已认证用户 B 用用户 A 的 chatId 调 execute | 按待定问题 13 的决定返回（拒绝码待定）；不写文件、不起 Run |
| M | 占位 Chat（upload 未带 agentKey）→ execute(agentKey=exec-agent) | 按待定问题 8 的决定；若允许，Chat 绑定 exec-agent，`chat.renamed` 一次 |
| N | 复用 chatId + multipart，目标 Chat 有活动 Run | `active_run_conflict`；按 C6/P11 的决定断言文件是否落盘 |
| O | 非视觉 fixture 模型 + 图片 references | 请求无 `image_url`、含文件名文本；ResultContract 仍生效（参照 `steer_images_test.go:166-174` 写法） |
| P | references 指向不存在的 `@chat/` 文件 | 4xx（码按 Prepare/ResolveResource 现有行为），不起 Run |
| Q | references 含绝对 http(s) URL | 按 C7/待定问题 12 决定：拒绝或下载（mock HTTP server，不访问外网） |
| R | Agent `interactionConfig.attachment.localFiles:false` | references 400 `interaction_disabled`；multipart `$file` 行为按决定 |
| S | 复用有历史 Run 的 Chat | 按 C5/待定问题 14 决定；若允许，断言模型请求中历史与本轮 `result_submit` 定义的形态 |
| T | 同一 ticket 两次 execute（串行） | 两次均可引用或按待定问题 16 决定 |
| U | 同一图片 reference 重复两次 | 断言 `image_url` 出现次数（去重语义待定，R9） |
| V | 复用 chatId 时的 400 类错误 | `data.chatId` 是否返回与文档一致（R12） |
| W | 复用 chatId 交卷成功后同 Chat 再 execute | 第二次 Run 拥有独立 ResultContract，首个合法提交被接受（R5） |

测试矩阵缺口：原文 A–K 未覆盖 L–W；现有 `handler_execute_test.go:322` 起的非法请求用例断言「不创建 Chat」，复用路径需另立断言。

### 6. 待定问题（续 9）

10. 文件名 `plans/cutej-execute-upload-references.md` 与标题是否更名为通用 Execute 名称（本次未改）？
11. 是否允许 Execute 复用「已有 Run / 属于其他 Agent 历史」的 Chat，还是只限占位 Chat（C5）？
12. Execute 的 file references 是否允许绝对 http(s) URL（服务端下载）？若允许，是否需要内网地址限制（R7，现状待确认）？
13. 目标 Chat 的调用方归属判断（R1）放在 Execute 专用路径，还是 Query 通用准入（会影响 `/api/query`、`/api/upload` 现有行为）？拒绝时的 HTTP 码与错误码？
14. 复用带历史的 Chat 时是否加载历史（`IncludeHistory`）及历史技能凭据？
15. WebClient `ExecuteRequest.references` 用 `unknown[]`（与 commands.ts 一致）还是 `ReferenceData[]`？
16. upload ticket 是否需要过期 / 一次性 / 绑定调用方语义，还是维持「Chat 内资源 URL 即可引用」？
17. file 引用数量、图片张数、总大小是否设上限，与 multipart 32 MiB 是否合并计算？重复引用是否去重？
18. 复用 chatId 且带 multipart 时，文件落盘是否必须在准入检查之后（C6）？失败时已写文件如何处理？
19. Execute 错误表是否补列复用路径的错误码，`data.chatId` 在复用场景的返回规则？
