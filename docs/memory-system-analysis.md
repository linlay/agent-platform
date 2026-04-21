# agent-platform 记忆系统实现分析

本文面向第一次接触这个项目的人，目标不是重复源码注释，而是把这套记忆系统的设计意图、实现路径和边界讲清楚。分析基于当前仓库实现，以及 2026-04-21 查阅到的 Hermes Agent 与 Claude Code 官方文档。

相关代码入口：

- [README.md](/Users/joe/gtja/linlay/agent-platform/README.md)
- [CLAUDE.md](/Users/joe/gtja/linlay/agent-platform/CLAUDE.md)
- [internal/app/app.go](/Users/joe/gtja/linlay/agent-platform/internal/app/app.go)
- [internal/server/handler_query.go](/Users/joe/gtja/linlay/agent-platform/internal/server/handler_query.go)
- [internal/server/memory_learning.go](/Users/joe/gtja/linlay/agent-platform/internal/server/memory_learning.go)
- [internal/memory/store.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/store.go)
- [internal/memory/sqlite_store.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/sqlite_store.go)
- [internal/memory/context_builder.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/context_builder.go)
- [internal/memory/lifecycle.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/lifecycle.go)
- [internal/memory/extractor.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/extractor.go)
- [internal/memory/feedback.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/feedback.go)
- [internal/chat/search.go](/Users/joe/gtja/linlay/agent-platform/internal/chat/search.go)
- [internal/tools/tool_memory.go](/Users/joe/gtja/linlay/agent-platform/internal/tools/tool_memory.go)
- [internal/tools/tool_session_search.go](/Users/joe/gtja/linlay/agent-platform/internal/tools/tool_session_search.go)

外部参考：

- Claude Code 官方文档: https://code.claude.com/docs/en/memory
- Hermes Agent 官方文档: https://hermes-agent.nousresearch.com/docs/user-guide/features/memory/

## 1. 项目介绍

### 1.1 这个项目是什么

`agent-platform-runner-go` 是一个 Go 版 agent runtime执行层：

- 目录驱动的 agent / team / skill / tool 注册系统
- OpenAI 兼容模型调用与多阶段 prompt 组装
- chat 历史落盘与流式事件追踪
- 内建 memory、session search、skill candidate 能力
- 工具调用、沙箱执行、审批与运行态管理

可以把它理解成：

- `catalog` 决定“有哪些 agent、技能、工具”
- `server + llm` 决定“一次 query 怎么执行”
- `chat` 决定“过程怎么保存”
- `memory` 决定“什么值得跨轮次记住”

这也是它和很多“只做 prompt + tool loop”的轻量 agent shell 最大的不同：它把“运行过程”和“长期积累”拆成了不同的子系统，而不是把所有历史一股脑塞回上下文。

### 1.2 一次请求在系统里怎么走

用户调用 `POST /api/query` 后，主链路大致是：

1. `internal/server/handler_query.go` 校验请求、补齐 `runId` / `requestId` / `chatId`。
2. chat store 确保当前 chat 存在，并取回历史消息。
3. 如果配置了 memory，调用 `BuildContextBundle(...)` 检索和本次问题相关的记忆上下文。
4. `internal/llm/prompt_builder.go` 把系统提示、静态 memory、运行时 memory、stage prompt、tool appendix 拼成最终 prompt。
5. 模型流式执行，期间 chat 事件和消息持续落盘。
6. run 结束后，`internal/server/memory_learning.go` 触发自动学习和反馈更新。

所以这个项目里的“记忆系统”不是一个独立外挂，而是 query 主流程的前置上下文构建器，加上 run 完成后的后置学习器。

### 1.3 这个项目为什么要自己做一套 memory

从代码看，这个项目解决的不是“模型完全没记忆”这么简单，而是三个更具体的问题：

1. 长期稳定信息不能和聊天流水混在一起。
2. 当前会话的重要观察，应该比几周前的旧观察更容易被用到。
3. 不是所有学到的内容都该直接升级成长期规则。

因此它没有采用“一个 `MEMORY.md` 全包”的方案，而是做成了六层结构：

- 静态记忆：agent 自带 `memory/memory.md`
- Stable Memory：长期事实 `fact`
- Session Memory：当前 chat 的 observation
- Observation Memory：其他 chat 的 observation
- Session Search：查历史过程
- Skill Candidate：沉淀可复用方法，但不直接入 prompt

