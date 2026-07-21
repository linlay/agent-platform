# KBASE LanceDB 检索与控制面

KBASE 已是 mode 中立的公共能力，`internal/kbase` 同时服务专用 `mode: KBASE` 和通过 `kbaseConfig.enabled: true` 挂载知识库的普通 Agent。它固定使用 LanceDB 作为唯一的 chunk、全文和向量检索库。首次索引、`force=true` 或 `indexHash` 变化时构建独立 generation，完成索引和校验后由 `control.db` 原子切换 active generation；日常文件变化直接增量更新 active generation，旧 generation 可用于内部 generation rollback。

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

`control.db` 使用当前 `application_id` 和 `user_version` 契约，且 `KBASE_META.schemaVersion` 必须为 `4`。只在进程启动装配期，标记恰为 `0/0` 的既有数据库才会先接受完整语义校验（表、列类型/NULL/default/主键、外键、索引、触发器和 FTS；忽略列物理顺序），随后在事务中写入当前标记。运行期的 refresh、search、status、热重载和只读打开只验证，绝不认领或改写。结构不匹配（例如 `KBASE_MIGRATIONS` 或 `MIGRATED_CHUNKS`）或 `schemaVersion` 不匹配时不会迁移、删除或备份数据。若 `control.db` 缺失但 storageDir 中存在 `kbase.db`、其 WAL/SHM 文件、根 `manifest.json` 或旧 generation，也按相同策略拒绝。

拒绝存储后的 Agent 策略取决于 requirement：专用 `mode: KBASE` 为 required，对应 Agent 会被隔离为管理端 `invalid_kbase_storage`，引用它的 Team 也不能运行；普通 Agent 的附加能力为 optional，Agent 保持可运行，KBASE status 报告 `degraded: true`、`stale: true` 与诊断，search/files/read 返回 unavailable。成功 refresh 后会清除 optional 的启动降级状态。

## 索引与恢复

首次 refresh 和 `force: true` 都创建新的蓝绿 generation。普通手工 refresh、启动 refresh 与周期 reconcile 会扫描 `kbaseConfig.source.root` 做全量文件对账，但只修改新增、变化或删除的文件；watcher refresh 直接消费 debounce 后的相对路径 change set，不遍历无关目录。事件溢出或待处理路径超过 1000 时自动退化为完整 reconcile。

新增文件执行抽取、切块和 embedding；修改文件先比较文件 SHA 与 chunk set，内容未变时只更新控制面元数据，chunk 变化时从 active generation 读取同文件旧向量并按 `contentHash + embedding model + dimension` 复用；删除文件执行幂等 `delete-file`。删除 tombstone 默认保留 `maintenance.version-retention` 后清理。每次跨 Lance/control 写入都先记录 file operation，重启后的 refresh 会验证并完成可确认的操作。

active generation 的日常变化不再调用 `indexes/build`。Lance 默认查询会覆盖尚未进入索引的行，删除行由 deletion index 屏蔽；累计新增/修改 chunk 达到 `optimize-change-threshold` 或未索引比例超过 10% 时，sidecar 只执行 delta index refresh。完整 compact/index merge/prune 由非 watcher 的周期维护按 `optimize-interval` 执行。

search 在没有 active generation 时返回 `stale: true` 并触发 refresh；sidecar 不可用时返回 unavailable，绝不回退 SQLite。`kbase_files` 也只读取 active generation 的控制面文件记录，默认只展示 active 文件；deleted tombstone 需显式指定 deleted/all 状态才可见。

## 配置与探活

`configs/kbase-settings.yml` 不再支持 `storage.engine` 或任何 `migration.*` 键；出现这两类键会使启动配置加载失败。存在 enabled capability 时 runtime 会启动并检查 Lance sidecar 的私有握手。若至少有一个 required capability，sidecar 故障使 `/healthz` 返回 `503`；只有 optional capability 时 `/healthz` 仍返回 `200`，同时在 `kbase` 状态中报告 `degraded` 和错误。所有知识库操作都显式返回 unavailable，绝不回退 SQLite。

KBASE 仍只检索从文本格式和文档抽取出的文本；chat、memory 及 KBASE 控制面的 SQLite 使用不受此变更影响。
