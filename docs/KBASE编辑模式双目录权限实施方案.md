# KBASE 编辑模式双目录权限实施方案

## 1. 状态与范围

- 状态：已按本方案完成运行时代码、相关测试和事实文档修改；全仓测试仍有 5 个与本次权限改动无关的既有失败用例，见第 12 节验证记录。
- 上位设计：[KBASE编辑模式双目录权限设计.md](KBASE编辑模式双目录权限设计.md)。
- 目标：在复用通用 Agent 文件权限、HITL 和文件执行链的前提下，实现 source root 与当前 chatspace 双目录行为。
- 数据库影响：无。
- 配置文件影响：无。
- HTTP、SSE、WebSocket 和 HITL 协议影响：无破坏性变化。

本次仅修改结构化文件工具：

```text
file_read file_glob file_grep file_write file_edit
```

不增加 Bash、删除、移动、重命名或独立目录创建工具。

## 2. 最终技术选择

### 2.1 继续使用一个 AccessPlan 入口

保留现有：

```go
filetools.BuildAccessPlanFromPolicy(...)
```

保持 `AccessPlan` 原有结构不变，在该函数返回前根据 session 根和 canonical `plan.Path` 应用 KBASE session hard ceiling。LLM 预检和 executor 已经调用此函数，因此无需增加 `BuildEffectiveAccessPlan` 或第二套决策入口。

### 2.2 路径归属实时判定

不增加 `PathScope` 枚举或 AccessPlan 字段。复用现有路径事实：

```text
workspace/chatspace 写边界：PathInSessionWorkspace(session, path)
source 内容边界：WithinRoot(path, ScopedFilePolicy.Root)
当前 chatspace：WithinRoot(path, SessionChatDir(session))
```

所有判断都基于 session 冻结根和当前 canonical 路径实时计算，不跨阶段传递路径归属标签。

### 2.3 KBASE scoped policy 只做薄约束

现有 `ScopedFilePolicy` 保留 source 内容规则，并将结构本身定义为写边界：

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

只要 session 存在 `ScopedFilePolicy`，写入就以 workspace 和当前 chatspace 为不可扩大的硬边界。无需增加默认值为 `false` 的安全开关。只读 KBASE 仍通过 `AllowRead/AllowWrite/AllowCreate=false` 拒绝伪造文件工具调用。

### 2.4 不增加对外字段

- 不新增请求参数。
- 不新增 KBASE 专用 approval schema。
- 不强制 tool result 返回 `pathDomain/pathScope`。
- 路径归属不进入 AccessPlan 或 FileChangeHook；各安全边界根据真实路径独立复核。

## 3. 修改总览

| 层次 | 文件 | 修改性质 |
|---|---|---|
| contract | `internal/contracts/interfaces.go` | ScopedFilePolicy 结构不变；FileChangeEvent 不承载路径归属 |
| access policy | `internal/accesspolicy/accesspolicy.go` | 复用 workspace/chatspace canonical 包含判断 |
| file plan | `internal/filetools/filetools.go` | AccessPlan 结构不变，仅应用 ceiling |
| scoped validation | `internal/filetools/scoped.go` | 根据真实路径实时应用 source/chatspace 规则 |
| read tool | `internal/tools/tool_file.go` | 按真实路径校验；hook 只携带真实文件路径 |
| glob tool | `internal/tools/tool_glob.go` | 仅真实 source 路径强制 Markdown |
| grep tool | `internal/tools/tool_grep.go` | 仅真实 source 路径强制 Markdown |
| KBASE hook | `internal/kbase/editing_hook.go` | 根据真实 FilePath 与 source root 独立复核 |
| session | `internal/server/session_builder.go` | 冻结 external write ceiling，校验目录关系 |
| prompt | `internal/agent/kbase/prompt.go` | 明确 source/chatspace 分工 |
| tests | 对应 `*_test.go` | 通用回归 + KBASE 权限矩阵 |
| docs | KBASE、权限、API、手工测试文档 | 同步事实边界 |