这六层里，真正自动进 prompt 的只有前四层。

## 2. 这套项目记忆系统是怎么实现的

### 2.1 总体设计思路

一句话概括：

> 它不是“把所有历史存起来”，而是“把长期事实、短期观察、历史过程、方法候选分开存，再按场景分别使用”。

对应到代码层面，有四条主链路：

1. 写入链路：`remember`、`learn`、memory tool 显式写入。
2. 检索链路：query 前按层次检索并构建 prompt。
3. 演化链路：observation 去重、归档、晋升为 fact。
4. 反馈链路：run 完成后根据回答是否引用了记忆，回写 confidence 和访问计数。

### 2.2 存储层：为什么是 SQLite + 投影表

生产实现是 [internal/memory/sqlite_store.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/sqlite_store.go) 里的 `SQLiteStore`。

核心表有四张：

- `MEMORIES`: 统一投影表，query 检索主要看这张表
- `MEMORY_FACTS`: 长期事实源表
- `MEMORY_OBSERVATIONS`: 观察源表
- `MEMORY_LINKS`: 记忆关系表，记录 `derived_from`、`supersedes` 等关系

还有一个 FTS5 虚表：

- `MEMORIES_FTS`: 给统一投影表做全文检索

这说明作者不是只把 SQLite 当 key-value 用，而是在为后续能力留扩展位：

- 统一查询走投影表，避免每次联表判断 fact / observation
- 源表保留不同生命周期语义
- 链接表支持“由哪条 observation 晋升而来”“哪条 fact 被谁取代”

这比“所有内容只是一堆 markdown 条目”更接近真正的 runtime memory store。

### 2.3 数据模型：它把“记什么”分成两类

在 [internal/memory/types.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/types.go) 里，最核心的类型区分只有两个：

- `fact`
- `observation`

它们的默认状态不同：

- `fact` 默认 `active`
- `observation` 默认 `open`

这背后的设计非常关键：

- `fact` 代表“系统愿意长期相信并直接拿来指导回答的内容”
- `observation` 代表“这是一条观察、经验或过程结论，但还没完全被升格为稳定规则”

这一步其实就是整套系统的分水岭。很多 memory 系统失败，不是不会存，而是没有分清“稳定事实”和“暂时观察”。

### 2.4 作用域设计：不是所有记忆都给所有人用

scope 在 [internal/memory/types.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/types.go) 里定义为：

- `user`
- `agent`
- `team`
- `chat`
- `global`

`normalizeScopeKey(...)` 会把逻辑作用域归一化成具体 key，例如：

- `user:<userKey>`
- `team:<teamID>`
- `chat:<chatID>`
- `agent:<agentKey>`

这说明它天然支持“同一平台、多 agent、多团队、多会话”的记忆隔离，而不是默认所有记忆都是全局共享。

### 2.5 Query 前的上下文构建：它真正怎么把记忆塞进 prompt

入口在 [internal/server/handler_query.go](/Users/joe/gtja/linlay/agent-platform/internal/server/handler_query.go)。收到 query 后，如果 memory 已配置，会调用：

```go
s.deps.Memory.BuildContextBundle(memory.ContextRequest{...})
```

真正的分层选择逻辑在 [internal/memory/context_builder.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/context_builder.go)。

它会做几件事：

1. 先筛 agentKey 和 scope。
2. 把 `fact` 和 `observation` 分开。
3. observation 再拆成：
   - 当前 chat 的 `sessionObs`
   - 其他 chat 的 `crossChatObs`
4. 事实按“有效重要度”排序。
5. observation 按两种模式排序：
   - 配置 embedding 时：向量相似度 + importance 混合评分
   - 未配置 embedding 时：子串匹配 + importance 排序
6. 最后按预算裁剪，生成三段 prompt：
   - `Runtime Context: Stable Memory`
   - `Runtime Context: Current Session`
   - `Runtime Context: Relevant Observations`

再由 [internal/llm/prompt_builder.go](/Users/joe/gtja/linlay/agent-platform/internal/llm/prompt_builder.go) 拼进最终系统 prompt。

最终顺序是：

1. `SoulPrompt`
2. agent 静态 memory
3. Stable Memory
4. Current Session
5. Relevant Observations
6. 其他 runtime context、stage prompt、工具说明

