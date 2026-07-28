# KBASE 编辑模式双目录权限设计

## 1. 文档状态

- 状态：已实现；当前代码和测试事实以本文及 `KBASE编辑模式.md` 为准。
- 适用范围：专用 `mode: KBASE` 且请求顶层 `editingMode:true` 的单次 run。
- 设计原则：复用通用 Agent 文件权限框架，仅叠加 KBASE 必需的安全上限和索引语义。
- 不改变项：`editingMode` 准入、现有 11 个工具、KBASE 检索协议、LanceDB generation、chat 存储结构。

目标策略：

```text
file_read / file_glob / file_grep
  - source root 内允许
  - 当前 chatspace 内允许
  - 两者之外交给 AccessPolicy，默认进入 HITL

file_write / file_edit
  - source root 内允许，并触发 kbase-index hook
  - 当前 chatspace 内允许，不触发 kbase-index hook
  - 两者之外硬拒绝
```

## 2. 术语

### 2.1 Source root

知识库唯一内容源：

```text
kbaseConfig.source.root
```

KBASE editing run 已将 effective workspace 设置为 source root，因此通用 AccessPolicy 中的 `@workspace` 在该 run 内就是 source root。

### 2.2 Chatspace

当前 `chatId` 独占的会话目录：

```text
AP_RUNTIME_CHATS_DIR/<chatId>
```

运行时对应：

```go
session.RuntimeContext.LocalPaths.ChatAttachmentsDir
```

chatspace 用于上传文件、临时文本、中间结果和会话交付物。它属于通用 Agent 已有的 `@chat` 会话根。

### 2.3 External

canonical target 不属于当前 session 的 workspace、chatspace 或显式 hostAccess 根时，归类为 `external`。

对 KBASE editing：

- external read/glob/grep 继续由 AccessPolicy 决定 allow、auto、HITL 或 block。
- `host_access` 和 `external` write/edit 都被 KBASE 专用写入上限硬拒绝。

## 3. 设计目标

1. source root 内可安全编辑 UTF-8 Markdown，并同步更新检索索引。
2. 当前 chatspace 可保存通用文本临时产物，且不污染知识库索引。
3. 外部读取完全复用通用 AccessPolicy 和 HITL。
4. `full_access`、hostAccess 或 approval 均不能扩大 KBASE 写边界。
5. canonical path、软链接、future target、写前读、并发校验、原子写和 file history 全部复用通用文件工具。
6. KBASE 与通用 Agent 使用同一条文件授权和执行主链路。
7. 其他 `chatId` 的 chatspace 不获得当前 session 的免审权限。

## 4. 非目标

- 不建设 KBASE 专用 AccessPolicy、HITL 或 approval store。
- 不允许 KBASE 写入 source root 和当前 chatspace 之外。
- 不把 chatspace 加入 KBASE watcher 或 LanceDB 索引。
- 不为普通 Agent 附加的 KBASE capability 开放 editing。
- 不新增 Bash、删除、移动、重命名、独立目录创建或通用 artifact 工具。
- 不修改 KBASE `control.db`、LanceDB schema 或 chat 数据库 schema。

## 5. 权限矩阵

### 5.1 Editing run

| 工具 | Source root / workspace | 当前 chatspace | hostAccess / external |
|---|---|---|---|
| `file_read` | 允许，KBASE source 文件规则生效 | 允许，通用读取规则生效 | 交给 AccessPolicy |
| `file_glob` | 允许，只返回 source 内 `.md` | 允许，结果限制在当前 chatspace | 交给 AccessPolicy |
| `file_grep` | 允许，只检索 source 内 `.md` | 允许，结果限制在当前 chatspace | 交给 AccessPolicy |
| `file_write` | 允许，触发 `kbase-index` | 允许，不触发 `kbase-index` | 硬拒绝 |
| `file_edit` | 允许，触发 `kbase-index` | 允许，不触发 `kbase-index` | 硬拒绝 |

### 5.2 只读 run

省略 `editingMode` 或传 `editingMode:false`：

