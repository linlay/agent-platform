# KBX 接入

## 当前结论

Platform 的 app 装配、五个知识库工具、HTTP status/refresh、health 和 wait 入口已接到 `internal/kbx.Manager`。旧 `internal/kbase.Manager`、目录 watcher、generation 和 Lance sidecar 不再从生产入口实例化，也没有自动回退。旧代码和数据保留；共用的配置、DTO、工具权限校验、引用发布和文件列表格式化仍保留在 `internal/kbase`。

**这是读取链路切换，尚不是可上线的完整索引维护闭环。** 对照 KBX `c5ff759`：单库单例、后台执行、断线重连的 update 协议尚未实现。Platform 的 refresh/wait 明确返回 unavailable；status 标记 stale/degraded；有必需知识库的 health 返回未就绪。不会运行旧 update/embed 来冒充新维护协议。

## 已接入行为

- `kbase_search` 使用 KBX `query --agent --no-graph --no-rerank --full`，校验 Agent envelope v2、retrieval contract v6 和 `resultUnit=chunk`。同一文档可返回多个 chunk，保留 chunkId、evidenceId、resultId 和抽取文本行号。
- `pathPrefix`、`pathGlob`、`type` 转成 KBX 原生 JSON filter；include/exclude 同样在召回前传入。目录前缀尊重边界，扩展名忽略大小写。没有先取 top N 再用 Go 过滤。搜索不支持 offset，非零值明确报错；不伪造精确 matchCount 或分页完成标志。保留降级、候选预算和实际召回通道信息。
- `kbase_read` 通过 chunkId（内容哈希和字节范围）精确回读，或以相对路径和一基行号 offset/limit 读取抽取文本。拒绝跨 collection 引用、目录越界和被配置排除的文档。原始 PDF 页码、PPT 页码不伪造。
- `kbase_files` 读取 KBX 完整 active 文档清单，再做目录树、通配符与分页展示；不触碰旧 control.db。未索引、失败、删除状态不冒充 active；不支持的 status 明确拒绝。不把 KBX 抽取正文的 bytes 当原文件大小。
- `kbase_status` 返回 engine=kbx、文件数和字符切块信息。当前 KBX agent status 不提供准确 chunk 总量，省略 chunks 并返回 chunksKnown=false。
- CLI 只从受管 builtin 目录解析 kbx，不走普通 PATH；独立私有配置、argv 调用、有界输出、临时凭据文件 0600 并在完成后清理；不回显原始模型诊断。
- 保留 Agent 挂载、执行上下文授权、KBASE editingMode 与源文件权限边界。

## 存储与配置

模型选择统一来自 `runtime.yml → kbx.embedding`（model-key、prompt）；中心与 Agent capability 共用部署级连接来源，Agent embedding 字段已退役。受管快照通过 --config 显式注入，调用前刷新注册表连接变化，旧 KBASE 引擎设置下线。见 [Agent 配置合并](Agent配置合并.md)。

默认新索引位置为 `<AP_RUNTIME_KBASE_DIR>/<agentKey>/kbx/<scopeHash>/index.sqlite`；workspace 存储为 `<workspaceRoot>/.kbx-platform/<agentKey>/<scopeHash>/index.sqlite`。scopeHash 包含解析后的 workspaceRoot、include/exclude 和 chunk 配置，防止切换 Workspace 或内容范围后复用错误索引。每个 Agent 隔离；库内路径拒绝符号链接替换。旧 `.kbase`、control.db、generations 保留，不迁移、不删除。

旧默认 1000 estimatedTokens/100 overlap 映射为 KBX 默认 3600/540 字符。自定义切块需明确改为 `unit: chars`；不猜测自定义 token 数的字符换算。旧非默认 RRF 权重明确拒绝；排序由 KBX 决定。topK 与候选预算继续映射。

Embedding 从 Platform 模型注册表映射 endpoint/model/API key/timeout，私有配置关闭 query expansion、reranker 和 graph。构建 fixture 使用 raw prompt。KBX 使用自身 HTTP 客户端；目前没有把 Platform 系统代理/PAC 行为移植给 KBX，loopback 测试需要 NO_PROXY。这不是代理行为已对齐的声明。

## 待接 update 协议

约定：KBX 对每个知识库用一把锁保证 update 单例。Platform 为该库开启后台 worker，运行可能耗时很长的 update。Platform 断联后再次运行同库 update，能获取原任务状态或最终结果；不同库可并行。

`internal/kbx/worker.go` 已验证：同库连接复用、异库并行、取消 HTTP 等待者不取消 worker、连接结束后显式重连、关闭本地连接。**它目前未接到生产维护调用，也未证明 KBX 任务能在进程退出后继续运行。** 该承诺必须由 KBX 新协议实现。

需要 KBX 提供参数、协议版本、任务 ID、进度和终态、连接失败与任务失败的区别、如何区分重连和新一轮更新，以及成功是否包含向量完成。之后补齐真正的后台调度、持久化运行状态、重启恢复和 wait 终态。旧一次性 update 的文本输出不能作为该协议。

## 分发边界

运行时依赖受管 builtin 目录中的 kbx（开发环境可用 AP_BUILTINS_BIN 指定目录）。KBX 已接入 builtins lock/cache/release：同步从相邻源码的 Cargo.toml 读取版本，在隔离目录构建；archive 同时包含二进制、LICENSE 和依赖许可证清单，校验 SHA 后进入 cache。正式发布必须包含 bin/kbx；旧的仅含 kbase-lance-engine 的 cache 会报错，旧 sidecar 不再随包分发。

```bash
bash scripts/sync-local-builtins.sh --target darwin/arm64
make release-program PROGRAM_TARGET_MATRIX=darwin/arm64
```

两个源码目录默认取相邻的 agent-platform-builtins 和 agent-platform-connectors；Git worktree 自动回退主仓库的相邻项目。显式路径参数和环境变量仍可覆盖。当前上游仅提供 darwin/arm64、darwin/amd64、windows/amd64 发行目标，Linux/Windows ARM64 在同步前明确拒绝；本机验收仅覆盖 macOS ARM64，Windows PowerShell 及其他架构还需对应 runner 验证。

## 验证

详见 [KBX 验证记录](KBX验证记录.md)。真实文档测试为 opt-in；所有索引位于测试临时目录，文档内容、大小和修改时间在前后校验。测试里的 collection add/embed 仅负责构造读取 fixture，未进入生产维护链路。

```sh
KBX_ACCEPTANCE_SOURCE='/Users/linlay/Documents/知识库测试用文档' \
KBX_ACCEPTANCE_BIN='/path/to/current/kbx/bin' \
go test -v ./internal/kbx -run 'TestRealKnowledgeBases|TestLiveChunkAndFilterContract|TestLiveVectorPrefilterAndLibraryIsolation' -count=1
```

向量测试启动 loopback HTTP 服务，仅使用人工文本和确定性向量，不把用户文档发送给外部模型，也不等于真实 embedding 模型的语义质量评估。