这意味着它不是简单做 RAG，而是做了带优先级的 prompt 编排。

### 2.6 它为什么要区分 Stable / Session / Observation 三层

这三层是整套实现最值钱的部分。

`Stable Memory`

- 存的是长期事实
- 自动进入 prompt
- 直接影响行为稳定性

`Session Memory`

- 存的是当前 chat 的 observation
- 解决“模型刚刚知道的东西，下一轮还要继续知道”
- 防止同一会话里反复重复背景

`Observation Memory`

- 存的是其他会话学到的观察
- 有帮助，但不一定是规则
- 用来让系统跨 chat 借鉴经验，而不是盲目固化

这三层组合起来，相当于：

- stable 回答“长期应该知道什么”
- session 回答“这次会话里刚发生了什么”
- observation 回答“别的会话里类似问题学到了什么”

### 2.7 排序不是静态的：它有时间衰减和访问强化

[internal/memory/context_builder.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/context_builder.go) 里的 `computeEffectiveImportance(...)` 不只看 `Importance`，还看：

- `LastAccessedAt`
- `UpdatedAt`
- `AccessCount`

这等于给记忆引入了两个动态信号：

- 越久没被访问，越容易衰减
- 越常被使用，越容易被强化

所以它不是静态优先级列表，而是“会随着使用情况变化的可检索记忆池”。

### 2.8 写入链路一：`remember` 更像最小摘要落盘

[internal/server/handler_memory.go](/Users/joe/gtja/linlay/agent-platform/internal/server/handler_memory.go) 里的 `handleRemember` 会从 chat detail 提取摘要，然后 `Memory.Remember(...)` 写入。

`Remember(...)` 的行为是：

- 抽取 chat 的 assistant summary
- 生成一条 `fact`
- 类别记为 `remember`
- 重要度固定为 `6`
- 同时把最小结果写成 `<MEMORY_DIR>/<chatId>.json`

所以 `remember` 更像“显式地把这次会话存个摘要”，不是完整自动学习器。

### 2.9 写入链路二：`learn` 和 auto-learn 才是真正的运行时学习

自动学习入口在 [internal/server/memory_learning.go](/Users/joe/gtja/linlay/agent-platform/internal/server/memory_learning.go)。

run 完成后，只要 `AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true`，系统就会：

1. 从 chat store 加载本次 run trace。
2. 调用 `Memory.Learn(...)`。
3. 从 trace 提取 observation。
4. 尝试生成 skill candidate。
5. 对 observation 做自动 consolidation。
6. 计算记忆反馈并回写。

[internal/memory/extractor.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/extractor.go) 当前实现相对保守：

- 优先提取 assistant 最终文本
- 写成一条 `observation`
- 默认 `importance=8`
- 默认 `confidence=0.75`
- scope 默认偏向 chat

这说明当前版本已经打通闭环，但“抽取质量”还是启发式为主，不是复杂的信息抽取 pipeline。

### 2.10 consolidation：为什么 observation 不会无限膨胀

整理逻辑在 [internal/memory/lifecycle.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/lifecycle.go)。

它主要做三件事：

1. 归档过期 observation
   - TTL 默认 30 天
2. 合并重复 observation
   - 同 category + 归一化 summary 视为重复
3. 晋升 observation 为 fact
   - 重复出现的 observation 更容易晋升
   - 或者高 importance + 高 confidence 的 observation 可晋升
   - `bugfix`、`workaround`、`preference`、`decision` 等类别有启发式晋升规则

晋升后在 [internal/memory/sqlite_store.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/sqlite_store.go) 里会：

- 新建 fact
- 建立 `derived_from` 关系
- 可选归档原 observation

这一步很像“从经验到规则”的蒸馏过程，也是这套系统优于单纯 session log 的地方。

### 2.11 feedback：系统会评估哪些记忆真正有用

[internal/memory/feedback.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/feedback.go) 负责计算反馈信号。

逻辑是：

1. 先拿到本轮真正被注入 prompt 的 memory item。
2. 查看 assistant 最终回答是否命中了这些记忆的关键词。
3. 命中则 `confidence +0.05`
4. 未命中则 `confidence -0.02`
5. 同时增加 `ACCESS_COUNT_`、刷新 `LAST_ACCESSED_AT_`

它不复杂，但闭环是成立的：

- 被注入
- 被使用或未被使用
- 回写置信度
- 下次排序发生变化