- 只暴露原 KBASE 六个工具。
- 五个结构化文件工具不进入模型工具集合。
- 伪造 `file_*` 调用由现有 KBASE scoped policy 和工具集合硬校验拒绝。
- 下一次 run 不继承前一次 editing 授权。

### 5.3 决策优先级

```text
管理员 AccessPolicy block
  > KBASE external write hard ceiling
  > AccessPolicy allow / auto / HITL / block
  > KBASE source 内容限制
  > 通用文件工具大小、编码、写前读和并发校验
```

KBASE 的 source/chatspace 允许不会覆盖管理员显式 block。KBASE 外部写硬上限也不会被 `full_access`、hostAccess 或 approval 放宽。

## 6. 文件类型与内容规则

### 6.1 Source root

保持 KBASE Editing 现有规则：

- 仅允许大小写不敏感的 `.md`。
- 文件内容必须为 UTF-8。
- 新文件父目录必须已经存在。
- 不创建目录。
- 已有文件写入前必须完整读取。
- 保留 SHA、mtime、size 并发校验、大小限制、原子替换和 file history。
- `.markdown`、DOCX、PPTX、PDF、图片和其他格式不通过 `file_write/file_edit` 修改。

### 6.2 Chatspace

chatspace 使用通用结构化文件工具语义：

- `file_write` 内容仍为字符串。
- `file_edit` 只处理可解码文本。
- 沿用全局 `max-write-bytes`、binary extension、设备文件、写前读、并发校验和原子替换。
- 不强制 `.md`，可生成 `.txt`、`.json`、`.csv`、`.yaml`、`.html` 等文本产物。
- `file_write` 沿用通用目录处理行为；KBASE 仍不获得独立建目录工具。

### 6.3 External read

- 复用通用 `file_read/file_glob/file_grep` 的编码、大小、设备文件和搜索边界。
- approval 复用通用 exact/rule 机制和 canonical fingerprint。
- 获批读取不获得任何写权限。

## 7. 与通用智能体的一致性审计

### 7.1 审计结论

本方案采用“通用文件权限框架 + KBASE 薄约束”。以下能力直接复用：

| 能力 | 现有复用点 | KBASE 处理 |
|---|---|---|
| workspace | `SessionWorkspaceRoot`、`@workspace` | editing run 已设置为 source root |
| 当前 chatspace | `SessionChatDir`、`@chat` | 直接复用 |
| 路径授权 | `AccessPolicy -> PathPlan -> AccessPlan` | 直接复用 |
| 越界读取 HITL | `read-outside-roots` | 直接复用 |
| exact/rule approval | 通用注册、消费和 fingerprint | 直接复用 |
| canonical path | `pathutil.Canonicalize/WithinRoot` | 直接复用 |
| 文件保护 | 大小、设备文件、写前读、SHA/mtime/size、原子写 | 直接复用 |
| file history | 通用 run/chat 文件历史 | 直接复用 |
| file change hook | 通用 `FileChangeHook` 链 | 根据 canonical `FilePath` 在 hook 内独立判定 source |

KBASE 只保留四项专用差异：

1. 顶层 `editingMode:true` 准入和固定 11 个工具。
2. source root 的 UTF-8 `.md` 等知识源约束。
3. `host_access/external` 写入硬拒绝。
4. 只有 source mutation 触发 `kbase-index`。

第 3 项是本需求相对通用 Agent 的有意差异：通用 Agent 外部写入可以按 AccessPolicy 进入 HITL，KBASE editing 外部写入始终硬拒绝。

### 7.2 当前缺口

当前 `ScopedFilePolicy` 把 source root 同时当作内容范围和唯一授权范围：

```go
type ScopedFilePolicy struct {
    Root                  string
    AllowedExtensions     []string
    AllowRead             bool
    AllowWrite            bool
    AllowCreate           bool
    RequireExistingParent bool
    RequireUTF8           bool
}
```

因此 chatspace 和 external read 会在通用 AccessPolicy 之前被拒绝。

