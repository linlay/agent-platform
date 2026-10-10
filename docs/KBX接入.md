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

- 工具 `kbase_search` 支持 query/search/vsearch/gsearch。纯全文不解析模型配置；严格向量或图检索不静默降级。query 保留真实通道和降级信息，重排仍未接入。图谱构建尚未接入自动维护；仅能使用已有完整图谱。
- 所有工具路径、`pathPrefix/pathGlob`、来源 chunk 的 Path 统一为 `<collection>/<relativePath>`。例如 `ai/design.md`、`research/design.md` 是不同文档。`pathPrefix: research` 限定集合，`pathGlob: "**/*.md"` 可跨集合匹配。`filter` 仍是原生 KBX 表达式/JSON，`sys.path` 是集合内路径，`sys.collection` 可限定集合。
- 查询前将过滤条件与每个 collection 的 include/exclude 取交集；结果正文、嵌套图证据和回读再次校验范围。`exclude` 是词法排除表达式，不是文件路径；`minScore` 是方法相关排名分，不是置信度。候选上限 2000、结果最多 50，不支持搜索 offset/cursor。
- `kbase_read.chunkId` 接收 chunkId/evidenceId/nextEvidence，精确回读证据；按路径读取使用一基行号 offset 和 limit 分页。`kbase_files` 只列 active 索引文件，不列主机任意文件。
- 发布来源 ID 为 `kbase:<libraryId>/<collection>/<relativePath>`，来源事件携带 libraryId 与 AgentKey，持久化/回放保留这些字段。
- `POST /api/chat/sources/read` 接收 chatId/sourceId/offset/limit，返回授权后的正文；`POST /api/chat/sources/file` 接收 chatId/sourceId，下载当前源文件。两者先核验身份和 Chat 权限，再查 Chat 已发布来源及 Agent 当前库绑定，不借 `/api/resource`，不把共享库来源加入 Workspace 的文件工具授权。重新绑定后旧来源不可回读，历史片段仍保留。正文是当前索引内容，下载是当前源文件，并非历史内容快照。

读取要求 Agent envelope v2 / retrieval contract v6；结构化失败保留有限机器码和固定提示，不泄露模型 stderr。KBX 不提供精确 chunk 数，status 返回 chunksKnown=false。

## 源过滤、存储和模型

配置位于 `kbases/<id>/library.yml`，索引固定在 `ru-kbases/<id>/index.sqlite`。collection 的 sourcePath、include/exclude/chunk 和部署强制排除目录进入指纹。默认包含 MD/TXT/HTML/HTM/PDF/DOCX/PPTX。源目录不得位于库配置、运行数据或 StateDir 内，必须与 ChatsRoot 分离；来源中的 runtime/state 子目录强制排除。隐藏文件、node_modules/vendor/dist/build 及符号链接不进入索引。include 无法覆盖这些排除。

维护规则只接受与 KBX globset 可证明等价的形式：明确相对路径、`dir/**`、`**/*.ext`、`docs/**/*.ext`；`docs/*.md`、`a?b.md` 等明确拒绝。自定义 chunk 使用 `unit: chars`、maxChars 和 overlapChars；修改后全量 update 重新发布分块。默认 1000 estimatedTokens / 100 overlap 映射为 3600 / 540 字符。

模型只来自 `runtime.yml → kbx.embedding` 和模型 registry，连接快照位于 StateDir 私有文件。CLI 用 argv 调用，配置权限 0600，输出有界，不继承用户 KBX 配置。query expansion、reranker 和 graph extraction 模型未配置。向量合同不兼容时 KBX 返回错误，管理员需处理模型合同或停机备份运行目录后重建，不能用不存在的 Agent force 工具。

## 升级与验证

这是硬切升级。Agent enabled/storage/include/exclude/chunk/tags/embedding 及非支持 retrieval 字段明确拒绝；`AP_RUNTIME_KBASE_DIR` 拒绝加载。旧 `ru-kbases/libraries/` 与旧 Agent 索引目录 `runtime/kbase` 完全忽略：不进行旧布局检查、不加载、不迁移，也不移动或删除其中数据。中心按 `kbases/<id>/library.yml` 配置自动在 `ru-kbases/<id>/` 生成索引；Agent 通过 `libraryId` 绑定中心库。`libraries` 是保留 ID，runtime 根下非库目录使用点前缀。

沿用受管 builtin 同步与发布链路，不手工改正式 lock。`internal/kbasescenter` 覆盖监听、增量批次保留、重启、旧布局、并发/故障状态与删除保护；`internal/kbx` 覆盖共享库路径、过滤、图证据与源读取；Server 覆盖授权来源 API。真实 CLI 验证入口：

```sh
KBX_ACCEPTANCE_BIN=/absolute/managed/bin KBX_CENTER_TEST_BIN=/absolute/managed/bin \
  go test ./internal/kbx -count=1
```

`TestLiveSharedLibraryLifecycle` 在临时库和本机 embedding fixture 验证同名文档、自动监听、排除、模型故障保持全文可读和恢复。测试不代表模型的真实语义质量。Windows 锁和 watcher 尚需原生 Windows 验证。

## Collection 元数据与指纹分类

collection 支持 `description`（可选文本，最多 4000 字节）和 `editable`（boolean，缺省 false）。二者不计入来源/向量指纹，管理 API 与 WebClient 表单保留这些字段。授权只适用于专用 KBASE 的 Host Run，需 editingMode；不向普通 Agent 授权，也不自动挂载容器。索引 include/exclude 不等于文件编辑范围，详见 [KBASE 编辑模式](KBASE编辑模式.md)。

中心显式投影来源字段以保持原有来源指纹；向量合同使用独立维护接口，只有向量变化时不撤销已提交全文，向量错误/中断独立降级。当前生产 CenterEngine 的向量指纹仍为空，逐库模型在后续阶段接入；全局 runtime.kbx 模型配置行为不变，未实现字段继续严格拒绝。
