# Memory System Design

## 这套系统到底在做什么

当前项目的"记忆系统"可以拆成三条独立能力：

1. 记住稳定信息
- 比如用户偏好、团队约定、项目规则
- 这些内容会在后续 query 时直接进入 prompt

2. 找回历史过程
- 比如上次是怎么排查的、某个 run 里调用了什么工具
- 这些内容不直接进 prompt，而是在需要时通过 session search 去查

3. 积累可复用的方法
- 比如一套排障流程、一套发布检查清单
- 这些内容先进入 skill candidate，作为未来成长为正式 skill 的候选

一句话说：

- `memory` 负责"长期记住什么"
- `session search` 负责"历史里发生过什么"
- `skill candidate` 负责"逐渐学会怎么做事"

## 用户能感知到什么

如果这套系统工作正常，用户能感知到的不是"数据库里多了几张表"，而是下面这些体验：

1. 不用重复说偏好
- 比如"回答简洁一点""先给结论再展开"

2. 不用重复说项目约定
- 比如"改 Go 代码前先看现有风格""合并前跑测试"

3. 它能分清"长期记忆"和"历史过程"
- 长期偏好应该被稳定记住
- 上次聊天里的具体步骤应该靠检索找回，而不是假装一直记着

4. 它会逐渐形成方法论
- 不是只记住事实
- 还会开始沉淀"这种问题通常怎么处理"

5. 它能理解语义相近的问题
- 当配置了 embedding 模型后，即使用户换了措辞提问，也能找到相关记忆
- 比如问"上次部署出了什么问题"能匹配到 summary 是"CI pipeline timeout on staging"的记忆

6. 它的记忆会自我进化
- 经常被引用的记忆会变得更重要
- 长期不被使用的记忆会逐渐淡化
- 每次 run 结束后系统会自动评估记忆的有效性

## 先看结论：有哪些层次的记忆

当前系统里，真正会影响回答行为的"记忆层次"可以分成六层。

### 1. 静态记忆

来源：

- agent 自带的 `memory/memory.md`

特点：

- 这是写死在 agent 定义里的静态背景
- 不来自运行时学习
- 更像"这个 agent 天生知道的背景信息"

如何被使用：

- 每次 query 都会直接放进系统 prompt
- 它排在 runtime memory 前面

适合放什么：

- agent 的长期背景
- 固定规则
- 不依赖具体用户或具体会话的说明

### 2. Stable Memory（稳定层）

来源：

- runtime memory 里的 `fact`

特点：

- 这是运行时真正的长期记忆
- 它会跨 query、跨 session 持续存在
- 只适合存稳定、可复用的信息

如何被使用：

- query 开始前，系统会调用 `BuildContextBundle(...)`
- 从 memory store 里挑出最相关的 `fact`
- 按有效重要度排序（考虑时间衰减和访问频率加成）
- 组装成 `Runtime Context: Stable Memory`
- 直接注入 prompt

适合放什么：

- 用户偏好
- 项目约定
- 团队规范
- 环境事实
- 已经多次验证过的稳定规则

### 3. Session Memory（会话层）

来源：

- runtime memory 里属于当前 chatID 的 `observation`

特点：

- 这是"当前会话里发生了什么"的短期记忆
- 只包含当前 chat 里的 observation
- 不会混入其他 chat 的内容

如何被使用：

- `BuildContextBundle(...)` 会把 observation 按 chatID 分流
- 当前 chat 的 observation → 组装成 `Runtime Context: Current Session`
- 注入 prompt，排在 Stable Memory 之后、Relevant Observations 之前

适合放什么：

- 本轮对话里上一个 run 学到的结论
- 当前会话中刚观察到的现象
- 只在这个会话上下文里才有意义的内容

### 4. Observation Memory（观察层）

来源：

- runtime memory 里不属于当前 chatID 的 `observation`
- 主要来自 `Learn(...)`、auto-learn 或工具写入

特点：

- 这是"其他会话里观察到的内容"
- 它不是永久事实
- 会被整理、归档、去重，必要时才晋升为 fact

如何被使用：

- `BuildContextBundle(...)` 从 store 里挑出与当前 query 相关的跨会话 observation
- 当配置了 embedding 模型时，使用混合语义检索（向量相似度 + 重要度）排序
- 未配置 embedding 时，使用子串匹配 + 重要度排序
- 组装成 `Runtime Context: Relevant Observations`
- 注入 prompt，排在 Session Memory 之后