修订方向：

- AccessPolicy 继续负责路径授权和 HITL。
- `ScopedFilePolicy` 只负责 editing 准入、source 内容约束和 KBASE 写入硬上限。

### 7.3 明确不新增

- 不新增 `ScopedPathRule`、KBASE 专用多根规则表或第二套路径决策器。
- 不新增 KBASE approval payload、approval store 或 fingerprint。
- 不复制 allow/auto/HITL/block 状态机。
- 不为 KBASE 重写 canonical path、软链接或 future target 解析。
- 不强制工具结果新增 KBASE 专用 `pathDomain`。

## 8. 会话根与通用路径分类

### 8.1 会话根直接复用

`BuildQuerySession` 已具备两个目标根：

```text
SessionWorkspaceRoot = canonical(kbaseConfig.source.root)
SessionChatDir       = canonical(AP_RUNTIME_CHATS_DIR/<chatId>)
```

通用默认 AccessPolicy 已包含：

```text
read-roots:  @workspace, @chat, @agent, @skills
write-roots: @workspace, @chat
```

所以 source root 和当前 chatspace 无需再注册一套 KBASE allowlist。

要求：

1. `chatId` 继续通过现有校验。
2. chatspace 由平台生成，客户端和工具参数不能覆盖。
3. 两个根在 run 开始时随 session 冻结。
4. source root 与平台 `AP_RUNTIME_CHATS_DIR` canonical 根必须完全分离；相同或任一方包含另一方时拒绝 editing run。
5. `PathInSessionWorkspace` 继续同时检查 workspace 和当前 chatspace；KBASE 通过上项配置校验保证其他 chatId 不会落入 source。

### 8.2 实时路径判定

不增加或保存 `PathScope`。每个安全边界根据 session 冻结根和 canonical 路径实时判断：

```text
写边界：PathInSessionWorkspace(session, path)
source 内容约束：WithinRoot(path, ScopedFilePolicy.Root)
当前 chatspace：WithinRoot(path, SessionChatDir(session))
```

规则：

1. 使用现有 `pathutil.Canonicalize` 和 `WithinRoot`。
2. KBASE editing 中 workspace 就是 source root。
3. 只有当前 session 的 `SessionChatDir` 作为 chatspace。
4. source 路径应用 `.md`、UTF-8 和既有父目录约束。
5. workspace/chatspace 外写入统一硬拒绝；无需区分 hostAccess 和 external。

相对路径仍按通用 working directory 解析。KBASE editing 的 working directory 是 source root；chatspace 文件使用 prompt 中已有的 `chat_dir` 绝对路径。本版本不增加工具参数级 `@chat/...` 语法。

路径归属不进入 `AccessPlan`、HTTP 请求、工具参数、`FileChangeEvent` 或其他跨模块结构。任何外部 `pathScope` 同名字段都不参与授权。

## 9. 单一有效 AccessPlan

### 9.1 统一预审批和执行计划

LLM 工具预检和 executor 都会调用 `BuildAccessPlanFromPolicy`。KBASE 外部写硬拒绝必须直接进入这个现有公共入口。只在 executor 内拒绝会导致 LLM 层先生成一个最终无法执行的 write approval。

不新增包装函数，也不扩展 `AccessPlan` 数据结构。

流程：

```text
BuildAccessPlanFromPolicy
  -> canonical path
  -> PathInSessionWorkspace
  -> applyScopedFileCeiling
  -> AccessPlan
```

KBASE scoped ceiling：

| 操作 | canonical 路径位置 | 有效计划 |
|---|---|---|
| read/glob/grep | 任意位置 | 保留 AccessPolicy 决策 |
| write/edit | workspace/chatspace | 保留 AccessPolicy 决策 |
| write/edit | 两者之外 | 强制 `Blocked=true` |

外部写使用稳定 reason：

```text
KBASE editing write is limited to the session workspace and current chatspace
```

### 9.2 统一调用点

现有以下位置已经使用同一入口，修改后自动获得一致语义：

