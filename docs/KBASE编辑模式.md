# KBASE 编辑模式

## 范围

KBASE Editing 是专用 `mode: KBASE` 的单次 Source mutation 授权。专用 KBASE 无论是否开启 editing，都固定提供以下结构化文件工具：

```text
file_read file_glob file_grep file_write file_edit
```

专用 KBASE 的 Workspace 始终是最终解析后的 `kbaseConfig.source.root`。当前 Chat 目录始终是 `<chatsDir>/<chatId>`，只通过 `ChatAttachmentsDir` 和 `@chat` 暴露，不是 Workspace：

| 状态 | Workspace | Source Workspace | 当前 Chat 目录 |
|---|---|---|---|
| 未开启 editing | Source root | 可读，不可 mutation | 可读写 |
| 开启 editing | Source root | 可读写 | 可读写 |

`editingMode` 不控制文件工具是否存在，也不改变 Workspace；它只允许本 run 修改 Source Workspace。它不复用 CODER planning，不产生第二个 execute run。普通 Agent 附加的 KBASE capability、Team 和其他 mode 不支持该字段。

这些工具处理普通文本文件，不按知识库索引格式限制扩展名或编码。`.md`、`.txt`、`.json`、`.csv`、`.html` 以及其他可被通用文本工具识别的格式均可读写；支持的非 UTF-8 编码沿用通用检测、显式编码和写回保留规则。DOCX、PPTX、PDF、图片等二进制格式仍需格式专用工具。

删除、重命名、创建目录和 Bash 不在 editing 工具集中。source 新文件仍要求父目录已存在。

## 协议

```json
{
  "agentKey": "docs_kbase",
  "message": "更新 docs/policy.html 中的退款条款",
  "editingMode": true
}
```

- HTTP 和 WebSocket `/api/query` 使用同一顶层字段。
- `false` 或省略时 Source Workspace 保持只读，当前 Chat 目录仍可读写；`params.editingMode` 不生效。
- 开启时，live `request.query`、JSONL、replay、export 和运行中 `activeRun` 保留 `editingMode:true`。
- 授权不写 Agent 配置，也不在下一次 query 中继承。

main 与 editing run 分别使用 `kbase:main`、`kbase:editing` cache 和独立 prompt，但两者的工具 schema 完全相同。`QuerySession.WorkspaceRoot`、`RuntimeContext.LocalPaths.WorkspaceDir` 和文件工具默认 working directory 都冻结为最终 Source root；当前 Chat 目录只保存在 `ChatAttachmentsDir`。因此相对文件路径始终指向 Source Workspace，写 Chat 产物必须使用 prompt 提供的明确 `chat_dir` 路径。

## 目录权限

Source Workspace、当前 Chat 目录、其他 chatId 和外部目录统一先生成 AccessPlan：

```text
AccessPolicy -> AccessPlan -> HITL -> FileTools
```

- shipped `default` policy 通过 `@workspace` 允许 Source，通过 `@chat` 允许当前 Chat 目录。
- 其他 chatId 不享受 `@chat`，按外部目录重新计算策略。
- 外部读写可由 policy 直接 allow、自动批准、进入 HITL 或 block。
- `runtimeConfig.hostAccess.readRoots/writeRoots` 和 `full_access` 按通用规则生效。
- 管理员配置的真正 block 是最终决策，不生成无意义的 HITL。
- 请求中的路径分类字段不受信任；`..`、绝对路径和 symlink 都按 canonical 实际目标计算 AccessPolicy、approval fingerprint 和 source 分类。
- read approval 不能复用于 write/edit，其他目标或其他操作的 approval 也不能重放。
- 固定工具集在执行器入口再次校验，不能伪造 Bash 或未声明工具调用。

AccessPlan 之后按 canonical 实际目标应用 Source mutation gate：

- Source read/glob/grep 在两种模式下均可用；
- 未开启 editing 的 Source write/edit 返回 `kbase_editing_mode_required`，且不会生成无法生效的 HITL；
- approval、`hostAccess`、`writeRoots`、`auto_approve` 和 `full_access` 都不能替代 `editingMode:true`；
- 当前 Chat 目录、其他 chatId 和 external 不受 Source gate 限制，继续服从实际 AccessPolicy 结果。

`ScopedFilePolicy` 不覆盖 AccessPlan，也不表达扩展名或编码限制。它只负责固定工具准入、Source canonical 路径识别、`SourceMutationEnabled` 和 Source 特有的写入保护：

- Source 已有文件必须在同一有效观察范围内完整 `file_read` 后才能 `file_write/file_edit`；
- Source 新文件的父目录必须已存在；
- 写入继续使用 SHA/mtime/size 并发检测、大小限制、原子替换和 file history。

`file_glob/file_grep` 在所有获准目录使用相同的通用搜索规则，不对 Source 注入 `.md` 过滤。文件是否进入知识库由 `kbaseConfig.include/exclude` 和 extractor 独立决定；可编辑不等于可索引。

## 异步索引

文件工具写入成功只表示内容已经落盘。工具结果不包含 `kbase-index` hook，也不直接调用 KBASE refresh。

KBASE 自身的目录 watcher 监听 Source root，在 debounce 后按 canonical 相对路径 change set 执行 delta refresh，并应用 `include/exclude`、extractor、内容 hash 和现有 generation 规则。当前 Chat 目录、其他 chatId 和外部目录不在该 watcher 范围内。

因此：

- source 写入成功后可能存在短暂的“已保存、尚不可检索”窗口；
- 被索引配置排除或 extractor 不支持的文件仍可成功保存，但 watcher 会跳过；
- 不应因为写结果没有索引状态而自动调用 `kbase_refresh`；
- `kbase_refresh` 只在用户明确要求手工刷新，或需要从索引故障中恢复时使用。

## Source 与 Chats 分离

所有 `kbaseConfig.enabled:true` 的 Agent 在 Catalog 加载阶段都会校验 source root 与运行时 chats root。两者相等、互为父子或经 symlink 解析后实际重叠时：

- 该 Agent 不进入运行时 Agent catalog；
- 管理端保留 `invalid` 条目和 `invalid_kbase_source_overlap` 诊断；
- 引用它的 Team 会把该成员标记为不可用，因而不能正常启动；
- 其他 Agent 和平台进程继续运行。

修正目录后，Catalog 热重载会重新执行校验并恢复准入。该检查不依赖 `editingMode`，普通 Agent 挂载 KBASE capability 时同样适用。

权限对抗覆盖见 [KBASE 编辑模式越权对抗测试报告](KBASE编辑模式越权对抗测试报告.md)。