适合放什么：

- 最近其他会话里学到的结论
- 某个问题在另一个会话里观察到的现象
- 暂时还不能当作长期规则的内容

### 5. Session Search

来源：

- chat/run 持久化数据

特点：

- 这不是长期记忆
- 它保存的是历史过程，而不是稳定结论

如何被使用：

- 当前不会在每次 query 自动注入 prompt
- 只有当 agent 或用户明确要查历史时，才通过：
  - `POST /api/session-search`
  - `_session_search_`
- 检索当前 chat 里的 query、message、event、submit 等内容

适合放什么：

- "上次你是怎么改这个文件的"
- "那次 run 调用了哪些工具"
- "之前某个审批步骤说了什么"

### 6. Skill Candidate

来源：

- `Learn(...)` 从 run trace 中提取 procedure-like 内容
- `_skill_candidate_write_` 显式写入

特点：

- 这不是直接用于回答的长期记忆
- 也不是历史回放
- 它是"未来可能成长为正式 skill 的候选"

如何被使用：

- 当前不会自动注入 prompt
- 主要通过：
  - `GET /api/skill-candidates`
  - `_skill_candidate_list_`
- 用于人工查看、后续筛选和升格

适合放什么：

- 排障流程
- 发布检查清单
- 一类问题的处理套路
- 可复用 workflow

## 不同层次的记忆是怎么一起工作的

最容易理解的方式，是看一次 query 发生时系统到底用了什么。

### 场景 1：普通 query

当用户发起一次 query 时，系统主要会用到四类内容：

1. 静态记忆
- 来自 agent 的 `memory/memory.md`

2. Stable Memory
- 来自 runtime memory 里的 `fact`

3. Current Session
- 来自 runtime memory 里当前 chatID 的 `observation`

4. Relevant Observations
- 来自 runtime memory 里其他 chatID 的 `observation`

实际注入 prompt 的顺序是：

1. 由 `agent.yml` 生成的 `Agent Identity`
2. `SOUL.md`
3. agent 静态 `memory/memory.md`
4. `Runtime Context: Stable Memory`（稳定层）
5. `Runtime Context: Current Session`（会话层）
6. `Runtime Context: Relevant Observations`（观察层）
7. stage prompt / tools appendix

也就是说：

- `Agent Identity` 负责提供当前 agent 的正式身份事实源
- 静态记忆负责给 agent 定底色
- stable memory 负责告诉它"长期应该记住什么"
- session memory 负责补充"当前会话里发生了什么"
- observation 负责补充"其他会话里最近学到了什么"

### 场景 2：需要回忆上次具体做过什么

这时不应该把全部历史都塞进长期记忆。

正确做法是：

1. 先正常使用 stable memory、session memory 和 observation 回答
2. 如果还需要具体过程，再主动调用 session search
3. 从 chat history 里找回相关片段

也就是说：

- `memory` 用来回答"我长期知道什么"
- `session memory` 用来回答"这轮对话里发生了什么"
- `session search` 用来回答"上次具体发生了什么"

### 场景 3：系统从一次 run 里学到新东西

当一个 run 完成后，系统会尝试学习。

可能出现三种结果：

1. 学到的是稳定结论
- 适合成为 `fact`

2. 学到的是近期观察
- 先写成 `observation`
- 后续再决定是否晋升为 `fact`

3. 学到的是一套方法
- 不直接塞进 memory
- 先写成 `skill candidate`

此外，run 完成后系统还会执行一个反馈循环：

4. 评估之前注入的记忆是否被引用
- 如果 assistant 回复中引用了某条记忆 → confidence +0.05
- 如果注入了但没被引用 → confidence -0.02
- 这使得有效记忆越来越可靠，无效记忆逐渐淡化

这就是当前系统最重要的设计边界：

- 事实进 memory
- 历史进 session search
- 方法进 skill candidate
- 有效性进 feedback loop

## 设计原则

### 1. 长期记忆只保存稳定信息

长期记忆适合保存：

- 用户偏好
- 团队/项目约定
- 环境事实
- 重复验证过的稳定规则

它不应该退化成：

- 聊天全文索引
- 调试流水账
- 临时经验碎片仓库