1. `internal/llm/run_stream_tools_approval.go` 的文件工具预检。
2. `internal/tools` 中 read/glob/grep/write/edit 的执行前计划。
3. 未来新增的结构化文件工具入口。

同一输入在两个阶段产生相同的 path、root、scope、decision 和 fingerprint。无需改动 approval store。executor 仍在实际 I/O 前重新 canonicalize；path key 变化时沿用现有 approval/fingerprint 失效机制。

### 9.3 KBASE 内容约束

AccessPlan 通过后再应用 KBASE 内容约束：

| scope | KBASE 额外约束 |
|---|---|
| workspace/source | `.md`、UTF-8、父目录已存在 |
| chat | 无 KBASE 扩展名限制，沿用通用文本工具约束 |
| host_access/external read | 无 KBASE 扩展名限制，沿用通用读取约束 |
| host_access/external write | 已由有效 AccessPlan 硬拒绝 |

`ScopedFilePolicy` 可以保持单 source root，只增加表达“允许当前 chat 根”和“写入限定 session roots”的最小字段；也可以将两个开关放入现有 mode capability。核心要求是它不重新实现 AccessPolicy。

## 10. 工具执行流程

### 10.1 `file_read`

```text
BuildAccessPlanFromPolicy
  -> block：返回通用 file_read_path_blocked
  -> requires approval：进入现有 file_read HITL
  -> allow/auto/approved：继续
  -> workspace/source：执行 KBASE .md + UTF-8 校验
  -> chat/external：执行通用读取校验
  -> 实际读取
```

### 10.2 `file_glob/file_grep`

- workspace/source：保留 KBASE Markdown filter。
- chat：使用通用搜索规则，执行根固定为当前 chatspace target。
- external：先走通用 AccessPolicy；需要审批时复用现有 read approval。
- 搜索结果受执行根约束，并对结果 canonical path 做边界过滤。
- `files_with_matches`、`count` 和 `content` 三种 grep 输出使用同一边界。

### 10.3 `file_write/file_edit`

```text
BuildAccessPlanFromPolicy
  -> host_access/external：立即返回通用 path_blocked
  -> workspace/source：执行 KBASE source 内容约束
  -> chat：执行通用文本写入约束
  -> 通用写前读、并发、大小和设备文件校验
  -> 执行前重新 canonicalize
  -> 原子写入
  -> 通用 file history
  -> 通用 FileChangeHook 链
```

外部写在 approval 生成、approval 消费、hostAccess 和 `full_access` 放宽之前已进入 blocked 计划，因此不出现无效 approval UI。

## 11. HITL 复用

只有 external read/glob/grep 可以进入路径审批。完整复用通用智能体的：

- `file_read_approval_required` 事件和 submit 协议。
- exact approval 与 rule approval。
- canonical path/root fingerprint。
- run-scoped approval 状态。
- 拒绝、消费、重放防护和执行前重新校验。

不增加 KBASE 专用 approval schema。reason 可以由有效 AccessPlan 提供，事件结构保持通用。

其他 chatId 的 chatspace 对当前 session 是普通 external 路径：

- read/glob/grep：按 AccessPolicy 处理，默认 HITL。
- write/edit：被 KBASE hard ceiling 拒绝。

## 12. Hook 分流

### 12.1 当前问题

成功的 `file_write/file_edit` 会进入通用 `FileChangeHook` 链。当前 KBASE hook 收到 source root 外路径时返回 `failed`。开放 chatspace 后，需要把 chat mutation 视为“不适用”，而不是索引失败。

### 12.2 文件变更事件保持事实化

`FileChangeEvent` 只携带 executor 已经 canonicalize 的 `FilePath`、Agent、chat、run 和变更类型等事实，不携带路径归属或其他授权结论。这样 hook 无需信任事件生产者传入的范围标签，也避免后续新增事件生产者时误把分类结果当作权限凭证。

### 12.3 KBASE hook

