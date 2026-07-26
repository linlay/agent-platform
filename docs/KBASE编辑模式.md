# KBASE 编辑模式

## 范围

KBASE Editing v1 是专用 `mode: KBASE` 的单次 run 授权。默认 KBASE 仍只读；请求顶层显式传 `editingMode:true` 后，本 run 可以读取、查找、新建和修改 `kbaseConfig.source.root` 内的 UTF-8 `.md`。它不复用 CODER planning，不产生 confirmation 或第二个 execute run。

v1 不支持 `.markdown`、删除、重命名、创建目录、Bash、批量事务或 DOCX/PPTX/PDF 写入。清空 `.md` 内容属于修改并保留文件历史。

## 协议

```json
{
  "agentKey": "docs_kbase",
  "message": "更新 docs/policy.md 中的退款条款",
  "editingMode": true
}
```

- HTTP 和 WebSocket `/api/query` 使用同一顶层字段。
- 只对专用 `mode: KBASE` 生效。普通 Agent 附加 KBASE capability、Team 和其他 mode 返回 `400 editing_mode_unsupported`。
- `false` 或省略保持只读；`params.editingMode` 不生效。
- 开启时，live `request.query`、JSONL、replay、export 和运行中 `activeRun` 保留 `editingMode:true`；false 时省略。
- 授权不写 Agent 配置，不在新 chat 或下一次 query 中继承。

WebClient 源码不在本仓库。客户端接入时只在 `agent.mode === "KBASE"` 展示默认关闭的“编辑知识库”开关，把其当前值写入本次 query 顶层；提交后立即重置。运行中从 `activeRun.editingMode` 恢复 badge。不要向 Agent `controls` 或 `meta` 注入隐式控制项。

## Session 与工具

editing run 使用独立 `kbase-editing` stage、`kbase:editing` cache 和 editing prompt。工具集固定为：

```text
kbase_search kbase_files kbase_read kbase_status kbase_refresh datetime
file_read file_glob file_grep file_write file_edit
```

`QuerySession` 冻结 `EditingMode`、`KBaseSourceRoot` 和 `ScopedFilePolicy`。workspace/working directory 绑定唯一事实源 `kbaseConfig.source.root`，不使用 chat 目录或可选 `runtimeConfig.workspaceRoot`。`/api/file` 和 `/api/agent/open-directory` 对专用 KBASE 同样使用 source root。

## 硬安全边界

每个文件工具先执行管理员 `AccessPolicy`，再执行不可扩大的 `ScopedFilePolicy`：

- canonical target 必须留在 canonical source root 内；`..`、绝对越界与 symlink escape 被拒绝。
- 扩展名大小写不敏感，但只允许 `.md`。
- 新文件只能位于已存在目录，`file_write` 不隐式建目录。
- 新旧文件都必须是 UTF-8。
- 已有文件必须完整读后再写，并复用 SHA/mtime/size 并发检测、大小限制、原子替换和 run-scoped file history。
- glob/grep 的遍历与结果限制为 source root 内 `.md`。
- 固定工具集在执行器入口再次校验，不能伪造 Bash 或未声明工具调用。

`full_access`、hostAccess 与 HITL approval 不能扩大上述边界；管理员显式 block 仍有效。shipped 默认 policy 下，source 内合法 `.md` mutation 不逐次 HITL。

典型错误码：

```text
kbase_editing_mode_required
kbase_editing_path_outside_source
kbase_editing_extension_unsupported
kbase_editing_parent_missing
kbase_editing_encoding_unsupported
kbase_editing_tool_unsupported
```

## 写后索引

文件原子写入成功后，KBASE Manager hook 同步调用现有 refresh coordinator：

```text
RefreshOptions{Mode:"editing", Scope:"delta", Paths:[relativeSourcePath]}
```

有 active generation 时为目标路径 delta；首次索引或 `indexHash` 变化沿用现有 rebuild。watcher 继续作为崩溃、取消和外部修改的兜底，并依赖 hash 去重。

工具结果示例：

```json
{
  "status": "edited",
  "filePath": "/knowledge/docs/policy.md",
  "lineStats": {
    "addedLines": 1,
    "deletedLines": 1,
    "editedLines": 1
  },
  "hooks": [
    {
      "name": "kbase-index",
      "status": "success",
      "filePath": "docs/policy.md",
      "data": {
        "scope": "delta",
        "changedFiles": 1,
        "indexedChunks": 4
      }
    }
  ]
}
```

文件写入失败时不调用 hook。文件已保存但索引失败时不回滚，mutation 顶层 status 仍表示写入成功，hook 单独返回 `failed`，capability 为 degraded；Agent 应明确告知并至多调用一次 `kbase_refresh`。路径被 include/exclude 排除时文件仍保存，hook 返回 `skipped` 和 `excluded_by_kbase_config`，不能声称可检索。

日志只记录 agent/chat/run、相对路径、前后 SHA、实际 scope 和 hook status，不记录正文。本功能不增加数据库 schema 或配置迁移。

## 后续格式

后续继续复用 `editingMode` 和 `ScopedFilePolicy`，但 DOCX/PPTX/PDF 必须使用格式专用工具和校验；不会直接向二进制文档开放通用 `file_write/file_edit`。
