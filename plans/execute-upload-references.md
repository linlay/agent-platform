# `/api/execute` 复用上传 Chat 与 references

本版替代此前的长版评估（见 git 历史）。结论：Execute 本来就是「一次 Query + 交卷合同」，把 `chatId`、`references` 原样交给 Query 的准入链路即可，不另造校验。

## 目标

```text
POST /api/upload（可多次，第 2 个起带同一 chatId）
  → chatId + 每个文件一条 file 引用
POST /api/execute { agentKey, chatId, message, references, outputSchema }
  → 在该 Chat 中跑一次 Run：图片经 references 进入视觉输入，result_submit 按本次 outputSchema 校验
  → 返回 { chatId, status, output | reason }，chatId 与上传时一致
```

同时让 WebClient 执行测试页能用这条路径，并补齐 WebClient 文档中上传与引用的说明。

## 为什么改动小

`Service.Execute` 已经组装 `QueryCommand` 交给 Query 的 `prepare` → `RegisterPreparedQuery`（`internal/runtime/query/execute_result.go:74-99`）。和 Query 相比只差两处：`:53` 固定 `newChatID()`；`:74-87` 不设 `References`。补上这两处后，Chat 复用、Agent 归属、运行冲突、引用解析和图片构造都走 Query 现有实现。

## 契约

新增两个可选字段，类型与 `/api/query` 相同：

| 字段 | 说明 |
|---|---|
| `chatId` | 不传：照旧新建 Chat。传入：与 `/api/query` 相同，已有 Chat 复用，不存在则以该 ID 新建 |
| `references` | `Reference[]`，与 `/api/query` 同一套解析、授权与 `interactionConfig` 约束；按本次 Run 所在 Chat 解析 |

规则：

1. **`chatId` 与 multipart 文件互斥**：同时出现返回 400 `invalid_request`，在写文件之前判断。往已有 Chat 加文件统一走 `/api/upload`。不传 `chatId` 时 multipart + `$file` 保持现状。
2. `message` 仍必填。
3. 准入失败沿用 Query 的错误码与状态：`target_owner_mismatch`（409）、`active_run_conflict`（409）、等待项与 `compact_in_progress`（409）、`interaction_disabled`（400）、`resource_reference_unavailable`（400）等。这些错误不带 `data.chatId`（调用方传入了，本就知道）。
4. 交卷语义不变：打回 3 次 `output_invalid`、提醒一次后 `output_missing`、审批即 `approval_required`、重复交卷拒绝。

### 调用方须知（写入接口文档）

- **Agent 需要视觉模型**，图片才会以 `image_url` 进入模型；非视觉模型只看到文件名，仍可能交出符合 Schema 的结果。
- 失败后用同一 `chatId` 重试，上一轮过程会作为历史带入；想要干净重试就重新上传、拿新 Chat。
- 上传时带了 `agentKey`，execute 必须用同一个；上传时不带，execute 时绑定。
- 上传文件的 `@chat/<name>` 也可以作为普通字符串写进 `input`，不需要再用 multipart 传一次。
- 同一 Chat 有 Run 在跑时返回 409，和 Query 一样。

### 不在本方案范围

- 调用方与 Chat 归属的比对：`/api/query` 和 `/api/upload` 目前同样不比对，多用户隔离属于平台级议题，另立。
- 给 Execute 单独收窄引用类型、给上传 ticket 加过期或一次性语义、新增数量上限。
- 改变交卷语义或放宽 Schema 校验。

## 批次 1：Platform 接通

| 位置 | 改动 |
|---|---|
| `internal/api/execute.go` | `ChatID string \`json:"chatId,omitempty"\``、`References []Reference \`json:"references,omitempty"\`` |
| `internal/runtime/types/execute.go` | `ExecuteCommand` 同步两个字段 |
| `internal/server/handler_execute.go` | 复制到 command |
| `internal/runtime/query/execute_result.go` | 校验段加规则 1（在 `:54` 写文件之前）；`:53` 有 `cmd.ChatID` 用之，否则 `newChatID()`；`QueryCommand.References = cmd.References` |
| `internal/server/handler_execute_test.go` | `:330` 不再把 `chatId` 当未知字段；补下方验收用例 |

验收（模拟 Provider，不发真实模型请求）：

1. upload → execute(chatId, 图片 references)：返回 `chatId` 等于上传 `chatId`；视觉 fixture 模型（参照 `steer_images_test.go:62-64` 追加 `isVision`）的请求含 `image_url`；`result_submit` 参数含本次 Schema。
2. 非视觉模型：请求无 `image_url`、含文件名；交卷照常。
3. `chatId` + multipart：400，不建 Run、目标 Chat 无新文件。
4. 上传绑定 Agent A，execute 用 Agent B：409 `target_owner_mismatch`。
5. `@chat/` 引用指向不存在的文件：400 `resource_reference_unavailable`。
6. 不带新字段的 JSON 与 multipart 用例全部保持通过。

## 批次 2：WebClient 执行测试支持 references

仓库：`../agent-webclient`（相邻仓库可能有并行修改，不 `git stash`）。