```text
canonical FilePath 位于该 Agent source root
  -> 执行现有 delta Refresh
  -> 返回 kbase-index success/skipped/failed

canonical FilePath 不位于该 Agent source root
  -> 返回空 FileChangeHookResult
  -> 不调用 Refresh
  -> 不改变 capability 状态
```

hook 必须从 AgentSpec 取得 source root，并对 `FilePath` 独立执行 canonical/WithinRoot 校验。hook 链、KBASE refresh coordinator、storage lock、generation 规则和 watcher hash 去重继续复用。

工具结果保持当前结构：

- source mutation：存在 `hooks[].name=kbase-index`。
- chatspace mutation：省略 `hooks`。

本版本不增加 `pathDomain` 或 `pathScope` 对外字段。产品如需展示路径范围，只能展示服务端生成的审计信息，且该信息不得作为后续请求的授权输入。

## 13. Prompt 与 API

### 13.1 Prompt

KBASE editing prompt 明确：

```text
Knowledge source: {{kbase_source_root}}
Current chatspace: {{chat_dir}}
```

规则：

1. 修改知识内容时写 source root，只允许 UTF-8 `.md`。
2. 临时分析结果和会话产物写当前 chatspace。
3. chatspace 不属于知识源，不对它调用 `kbase_refresh`。
4. source mutation 检查 `kbase-index`；chat mutation 不期待该 hook。
5. 两个目录之外的读取服从通用 AccessPolicy。
6. 两个目录之外的写入固定拒绝。

工具集合保持：

```text
kbase_search kbase_files kbase_read kbase_status kbase_refresh datetime
file_read file_glob file_grep file_write file_edit
```

### 13.2 API

不增加请求字段：

```json
{
  "agentKey": "docs-kbase",
  "chatId": "chat-1",
  "editingMode": true,
  "message": "更新知识库并在会话目录生成对比报告"
}
```

chatspace 由服务端根据 `chatId` 计算。HITL、SSE、WebSocket、JSONL、replay 和 export 使用现有通用协议。

### 13.3 System init

可以记录不含绝对路径的策略摘要：

```json
{
  "editingMode": true,
  "scopedFilePolicy": {
    "sourceContent": "utf8_markdown",
    "allowCurrentChat": true,
    "denyWriteOutsideSessionRoots": true
  }
}
```

它是既有 session 策略摘要，不是新的前端控制参数。

## 14. 错误语义

优先复用通用错误：

| 场景 | 错误 |
|---|---|
| external read 被 block | `file_read_path_blocked` / `glob_path_blocked` / `grep_path_blocked` |
| external read 需要 HITL | `file_read_approval_required` |
| KBASE external write | `file_write_path_blocked` |
| KBASE external edit | `file_edit_path_blocked` |

保留确有 KBASE 语义的现有错误：

```text
kbase_editing_mode_required
kbase_editing_extension_unsupported
kbase_editing_parent_missing
kbase_editing_encoding_unsupported
kbase_editing_tool_unsupported
```

错误 payload 的 `reason` 说明 KBASE editing 写上限。无需新增 `kbase_editing_write_outside_roots`、`kbase_editing_chat_mismatch` 或专用 approval 错误。

source root 与平台 chats 根重叠时使用 session 构建错误。该错误只表示服务端配置异常，不进入文件工具常规协议。

## 15. 审计与安全

### 15.1 审计字段

沿用现有文件工具和 HITL 审计字段，不新增路径归属标签：

```text
agentKey chatId runId tool operation canonicalPath
approvalDecision beforeSha afterSha hookStatus
```

日志不记录文件正文、token、凭据或完整模型输入。

### 15.2 安全约束

1. workspace/chat 根来自 session 冻结。
2. canonical path 和软链接只使用通用 `pathutil`。
3. future target 使用通用最近存在父目录解析。
4. LLM 预检与 executor 使用同一有效 AccessPlan。
5. 执行前重新 canonicalize，fingerprint 变化使批准失效。
6. read approval 不能被 write/edit 消费。
7. `full_access`、hostAccess writeRoots 和伪造 approval 不能扩大 KBASE 写边界。
8. chatspace 写入不触发 KBASE refresh，也不使 KBASE capability degraded。
9. 被拒绝的写入不产生目标文件、file history 或 hook。