### 2. 历史过程和长期记忆必须分开

"之前怎么做的"不等于"以后都适用的规则"。

因此：

- 历史过程走 session search
- 稳定结论走 memory

### 3. 程序性成长必须单独成层

"会做某件事的方法"不是简单事实。

因此：

- procedure 先进入 skill candidate
- 不直接把 memory 变成技能库

### 4. 写入前先做安全拒绝

明显的危险内容不应入库：

- prompt injection
- secret-like 内容
- 不可见 Unicode

### 5. Observation 默认是可整理、可淘汰的

observation 不等于永久记忆。

它默认允许：

- stale archive
- duplicate merge
- promotion to fact
- supersede old fact

### 6. 记忆需要自我进化

静态的记忆库会逐渐失真。因此：

- 重要度（importance）不是写死的，而是动态计算的
- 访问频率高的记忆会被强化
- 长期未访问的记忆会自然衰减
- 每次 run 后的反馈循环会调整可信度（confidence）

## 当前总体架构

### 1. Runtime Memory

实现位置：

- [internal/memory/store.go](../internal/memory/store.go) — Store 接口 + FileStore 实现
- [internal/memory/sqlite_store.go](../internal/memory/sqlite_store.go) — SQLiteStore 生产实现
- [internal/memory/context_builder.go](../internal/memory/context_builder.go) — 上下文构建与披露逻辑
- [internal/memory/embedding.go](../internal/memory/embedding.go) — 向量嵌入服务
- [internal/memory/feedback.go](../internal/memory/feedback.go) — 反馈信号计算

职责：

- 保存 `fact` 和 `observation`
- 为 query 构建 runtime context（三层渐进式披露）
- 提供读写、搜索、时间线、整理、晋升等能力
- 在写入时生成向量嵌入（当配置了 embedding provider 时）
- 在 run 结束后计算反馈信号、调整记忆可信度

### 2. Session / Chat Search

实现位置：

- [internal/chat/search.go](../internal/chat/search.go)
- [internal/server/handler_chat_search.go](../internal/server/handler_chat_search.go)
- [internal/tools/tool_session_search.go](../internal/tools/tool_session_search.go)

职责：

- 搜索 chat/run 历史
- 找回 query、message、event、submit 等过程信息

### 3. Skill Candidate Lane

实现位置：

- [internal/skills/candidate_store.go](../internal/skills/candidate_store.go)
- [internal/skills/extractor.go](../internal/skills/extractor.go)
- [internal/server/handler_skill_candidates.go](../internal/server/handler_skill_candidates.go)
- [internal/tools/tool_skill_candidate.go](../internal/tools/tool_skill_candidate.go)

职责：

- 保存 procedure 候选
- 为后续成长为正式 skill 做准备

## Memory 数据模型

定义见 [internal/memory/types.go](../internal/memory/types.go) 和 [internal/api/types.go](../internal/api/types.go)。

### Kind

- `fact`
- `observation`

含义：

- `fact`：长期稳定、可反复复用
- `observation`：近期观察、暂时结论、待验证经验

### Scope

- `user`：只跟某个用户有关
- `agent`：某个 agent 自己的运行记忆
- `team`：团队共享约定
- `chat`：某个会话范围内有效
- `global`：跨 agent 的全局规则

### Status

- `active`：fact 默认状态
- `open`：observation 默认状态
- `superseded`：旧 fact 被替代后的状态
- `archived`：无效或过期的 observation
- `contested`：两条记忆存在冲突时的标记

### StoredMemoryResponse 核心字段

```
ID, RequestID, ChatID, AgentKey, SubjectKey
Kind (fact | observation), RefID
ScopeType, ScopeKey
Title, Summary, SourceType, Category
Importance (1-10), Confidence (0-1), Status
Tags, CreatedAt, UpdatedAt
AccessCount, LastAccessedAt
```

其中 `AccessCount` 和 `LastAccessedAt` 用于计算有效重要度。

## 存储实现

### SQLite 主存储

主实现位于 [internal/memory/sqlite_store.go](../internal/memory/sqlite_store.go)。

核心表：