**做法：references JSON 是唯一数据源，上传按钮只负责往里追加。** 调用方既可以直接粘贴已有的 `chatId` + references JSON，也可以在页面上传文件自动生成。

界面（放在现有「附件与权限」折叠区，附件之后）：

- `chatId` 输入框，可空。
- `references` JSON 文本框，内容为数组，空表示不发送。
- 「上传为引用」按钮：选择一个或多个文件，依次调用现有 `uploadFile`；第 1 个文件带当前 `chatId`（可为空），之后沿用返回的 `chatId`。每次用 `extractUploadReferences` 把结果追加进 references JSON，用 `extractUploadChatId` 回填 `chatId`。
- 「清空引用」按钮：清空 `chatId` 与 references。

规则：

- references 文本必须是 JSON 数组，否则提示 `execute.validation.references`。
- `chatId` 非空时不能同时选 multipart 附件，前端提示（Platform 也会 400）；按钮互斥即可。
- 上传过程中禁用执行；上传失败显示错误，保留已追加的 references 和 `chatId`。
- 选择示例时与附件一起清空 `chatId` 与 references。
- 发请求时 `chatId`、references 有值才放入；没有 multipart 附件时发 JSON。

| 位置 | 改动 |
|---|---|
| `src/shared/data/api/dto/execute.ts` | `chatId?: string`、`references?: unknown[]`（与 `dto/commands.ts` 的 QueryRequest 一致） |
| `src/features/execute/lib/executeForm.ts` | 解析 references、互斥校验、按需放入字段 |
| `src/features/execute/components/ExecuteAgentModal.tsx` | 上述界面与上传流程 |
| i18n | 新增字段、按钮、校验文案（中英） |
| `executeForm.test.ts` | references 非数组、`chatId` + 附件冲突、空值不发送 |
| `ExecuteAgentModal.test.tsx` | 上传两个文件：第 2 次带第 1 次的 `chatId`，JSON 含两项，execute 以 JSON 发出且带 `chatId`/references |

## 批次 3：文档补齐

### Platform

- `docs/智能体执行接口.md`：字段表加 `chatId`、`references`；删除 `:26`「`chatId`、`references` 都不接受：每次执行都新建 Chat」；新增「复用上传 Chat」小节（流程、规则 1–4、调用方须知、示例）；错误表补规则 3 的错误码。
- `docs/API与协议.md`：`:406` execute 行补两个字段；`:891` upload 表补上 handler 已读取但文档未列的 `agentKey`；`:897` 占位会话说明补「`/api/execute` 同样可以使用」。
- `AGENTS.md:140`：「`POST /api/execute` 在新 Chat 中执行一次 Run」改为在新 Chat 或调用方传入的 `chatId` 中执行，并写明 `chatId` 与 multipart 互斥。

### WebClient：上传与引用能力补齐

- `docs/24-对话输入-附件上传与引用.md`：现在只讲 Composer 流程和 chat/site 引用结构，补齐：
  - `/api/upload` 请求字段（`requestId`、`chatId`、`file`；`uploadFile` 另发的 `sha256` Platform 不读取，平台自行计算）、响应 `{requestId, chatId, upload}`、不带 `chatId` 时创建占位 Chat 且首条正式请求前不进列表。
  - 上传结果到 file 引用的转换：`extractUploadReferences` / `extractUploadChatId`，file 引用字段 `{id, type, name, path, mimeType, sizeBytes, url, sha256}`，`url` 为 `@chat/<name>`，与已有 chat/site 结构并列。
  - 引用的消费入口：query、steer、execute，各自限制（steer 只接受当前 Chat 文件；execute 见执行测试文档）。
  - 图片进入模型的条件：Agent 为视觉模型；否则只有文件名。
  - Composer 与执行测试页是两个上传消费方，共用 `uploadFile` 与上述转换函数。
- `docs/11-协议数据-API端点注册与DTO.md`：新增「上传与执行」小节，说明 `UploadFileParams`、上传响应、`ExecuteRequest` 新字段，`executeAgent` 的 JSON/multipart 选择规则（仅 HTTP、不重试）。
- `docs/64-页面能力-Execute执行测试.md`：把「文件选择不会先调用上传接口」改写为两种方式并列——multipart 附件（`$file`，新建 Chat）与上传为引用（`chatId` + references，复用上传 Chat），写明互斥、上传按钮行为与验证范围。
- `docs/92-质量验证-手工测试用例.md`：增加上传→执行用例（视觉模型看到图片、非视觉模型只见文件名、`chatId` 与附件冲突提示、结果中的 Chat 链接指向上传 Chat）。

## 实施约束

- 按批次推进，每批完成后跑对应测试再进入下一批：Platform `go test ./internal/server/... ./internal/runtime/query/...`，WebClient 对应单测与构建。
- 不改真实 Agent、Skill、凭据与业务数据；不重启服务；不发真实模型请求。
- 实际视觉模型与部署代理长连接需在已登录环境联调；本地测试通过不代表真实执行已验证，交付说明里写明。
