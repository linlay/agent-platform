# KBX 接入

## 当前实现

Platform 的 app 装配、知识库工具、HTTP status/refresh、health 和 wait 使用 `internal/kbx.Manager`。读取要求 Agent envelope v2、retrieval contract v6；维护要求 KBX `capabilities --format json` 声明维护协议 v1、结构化错误、无扫描注册及文件路径更新。不能用二进制版本号代替能力探测。

**Platform 负责目录监听和后台调度，不启动 `kbx watch`。** KBX 负责抽取、分块、全文/向量索引、写锁与事务。中立配置、DTO、工具处理器、引用发布与文件列表格式化归 `internal/knowledge`，该包不依赖 agent/catalog，不访问索引存储或启动进程。专用 mode 的规则归 `internal/agent/kbase`。

`/healthz` 的 `data.kbase.sidecar` 及 status 的 sidecar 字段保持兼容；内部使用 `knowledge.RuntimeState` 与 KBX `ProbeRuntime`。JSON 名称、字段类型和 omitempty 保持稳定，KBX 未提供的版本信息不输出。Poppler 继续随包分发：KBX PDF 抽取仍调用 pdftotext，Agent 也可按 Bash 权限直接调用。

## 维护生命周期

- Platform 启动后为每个启用知识库能力的 Agent 建立 worker，先监听再首次全目录对账。Catalog 发布新增或更换索引范围时启动新 worker，删除或停用时取消旧 worker。旧索引保留，不迁移或删除源文档。
- 每库串行执行，全局最多两库同时维护。默认合并窗口 500ms，从第一条变化计时，持续写入不会无限推迟；路径集合超过 4096 项转全目录对账。创建目录、删除/重命名、监听异常保守使用全目录对账；普通文件新增/修改使用 `--paths-from`。每五分钟补充全目录对账，弥补漏事件。监听失败会在状态报告，周期对账继续工作。
- 请求批次开始时冻结已观察变化。执行期间的新事件留在下一批，结束时不清空它们。显式 refresh 不附着到已经运行的旧批次，force 单独排队；每库最多 256 个待执行显式请求，超限返回 unavailable。
- 注册使用 `collection add --no-index`；先保存 pattern、ignore、字符切块，并用 `collection show --format json` 验证，再执行 `update -c workspace --no-commands --format json`。恢复时已有数据库先 list 并核对唯一 workspace collection 及源目录，拒绝串库。无数据库才尝试首次注册。
- 更新成功且配置了 embedding 模型、库非空时调用 `embed -c workspace --format json`。每个批次冻结一份部署级模型配置；普通模型失败不自动 force。`force=true` 表示全目录内容对账并调用全库 `embed --force`，不带 `-c`；用户须明确授权重建。KBX update 会读取文件内容并核对哈希，不只依赖修改时间。
- 同时校验维护响应 type/version/operation/status/exitCode、文件失败数和最终索引能力。非零退出仍解析 JSON；partial（包括退出码 0）不冒充完成。`INDEX_BUSY` 和普通 embed 的 `SESSION_LIMIT` 最多三次尝试，分别延迟一、二秒；force embed 的 partial 不自动重复 force。每批总超时 35 分钟，embed 单次预算 30 分钟；其他失败由手动或周期对账重试。
- 不使用 KBX nextActions 执行任意命令，不运行 collection update-command。没有稳定进度流或准确百分比，不编造进度。

## 刷新回执与等待

`kbase_refresh` / HTTP refresh 异步返回 `status: pending` 和 `refreshId`。Platform 在返回前将请求回执原子写入 `<AP_RUNTIME_STATE_DIR>/kbx/refresh/`，记录 Agent、索引范围路径及 force；运行中改为 running，最终为 completed/failed/canceled/interrupted。同一 runtime 的调度器持有文件锁，禁止两个 Platform 同时维护其回执。

通用 wait 条件 `kbase.refreshTerminal` 使用 agentKey/refreshId；只接受当前 Agent 的回执，只有明确的终态会完成等待。等待取消或 HTTP 断开不会取消后台维护。completed 表示本次文本维护与所配置 embedding 已完成；未配置 embedding 时可完成全文索引，向量能力仍报告不可用。

重启将未完成旧回执标记 interrupted，并发起新的首次全量对账；历史终态不会被后续批次覆盖。这里是重新执行、重新对账，不是重连 KBX 持久任务。超时强制终止可能没有最终 JSON，不据此认定成功。回执写入失败会报告错误；不能持久化的请求不确认接收。