## 4. 第一批：公共 contract 与实时路径判定

### 4.1 `internal/contracts/interfaces.go`

保持 `ScopedFilePolicy` 原有字段，并明确其结构语义：

- `ScopedFilePolicy != nil` 表示结构化文件工具存在 run 级硬边界。
- `ScopedFilePolicy` 目前由专用 KBASE session 使用。
- 普通 Agent 的该字段为 `nil`，继续使用通用 AccessPolicy/HITL 行为。
- `FileChangeEvent` 不新增可由调用方传递的授权分类字段。

### 4.2 `internal/accesspolicy/accesspolicy.go`

复用现有 `PathInSessionWorkspace`。该函数已经对 `SessionWorkspaceRoot` 和 `SessionChatDir` 分别执行 canonical 包含判断，因此无需新增路径分类器。hostAccess 与 external 对 KBASE 写入具有相同结果，读取则继续由原 AccessPolicy decision 处理。

session roots 优先于 hostAccess，避免 source root 内路径因为同时落入 hostAccess 子目录而被 KBASE external-write ceiling 错误拦截。

### 4.3 KBASE 目录关系校验

在 session 构建阶段校验：

```text
canonical(source root) 与 canonical(AP_RUNTIME_CHATS_DIR) 必须不重叠
```

这里校验 chats 根目录，而不只校验当前 `<chatId>` 目录。原因是 source root 如果包含整个 chats 根，其他 chatId 的目录会被错误识别为 source。

建议新增 server 内部函数：

```go
func validateKBaseEditingRoots(sourceRoot string, chatsRoot string) error
```

拒绝条件：

```text
source == chatsRoot
source contains chatsRoot
chatsRoot contains source
```

该校验只针对专用 KBASE editing run，不修改普通 Agent workspace 行为。

### 4.4 单元测试

修改 `internal/accesspolicy/accesspolicy_test.go`：

1. workspace path 返回 `workspace`。
2. 当前 chat path 返回 `chat`。
3. host read/write root 返回 `host_access`。
4. 其余返回 `external`。
5. `..` 经过 canonical 后正确分类。
6. source/chat 软链接按真实目标分类。
7. workspace/chat 父子根时选择更具体根。
8. `/source` 与 `/source-evil` 不发生前缀误判。

修改 `internal/server/session_builder_test.go`：

1. KBASE editing session 冻结非空 `ScopedFilePolicy`。
2. 只读 KBASE 仍为 deny policy。
3. source 与 chats root 重叠时构建失败。
4. source 与 chats root 分离时构建成功。

## 5. 第二批：AccessPlan hard ceiling

### 5.1 `internal/filetools/filetools.go`

保持 `AccessPlan` 字段不变。在 `BuildAccessPlanFromPolicy` 中保持当前流程：

```text
accesspolicy.BuildPathPlan
  -> canonical path/root
  -> 构造 AccessPlan
```

随后调用 `applyScopedFileCeiling`。

新增内部函数：

```go
func applyScopedFileCeiling(
    session QuerySession,
    plan AccessPlan,
) AccessPlan
```

具体逻辑：

```go
policy := session.ScopedFilePolicy
if policy == nil ||
    plan.Mode != WriteAccess {
    return plan
}

if PathInSessionWorkspace(session, plan.Path) {
    return plan
}

plan.AllowedByWhitelist = false
plan.AutoApproved = false
plan.Blocked = true
plan.Reason = "KBASE editing write is limited to the session workspace and current chatspace"
return plan
```

关键点：

- 不修改原有 `Fingerprint`、`RuleKey` 和 canonical key。
- blocked 计划不会被 `fileAccessPlanNeedsApproval` 识别为待审批。
- executor 在 `ConsumeAccessApproval` 前已经检查 `access.Blocked`。
- hostAccess 和 `full_access` 产生的 allow 会被 session ceiling 收紧。

