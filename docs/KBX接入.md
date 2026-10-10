# KBX 接入

## 所有权与绑定

知识库中心是唯一索引所有者。`internal/kbasescenter` 按 library ID 管理配置、目录监听、维护调度和状态；`internal/kbx.CenterEngine` 调用受管 CLI 维护；`internal/kbx.Manager` 把 Agent 绑定解析为数据库和允许的 collection 集合，保留过滤、chunk/evidence 分页与图证据查询。`internal/knowledge` 只保存中立配置、DTO、工具和引用发布契约，不依赖 catalog、不持有索引或进程。

所有 Native GENERAL/CODER/KBASE（及 PLAN-EXECUTE）使用相同的 `kbaseConfig.libraryId` 契约。省略绑定即不开启知识库能力；配置不能隐式授予工具。一个 Agent 绑定一个库，一个库可包含 1–32 个 collection，并被多个 Agent 共用。Workspace 与知识范围无关；KBASE 的 Workspace 仍决定项目展示和相对路径；专用 Host KBASE 可在 Run 启动时额外冻结 editable collection 目录，editingMode 控制两者的 mutation。

```yaml
key: kbase-tmmsin
name: 研发知识助手
mode: KBASE
modelConfig:
  modelKey: deepseek-v4-pro
runtimeConfig:
  workspaceRoot: /Users/linlay/Documents/知识库测试用文档/AI建设文档
kbaseConfig:
  libraryId: 1d1fd14be02307fe58a05549
  retrieval:
    topK: 8
    candidateFloor: 30
    candidateMultiplier: 4
    candidateMax: 500
toolConfig:
  tools:
    - kbase_search
    - kbase_files
    - kbase_read
    - kbase_status
```

示例模型键须替换为部署中的有效模型。绑定 ID 在 catalog 只检查格式；Run 开始检查库就绪状态，工具每次调用重新检查当前绑定与已应用来源指纹。删除或破坏配置只使该库和依赖调用失败，管理端显示 warning，不因为单个绑定失效把 `/healthz` 变成 503。知识检索的来源范围不冻结到旧 Run；下一次查询按当前配置重新授权。文件编辑是独立边界：专用 Host KBASE 的 editable collection canonical 目录与说明在 Run 启动时冻结，库修改仅影响下次 Run，不能中途扩大文件授权。ACP、PROXY、CHANNEL 不支持 Native 知识工具。

## 维护生命周期

Platform 不运行 `kbx watch`。中心先监听，再启动首次全量对账；每库串行，全局同时最多两库维护。合并窗口 500ms，从第一条事件计时；每五分钟全量对账。普通新增/修改文件使用 `--paths-from`；目录变化、删除、重命名、监听错误或路径数超过 4096 时全量对账。更新期间新事件保留到下一批。失败由后续变更或周期对账重试，重启自动重新对账。

库锁相互独立。查询持有本库读取租约；维护期间允许同库查询，编辑/删除冲突立即返回 busy。中心手动刷新使用同一调度和状态链路。持久的 `.worker.lock` 防止两个 Platform 调度同一 runtime。每批最长 35 分钟，embed 单次预算 30 分钟。

CLI 必须声明维护协议 v1、结构化错误、无扫描注册与文件路径更新能力。先 `collection add --no-index`，配置 include/exclude/chunk 并读回确认，再逐集合 `update --no-commands`，配置 embedding 时执行 `embed`。不运行来源提供的 update-command，不执行 nextActions。partial、失败文件和非零退出不得冒充成功；`INDEX_BUSY` 和可续跑的 embed `SESSION_LIMIT` 有界重试。

普通内容更新保持已有索引可读，状态为 `indexing/stale`。KBX 原地提交，读取可能看到已经提交的新内容，**不承诺更新前的固定快照**。全文更新已确认完整而 embedding 失败时，保持全文可读并报告 degraded/refreshError；未知 partial 或中断禁读，等待自动重新对账。首次扫描、集合增删、来源或过滤/切块变更会撤销旧范围的读取。`ErrNotStarted` 仅用于尚未进行索引写入的失败，可保留刷新前状态。

