# Memory Configuration

本文集中说明当前 runner 中所有与记忆系统直接相关的配置项，包括存储、检索、embedding、自动学习、LLM 总结和日志。

## 快速结论

最常用的几项是：

- `MEMORY_DIR`
- `AGENT_MEMORY_AUTO_REMEMBER_ENABLED`
- `AGENT_MEMORY_REMEMBER_MODEL_KEY`
- `AGENT_MEMORY_REMEMBER_TIMEOUT_MS`
- `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY`
- `AGENT_MEMORY_EMBEDDING_MODEL`
- `LOGGING_AGENT_MEMORY_ENABLED`
- `LOGGING_AGENT_MEMORY_FILE`

如果你希望“更新记忆时把当前内容和历史记忆一起发给大模型做筛选、总结、合并”，至少需要配置：

```bash
AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true
AGENT_MEMORY_REMEMBER_MODEL_KEY=<your-model-key>
AGENT_MEMORY_REMEMBER_TIMEOUT_MS=60000
```

其中 `AGENT_MEMORY_REMEMBER_MODEL_KEY` 填的是 model registry 里的 `model key`，不是裸模型名。

## 配置来源

默认值定义在：

- [internal/config/config.go](../internal/config/config.go)

环境变量装载在：

- [internal/config/config.go](../internal/config/config.go)

LLM 记忆总结器接入点在：

- [internal/app/app.go](../internal/app/app.go)
- [internal/memory/summarizer.go](../internal/memory/summarizer.go)

embedding 接入点在：

- [internal/app/app.go](../internal/app/app.go)
- [internal/memory/embedding.go](../internal/memory/embedding.go)

## 配置项总表

| 环境变量 | 默认值 | 作用 |
| --- | --- | --- |
| `MEMORY_DIR` | `runtime/memory` | 记忆存储根目录 |
| `AGENT_MEMORY_DB_FILE_NAME` | `memory.db` | SQLite 文件名 |
| `AGENT_MEMORY_CONTEXT_TOP_N` | `5` | query 时每层最多选取多少条记忆 |
| `AGENT_MEMORY_CONTEXT_MAX_CHARS` | `4000` | 注入 prompt 的 memory context 总字符预算 |
| `AGENT_MEMORY_SEARCH_DEFAULT_LIMIT` | `10` | `_memory_*` 工具默认返回条数 |
| `AGENT_MEMORY_HYBRID_VECTOR_WEIGHT` | `0.7` | 混合检索中向量分数权重 |
| `AGENT_MEMORY_HYBRID_FTS_WEIGHT` | `0.3` | 混合检索中 FTS 分数权重 |
| `AGENT_MEMORY_DUAL_WRITE_MARKDOWN` | `true` | 是否同时刷新 markdown 快照 |
| `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` | 空 | embedding provider key，空表示不启用 embedding |
| `AGENT_MEMORY_EMBEDDING_MODEL` | 空 | embedding 模型 ID；未设置时运行时会回退到 `text-embedding-3-small` |
| `AGENT_MEMORY_EMBEDDING_DIMENSION` | `1024` | embedding 维度 |
| `AGENT_MEMORY_EMBEDDING_TIMEOUT_MS` | `15000` | embedding 请求超时 |
| `AGENT_MEMORY_AUTO_REMEMBER_ENABLED` | `true` | run 完成后是否自动做 learn / auto-remember |
| `AGENT_MEMORY_REMEMBER_MODEL_KEY` | 空 | 用于记忆筛选/总结/合并的 LLM model key |
| `AGENT_MEMORY_REMEMBER_TIMEOUT_MS` | `60000` | 记忆总结 LLM 超时 |
| `LOGGING_AGENT_MEMORY_ENABLED` | `true` | 是否启用 memory 独立日志 |
| `LOGGING_AGENT_MEMORY_FILE` | `runtime/logs/memory.log` | memory 独立日志路径 |

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

### 2. Query 时的记忆注入

`AGENT_MEMORY_CONTEXT_TOP_N`

- query 构建上下文时，每层最多选多少条候选。
- 层包括 stable / session / observation。

`AGENT_MEMORY_CONTEXT_MAX_CHARS`

- 最终注入 prompt 的 memory context 字符预算。
- 超预算时会截断或提前停止披露。