### 5.2 LLM 层无需新增流程

`internal/llm/run_stream_tools_approval.go` 继续调用：

```go
filetools.BuildAccessPlanFromPolicy(...)
```

确认以下现有逻辑保持：

```go
if plan.Blocked || plan.AutoApproved {
    return false
}
```

因此 KBASE external write：

- 不进入 `fileAccessApprovalRequest`。
- 不注册 exact/rule approval。
- 直接进入 executor，由通用 `file_write_path_blocked` 或 `file_edit_path_blocked` 返回。

无需修改 approval store 或 HITL payload。

### 5.3 单元测试

修改 `internal/filetools/filetools_test.go`：

1. 普通 Agent external write 按原 AccessPolicy 保持 HITL/allow/block。
2. KBASE scoped session workspace write 保持 allow。
3. KBASE scoped session chat write保持 allow。
4. KBASE scoped session hostAccess write强制 block。
5. KBASE scoped session external write强制 block。
6. `auto_approve/full_access` 不能清除 block。
7. external read 的 allow/auto/HITL/block 不受 ceiling 影响。
8. 相对路径和绝对路径经过 canonical 后得到相同 ceiling 结果。

修改 `internal/llm/run_stream_test.go`：

1. KBASE external read 仍产生一个 approval awaiting。
2. KBASE external write/edit 不产生 approval awaiting。
3. blocked write 不进入 combined access/write approval。
4. 普通 Agent external write combined approval 回归不变。

## 6. 第三批：scoped source 校验按真实路径生效

### 6.1 `internal/filetools/scoped.go`

删除 KBASE 特有的 raw `..` 提前拒绝：

```go
ValidateScopedRawPath(...)
```

原因：

- `BuildPathPlan` 已完成 `filepath.Clean` 和 canonicalization。
- external read 中合法出现 `..` 时应进入 AccessPolicy，而不是被 source policy 提前拒绝。
- external write 在 canonical 分类后由 hard ceiling 拒绝。

函数签名保持仅接收 session、真实路径和操作选项：

```go
func ValidateScopedRead(
    session QuerySession,
    path string,
    allowDirectory bool,
) error

func ValidateScopedWrite(
    session QuerySession,
    path string,
) error

func ScopedPathAllowed(
    session QuerySession,
    path string,
    allowDirectory bool,
) bool

func ScopedFilePolicyRequiresUTF8(
    session QuerySession,
    path string,
) bool
```

`ValidateScopedRead`：

```text
policy == nil                    -> return nil
AllowRead == false              -> kbase_editing_mode_required
path 位于 policy.Root           -> 校验 source root、.md、文件类型
path 位于 source 外             -> return nil，继续服从 AccessPlan 和通用文件约束
```

`ValidateScopedWrite`：

```text
policy == nil                    -> return nil
AllowWrite == false             -> kbase_editing_mode_required
path 位于 policy.Root           -> source root、.md、父目录、创建规则
path 位于当前 chatspace         -> return nil，继续通用写入约束
两者之外                        -> 防御性拒绝
```

`ScopedFilePolicyRequiresUTF8` 仅在当前 canonical 路径位于 `policy.Root` 时返回 true。

### 6.2 写前读范围

当前 `tool_file.go` 使用：

```go
scopedPolicy := accessSession.ScopedFilePolicy != nil
```

它会让 chatspace 也强制使用 KBASE source 的写前读策略。修改为：

```go
scopedSource := filetools.ScopedPathInSource(accessSession, access.Path)
```

最终行为：

- source existing file：始终要求本 run 完整读取。
- chat existing file：服从通用 `FileTools.RequireReadBeforeWrite` 配置。
- external：写入前已拒绝。

### 6.3 执行前二次验证