这就是“记忆不是只会越存越多，而是会逐渐筛掉无效项”的核心。

### 2.12 session search：它明确把“过程检索”从“长期记忆”里剥离了

`_session_search_` 工具定义在 [internal/resources/tools/_session_search_.yml](/Users/joe/gtja/linlay/agent-platform/internal/resources/tools/_session_search_.yml)，实现位于 [internal/tools/tool_session_search.go](/Users/joe/gtja/linlay/agent-platform/internal/tools/tool_session_search.go) 和 [internal/chat/search.go](/Users/joe/gtja/linlay/agent-platform/internal/chat/search.go)。

这个模块检索的是：

- `query`
- `message`
- `event`
- `submit`
- `approval`

也就是历史过程本身，而不是抽象后的事实。

这是一个很成熟的边界划分：

- 要“记规则”时，走 memory
- 要“找当时发生了什么”时，走 session search

如果没有这条边界，memory 很容易被历史日志污染，最终谁都不好用。

### 2.13 skill candidate：它不是 memory，但和 memory 构成成长闭环

在 [internal/app/app.go](/Users/joe/gtja/linlay/agent-platform/internal/app/app.go) 中，memory store 旁边还初始化了 `skill-candidates` 存储。

`Learn(...)` 里如果能从 run trace 识别出 procedure-like 内容，就会写 skill candidate。

这说明作者想解决的不是“记住事实”而已，而是让 agent 逐渐形成可复用工作法：

- fact 解决“知道什么”
- observation 解决“看到什么”
- skill candidate 解决“以后应该怎么做”

这三个层次分得很清楚。

### 2.14 安全措施：为什么 memory 写入前要扫注入和密钥

[internal/memory/safety.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/safety.go) 会拦截几类风险内容：

- prompt injection 语句
- `api_key=...`、`secret=...`、`password=...` 这类疑似凭据
- 不可见 Unicode 字符

原因很直接：这套系统的记忆会再次进入 prompt。只要写入时不做安全校验，恶意内容就可能变成“持久化 prompt 注入”。

这点和一般“聊天记录落盘”不一样，后者只是存储；这里是“存储后还要重喂模型”，风险更高。

### 2.15 可观察性与导出：它不只是数据库黑盒

这套系统还做了两个很实用的落地能力：

- `journal`
  - [internal/memory/journal.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/journal.go)
  - 每次写入都会按日期追加 markdown 日志
- `snapshot / exports`
  - [internal/memory/snapshot_renderer.go](/Users/joe/gtja/linlay/agent-platform/internal/memory/snapshot_renderer.go)
  - 会导出 `USER.md`、`PROJECT.md`、`AGENT.md`、`TEAM.md`、`GLOBAL.md`
  - 也会导出 recent observations、open todos

这带来两个价值：

- 运维上更容易审计
- 后续若要兼容 markdown-based memory 系统，会更容易对接

## 3. 和 Hermes、Claude Code 的记忆系统有什么不同

这一节只比较“记忆系统设计”，不比较模型能力、工具能力或产品形态。

### 3.1 先给结论

如果用一句话总结三者：

- `agent-platform`：数据库驱动、分层检索、带生命周期和反馈的运行时 memory
- `Hermes`：受限容量的持久记忆文件 + 会话搜索 + 可插拔外部记忆提供者
- `Claude Code`：`CLAUDE.md` 指令体系 + auto memory 目录，偏“工作树级持久上下文”

### 3.2 和 Hermes 的核心差异

根据 Hermes 官方文档，Hermes 的内建持久记忆主要是两份文件：

- `MEMORY.md`
- `USER.md`

并且它们会以“frozen snapshot”的方式在 session 启动时注入 system prompt；session search 则单独用于搜索 SQLite 中的历史会话。

对比下来，差异在这里：