## 16. 实现改动点

### 16.1 通用层

`internal/accesspolicy`：

- 复用现有 `PathInSessionWorkspace` 对 workspace 和当前 chatspace 做 canonical 包含判断。
- 扩展现有 `BuildAccessPlanFromPolicy`，在返回 AccessPlan 前应用 session scoped ceiling。
- 保留 canonical、root alias、AccessPolicy decision 和 approval fingerprint。

`internal/llm/run_stream_tools_approval.go`：

- 文件工具预审批继续使用 `BuildAccessPlanFromPolicy`，无需增加第二入口。
- blocked external write 直接返回拒绝，不生成 approval。

`internal/tools`：

- read/glob/grep/write/edit 全部改用同一有效计划。
- chatspace 走现有通用文件执行链。
- `FileChangeEvent` 只填充 canonical `FilePath` 等变更事实。

`internal/contracts`：

- `ScopedFilePolicy` 只做最小扩展，不改成 KBASE 专用多根规则引擎。
- `FileChangeEvent` 不承载路径授权分类。

### 16.2 KBASE 层

`internal/server/session_builder.go`：

- 继续复用 source 作为 workspace、当前 chatspace 作为 `ChatAttachmentsDir`。
- 冻结 source 内容约束和 external write hard ceiling。
- 校验 source root 与平台 chats canonical 根完全分离。

`internal/filetools/scoped.go`：

- source scope 应用 `.md`、UTF-8、父目录等限制。
- chat 和 external read 不再被 source-only 校验提前拒绝。
- raw path 中的 `..` 改由通用 canonical 分类，不保留 KBASE 特例。

`internal/kbase/editing_hook.go`：

- 从 AgentSpec 取得 source root，独立 canonicalize `FilePath` 并判断是否位于 source。
- source 内 mutation 执行索引；source 外 mutation 返回空 hook result。
- source 外 mutation 不改变 capability 状态。

`internal/agent/kbase`：

- 只更新 source/chatspace 分工 prompt。
- editing 工具集合、mode 准入和 memory 边界保持不变。

### 16.3 文档

实现后同步更新：

- `docs/KBASE编辑模式.md`
- `docs/工具目录权限.md`
- `docs/API与协议.md`
- `docs/手工测试用例.md`
- `docs/智能体配置说明.md`
- `AGENTS.md`

## 17. 测试方案

### 17.1 通用框架复用回归

1. 普通 Agent 的 workspace/chat/external read/write 行为保持不变。
2. 普通 Agent 的 exact/rule HITL、重放防护和 fingerprint 测试保持通过。
3. 普通 Agent 的写前读、并发校验、原子写和 file history 保持通过。
4. KBASE 与普通 Agent 对同一路径得到一致的 canonical path 和根包含判断。
5. LLM 预检和 executor 对同一 KBASE 调用得到相同有效 AccessPlan。
6. KBASE external write 在 LLM 预检阶段已 blocked，不产生 awaiting approval。

### 17.2 Session 与准入

1. 省略或传 `editingMode:false` 时不暴露文件工具，伪造调用被拒绝。
2. `params.editingMode:true` 不生效。
3. 非专用 KBASE 顶层 `editingMode:true` 返回 400。
4. editing session 的 workspace 是 source root，chat dir 是当前 chatspace。
5. source root 与平台 chats 根相同或互相包含时拒绝。
6. 通用分类器的 workspace/chat 父子目录按最具体根分类。
7. 下一次 query 不继承上一次 editing 授权。

### 17.3 Source root

1. 相对 `.md` read/glob/grep 成功。
2. `.md` 新建、修改成功。
3. source mutation 返回一次 `kbase-index` success/skipped/failed。
4. `.markdown`、`.txt`、非 UTF-8、缺失父目录被拒绝。
5. 已有文件未完整读取时 write/edit 被拒绝。
6. 同 run 文件被外部修改后并发校验拒绝写入。