`file_write/file_edit` 在最终替换目标前再次调用 `ValidateScopedWrite(session, plan.FilePath)`，根据当时真实 canonical 路径重新应用 source/chatspace/外部规则。executor 仅在本次函数调用的局部变量中保留初始 `scopedSource` 布尔值，并在落盘前重新计算；若路径跨越 source 边界则拒绝。本地比较不进入 AccessPlan 或跨模块结构。

规则：

1. 重新 canonicalize `plan.FilePath`。
2. 当前目标位于 source 时重新应用 `.md`、父目录和文件类型规则。
3. 当前目标位于 chatspace 时走通用写入。
4. 当前目标逃出两者时拒绝，不重新消费 approval。
5. 当前 source 归属与本次调用初始值不同则拒绝，避免不同内容规则之间发生软链接切换。

该校验覆盖审批后或写入过程中发生的软链接替换。

### 6.4 单元测试

修改 `internal/filetools/scoped_test.go`：

1. source `.md` read/write 通过。
2. source `.txt/.markdown` 拒绝。
3. source 非 UTF-8 拒绝。
4. source 缺失父目录拒绝。
5. chat `.txt/.json/.html` 不受 source 扩展名限制。
6. external read 不受 source 扩展名限制。
7. `AllowRead=false` 对所有路径拒绝。
8. `AllowWrite=false` 对所有路径拒绝。
9. source/chat 软链接逃逸按真实目标重新判断。
10. raw path 带 `..` 按 canonical 目标处理。

## 7. 第四批：五个文件工具

### 7.1 `internal/tools/tool_file.go`

`invokeRead`：

- 保留 `BuildAccessPlanFromPolicy`。
- 删除 `ValidateScopedRawPath`。
- 调用 `ValidateScopedRead(session, access.Path, false)`。
- external read 继续使用 `ConsumeReadApproval`。

`invokeWrite`：

- 删除 `ValidateScopedRawPath`。
- 调用 `ValidateScopedWrite(session, access.Path)`。
- UTF-8 限制改为 `ScopedFilePolicyRequiresUTF8(session, access.Path)`。
- 强制写前读只在 `ScopedPathInSource(session, access.Path)` 生效。
- 最终替换前再次执行 `ValidateScopedWrite(session, plan.FilePath)`。
- 构造 `FileChangeEvent` 时只写入 canonical `FilePath` 等变更事实，不传递授权分类。

`invokeEdit` 做相同调整。

错误保持：

```text
file_write_path_blocked
file_edit_path_blocked
file_read_approval_required
```

不新增 KBASE external-write 错误码。

### 7.2 `internal/tools/tool_glob.go`

修改条件：

```go
scopedSource := filetools.ScopedPathInSource(accessSession, access.Path)
```

只有 `scopedSource=true` 时：

- 注入 `kbasemd:*.[mM][dD]`。
- 使用 `ScopedPathAllowed` 过滤结果。

chat/external：

- 不注入 `kbasemd`。
- 保留用户 pattern 和通用 rg 限制。
- 继续按 AccessPlan/HITL 限定执行根。

### 7.3 `internal/tools/tool_grep.go`

同 glob：

- 只有 source/workspace 注入 `kbasemd`。
- chat/external 允许通用文本搜索。
- `type` 参数在 source 外沿用通用行为。
- `content/files_with_matches/count` 三种模式都覆盖测试。

### 7.4 工具集成测试

修改：

- `internal/tools/tool_file_test.go`
- `internal/tools/tool_glob_test.go`
- `internal/tools/tool_grep_test.go`

覆盖：

| 场景 | 预期 |
|---|---|
| source `.md` read/glob/grep | 成功 |
| source `.txt` read/write/edit | KBASE source 规则拒绝 |
| chat `.txt/.json/.html` | 成功 |
| external `.txt` read | 默认 approval required |
| external read 获批 | 成功 |
| external write/edit | path blocked |
| hostAccess write/edit | path blocked |
| `full_access` external write | path blocked |
| 其他 chatId read | 默认 approval required |
| 其他 chatId write/edit | path blocked |

