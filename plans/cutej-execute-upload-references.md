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
