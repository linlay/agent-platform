# KBASE LanceDB 检索与控制面

KBASE 固定使用 LanceDB 作为唯一的 chunk、全文和向量检索库。每次索引都构建独立 generation，完成索引和校验后由 `control.db` 原子切换 active generation；旧 generation 可用于内部 generation rollback。

`control.db` 是 SQLite 控制面，不保存 chunk 正文、FTS 倒排或 embedding。它只保存：

- `KBASE_META`：active generation 和当前索引元数据；
- `KBASE_FILES`：文件抽取、解析、hash、状态和 chunk 计数；
- `KBASE_GENERATIONS`：generation 生命周期与统计；
- `KBASE_FILE_OPS`：Lance 写入与控制面提交之间的崩溃恢复日志；
- `KBASE_INDEX_RUNS`：refresh/rebuild 的运行记录。

## 运行目录

```text
<storageDir>/
  control.db
  generations/
    <generationId>/
      lance/
      manifest.json
```

旧运行目录可能仍有 `kbase.db`、其 WAL/SHM 文件或根 `manifest.json`。当前版本永不打开、迁移、删除或更新这些遗留文件；若 `control.db` 没有 active Lance generation，refresh 只会从 workspace 冷建新的 Lance generation。

## 索引与恢复

首次 refresh 和 `force: true` 都创建新的蓝绿 generation。普通 refresh 对 active generation 仅处理变化或删除的文件；每次跨 Lance/control 写入都先记录 file operation，重启后的 refresh 会验证并完成可确认的操作。优化和 generation rollback 都只操作 Lance generation 与控制面元数据。

search 在没有 active generation 时返回 `stale: true` 并触发 refresh；sidecar 不可用时返回 unavailable，绝不回退 SQLite。`kbase_files` 也只读取 active generation 的控制面文件记录。

## 配置与探活

`configs/kbase-settings.yml` 不再支持 `storage.engine` 或任何 `migration.*` 键；出现这两类键会使启动配置加载失败。Lance sidecar 对所有存在 KBASE agent 的部署都是必需的，`/healthz` 会检查其私有握手。

KBASE 仍只检索从文本格式和文档抽取出的文本；chat、memory 及 KBASE 控制面的 SQLite 使用不受此变更影响。
