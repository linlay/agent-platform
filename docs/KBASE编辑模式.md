# Workspace 编辑开关

## 范围与默认值

`editingMode` 是 GENERAL/CODER/KBASE/TEAM 共用的单次 Workspace 编辑开关，显式 `true` 或 `false` 不按 mode 限制。mode 仅提供省略字段时的模板默认值：KBASE 为 false，其他三种为 true。

该开关只控制最终 canonical `runtimeConfig.workspaceRoot`。当前 Chat 目录独立保存在 `ChatDir`，沿用 `@chat` 权限；其他目录按 Agent `runtimeConfig.hostAccess`、AccessPolicy 与 HITL 判断。库绑定和选库不授予来源目录文件权限。

```json
{"agentKey":"docs","message":"更新 Workspace 中的文档","editingMode":true}
```

HTTP 和 WebSocket query 使用同一顶层 boolean。`params.editingMode` 无效；本次设置不写 Agent 配置、不从历史 Chat 继承。规划 Run 始终只读，批准后的普通执行 Run 使用原请求的编辑设置。

Run 会冻结解析后的 Workspace 编辑能力。`request.query` 持久化按模板解析后的请求值（含 false），用于同进程续接和批准计划后的执行；JSONL、replay/export 与恢复不会把显式 false 当成省略。`activeRun.editingMode` 返回当前 Run 的实际能力，规划 Run 为 false。Platform 重启统一取消未完成 Run；下一次 Query 使用新请求与当前模板默认值。

## 目录权限

文件工具和 Bash 共用 `AccessPolicy -> AccessPlan -> HITL` 路径检查。所有 mode 的显式 false 都硬拒绝 Workspace mutation，即使 full_access、hostAccess、writeRoots 或已有 approval 也不能放宽；Workspace 内无法分析的程序也拒绝执行，Bash 默认工作目录移到当前 Chat。

显式 true 允许按通用策略编辑 Workspace，不补工具。readonly roots、平台保护路径与管理员 block 仍优先；Git 可执行配置、破坏性操作、远端 mutation 与脚本执行审批保持独立。路径按 canonical 实际目标检查，Workspace 内符号链接不授予外部目标权限。

来源位于 Workspace 内时按 Workspace 规则检查；位于外部时是普通 external，不受 Workspace editing gate 控制。容器的目录挂载仍需要显式配置。

## 知识库与文件权限的边界

`library.yml` 的 collection 只定义来源和索引范围。`collections[].editable`、Run collection roots 和 KBASE 专属 collection 授权均已删除；旧字段在 YAML、HTTP 和管理工具输入中拒绝，升级时应从库配置移除。

`description` 是集合元数据；include/exclude、chunk、textEncoding、models 和 retrieval 均不授予文件权限。读取知识库索引与已发布来源回读沿用知识工具协议，也不授予普通 file/Bash 读写范围。

已有目录授权下修改共享来源会影响共享该库的后续检索，库 watcher 异步维护索引。写入成功不等于已进入索引，被 include/exclude 排除或 extractor 不支持的文件仍可能保存但不索引。

## 仍待审查的 mode 特殊行为

KBASE 原有的 `ScopedFilePolicy`（强制先读后写、新文件父目录必须存在、专用错误码、专属工具集准入）已移除，文件工具行为与其他 mode 完全一致。editingMode 只是本次 Run 的编辑状态，不是一种运行形态：KBASE 不再有 `kbase-editing` stage 与 `kbase:editing` cache key，`agent-prompt.yml` 的 `kbase.editing-prompt` 已删除（出现即拒绝加载，相关要求并入 `workspace-prompt`），四种 mode 的 system-init fingerprint 都不含 editingMode；editing_mode 模板变量也已删除，配置出现即拒绝加载。切换编辑不改变 system prompt、cache key 或 fingerprint；模型通过被拒绝的写入得知当前只读。历史 JSONL 中的 `kbase:editing` 快照不迁移，仍可按引用回放。

权限回归入口：`internal/runtime/session/workspace_editing_test.go`、`internal/accesspolicy/capability_test.go`、`internal/tools/kbase_editing_adversarial_test.go`。