| 表 | 用途 |
|---|------|
| `MEMORIES` | 统一投影表，方便读和搜；包含 `EMBEDDING_` blob、`ACCESS_COUNT_`、`LAST_ACCESSED_AT_` |
| `MEMORY_FACTS` | 长期事实，带 `DEDUPE_KEY_`、`EXPIRES_AT_`、`LAST_CONFIRMED_AT_` |
| `MEMORY_OBSERVATIONS` | 观察，带 `RUN_ID_`、`TYPE_`、`DETAIL_`、`FILES_JSON_`、`TOOLS_JSON_` |
| `MEMORY_LINKS` | 记忆间关系，如 `derived_from`、`supersedes` |
| `MEMORIES_FTS` | FTS5 全文搜索索引，自动通过 trigger 同步 |

### 搜索与检索

当前搜索有三种模式，按优先级递降：

1. **混合语义检索**（需配置 embedding provider）
   - 对 query 调用 `EmbedSingle(query)` 生成向量
   - 从 `MEMORIES.EMBEDDING_` 加载候选项的向量
   - 最终得分 = `vectorWeight × cosineSimilarity + ftsWeight × importanceNorm`
   - 权重默认：`vectorWeight=0.7, ftsWeight=0.3`

2. **FTS5 全文检索**
   - 使用 BM25 算法
   - 搜索 `SUMMARY_`、`SUBJECT_KEY_`、`CATEGORY_`、`TAGS_` 字段

3. **LIKE 子串匹配**（FTS 失败时回退）
   - 简单 `LIKE '%query%'` 匹配

### 向量嵌入

实现位置：[internal/memory/embedding.go](../internal/memory/embedding.go)

`EmbeddingProvider` 调用 OpenAI 兼容的 `/v1/embeddings` 端点。

**写入时嵌入**：`SQLiteStore.writeLocked()` 在每次写入后，如果 `embedder != nil`，会自动为 `Title + Summary` 生成向量并存入 `MEMORIES.EMBEDDING_`。

**查询时使用**：`BuildContextBundle()` 在构建上下文时，如果 embedder 可用且 query 非空，会：
1. 为 query 生成向量
2. 批量加载候选 observation 的向量
3. 计算混合得分并排序

**配置方式**：

```bash
# .env — provider key 对应 REGISTRIES_DIR/providers/*.yml 中的 key 字段
AGENT_MEMORY_EMBEDDING_PROVIDER_KEY=openai
AGENT_MEMORY_EMBEDDING_MODEL=text-embedding-3-small
AGENT_MEMORY_EMBEDDING_DIMENSION=1536
AGENT_MEMORY_EMBEDDING_TIMEOUT_MS=15000
```

启动时 `app.go` 会：
1. 从 `modelRegistry.GetProvider(providerKey)` 获取 `BaseURL` 和 `APIKey`
2. 创建 `EmbeddingProvider` 并通过 `SetEmbedder()` 注入 `SQLiteStore`
3. 如果 provider key 未设置或查找失败，混合检索自动降级为 FTS + importance 排序

**如果 embedding 未配置**：系统仍然正常工作，只是 observation 的选取基于子串匹配和静态重要度排序，无法做语义相似度检索。所有已写入记忆的 `EMBEDDING_` 列为 NULL。

### FileStore 兼容实现

[internal/memory/store.go](../internal/memory/store.go) 里保留了 `FileStore`，主要用于测试和轻量运行。

它和 SQLiteStore 共享接口，但不具备 FTS、embedding、access tracking 等能力。

### 导出文件

实现位置：

- [internal/memory/snapshot_renderer.go](../internal/memory/snapshot_renderer.go)
- [internal/memory/journal.go](../internal/memory/journal.go)

当前会生成：

- `snapshot/*.md`
- `exports/recent-observations.md`
- `exports/open-todos.md`
- markdown journal（`journal/YYYY-MM/YYYY-MM-DD.md`）

这些文件是导出视图，不是 query 时真正使用的事实源。

## Query 时怎么构建可用记忆

实现位置：

- [internal/server/handler_query.go](../internal/server/handler_query.go)
- [internal/memory/context_builder.go](../internal/memory/context_builder.go)

### 输入

当前 query 流程会调用 `Memory.BuildContextBundle(ContextRequest)`，输入包括：