## 状态与读取

`kbase_status` 提供 refreshId、state（unindexed/indexing/ready/refreshing/degraded/error）、indexing/stale、全文及向量 readiness、文件数、最近完成时间和错误。是否与源目录同步由 Platform worker 判断，不能从 KBX 空库或 fullText.ready 推断已扫描。未完成首次扫描的注册空库不可作为空知识库查询。未就绪工具错误提供当前 refreshId/indexing/state，模型可等待后重试。

- `kbase_search.method` 支持 `query`（缺省混合）、`search`（纯全文 BM25）、`vsearch`（严格向量）和 `gsearch`（实体关系图检索）。四种方法均限制当前 Agent 的 `workspace` collection。中心选库、多 collection 授权尚未接入 Agent 工具。
- `query/search/vsearch` 使用 `--agent --full` 并校验 retrieval contract v6；混合检索允许 KBX 按可用全文、向量与已有图谱召回，并保留真实通道、降级和候选预算信息。`query` 的 `noGraph:true` 可关闭图召回；重排仍由 `--no-rerank` 关闭，部署配置目前只提供 embedding。`search` 不解析模型连接配置、不调用模型；显式 `vsearch/gsearch` 失败时不自动换成其他方法。
- `gsearch` 使用独立命令和图结果协议，不发送不兼容的 `--full`、`-C` 或全文/向量排名参数。支持 `entities`、`relations`、`direction`（auto/out/in/both）和 `maxHops`（1–3，缺省 2），结果保留 `graph.links`、`graph.bestPath` 的节点、关系及每条边的原文证据。图返回没有候选预算 trace，因此不输出伪造的 candidateBudgetExhausted。要求已有完整图谱；Platform worker 的 update/embed 不构建图谱，不能靠反复 refresh 补齐。
- 所有方法的 `pathPrefix`、`pathGlob`、`type`、`filter` 与 Agent include/exclude 在召回前取交集，不是取 top N 后再过滤。`filter` 接受 KBX 表达式或 JSON 字符串，例如 `project = payments and owner exists`、`sys.size >= 1024`、`(ext = md or ext = pdf) and not path ^= deprecated`；元数据字段必须已存在于索引。返回正文和嵌套图证据再次检查 collection、路径与 Agent include/exclude，不允许跨范围来源。
- `query/search/vsearch` 支持 `exclude`（字符串数组，任一词法表达式命中即排除文档）、`intent`（正向提示）、`minScore`（有限数值，方法相关排名分，不是可信度）、`candidateLimit`（最多 2000，至少为实际 limit，并受 Agent candidateMax 限制；candidateMax 小于实际 limit 时按 limit 提升）、`recencyWeight`（0–1）和 `recencyHalfLifeDays`（正数，KBX 缺省 90）。不适用当前 method 的参数及未知字段明确报错，不静默忽略。limit 最多 50；搜索不支持 offset/cursor，也不开放无界 `--all`，不伪造精确 matchCount 或分页完成标志。
- `kbase_read.chunkId` 可接收 chunkId、evidenceId 或前次读取的 nextEvidence，精确回读对应内容；图命中的 evidence 可能只覆盖 chunk 的一部分，应使用 evidenceId 核验引文。也可用相对路径和一基行号分页，拒绝跨 collection、目录越界和配置排除内容。`kbase_files` 只展示 KBX active 文档清单。`context`、`tsearch` 和 `multi-get` 尚未接入专用工具。
- 非零退出仍解析合法 KBX 错误 envelope，返回受限机器错误码及固定修复提示，不回显可能含模型凭据的原始 message/hint，也不把失败伪装成空结果。
- KBX 没有精确 chunk 总数，status 省略 chunks 并返回 chunksKnown=false。向量未配置或未完整时不声称向量可用。
- health 探测受管 CLI 的维护能力；首次索引未完成不会被解释为 CLI 故障。旧二进制缺少协议或调度器启动失败则明确不可用。

## 源文件过滤

include/exclude 在抽取和 embedding **之前**配置。KBX 额外排除隐藏组件、node_modules、vendor、dist、build 及知识库自身文件；include 不能重新纳入。Platform 另外排除 workspace 内的 runtime/state 目录，监听也忽略这些目录；不跟随源目录符号链接。

