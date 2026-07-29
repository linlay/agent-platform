# KBASE 编辑模式越权对抗测试报告

## 结论

KBASE 不再建立独立的 external 写入硬上限。安全目标是：所有目录先服从通用 AccessPolicy/HITL；只有 Source mutation 额外要求 `editingMode:true`。该 gate 是专用 KBASE 能力边界，不是目录权限，任何 approval 或 access level 都不能替代。

自动化回归覆盖以下事实：

- main/editing 两种 stage 都使用 Source root 作为 Workspace，并提供相同的五个文件工具。
- Source、当前 Chat 目录、其他 chatId 和 external 都先使用同一 AccessPolicy。
- 非 editing Source 可读不可 mutation；write/edit 不产生无意义 HITL。
- `full_access`、auto approve、writeRoots、hostAccess 和 approval 都不能放宽非 editing Source mutation。
- 当前 Chat 目录在两种模式下均可读写。
- 默认 external 与其他 chatId 写入先进入 HITL，批准后可成功。
- AccessPolicy `writeRoots`、`runtimeConfig.hostAccess.writeRoots` 与 `full_access` 可直接允许写入。
- 管理员 `block` 保持最终拒绝，且不会被 approval 放宽。
- `..`、绝对路径、source 内 symlink 和伪造的 `pathScope` 都按 canonical 实际目标判定。
- read approval 不能复用为 write approval。
- 未在专用 KBASE 固定工具集内的工具不能通过伪造 tool call 注入。
- source 支持通用文本格式和通用编码；已有文件仍强制先读后写，新文件父目录必须存在。
- source mutation 不返回同步 `kbase-index` hook。

## 执行器矩阵

主要用例位于：

```text
internal/tools/kbase_editing_adversarial_test.go
internal/tools/tool_file_test.go
internal/filetools/filetools_test.go
internal/filetools/scoped_test.go
```

### 默认外部写入

对 external 绝对路径、`../` 目标、其他 chatId 和 source symlink 目标：

1. 服务端 canonicalize 目标路径。
2. 未批准时返回 `file_write_path_approval_required`，目标不变化。
3. 注册匹配 canonical target 的 write access approval 后，写入成功。
4. 请求自带的 `pathScope/path_scope` 不参与判定。

### 策略放宽与收紧

- `hostAccess.writeRoots`：目标位于额外 write root 时直接成功。
- `full_access`：host path 按通用 level 直接成功。
- `writeOutsideRoots: block`：返回 `file_write_path_blocked`，不生成 approval。
- read rule approval 后尝试 write：仍要求独立的 write approval。

以上放宽只适用于 external/其他 chatId。对非 editing Source，测试覆盖相对路径、绝对路径、已有文件、新文件、`../` 与 Chat 指向 Source 的 symlink；即使设置 `full_access`、auto approve、writeRoots、hostAccess 或预注册 exact/rule approval，仍返回 `kbase_editing_mode_required`。若管理员将 Source 配为 readonly root，则 AccessPolicy Block 先返回 path-blocked。

### 工具集注入

在 main/editing session 中伪造以下工具调用：

```text
bash
artifact_publish
desktop_action
memory_search
run_query
agent_invoke
```

执行器统一返回 `kbase_editing_tool_unsupported`。

## 文件与索引分离

source 文件工具回归覆盖：

- `.md`、`.txt`、`.html`、`.json` 和自定义文本扩展名；
- GB18030 等通用支持编码的读取、编辑和原编码写回；
- source 已有文件未读先写失败，读后写成功；
- source 新文件父目录不存在时返回 `kbase_editing_parent_missing`；
- glob/grep 不再过滤为 `.md`；
- mutation 结果不包含 `hooks[].name="kbase-index"`。

索引由 `internal/kbase` 的 watcher、change set 和 refresh coordinator 测试独立覆盖。文件编辑能力不读取 `include/exclude/extractor` 来拒绝写入；这些配置只决定 watcher 是否索引该文件。

## Catalog 隔离

以下用例覆盖 source/chats 分离：

```text
internal/kbase/source_validation_test.go
internal/catalog/agent_loader_test.go
```

验证相等、互为父子和 symlink 实际重叠均产生 `invalid_kbase_source_overlap`；只有目标 Agent 从运行时 catalog 隔离，其他 Agent 保持可用，依赖 Team 的重叠成员进入 `InvalidAgentKeys`。

## 回归命令

```bash
go test ./internal/filetools ./internal/tools ./internal/agent/kbase ./internal/kbase ./internal/catalog ./internal/llm ./internal/server
go test ./...
```

macOS 受限 sandbox 若禁止 `httptest` 监听 loopback，需要在允许本地监听的测试环境中执行 `internal/server` 和全仓测试。