| 字段 | 含义 | 默认值 |
|------|------|--------|
| `AgentKey` | 当前 agent | — |
| `TeamID` | 当前团队 | — |
| `ChatID` | 当前会话（用于区分 session 层 vs observation 层） | — |
| `UserKey` | 当前用户 | `_local_default` |
| `Query` | 用户输入文本（用于语义匹配） | — |
| `TopFacts` | 最多取几条 fact | 5 |
| `TopObs` | 最多取几条 observation | 5 |
| `MaxChars` | 总字符预算 | 4000 |
| `AvailableTokens` | 可用 token 数（如设置，覆盖 MaxChars，按 ×4 换算字符） | 0 |

### 输出

`ContextBundle` 包含：

| 字段 | 含义 |
|------|------|
| `StableFacts` | 选中的 fact 列表 |
| `SessionSummaries` | 选中的当前会话 observation 列表 |
| `RelevantObservations` | 选中的跨会话 observation 列表 |
| `StablePrompt` | 渲染好的 `Runtime Context: Stable Memory` 文本 |
| `SessionPrompt` | 渲染好的 `Runtime Context: Current Session` 文本 |
| `ObservationPrompt` | 渲染好的 `Runtime Context: Relevant Observations` 文本 |
| `DisclosedLayers` | 实际披露了哪些层（如 `["stable","session","observation"]`） |
| `StopReason` | 停止原因（`no_memory` / `stable_only` / `session_added` / `observation_added`） |
| `SnapshotID` | 内容哈希，用于审计追踪 |
| `CandidateCounts` | 各层候选数量 |
| `SelectedCounts` | 各层实际选中数量 |
| `Decisions` | 每层的披露决策（层名、选中 ID、原因） |

### 渐进式披露流程

```
1. 加载所有 items (listProjectionItemsLocked)
2. 按 scope / agent / status 过滤
3. 分流：
   - fact (status=active) → stable 层
   - observation (当前 chatID) → session 层
   - observation (其他 chatID) → observation 层
4. 排序：
   - stable 层：按 computeEffectiveImportance 降序
   - session 层：按 computeEffectiveImportance 降序
   - observation 层：
     - 如果 embedder 可用 → 混合语义得分排序
     - 否则 → 子串匹配 + computeEffectiveImportance 排序
5. 截断：各层按 topN 截断
6. 动态预算分配：
   - 先渲染各层完整文本
   - 如果总长 ≤ 预算 → 全部保留
   - 如果总长 > 预算 → 按比例分配，保证最低份额（stable 30%, session 20%）
7. 截断文本到各自预算
8. 记录披露决策
```

### 有效重要度计算

实现位置：[internal/memory/context_builder.go](../internal/memory/context_builder.go) `computeEffectiveImportance()`

```
effectiveImportance = baseImportance - timeDecay + accessBoost

其中：
- timeDecay = min(2.0, daysSinceLastAccess / 30 × 0.5)
  - 30 天未访问衰减 0.5，最多衰减 2.0
- accessBoost = min(2.0, accessCount × 0.1)
  - 每次访问增加 0.1，最多增加 2.0
- 下限兜底 = 1.0
```

这意味着：
- 一个 importance=7 的记忆，如果 90 天未访问 → 有效重要度降到 5.5
- 一个 importance=5 的记忆，如果被访问了 20 次 → 有效重要度升到 7.0
- 存储的 `Importance` 字段本身不变，有效重要度只在排序时动态计算

### Memory Usage Summary

每次 query 都会返回一个 `MemoryUsageSummary`，通过 SSE 事件传给前端。

包含字段：

| 字段 | 含义 |
|------|------|
| `hasStaticMemory` | 是否有静态记忆 |
| `stableCount` / `sessionCount` / `observationCount` | 各层选中数量 |
| `stableChars` / `sessionChars` / `observationChars` | 各层文本字符数 |
| `stableItems` / `sessionItems` / `observationItems` | 各层选中条目摘要 |
| `disclosedLayers` | 实际披露的层名列表 |
| `snapshotId` | 内容哈希 |
| `stopReason` | 停止原因 |
| `candidateCounts` / `selectedCounts` | 候选 vs 选中数量 |

## 写入路径

当前有四类主要写入路径。

### 1. Remember

入口：[internal/server/handler_memory.go](../internal/server/handler_memory.go)

流程：

- `POST /api/remember`
- `Chats.LoadChat(...)`
- `Memory.Remember(...)`

结果：

- 从 chat 里提炼摘要
- 直接写成 `fact`
- 如果 embedding provider 可用，自动生成向量嵌入

