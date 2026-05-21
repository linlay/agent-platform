# Memory Configuration

本文说明当前 runtime 中与记忆系统直接相关的配置入口。当前规则是：`.env` 只负责 memory 存储目录；agent 是否启用 memory、是否自动学习、使用哪个总结模型，全部放在 agent 配置；embedding 默认参数放在 provider 配置。

## 快速结论

`.env` 只保留：

```bash
MEMORY_DIR=runtime/memory
```

每个需要 runtime memory 的 agent 显式配置：

```yaml
memoryConfig:
  enabled: true
  managementTools: false
  embedding:
    providerKey: babelark
  autoRemember:
    enabled: true
    modelKey: minimax-m2_7-anthropic
    timeoutMs: 60000
```

provider 可选配置 embedding 默认值：

```yaml
memory:
  embedding:
    model: text-embedding-3-small
    dimension: 1536
    timeoutMs: 15000
```

旧的 `MEMORY_ENABLED`、`MEMORY_AUTO_REMEMBER_ENABLED`、`MEMORY_REMEMBER_*`、`MEMORY_EMBEDDING_*` 不再读取。

## 配置来源

默认值定义与环境变量装载：

- [internal/config/config.go](../internal/config/config.go)

agent 侧 `memoryConfig` 解析：

- [internal/catalog/agent_loader.go](../internal/catalog/agent_loader.go)

provider 侧 `memory.embedding` 解析：

- [internal/models/model_registry.go](../internal/models/model_registry.go)

运行时按 agent 选择 summarizer / embedder：

- [internal/app/app.go](../internal/app/app.go)
- [internal/memory/sqlite_store.go](../internal/memory/sqlite_store.go)

## 配置项总表

| 配置位置 | 字段 | 默认值 | 作用 |
| --- | --- | --- | --- |
| `.env` | `MEMORY_DIR` | `runtime/memory` | 记忆存储根目录 |
| env | `AGENT_MEMORY_DB_FILE_NAME` | `memory.db` | SQLite 文件名 |
| env | `AGENT_MEMORY_CONTEXT_TOP_N` | `5` | query 时每层最多选取多少条记忆 |
| env | `AGENT_MEMORY_CONTEXT_MAX_CHARS` | `4000` | 注入 prompt 的 memory context 总字符预算 |
| env | `AGENT_MEMORY_SEARCH_DEFAULT_LIMIT` | `10` | `memory_*` 工具默认返回条数 |
| env | `AGENT_MEMORY_HYBRID_VECTOR_WEIGHT` | `0.7` | 混合检索中向量分数权重 |
| env | `AGENT_MEMORY_HYBRID_FTS_WEIGHT` | `0.3` | 混合检索中 FTS 分数权重 |
| env | `AGENT_MEMORY_DUAL_WRITE_MARKDOWN` | `true` | 是否同时刷新 markdown 快照 |
| agent | `memoryConfig.enabled` | `false` | 该 agent 是否启用 runtime memory |
| agent | `memoryConfig.managementTools` | `false` | 是否额外注入 memory 管理工具 |
| agent | `memoryConfig.embedding.providerKey` | 空 | 指向 `REGISTRIES_DIR/providers/*.yml` 的 provider key |
| agent | `memoryConfig.embedding.model` | provider 默认值 | 覆盖 provider 的 embedding 模型 ID |
| agent | `memoryConfig.embedding.dimension` | provider 默认值 | 覆盖 provider 的 embedding 维度 |
| agent | `memoryConfig.embedding.timeoutMs` | provider 默认值或 `15000` | 覆盖 provider 的 embedding 超时 |
| agent | `memoryConfig.autoRemember.enabled` | `false` | run 完成后是否自动 learn / auto-remember |
| agent | `memoryConfig.autoRemember.modelKey` | 空 | 用于记忆筛选/总结/合并的 LLM model key |
| agent | `memoryConfig.autoRemember.timeoutMs` | `60000` | 记忆总结 LLM 超时 |
| provider | `memory.embedding.model` | 空 | provider 默认 embedding 模型 ID |
| provider | `memory.embedding.dimension` | `0` | provider 默认 embedding 维度 |
| provider | `memory.embedding.timeoutMs` | `0` | provider 默认 embedding 超时 |
| env | `LOGGING_MEMORY_ENABLED` | `true` | 是否启用 memory 独立日志 |
| env | `LOGGING_AGENT_MEMORY_FILE` | `runtime/logs/memory.log` | memory 独立日志路径 |

## 分组说明

### 1. 存储

`MEMORY_DIR`

- 记忆主目录。
- SQLite、导出快照、技能候选等内容都在这下面。

`AGENT_MEMORY_DB_FILE_NAME`

- SQLite 文件名。
- 最终路径通常是 `MEMORY_DIR/<db-file>`。

`AGENT_MEMORY_DUAL_WRITE_MARKDOWN`

- 控制是否同时刷新 markdown 快照导出。
- 这不会替代 SQLite，只是额外导出。

### 2. Agent memory 开关

`memoryConfig.enabled`

- 每个 agent 的 runtime memory 总开关。
- 不配置或为 `false` 时，不注入 memory context，也不注入 `memory_*` 基础工具。
- 为 `true` 时，注入 `memory_write`、`memory_read`、`memory_search`。