`kbase_refresh`、`POST /api/kbase/{agentKey}/refresh`、`kbase.refreshTerminal` 和 Agent 刷新回执已删除。Agent 可用 `kbase_status` 查看绑定库状态；管理员仍可在中心手工刷新。没有 force 工具或自动旧索引迁移。

## 查询、路径与引用

- 工具 `kbase_search` 支持 query/search/vsearch/gsearch。纯全文不解析模型配置；严格向量或图检索不静默降级。query 保留真实通道和降级信息，支持逐库重排和查询扩展。图谱构建尚未接入自动维护；仅能使用已有完整图谱。
- 所有工具路径、`pathPrefix/pathGlob`、来源 chunk 的 Path 统一为 `<collection>/<relativePath>`。例如 `ai/design.md`、`research/design.md` 是不同文档。`pathPrefix: research` 限定集合，`pathGlob: "**/*.md"` 可跨集合匹配。`filter` 仍是原生 KBX 表达式/JSON，`sys.path` 是集合内路径，`sys.collection` 可限定集合。
- 查询前将过滤条件与每个 collection 的 include/exclude 取交集；结果正文、嵌套图证据和回读再次校验范围。`exclude` 是词法排除表达式，不是文件路径；`minScore` 是方法相关排名分，不是置信度。候选上限 2000、结果最多 50，不支持搜索 offset/cursor。
- `kbase_read.chunkId` 接收 chunkId/evidenceId/nextEvidence，精确回读证据；按路径读取使用一基行号 offset 和 limit 分页。`kbase_files` 只列 active 索引文件，不列主机任意文件。
- 发布来源 ID 为 `kbase:<libraryId>/<collection>/<relativePath>`，来源事件携带 libraryId 与 AgentKey，持久化/回放保留这些字段。
- `POST /api/chat/sources/read` 接收 chatId/sourceId/offset/limit，返回授权后的正文；`POST /api/chat/sources/file` 接收 chatId/sourceId，下载当前源文件。两者先核验身份和 Chat 权限，再查 Chat 已发布来源及 Agent 当前库绑定，不借 `/api/resource`，不把共享库来源加入 Workspace 的文件工具授权。重新绑定后旧来源不可回读，历史片段仍保留。正文是当前索引内容，下载是当前源文件，并非历史内容快照。

读取要求 Agent envelope v2 / retrieval contract v6；结构化失败保留有限机器码和固定提示，不泄露模型 stderr。KBX 不提供精确 chunk 数，status 返回 chunksKnown=false。

## 源过滤、存储和模型

配置位于 `kbases/<id>/library.yml`，索引固定在 `ru-kbases/<id>/index.sqlite`。collection 的 sourcePath、include/exclude、逐字段合并后的 chunk、库级 textEncoding 和部署强制排除目录进入来源指纹。默认包含 MD/TXT/HTML/HTM/PDF/DOCX/PPTX。源目录不得位于库配置、运行数据或 StateDir 内，必须与 ChatsRoot 分离；来源中的 runtime/state 子目录强制排除。隐藏文件、node_modules/vendor/dist/build 及符号链接不进入索引。include 无法覆盖这些排除。

维护规则只接受与 KBX globset 可证明等价的形式：明确相对路径、`dir/**`、`**/*.ext`、`docs/**/*.ext`；`docs/*.md`、`a?b.md` 等明确拒绝。chunk 按平台默认 → 库级 → collection 逐字段合并，unit 仅为 chars，strategy 为 window/regex/structural；默认值统一由 `knowledge.DefaultChunkConfig()` 提供 chars/window/3600/540，显式 overlapChars: 0 保留。有效切块或 textEncoding 变化后全量 update 重新抽取、发布分块；不再使用 token 默认值映射。

`library.yml → models.embedding` 可以选择共享 registry 的 embedding 模型和 prompt；省略时继承 `runtime.yml → kbx.embedding`，Agent 不覆盖。每次调用在 StateDir 的库专属目录生成 0600 私有配置快照，携带有效模型、chunking、text_encoding，以 --config 显式传入，子进程结束后删除。CLI 用 argv 调用，输出有界，不继承用户 KBX 配置。模型键、endpoint/modelId/dimension/prompt 进入向量指纹；密钥、超时、batchSize 下次调用生效而不重建。