适合显式"把这件事记住"。

### 2. Memory Tools

入口：[internal/tools/tool_memory.go](../internal/tools/tool_memory.go)

当前工具：

| 工具 | 分类 | 用途 |
|------|------|------|
| `_memory_write_` | 基础集 | 写入新记忆 |
| `_memory_read_` | 基础集 | 读取单条记忆 |
| `_memory_search_` | 基础集 | 搜索记忆（FTS + 可选混合检索） |
| `_memory_update_` | 管理集 | 更新记忆字段 |
| `_memory_forget_` | 管理集 | 归档记忆 |
| `_memory_timeline_` | 管理集 | 查看记忆关系时间线 |
| `_memory_promote_` | 管理集 | 将 observation 晋升为 fact |
| `_memory_consolidate_` | 管理集 | 触发整理（归档 + 去重 + 晋升） |

是否自动注入这些工具，由 `memoryConfig` 控制，见 [internal/catalog/agent_loader.go](../internal/catalog/agent_loader.go)。

### 3. Learn

入口：[internal/server/handler_memory.go](../internal/server/handler_memory.go)

流程：

- `POST /api/learn`
- `Chats.LoadRunTrace(...)`
- `Memory.Learn(...)`

默认行为：

- 从 run trace 中提取 assistant 输出
- 写成 `observation`（写入时自动嵌入向量）
- 如果更像 procedure，则额外写一份 skill candidate
- 写完后自动做一轮轻量 consolidate

### 4. Auto Learn + Feedback

入口：[internal/server/memory_learning.go](../internal/server/memory_learning.go)

触发点：run completion 成功持久化后

流程：

```
persistRunCompletionIfNeeded
  → autoLearnIfEnabled
    → Memory.Learn(...)              # 提取 observation
    → applyMemoryFeedback(...)       # 反馈循环
      → BuildContextBundle(...)      # 重建上下文找出被披露的记忆
      → ComputeFeedback(...)         # 比对 assistant 文本，计算信号
      → ApplyFeedback(signals)       # 更新 confidence
```

反馈循环的逻辑：

- 对每条被披露的记忆，检查 assistant 回复中是否包含该记忆的关键词
- 被引用 → `confidence += 0.05`
- 未被引用 → `confidence -= 0.02`
- confidence 始终钳位在 `[0.1, 1.0]`
- 同时更新 `ACCESS_COUNT_` 和 `LAST_ACCESSED_AT_`

## 安全模型

实现位置：[internal/memory/safety.go](../internal/memory/safety.go)

当前安全策略分两层。

### 1. 写入前拒绝

`ValidateMemoryText(...)` 会拒绝：

- prompt injection 模式
- secret-like 内容（API key、密码、token）
- 不可见 Unicode 字符

对应错误：`ErrMemoryPromptInjection`、`ErrMemorySecretLeak`、`ErrMemoryInvalidUnicode`

### 2. 展示时净化

`sanitizeMemoryText(...)` 会对导出和 prompt 文本做轻量过滤，避免可疑内容原样出现在系统提示或快照里。

原则：

- 高风险内容不入库
- 低风险问题在展示层再净化

## Observation 生命周期

实现位置：

- [internal/memory/lifecycle.go](../internal/memory/lifecycle.go)
- [internal/memory/sqlite_store.go](../internal/memory/sqlite_store.go)

observation 不是永久记忆，系统会自动整理它。

### 1. 过期归档

- `observationTTL = 30 days`
- 超过 TTL 的旧 observation 会归档

### 2. 重复合并

- 通过 `category + normalized summary` 形成 fingerprint
- 重复 observation 只保留较新的一个
- 较旧的 observation 会归档

### 3. 晋升为 Fact

显式晋升：

- `_memory_promote_`

行为：

- 将 observation 提升为 fact
- 建立 `derived_from` link（SQLiteStore 会写入 `MEMORY_LINKS`）

自动晋升：

- 重复出现的 observation 可以自动晋升
- 更激进的 heuristic promotion 只在显式 `Consolidate(...)` 中运行
- 晋升条件：`importance >= 9 && confidence >= 0.75`，或特定 category（bugfix/workaround/preference/decision）且 `importance >= 8`

### 4. Fact 替代旧 Fact