### 17.4 Chatspace

1. 当前 chatspace read/glob/grep 成功。
2. `.md/.txt/.json/.csv/.yaml/.html` 文本写入成功。
3. chat mutation 不包含 `kbase-index` hook。
4. chat mutation 不调用 KBASE Refresh，不改变 capability 状态。
5. chat mutation 进入通用 file history。
6. 已有 chat 文件继续执行写前读和并发校验。
7. 其他 chatId 的 write/edit 硬拒绝。
8. 其他 chatId 的 read/glob/grep 按 external 处理，默认进入 HITL。

### 17.5 External read

1. default 下 external read/glob/grep 产生现有 HITL。
2. allow、auto、approval、block 与普通 Agent 的 AccessPolicy 语义一致。
3. 批准后只访问获批 canonical target/root。
4. 拒绝后不返回文件正文或搜索结果。
5. approval 重放、跨 run 使用和 path fingerprint 变化均失败。
6. external glob/grep 结果不能逃出获批根。

### 17.6 External write

1. 绝对 external write/edit 硬拒绝。
2. `../` 解析到 external 后硬拒绝。
3. source/chatspace 内指向 external 的软链接写入硬拒绝。
4. `default`、`auto_approve`、`full_access` 下均硬拒绝。
5. hostAccess writeRoots 不扩大边界。
6. exact/rule/伪造 approval 均不能扩大边界。
7. 拒绝发生在 awaiting approval 之前。
8. 拒绝后无目标文件、file history 或 hook。

### 17.7 Hook 与协议

1. source write/edit 各调用一次 KBASE delta refresh。
2. chat write/edit 调用 KBASE refresh 的次数为 0。
3. source hook 失败不回滚文件，并保持现有 degraded 语义。
4. chat 写入失败不改变 KBASE capability。
5. include/exclude 只作用于 source hook。
6. HTTP 与 WebSocket query 行为一致。
7. 现有 SSE、JSONL、replay、export 和 HITL schema 无破坏性变化。

### 17.8 路径兼容

1. macOS/Windows 大小写规则正确。
2. Unicode NFC/NFD canonical key 正确。
3. `/source` 与 `/source-evil` 前缀不能混淆。
4. future target 的软链接父目录正确解析。
5. 通用分类器的 workspace/chat 父子目录按最具体根分类。
6. KBASE source root 与平台 chats 根重叠时被 session 构建拒绝。

## 18. 发布准入

1. 普通 Agent 文件权限与 HITL 全量回归通过。
2. source root 合法 `.md` 编辑和同步索引闭环通过。
3. 当前 chatspace 文本读写和 file history 闭环通过。
4. chatspace 写入触发 `kbase-index` 的次数为 0。
5. source mutation 缺失预期 `kbase-index` 的次数为 0。
6. external write 在所有 access level、hostAccess 和 approval 情况下成功次数为 0。
7. external write 产生 approval awaiting 的次数为 0。
8. default 下 external read 未经 HITL 获取内容的次数为 0。
9. 其他 chatId chatspace 的未授权读写次数为 0。
10. 软链接、`..`、绝对路径和 TOCTOU 测试全部通过。
11. 全量 `go test ./...` 通过。

## 19. 推荐实施顺序

1. 复用 `PathInSessionWorkspace` 和 `WithinRoot` 增加实时路径判定测试。
2. 在现有 AccessPlan 构建入口应用 session scoped ceiling，同时接入 LLM 预检和 executor。
3. 调整 `ScopedFilePolicy`，让 source 内容限制与通用路径授权分离。
4. 打通 chatspace read/glob/grep/write/edit 的通用执行链。
5. 保持 `FileChangeEvent` 只携带 canonical 路径事实，由 KBASE hook 独立判断 source 范围。
6. 更新 KBASE prompt 和非敏感 system-init 摘要。
7. 完成普通 Agent 回归、KBASE 权限矩阵、HITL、软链接和真实文件系统集成测试。