| 维度 | agent-platform | Hermes |
| --- | --- | --- |
| 核心存储 | SQLite 主存储 + 投影表 + FTS5 + 可选 embedding | 以 `MEMORY.md` / `USER.md` 为主的受限文件记忆 |
| 记忆分层 | fact / session observation / cross-chat observation / session search / skill candidate | 持久文件记忆 + session search，结构更轻 |
| 中途更新可见性 | 每次 query 前实时构建 bundle，下一轮即可按最新库检索 | 文档强调 session start 注入 frozen snapshot，本轮中写入通常下轮更明显 |
| 生命周期管理 | TTL、去重、promotion、supersede、feedback | 更强调容量受限和 curated memory |
| 语义检索 | observation 支持 embedding 混合检索 | 内建文档里重点是文件记忆和 session search，深语义检索更多交给外部 provider |
| 关系建模 | `derived_from`、`supersedes` 等 links | 内建 memory 更偏条目维护 |
| 方法沉淀 | skill candidate 和 memory 并行演化 | Hermes 也有技能系统，但内建 memory 本身更接近紧凑型笔记 |

最本质的不同是：

- Hermes 的内建 memory 更像“高价值、强约束、低容量的个人记事本”
- 这个项目的 memory 更像“可演化的运行时知识库”

这带来的工程取舍也不同：

- Hermes 更简单、可控、token 成本稳定
- 这个项目更强，但复杂度、调试成本和抽取质量要求更高

### 3.3 和 Claude Code 的核心差异

Claude Code 官方文档明确把记忆分成两套：

- `CLAUDE.md`：人写的长期指令
- auto memory：Claude 自己写的笔记，存到项目对应的 memory 目录

并且两者都会在每个 session 启动时加载；`MEMORY.md` 还有 startup 加载长度限制。

和本项目相比，差异主要有五点：

1. 目标不同

- Claude Code 更强调“项目上下文持续存在”
- 这个项目更强调“agent 运行过程中如何学习和筛选经验”

2. 结构不同

- Claude Code 的主结构是 markdown 文件体系
- 这个项目的主结构是结构化数据库记录

3. 记忆语义不同

- Claude Code 的 `CLAUDE.md` 本质是 instruction memory
- 这个项目把 instruction、static memory、runtime facts、observations 明确拆开

4. 检索方式不同

- Claude Code 偏“启动加载 + 按需读文件”
- 这个项目偏“每轮 query 都做结构化检索和预算分配”

5. 闭环强度不同

- Claude Code 会自动积累 memory，但官方文档更强调目录管理、加载规则和人工审计
- 这个项目额外做了 confidence feedback、promotion、supersede 等运行时演化机制

如果再说直白一点：

- Claude Code 像“会持续维护项目说明书和个人备忘录”
- 这个项目像“会把历史运行经验抽成层次化知识，再决定哪些该进 prompt”

### 3.4 三者最大的设计分歧

三者其实代表了三种 memory 哲学：

`Claude Code`

- 先把上下文文件体系建立好
- 记忆首先服务于“协作规范和项目理解”

`Hermes`

- 先保证持久记忆足够紧凑、可控、可注入
- 再用 session search 和外部 provider 拉高上限

`agent-platform`

- 先把不同时间尺度的信息拆层
- 再用检索、生命周期和反馈控制它们如何进入回答

因此，这个项目比 Claude Code / Hermes 更“数据库化”和“系统化”，但也更依赖：

- 良好的抽取策略
- 合理的 importance / confidence 初始化
- 稳定的 consolidation 规则

否则结构再漂亮，也可能写入一堆价值不高的 observation。

## 4. 这套设计的优点和风险

### 4.1 优点

1. 分层清晰，避免把历史日志误当长期知识。
2. 作用域明确，适合多 agent、多团队、多用户场景。
3. retrieval 不是一刀切，支持 stable / session / observation 分别处理。
4. 有生命周期治理，不会只进不出。
5. 有反馈回路，记忆排序能随着使用结果变化。
6. 有安全校验，减少持久化 prompt 注入风险。
7. 可以导出 markdown snapshot，便于审计和兼容。

### 4.2 风险

1. `extractLearnedMemories(...)` 目前偏启发式，抽取粒度比较粗。
2. feedback 仅靠关键词命中，可能高估或低估真正引用情况。
3. observation promotion 规则当前主要是启发式，不是基于显式验证。
4. embedding 只主要用于 observation 检索，事实层仍以结构和 importance 为主。
5. 如果 importance / confidence 初值设置不佳，会影响长期演化质量。

## 5. QA：针对这套设计的 10 个关键问题与回答

### Q1：为什么不把所有历史聊天都直接当 memory？

A：因为“历史过程”和“长期知识”不是一回事。历史聊天里混有大量一次性步骤、试错过程和上下文噪音。如果全部直接进入长期记忆，prompt 很快会被污染。这个项目因此把历史过程交给 `session search`，把长期事实交给 `fact`，两者分工明确，所以回答链路是闭环的。