新 fact 写入时，如果 dedupe key（`scopeType|scopeKey|normalizedTitle`）命中旧 fact：

- 旧 fact 标记为 `superseded`
- 建立 `supersedes` link

### 5. Learn 后自动轻量整理

`Learn(...)` 成功后会自动执行：

- stale archive
- duplicate merge
- duplicate-triggered promotion

更重的整理仍然通过 `_memory_consolidate_` 手动触发。

## Session Search 设计

实现位置：

- [internal/chat/search.go](../internal/chat/search.go)
- [internal/server/handler_chat_search.go](../internal/server/handler_chat_search.go)
- [internal/tools/tool_session_search.go](../internal/tools/tool_session_search.go)

当前提供：

- `POST /api/session-search`
- `_session_search_`

可检索内容包括：

- `query`
- step messages
- approval summaries
- events
- submit / answer

返回字段：`kind`、`chatId`、`runId`、`stage`、`role`、`timestamp`、`snippet`、`score`

## Skill Candidate 设计

实现位置：

- [internal/skills/candidate_store.go](../internal/skills/candidate_store.go)
- [internal/skills/extractor.go](../internal/skills/extractor.go)
- [internal/server/handler_skill_candidates.go](../internal/server/handler_skill_candidates.go)
- [internal/tools/tool_skill_candidate.go](../internal/tools/tool_skill_candidate.go)

### 为什么需要这一层

如果把下面这些内容都直接写进 memory，系统会很快失真：

- workflow
- 排障套路
- 发布检查清单
- 一类问题的通用处理方法

这些内容不是单次历史，也不完全是稳定 fact，更适合先进入 candidate lane。

### 当前能力

- 模型：`Candidate`、`CandidateInput`、`FileCandidateStore`
- 接口：`GET /api/skill-candidates`、`_skill_candidate_write_`、`_skill_candidate_list_`
- 自动来源：`Learn(...)` 会调用 `skills.CandidateFromRunTrace(...)`

### 当前限制

还没有：

- 自动生成正式 `SKILL.md`
- 自动写入 `skills-market`
- 完整的人工审阅和升格工作流

## API 与 Tool 概览

### Memory API

- `POST /api/remember`
- `POST /api/learn`

### Session API

- `POST /api/session-search`

### Skill Candidate API

- `GET /api/skill-candidates`

### Memory Tools

- `_memory_write_` / `_memory_read_` / `_memory_search_`
- `_memory_update_` / `_memory_forget_` / `_memory_timeline_`
- `_memory_promote_` / `_memory_consolidate_`

### Session Tool

- `_session_search_`

### Skill Candidate Tools

- `_skill_candidate_write_` / `_skill_candidate_list_`

## 配置参考

### 环境变量

| 变量 | 含义 | 默认值 |
|------|------|--------|
| `MEMORY_DIR` | 记忆存储根目录 | `./runtime/memory` |
| `AGENT_MEMORY_DB_FILE_NAME` | SQLite 文件名 | `memory.db` |
| `AGENT_MEMORY_CONTEXT_TOP_N` | 每层最多选几条 | `5` |
| `AGENT_MEMORY_CONTEXT_MAX_CHARS` | 总字符预算 | `4000` |
| `AGENT_MEMORY_SEARCH_DEFAULT_LIMIT` | 搜索默认返回数 | `10` |
| `AGENT_MEMORY_HYBRID_VECTOR_WEIGHT` | 混合检索向量权重 | `0.7` |
| `AGENT_MEMORY_HYBRID_FTS_WEIGHT` | 混合检索 FTS 权重 | `0.3` |
| `AGENT_MEMORY_DUAL_WRITE_MARKDOWN` | 是否同时写 markdown 快照 | `true` |
| `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` | embedding 服务的 provider key | 空（不启用） |
| `AGENT_MEMORY_EMBEDDING_MODEL` | embedding 模型 ID | `text-embedding-3-small` |
| `AGENT_MEMORY_EMBEDDING_DIMENSION` | 向量维度 | `1024` |
| `AGENT_MEMORY_EMBEDDING_TIMEOUT_MS` | embedding 请求超时 | `15000` |
| `AGENT_MEMORY_AUTO_REMEMBER_ENABLED` | 是否启用 auto-learn | `false` |

### 启用 Embedding 的前置条件