`AGENT_MEMORY_SEARCH_DEFAULT_LIMIT`

- `_memory_search_`、`_memory_read_` 等工具的默认 limit。

### 3. embedding / 混合检索

`AGENT_MEMORY_EMBEDDING_PROVIDER_KEY`

- 指向 provider registry 中的 provider key。
- 不配置时，不启用 embedding，系统回退到 FTS / 子串匹配 + importance。

`AGENT_MEMORY_EMBEDDING_MODEL`

- embedding 模型 ID。
- 若为空，运行时默认会尝试 `text-embedding-3-small`。

`AGENT_MEMORY_EMBEDDING_DIMENSION`

- 向量维度。
- 需要和 embedding 模型实际维度匹配。

`AGENT_MEMORY_EMBEDDING_TIMEOUT_MS`

- embedding 请求超时。

`AGENT_MEMORY_HYBRID_VECTOR_WEIGHT`

- 混合检索里向量相似度的权重。

`AGENT_MEMORY_HYBRID_FTS_WEIGHT`

- 混合检索里 FTS 分数的权重。

### 4. 自动学习

`AGENT_MEMORY_AUTO_REMEMBER_ENABLED`

- 控制 run 结束后是否自动从 run trace 做 `Learn`。
- 开启后，系统会尝试把本轮结果沉淀为 observation 或后续可晋升的 fact。

### 5. LLM 记忆总结与合并

`AGENT_MEMORY_REMEMBER_MODEL_KEY`

- 这是记忆系统里“用于总结的模型”配置。
- 当它被配置后：
  - `remember` 不再直接把整段对话/assistant 输出写进记忆
  - `learn/auto-learn` 也不再只靠本地启发式摘要
  - 系统会把“当前内容 + 历史记忆”发给大模型
  - 由大模型判断哪些值得长期存储，并尽量输出合并后的记忆

`AGENT_MEMORY_REMEMBER_TIMEOUT_MS`

- 上述 LLM 总结流程的超时。

如果 `AGENT_MEMORY_REMEMBER_MODEL_KEY` 没配：

- 系统不会调用大模型做记忆总结
- 会回退到本地启发式逻辑

### 6. 日志

`LOGGING_AGENT_MEMORY_ENABLED`

- 是否单独记录 memory 操作日志。

`LOGGING_AGENT_MEMORY_FILE`

- memory 日志文件路径。
- 适合排查 write / learn / context build / feedback / debug 行为。

## 常见场景

### 只想启用基础记忆

```bash
MEMORY_DIR=runtime/memory
AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true
```

### 想让大模型参与记忆筛选、总结、合并

```bash
AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true
AGENT_MEMORY_REMEMBER_MODEL_KEY=gpt-4.1-mini
AGENT_MEMORY_REMEMBER_TIMEOUT_MS=60000
```

### 想启用 embedding 语义检索

```bash
AGENT_MEMORY_EMBEDDING_PROVIDER_KEY=openai
AGENT_MEMORY_EMBEDDING_MODEL=text-embedding-3-small
AGENT_MEMORY_EMBEDDING_DIMENSION=1536
AGENT_MEMORY_EMBEDDING_TIMEOUT_MS=15000
```

### 想排查 memory 行为

```bash
LOGGING_AGENT_MEMORY_ENABLED=true
LOGGING_AGENT_MEMORY_FILE=runtime/logs/memory.log
```

## 注意事项

1. `AGENT_MEMORY_REMEMBER_MODEL_KEY` 配的是 model registry key，不是 provider 原始模型名。
2. `AGENT_MEMORY_EMBEDDING_MODEL` 配的是模型 ID，不是 model registry key。
3. embedding 和 remember summarizer 是两条独立链路：
   - embedding 用于检索
   - remember model 用于总结/筛选/合并
4. 如果只配了 embedding，没有配 `AGENT_MEMORY_REMEMBER_MODEL_KEY`，记忆写入仍然不会经过大模型总结。
5. 旧的 `AGENT_MEMORY_STORAGE_DIR` 已废弃，不再生效。

## 相关文档

- [memory-system-design.md](./memory-system-design.md)
- [memory-evaluation.md](./memory-evaluation.md)
- [configuration-reference.md](./configuration-reference.md)