向量合同变化执行全库 embed --force（无 -c），不重新扫描来源、不改变已发布 chunk。普通内容维护完成全部 collection 的 update 后执行全库 embed，Platform 合同变化或 KBX 报不兼容则使用 force；SESSION_LIMIT 后续跑不重复 force。向量变化/失败期间全文可读，query 使用全文，严格 vsearch 拒绝旧合同；成功后自动恢复。query expansion 与 reranker 仅注入检索调用；graph extraction 仍未接入。配置继承、PUT 重置规则见 [知识库中心](知识库中心.md#库级配置与模型)。

## 升级与验证

这是硬切升级。Agent enabled/storage/include/exclude/chunk/tags/embedding 及非支持 retrieval 字段明确拒绝；`AP_RUNTIME_KBASE_DIR` 拒绝加载。旧 `ru-kbases/libraries/` 与旧 Agent 索引目录 `runtime/kbase` 完全忽略：不进行旧布局检查、不加载、不迁移，也不移动或删除其中数据。中心按 `kbases/<id>/library.yml` 配置自动在 `ru-kbases/<id>/` 生成索引；Agent 通过 `libraryId` 绑定中心库。`libraries` 是保留 ID，runtime 根下非库目录使用点前缀。

沿用受管 builtin 同步与发布链路，不手工改正式 lock。`internal/kbasescenter` 覆盖监听、增量批次保留、重启、旧布局、并发/故障状态与删除保护；`internal/kbx` 覆盖共享库路径、过滤、图证据与源读取；Server 覆盖授权来源 API。真实 CLI 验证入口：

```sh
KBX_ACCEPTANCE_BIN=/absolute/managed/bin KBX_CENTER_TEST_BIN=/absolute/managed/bin \
  go test ./internal/kbx -count=1
```

`TestLiveSharedLibraryLifecycle` 在临时库和本机 embedding fixture 验证同名文档、自动监听、排除、模型故障保持全文可读和恢复。`TestCenterRealLibraryChunkAndEncoding` 与 `TestCenterRealLibraryModelSwitchDoesNotScanSources` 覆盖库级策略、编码变更、两库模型隔离和向量切换失败/恢复。测试不代表模型的真实语义质量。Windows 锁和 watcher 尚需原生 Windows 验证。

## Collection 元数据与指纹分类

collection 支持 `description`（可选文本，最多 4000 字节）和 `editable`（boolean，缺省 false）。二者不计入来源/向量指纹，管理 API 与 WebClient 表单保留这些字段。授权只适用于专用 KBASE 的 Host Run，需 editingMode；不向普通 Agent 授权，也不自动挂载容器。索引 include/exclude 不等于文件编辑范围，详见 [KBASE 编辑模式](KBASE编辑模式.md)。

中心显式投影来源字段；有效 chunk/textEncoding 属于来源指纹，embedding 合同属于向量指纹。只有向量变化时不撤销已提交全文，向量错误/中断独立降级；collection description/editable 不参与指纹。retrieval、models.reranker/queryExpansion 与 defaultQuery 下次查询生效，不计入指纹；graph/metadata 等未实现字段继续严格拒绝。

## 检索配置合并与可选模型

库 retrieval → Agent 显式 retrieval → kbase_search 单次参数逐字段覆盖，平台缺省仍为 topK=8、candidateFloor=30、candidateMultiplier=4、candidateMax=500；零分数/时效权重及 false 开关不会被省略。新增 collections（显式选择）、rerank（query）和 queryExpansion（query/vsearch）参数；不适用方法的显式参数拒绝。search 始终不解析模型；gsearch 不接收文本排序默认值。

reranker 使用 registry type: reranker 和必填 reranker.endpointPath，queryExpansion 使用 type: chat / protocol: OPENAI 的 Chat Completions endpoint；KBX 不承接平台自定义 headers/compat 或其他聊天协议。去掉强制 --no-rerank，仅在有效 rerank=false 时传递。配置或单次开关关闭时不注入对应模型。模型 HTTP 失败保留原查询/原排序并报告 degraded，Agent optionalUnavailable 包含对应角色；错误键/协议直接报配置错误。模型角色不进入维护配置和索引指纹。

defaultQuery 缺省 true。维护时同步 collection include/exclude，检索时按当前默认或显式 collections 传入 -c，全部默认排除返回空数组；过滤不能扩大绑定库范围，files/read 不受默认检索选择影响。详细配置、registry 示例和边界见 [知识库中心](知识库中心.md#检索默认值可选模型和默认集合)。

`TestCenterRealRetrievalModelsDefaultsAndDegradation` 使用当前受管 KBX 和本机 HTTP fixture 验证两种模型调用、禁用/全文零调用、失败回退与 collection 隔离。真实部署配置的小样本记录见下节；不将 fixture 时间或小样本时间写成生产指标。缓存、扩展向量和重试会改变请求数量。

### 2026-10-10 本机真实模型实测

使用当前部署 registry、受管 `kbx 0.1.0`，通过 Platform CenterEngine / Manager 运行临时库。扩展模型为 `th-gpt-5_6-luna`，embedding 为 `th-text-embedding-v4`。测试库含六篇合成短文，另一个默认排除的 collection 放一篇冲突文档；模型仅接收这些合成资料与三条测试问题。未修改真实库、模型配置或开关，也未测试图谱与元数据。

以下为每条问题的一次对照，结果数量 3，图谱和重排关闭。基线先执行，随后开启查询扩展，因此原问题的向量可能复用缓存；扩展步骤的 trace 均确认首次调用未命中缓存。时间包含 Platform / CLI / 模型调用，不是模型服务的独立耗时，也不是 P95 或负载测试结果。

| 测试问题 | 基线耗时 | 开启扩展耗时 | 基线 / 扩展命中 |
| --- | ---: | ---: | --- |
| QuartzOrchid（精确标识） | 1.001 秒 | 3.373 秒 | 均第 1 条命中审计日志文档 |
| 电脑不见了该找谁处理 | 1.016 秒 | 4.987 秒 | 均第 1 条命中终端遗失文档 |
| 上线后出问题怎么恢复旧版 | 1.013 秒 | 4.086 秒 | 均第 1 条命中发布回退文档 |

三条问题的前 3 条结果及顺序在扩展前后相同；再次运行精确标识查询，扩展缓存命中，整次查询 0.148 秒。另一次不配置 embedding 的全文对照中，扩展调用成功但没有改善命中：精确标识命中，两条中文改写均未命中，扩展开启后的耗时为 3.003–4.226 秒。这个小样本未显示扩展带来的召回收益，因此尚无依据默认开启；需要业务评测集才能判断其他文档和问题上的质量。

故障测试只替换单次调用的扩展 endpoint，移除真实凭据并注入本机 503、超时、空 choices。为缩短故障实验，将扩展 timeout 临时设为 100ms，三种故障均保留正确文档、标记 degraded，并在 optionalUnavailable 返回 query_expansion。有 embedding 时整次查询为 0.912–0.990 秒，无 embedding 时为 0.126–0.267 秒；这些时间不代表真实部署默认超时下的等待上限。所有查询均未返回默认排除集合的文档。

当前部署未配置 `type: reranker`，真实重排的服务兼容性、耗时和质量仍未验证；已有模拟服务验收不能替代该项。部分聊天模型含自定义 compat，Platform 会明确拒绝把这类配置直接交给 KBX，不能通过忽略这些字段声称已兼容。

可复现入口（显式启用后会调用真实模型，普通测试默认跳过）：

```sh
KBX_CENTER_TEST_BIN=/absolute/managed/bin \
KBX_LIVE_REGISTRY=/absolute/runtime/registries \
KBX_LIVE_EXPANSION_MODEL=your-expansion-model \
KBX_LIVE_EMBEDDING_MODEL=your-embedding-model \
  go test ./internal/kbx -run '^TestLiveRetrievalDeployment$' -count=1 -v
```

省略 `KBX_LIVE_EMBEDDING_MODEL` 测试全文加扩展；配置真实重排后追加 `KBX_LIVE_RERANKER_MODEL`。测试仅在临时目录创建来源、库及配置快照，结束自动清理；输出合成文档路径、命中情况、耗时和有限 trace 字段，不输出 endpoint 或凭据。质量命中是观测数据，不作为保证模型改善的断言；模型调用成功、默认集合隔离和故障降级有独立断言。