1. `REGISTRIES_DIR/providers/` 下有对应的 provider 配置文件（如 `openai.yml`），包含 `key`、`baseURL`、`apiKey`
2. `.env` 中设置 `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` 为该 provider 的 `key` 值
3. `.env` 中设置 `AGENT_MEMORY_EMBEDDING_MODEL` 为支持 embedding 的模型 ID

### 启动日志验证

如果 embedding 配置成功，启动日志会输出：

```
memory embedding provider ready (provider=openai model=text-embedding-3-small dim=1536)
```

如果 provider key 不存在，会输出：

```
[memory][embedding] provider "xxx" not found in model registry, hybrid search disabled: ...
```

如果 `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` 未设置，不会有任何 embedding 相关日志，系统静默回退到 FTS + importance 排序。

## 日志与观测

当前 memory 相关日志会写入 `memory.log`，实现见：

- [internal/memory/logging.go](../internal/memory/logging.go)
- [internal/tools/memory_logging.go](../internal/tools/memory_logging.go)
- [internal/llm/logging.go](../internal/llm/logging.go)

重点事件包括：

| 事件 | 含义 |
|------|------|
| `write` | 记忆写入 |
| `write_rejected` | 写入被安全校验拒绝 |
| `learn` | auto-learn 完成 |
| `promote` | observation 晋升为 fact |
| `consolidate` | 整理完成（归档 / 去重 / 晋升数） |
| `build_context_bundle` | 上下文构建完成（各层数量、是否使用 hybrid） |
| `apply_feedback` | 反馈循环执行（信号数量） |
| `tool_invocation` | 工具调用 |
| `llm_prompt_memory` | prompt 中的记忆内容 |
| `auto_learn` | 自动学习触发 |

## 和 Hermes Agent 的主要差异

| 维度 | Hermes Agent | 本项目 |
|------|-------------|--------|
| 存储 | 文件式（MEMORY.md + USER.md，硬字符上限） | SQLite + FTS5 + 可选向量嵌入 |
| 作用域 | 单 agent，全局两个文件 | 多 agent / 多 scope（user/agent/team/chat/global） |
| 检索 | frozen snapshot，每次会话启动时全量加载 | 每次 query 动态构建，支持语义检索 |
| 上下文管理 | protect_first_n + protect_last_n 压缩策略 | 三层渐进式披露 + 动态预算分配 |
| 记忆进化 | 无自动进化 | 有效重要度衰减/强化 + 反馈循环 + 生命周期整理 |
| 安全 | 注入检测 + 凭据检测 | 同上 + 不可见 Unicode 检测 |
| 分层 | 内置记忆 + 外部 provider（可选） | 静态记忆 + stable + session + observation + session search + skill candidate |

可以简单理解成：

- Hermes 更强调"怎么把长期记忆管住"
- 本项目更强调"怎么把长期记忆、会话记忆、历史检索、程序性成长拆开，并且让记忆自我进化"

## 当前局限

1. **还没有 frozen snapshot**
- 当前是每次 query 动态构建 memory bundle
- 如果同一个 session 内记忆发生变化，prompt 也会变化

2. **skill candidate 还不能正式升格为 skill**
- 现在只有候选写入和查询

3. **反馈循环基于关键词启发式**
- `ComputeFeedback` 使用子串匹配判断 assistant 是否引用了记忆
- 不是语义级别的判断，可能有误判

4. **embedding 需要额外网络调用**
- 每次写入和每次 query 都需要调用 embedding API
- 如果 embedding 服务不可用，会导致写入变慢（虽然不会失败）

5. **用户可见性还不够**
- 后端已经能区分不同层次的记忆
- 但前端还没有很好展示"本轮到底用了哪层记忆"

## 下一步最值得做什么

1. **做 memory/session/candidate 面板**
- 让用户能看见系统到底记住了什么

2. **做 session-start frozen snapshot**
- 让一个 session 内的长期记忆更稳定

3. **打通 candidate 到正式 skill 的升格流程**
- 把"学到的方法"真正变成可复用能力

4. **增强反馈循环的语义判断**
- 用 embedding 相似度替代关键词匹配来判断记忆是否被引用

5. **增加 embedding 回填机制**
- 对已有但缺少向量的记忆进行批量回填
- 避免新启用 embedding 后历史记忆无法参与语义检索
