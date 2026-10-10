# KBASE 编辑模式

## 范围

KBASE Editing 是专用 `mode: KBASE` 的单次 Workspace 与可编辑 collection mutation 授权（collection 仅支持 Host）。专用 KBASE 没有固定工具集：main 与 editing 两种 stage 都使用 `agent.yml` 声明的同一组工具（可以包含 Bash、技能和连接器带来的工具）。新建 KBASE 时默认写入以下结构化文件工具：

```text
file_read file_glob file_grep file_write file_edit
```

专用 KBASE 的 Workspace 始终是最终 canonical `runtimeConfig.workspaceRoot`。旧 `kbaseConfig.source` 会硬失败。当前 Chat 目录始终是 `<chatsDir>/<chatId>`，只通过 `ChatDir` 和 `@chat` 暴露，不是 Workspace：

| 状态 | KBASE Workspace | 当前 Chat 目录 |
|---|---|---|
| 未开启 editing | 可读，不可 mutation | 可读写 |
| 开启 editing | 可读写 | 可读写 |

`editingMode` 不控制文件工具是否存在，也不改变 Workspace；它允许本 Run 修改 KBASE Workspace，以及启动时冻结的 editable collection 目录。它与 `planningMode` 相互独立，自身不产生第二个 execute run；二者同时出现时规划 Run 保持只读，`editingMode` 在已确认计划的执行 Run 生效。普通 Agent 附加的 KBASE capability、Team 和其他 mode 不支持该字段。

这些工具处理普通文本文件，不按知识库索引格式限制扩展名或编码。`.md`、`.txt`、`.json`、`.csv`、`.html` 以及其他可被通用文本工具识别的格式均可读写；支持的非 UTF-8 编码沿用通用检测、显式编码和写回保留规则。DOCX、PPTX、PDF、图片等二进制格式仍需格式专用工具。

editing 不额外注入删除、重命名、创建目录或 Bash 工具；显式声明的 Bash 仍走通用策略。Workspace 和可编辑 collection 的结构化文件工具新建文件均要求父目录已存在。

## 协议

```json
{
  "agentKey": "docs_kbase",
  "message": "更新 docs/policy.html 中的退款条款",
  "editingMode": true
}
```

- HTTP 和 WebSocket `/api/query` 使用同一顶层字段。
- `false` 或省略时 KBASE Workspace 保持只读，当前 Chat 目录仍可读写；`params.editingMode` 不生效。
- 开启时，live `request.query`、JSONL、replay、export 和运行中 `activeRun` 保留 `editingMode:true`。
- 授权不写 Agent 配置，也不在下一次 query 中继承。

main 与 editing run 分别使用 `kbase:main`、`kbase:editing` cache 和独立 prompt，但两者的工具 schema 完全相同。`QuerySession.WorkspaceRoot` 与 `RuntimeContext.LocalPaths.WorkspaceDir` 都冻结为最终 KBASE Workspace；当前 Chat 目录只保存在 `ChatDir`。因此相对文件路径始终指向 KBASE Workspace，写 Chat 产物必须使用 `@chat` 或 prompt 提供的明确 `chat_dir` 路径。

## 目录权限

KBASE Workspace、当前 Chat 目录、其他 chatId 和外部目录统一先生成 AccessPlan：

```text
AccessPolicy -> AccessPlan -> HITL -> FileTools
```

- shipped `default` policy 通过 `@workspace` 允许 KBASE Workspace，通过 `@chat` 允许当前 Chat 目录。
- 其他 chatId 不享受 `@chat`，按外部目录重新计算策略。
- 外部读写可由 policy 直接 allow、自动批准、进入 HITL 或 block。
- `runtimeConfig.hostAccess.readRoots/writeRoots` 和 `full_access` 按通用规则生效。
- 管理员配置的真正 block 是最终决策，不生成无意义的 HITL。
- 请求中的路径分类字段不受信任；`..`、绝对路径和 symlink 都按 canonical 实际目标计算 AccessPolicy、approval fingerprint 和 Workspace 分类。
- read approval 不能复用于 write/edit，其他目标或其他操作的 approval 也不能重放。
- 执行器入口按本次会话的工具集再次校验，不能伪造 `agent.yml` 未声明的工具调用。声明了 Bash 时，未开启 editing 的 Workspace 写入和在 Workspace 内运行无法分析的程序仍由通用访问策略硬拒绝。

AccessPlan 之后按 canonical 实际目标应用 Workspace mutation gate：

- KBASE Workspace read/glob/grep 在两种模式下均可用；
- 未开启 editing 的 KBASE Workspace write/edit 返回 `kbase_editing_mode_required`，且不会生成无法生效的 HITL；
- approval、`hostAccess`、`writeRoots`、`auto_approve` 和 `full_access` 都不能替代 `editingMode:true`；
- 当前 Chat 目录、其他 chatId 和 external 不受 KBASE Workspace gate 限制，继续服从实际 AccessPolicy 结果。

`ScopedFilePolicy` 不覆盖 AccessPlan，也不表达扩展名或编码限制。它只负责会话工具准入、KBASE Workspace canonical 路径识别、`WorkspaceMutationEnabled` 和 KBASE Workspace 特有的写入保护：

- KBASE Workspace 已有文件必须在同一有效观察范围内完整 `file_read` 后才能 `file_write/file_edit`；
- KBASE Workspace 新文件的父目录必须已存在；
- 写入继续使用 SHA/mtime/size 并发检测、大小限制、原子替换和 file history。

`file_glob/file_grep` 在所有获准目录使用相同的通用搜索规则，不对 Workspace 注入 `.md` 过滤。文件是否进入知识库由 `library.yml` 中 collection 的 `include/exclude` 和 extractor 独立决定；可编辑不等于可索引。

## 多目录授权与异步索引

Workspace 与 library 来源不要求包含或相等。专用 KBASE 的 Host Run 在启动时将 `editable:true` collection 的现存 canonical 路径冻结进 `ScopedFilePolicy.EditableCollectionRoots`，与 Workspace 取并集；相对路径仍基于 Workspace，collection 使用提示词列出的绝对目录。`description` 和 editable 开关均在下次 Run 生效，不重建索引。

该并集复用上文 editing 硬门禁、已有文件先读后写、新文件父目录已存在，以及 Bash 的写入/不透明执行检查；未开 editing 时即使 full_access 也不能写，不产生 HITL。readonly、平台保护目录和管理员 block 仍优先。符号链接以当前实际目标判定，不能借 collection 内的链接获得外部写授权，冻结根也不会因后续链接改向而跟随。Workspace 与 collection 重叠只产生一份有效范围，editable=false 不撤销已有 Workspace 权限。

库在 Run 中途改目录、开关或说明不修改当前 Run 的文件授权和提示词；检索工具仍检查当前库范围。普通 GENERAL/CODER 绑定同库不获得 collection 文件权限。容器沙箱不自动挂载 collection、不继承 Host collection 授权；现有 Workspace 权限保持原契约。

写入共享来源会影响所有绑定 Agent，由中心按 500ms 合并及增量策略异步维护。

写入成功不等于已进入索引；被 collection include/exclude 排除或 extractor 不支持的文件仍可保存。Agent 没有 kbase_refresh；自动维护失败由周期任务重试，也可在知识库中心手工刷新。源目录与 ChatsRoot/StateDir 的隔离由库校验负责，Workspace 继续遵守通用项目和文件权限校验。

权限回归入口：`internal/tools/kbase_editing_adversarial_test.go`、`internal/filetools/scoped_test.go`。