## 8. 第五批：KBASE hook 分流

### 8.1 `internal/kbase/editing_hook.go`

`AfterFileChange` 不接收 executor 的路径分类，而是从 AgentSpec 取得 source root，并独立校验事件中的 canonical 文件路径：

```go
relativePath, insideSource, err := editingRelativeSourcePath(sourceRoot, event.FilePath)
if err != nil {
    return failedHookResult(err)
}
if !insideSource {
    return contracts.FileChangeHookResult{}
}
```

source 内分支继续执行现有：

```text
AgentSpec
  -> editingRelativeSourcePath
  -> include/exclude
  -> Manager.Refresh(scope=delta)
```

source 外分支（包括 chatspace）不调用：

- `Manager.Refresh`
- `state.SetFailure`

### 8.2 `internal/tools/tool_file.go`

write/edit 的两个 `FileChangeEvent` 都只填入 canonical `FilePath`、Agent、chat、run 和变更类型等事实。路径归属由 hook 根据真实路径独立判断。

### 8.3 测试

修改 `internal/kbase/editing_hook_test.go`：

1. source `.md` 调用一次 delta refresh。
2. chat path 返回空 hook result。
3. chat path 不改变 capability 状态。
4. source 外 canonical 路径防御性返回空结果。
5. source/path canonicalization 失败时返回 failed。

修改 `internal/tools/tool_file_test.go`：

1. recording hook 收到 canonical source `FilePath`。
2. recording hook 收到 canonical chat `FilePath`。
3. source tool result 包含 `kbase-index`。
4. chat tool result省略 `hooks`。

## 9. 第六批：session、prompt 与 system-init

### 9.1 `internal/server/session_builder.go`

editing 和只读 KBASE 都冻结非空 `ScopedFilePolicy`，结构本身即表示 workspace/chatspace 写 ceiling；只读 run 仍由 `AllowRead/AllowWrite=false` 更早拒绝。

调用 `validateKBaseEditingRoots`：

- 仅专用 KBASE 且 `editingMode=true` 时执行。
- 使用 canonical source root 和 `RuntimeContext.LocalPaths.ChatsDir`。
- 配置异常直接拒绝 run，不降级成 HITL。

### 9.2 `internal/agent/kbase/prompt.go`

替换当前“所有文件工具只可用于 source”的提示，明确：

```text
- Knowledge source: {{kbase_source_root}}
- Current chatspace: {{chat_dir}}
- Source mutations only support UTF-8 .md and must produce kbase-index.
- Chatspace may contain temporary text artifacts and must not produce kbase-index.
- Reads outside both roots follow AccessPolicy and may require approval.
- Writes outside both roots are blocked.
```

保留：

- 先读后改的 source 要求。
- 禁止 shell 绕过。
- hook 失败与文件已保存的状态区分。
- 最终列出变更路径和 lineStats。

### 9.3 `internal/agent/kbase/system_init.go`

无需新增 stage 或 tool set。现有 `EditingSystemInitSpec` 保持。

`internal/llm/system_init.go` 已把 `ScopedFilePolicy` 放入 editing fingerprint。新增 ceiling 字段后 fingerprint 会自然变化。

更新 `internal/llm/system_init_test.go`：

1. editing fingerprint 对 ceiling 字段敏感。
2. read-only KBASE fingerprint 仍遵守现有预期。
3. 11 个工具集合不变。

## 10. 不需要修改的模块

以下模块保持原样：

- `internal/api`：不增加 `editingMode` 之外的请求字段。
- `internal/config` 和 `configs/tools.yml`：不增加 KBASE 专用路径配置。
- `internal/chat`：chatspace 和事件持久化结构不变。
- `internal/kbase` storage/control/LanceDB schema：不变。
- MCP、ACP、automation、runops：不受影响。
- KBASE 五个知识工具：接口不变。