### Q2：既然已经有 `fact`，为什么还要 `observation`？

A：因为很多运行中学到的内容一开始并不够稳定。它可能只是某次会话里的发现、某次排障里的结论、或者只对当前上下文成立。先放进 `observation`，再经过去重、过期、晋升，才能避免“刚学到一个临时现象就被永久固化”的问题。也就是说，`observation` 是事实形成前的缓冲层。

### Q3：为什么还要把 `observation` 拆成 session 和 cross-chat 两类？

A：因为当前会话里的观察和其他会话的观察权重不应该一样。当前会话里的 observation 通常更贴近当下问题，应该优先保留；其他会话的 observation 更像补充经验，只在相关时拿出来。拆分后才能在 prompt 里形成合理顺序，避免旧会话经验压过当前上下文。

### Q4：为什么 Stable Memory 不走纯向量检索？

A：因为 Stable Memory 代表“长期要信”的事实，它更适合受 scope、status、importance、confidence 管控，而不是完全交给语义相似度。纯向量检索容易把语义接近但作用域不对、状态已过期或可信度不足的条目带进 prompt。这个项目把 embedding 重点放在 observation，是更稳妥的做法。

### Q5：为什么要做 `supersede`，直接覆盖旧 fact 不行吗？

A：直接覆盖会丢失演化历史，也无法知道新规则替代了谁。保留 `supersedes` 关系后，系统既能维持当前有效事实，又能追踪知识更新链路。这对审计、调试和后续解释都很重要，因此不是冗余设计。

### Q6：为什么 feedback 只调 confidence，不直接删除未引用记忆？

A：因为“这轮没用到”不等于“永远没用”。删除太激进，系统会变得短视；完全不处理又会让噪音累积。先做小幅 confidence 增减，再结合访问次数和时间衰减调整排序，是更稳妥的折中。这让系统具备淘汰趋势，但不至于过拟合单次结果。

### Q7：为什么还要 skill candidate，直接把 procedure 写进 memory 不行吗？

A：procedure 和事实不是一种东西。事实回答“知道什么”，procedure 回答“怎么做”。如果把流程性知识全部塞进 memory，prompt 会越来越像操作手册，反而挤占真正的事实上下文。独立 skill candidate 后，memory 保持轻量，技能库负责方法沉淀，职责才闭环。

### Q8：为什么写 memory 前要做安全扫描？

A：因为这里的 memory 不是冷存储，而是以后会再次进入 prompt。如果把提示注入、密钥、不可见字符等内容持久化下来，相当于把一次攻击升级成长期攻击面。写入前拦截，才能保证“可回放的记忆”不会变成“可复用的漏洞”。

### Q9：这套系统最依赖哪一段实现质量？

A：最依赖 `Learn -> observation -> consolidation -> feedback` 这一整条演化链。如果抽取质量差，写入的 observation 本身就低价值；如果 consolidation 太保守，库会膨胀；如果 feedback 太弱，排序不会自我修正。所以真正决定这套系统长期表现的，不是 SQLite，也不是 FTS，而是这条学习闭环的质量。

### Q10：如果要继续演进，这套设计下一步最值得加强什么？

A：最值得加强的是“验证型晋升”。现在 observation 晋升为 fact 主要还是启发式规则。下一步更好的方向，是让晋升前经过更多证据校验，例如多次独立命中、人工确认、工具结果验证、或跨 run 一致性证明。这样 fact 层会更干净，整套系统的长期可信度会明显提高。

## 6. 最后结论

这个项目的记忆系统不是一个“帮模型多记点东西”的附属模块，而是一个围绕 agent runtime 做出来的分层知识系统。

它最有价值的地方不在于用了 SQLite、FTS 或 embedding，而在于三条边界划分得比较成熟：

1. 长期事实和短期观察分开。
2. 历史过程和长期记忆分开。
3. 事实记忆和方法沉淀分开。

相比 Hermes 和 Claude Code，它明显更偏运行时系统设计，而不是文件型持久上下文设计。代价是复杂度更高，收益是可演化空间更大。只要后续继续补强抽取质量、晋升验证和反馈精度，这套 memory 架构是有机会长成真正 agent memory kernel 的。