`memoryConfig.managementTools`

- 为 `true` 时额外注入 `memory_update`、`memory_forget`、`memory_timeline`、`memory_promote`、`memory_consolidate`。

### 3. Query 时的记忆注入

`AGENT_MEMORY_CONTEXT_TOP_N`

- query 构建上下文时，每层最多选多少条候选。
- 层包括 stable / session / observation。

`AGENT_MEMORY_CONTEXT_MAX_CHARS`

- 最终注入 prompt 的 memory context 字符预算。
- 超预算时会截断或提前停止披露。

`AGENT_MEMORY_SEARCH_DEFAULT_LIMIT`

- `memory_search`、`memory_read` 等工具的默认 limit。

### 4. embedding / 混合检索

provider 侧定义 embedding 默认值：

```yaml
memory:
  embedding:
    model: text-embedding-3-small
    dimension: 1536
    timeoutMs: 15000
```

agent 侧引用 provider：

```yaml
memoryConfig:
  embedding:
    providerKey: babelark
```

启用条件：

- agent 配置了 `memoryConfig.embedding.providerKey`
- provider 存在并配置了可用的 `baseUrl` / `apiKey`
- 最终解析后的 embedding `model` 非空
- 最终解析后的 embedding `dimension` 大于 0

如果 provider 没有 `memory.embedding`，或者 agent 没有配置 `providerKey`，系统不会中断启动；该 agent 的 memory 检索会回退到 FTS / 子串匹配 + importance。

agent 可覆盖 provider 默认值：

```yaml
memoryConfig:
  embedding:
    providerKey: babelark
    model: text-embedding-3-small
    dimension: 1536
    timeoutMs: 15000
```

`AGENT_MEMORY_HYBRID_VECTOR_WEIGHT` 与 `AGENT_MEMORY_HYBRID_FTS_WEIGHT` 仍控制混合检索中向量分数和 FTS 分数的权重。

### 5. 自动学习

`memoryConfig.autoRemember.enabled`

- 控制该 agent 的 run 结束后是否自动从 run trace 做 `Learn`。
- 开启后，系统会尝试把本轮结果沉淀为 observation 或后续可晋升的 fact。

### 6. LLM 记忆总结与合并

`memoryConfig.autoRemember.modelKey`

- 这是记忆系统里“用于总结的模型”配置。
- 填的是 `REGISTRIES_DIR/models/*.yml` 中的 model key，不是 provider 原始模型名。
- 当它被配置后：
  - `remember` 不再直接把整段对话/assistant 输出写进记忆
  - `learn/auto-learn` 也不再只靠本地启发式摘要
  - 系统会把“当前内容 + 历史记忆”发给大模型
  - 由大模型判断哪些值得长期存储，并尽量输出合并后的记忆

`memoryConfig.autoRemember.timeoutMs`

- 上述 LLM 总结流程的超时。
- 未设置时默认 `60000`。

如果 `modelKey` 没配：

- 系统不会调用大模型做记忆总结
- 会回退到本地启发式逻辑

### 7. 日志

`LOGGING_MEMORY_ENABLED`

- 是否单独记录 memory 操作日志。

`LOGGING_AGENT_MEMORY_FILE`

- memory 日志文件路径。
- 适合排查 write / learn / context build / feedback / debug 行为。

## 常见场景

### 只想启用基础记忆

```yaml
memoryConfig:
  enabled: true
```

### 想让大模型参与记忆筛选、总结、合并

```yaml
memoryConfig:
  enabled: true
  autoRemember:
    enabled: true
    modelKey: minimax-m2_7-anthropic
    timeoutMs: 60000
```

### 想启用 embedding 语义检索

provider：

```yaml
memory:
  embedding:
    model: text-embedding-3-small
    dimension: 1536
    timeoutMs: 15000
```

agent：

```yaml
memoryConfig:
  enabled: true
  embedding:
    providerKey: babelark
```

### 想排查 memory 行为

```bash
LOGGING_MEMORY_ENABLED=true
LOGGING_AGENT_MEMORY_FILE=runtime/logs/memory.log
```

## 注意事项

1. `memoryConfig.autoRemember.modelKey` 配的是 model registry key，不是 provider 原始模型名。
2. provider 的 `memory.embedding.model` 配的是 embedding 模型 ID，不是 model registry key。
3. embedding 和 remember summarizer 是两条独立链路：
   - embedding 用于检索
   - remember model 用于总结/筛选/合并
4. 如果只配了 embedding，没有配 `memoryConfig.autoRemember.modelKey`，记忆写入仍然不会经过大模型总结。
5. 旧的 `MEMORY_ENABLED`、`MEMORY_AUTO_REMEMBER_ENABLED`、`MEMORY_REMEMBER_*`、`MEMORY_EMBEDDING_*` 已删除，不再生效。
6. 旧的 `AGENT_MEMORY_STORAGE_DIR` 已废弃，不再生效。

## 相关文档

- [memory-system-design.md](./memory-system-design.md)
- [memory-evaluation.md](./memory-evaluation.md)
- [configuration-reference.md](./configuration-reference.md)