## 11. 提交拆分

建议拆为四个可独立评审的提交：

### Commit 1：实时路径判断与 AccessPlan ceiling

```text
internal/contracts/interfaces.go
internal/accesspolicy/accesspolicy.go
internal/accesspolicy/accesspolicy_test.go
internal/filetools/filetools.go
internal/filetools/filetools_test.go
```

验收：普通 Agent 行为不变，KBASE external write plan 为 blocked。

### Commit 2：文件工具双目录执行

```text
internal/filetools/scoped.go
internal/filetools/scoped_test.go
internal/tools/tool_file.go
internal/tools/tool_glob.go
internal/tools/tool_grep.go
对应 tools tests
```

验收：source/chat/external 行为矩阵通过。

### Commit 3：hook、session 和 prompt

```text
internal/kbase/editing_hook.go
internal/kbase/editing_hook_test.go
internal/server/session_builder.go
internal/server/session_builder_test.go
internal/agent/kbase/prompt.go
internal/agent/kbase/profile_test.go
internal/llm/system_init_test.go
```

验收：source 有 hook，chat 无 hook；工具仍固定 11 个。

### Commit 4：集成回归与文档

```text
internal/llm/run_stream_test.go
internal/server/handler_query_integration_test.go
docs/*
AGENTS.md
```

验收：HTTP/WS/HITL 回归通过，文档与代码事实一致。

## 12. 测试命令

每个提交先运行相关包：

```bash
go test ./internal/accesspolicy ./internal/filetools
go test ./internal/tools ./internal/kbase
go test ./internal/agent/kbase ./internal/server ./internal/llm
```

最终运行：

```bash
go test ./...
```

本次验证记录：

- `internal/accesspolicy`、`internal/filetools`、`internal/tools`、`internal/kbase`、`internal/agent/kbase`、`internal/llm` 和 KBASE 相关 `internal/server` 用例均通过。
- 排除以下既有失败用例后，`go test ./...` 全部通过：`TestInvokeGrepContentCountTypeAndPagination`、`TestAdminSourceSkillTextReadWriteAndBinaryGuard`、`TestAdminSourceRegistryReadWriteAndConflict`、`TestProxyLiveTextOnlyPlanEmitsPlanningSnapshotBeforeAwaiting`、`TestQueryGateRejectsPendingAwaitingModes`。
- 上述失败分别位于旧 grep 分页断言、admin source fixture/校验、proxy planning 事件和 planning fixture，不经过本次 KBASE 实时路径判断、AccessPlan ceiling 或 editing hook 代码路径。

必要的真实文件系统集成场景：

1. source、chat、external 三个独立临时目录。
2. source/chat 内分别创建指向 external 的软链接。
3. external read 批准和拒绝各一次。
4. external write 在 default、auto_approve、full_access 下各执行一次。
5. other-chat 目录 read/write 各执行一次。
6. source/chat mutation 后检查 hook 调用次数和 capability 状态。

## 13. 完成标准

以下条件全部满足才算实现完成：

1. 省略或传 `editingMode:false` 时保持只读。
2. 只有专用 `mode: KBASE` 的顶层 `editingMode:true` 可启用编辑。
3. editing run 工具集合仍精确等于 11 个。
4. source root 仅允许 UTF-8 `.md`，mutation 后同步触发一次 `kbase-index`。
5. 当前 chatspace 的通用文本 read/write/edit 成功，`kbase-index` 调用次数为 0。
6. external read 使用通用 AccessPolicy，默认未经批准不能读取内容。
7. external/hostAccess write/edit 在所有 access level 和 approval 状态下成功次数为 0。
8. other-chat 未经授权读取次数为 0，写入成功次数为 0。
9. 普通 Agent 文件权限、HITL 和写入审批行为无回归。
10. 本次新增及直接涉及的测试全部通过，且排除已记录的既有失败后全仓无新增回归。