KBX globset 当前允许 `*` / `?` 跨 `/`，与 Platform 原有语义不同。为防止扩大源范围，目前只接受可证明等价的维护规则：明确相对路径、`dir/**`、`**/*.ext`、`docs/**/*.ext` 等；例如 `docs/*.md`、`a?b.md`、`**/private*.txt` 会在扫描前明确拒绝。默认 include/exclude 已覆盖。多个 include 用花括号 OR，字面逗号转义。更一般的规则需要 KBX 提供源 glob 的 literalSeparator 契约后接入，不能静默转换语义。

## 存储与配置

模型统一来自 `runtime.yml → kbx.embedding`（model-key、prompt），中心和 Agent 共用部署级连接来源。每个进程传入私有 `--config`，不配置 query expansion、reranker 与 graph extraction 模型，不继承用户的 KBX 配置；已有图谱的本地召回不需要 extraction 模型。Agent 纯全文、图检索及读取不解析 embedding 配置。CLI 仅从受管 builtin 目录解析，以 argv 调用，不经过 shell；输出上限 16MiB，临时配置权限 0600，用后清理，不回显原始模型 stderr。

索引位置为 `<AP_RUNTIME_KBASE_DIR>/<agentKey>/kbx/<scopeHash>/index.sqlite`；workspace 模式为 `<workspaceRoot>/.kbx-platform/<agentKey>/<scopeHash>/index.sqlite`。scopeHash 含 canonical workspaceRoot、include/exclude、chunk 配置；更换范围时隔离。库目录拒绝符号链接替换。启动和维护不删除其他索引范围的数据或源文档。

默认 1000 estimatedTokens/100 overlap 映射为 3600/540 字符；自定义切块需配置 `unit: chars`。非默认 RRF 权重不支持，topK 与候选预算映射到 KBX 查询参数。模型连接地址/密钥改变不自动重建向量；模型/prompt 合同不兼容由 KBX 明确报错，显式 force 才重建。

调度默认值见 `internal/kbx.NewManager`，由 App 注入有效 StateDir；没有新增 YAML 目录配置。KBX 使用自身 HTTP 客户端，Platform 系统代理/PAC 尚未映射；本地模型 mock 通过 NO_PROXY 绕过系统代理。

## 分发与验证

沿用 `scripts/sync-local-builtins.sh` 校验缓存，再由 make run/build/release 消费；不手工覆盖 manifest、正式 lock 或其他平台 SHA。实际受管二进制必须通过维护能力探测，旧 cache 需要同步。正式版本与源码必须符合现有发布规则。

macOS ARM64 上的 `TestLivePlatformLifecycle` 验证首次自动索引、读取证据、监听新增/删除及异步 refresh；`TestLivePlatformEmbeddingExcludesAndFailure` 用本地模型 mock 验证排除发生在 embedding 前、向量完成、模型失败降级及显式 force。索引和文档均在测试临时目录，不外发用户文档。Windows 锁和 watcher 仍需原生 Windows 验证；KBX 当前未提供 Linux/Windows ARM64 发行目标。

`TestLiveSearchMethodsAndAdvancedFilters` 使用当前受管 KBX 验证全文/混合、过滤表达式、排除、分数与时效参数；`TestLiveVectorPrefilterAndLibraryIsolation` 覆盖严格 vsearch。`TestLiveGraphSearchEvidenceAndHybridRecall` 通过本机模拟 extraction 服务仅在临时库建图，验证图方向、两跳关系与跳数限制、范围过滤、关系证据回读和 query 的图召回；这不代表 Platform 自动建图已接通，也不代表真实模型语义质量。工具单测检查不支持参数拒绝、范围不可扩张和嵌套图来源发布。

```sh
KBX_ACCEPTANCE_BIN=/absolute/managed/bin \
go test ./internal/kbx -run 'TestLivePlatform|TestLiveChunkAndFilterContract|TestLiveVectorPrefilterAndLibraryIsolation|TestLiveSearchMethodsAndAdvancedFilters|TestLiveGraphSearchEvidenceAndHybridRecall' -count=1
```

部署级知识库中心使用 `kbases/<id>/library.yml` 与持久的 `ru-kbases/libraries/<id>/`，仍使用独立管理入口，不由 Agent worker 监听；此布局不改变上述 Agent 索引路径。
