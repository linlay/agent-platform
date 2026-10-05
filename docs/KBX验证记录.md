# KBX 验证记录

2026-10-04；Platform 基线 `1d8c4cbc`，KBX 源码 `c5ff759`，macOS arm64；重新执行 `cargo +stable build --release --locked --bin kbx -j 2`，没有使用先前缺少 filter 参数的旧二进制。

## 真实知识库

来源：`/Users/linlay/Documents/知识库测试用文档`。每个子目录建立独立临时 KBX 索引，测试后清理；原文档不改写、不转换、不删除。未使用 `kbase-基金从业` 中的旧索引文件作为源文档，也未扫描大型视频及年度报告目录。

| 子知识库 | 已索引文档 | 格式 | 精确证据回读 | 结果 |
|---|---:|---|---:|---|
| 冒烟文档 | 27 | 23 MD、4 PDF | 25 次 | 通过 |
| AI建设文档 | 16 | 13 DOCX、2 PDF、1 PPTX | 25 次 | 通过 |
| 研发中心各条线述职 | 19 | 18 DOCX、1 PPTX | 25 次 | 通过 |
| 期货运营 | 本轮整体回滚 | DOCX/XLSX，另有旧 DOC | 未继续 | KBX 元数据解析失败 |

前三个库测试了 status、完整文件清单、5 组查询、精确文件 glob、大小写扩展名、子目录前缀以及 chunkId 回读。返回的完整 chunk 文本与回读内容逐字一致。源文件内容、路径、大小和修改时间的综合 SHA-256 前后相同。KBX 构建日志包含 grid_only 警告，因此没有把全文通过解读为全部表格单元格都可由普通文本搜索召回。

### 期货运营阻塞

文件：`交易所规则/中金所/中国金融期货交易所风险准备金管理办法.docx`。

`docProps/custom.xml` 中 `Created` 的 `vt:filetime` 值为 `2026-04-27T09:14:34`，没有时区。KBX 在 `src/metadata/collect.rs` 将其标为 datetime，`src/metadata/store.rs` 使用 RFC3339 强校验，报 `premature end of input`；`src/indexer.rs` 将元数据失败升级为整个 collection 回滚。

这是实际原文档复现，未通过改写文档或跳过文件把整库标为成功。KBX 可考虑对无法规范化的可选 Office 属性保留原始文本或记录有界警告，而不是阻断正文索引。4 份旧 `.doc` 另被报告为不支持，需单独确定转换策略。

## 受控集成与单元测试

- 人工长文档真实 CLI 检索一次返回同文件的 5 个不同 chunk。
- `allowed` 前缀不匹配 `allowed-old`；`.MD` 匹配 `.md`；空范围返回空结果；精确 chunk 回读、按一基行号翻页、非法路径拒绝均通过。
- 本地确定性 embedding 服务验证了模型配置、鉴权、embed 后的纯向量召回、向量召回前过滤及知识库隔离。没有外发真实文档；不声称验证了真实模型语义效果。
- worker 测试覆盖同库复用、异库并行、取消等待者、重新连接、关闭和错误传递。真实 KBX singleton update/进程断线续跑尚不可测试。
- KBX/app/kbase/reload/llm/tools 的 race 检查通过；Server 普通回归通过，但扩大 race 检查仍有以下基线问题。`CGO_ENABLED=0 go build ./cmd/agent-platform`、`go vet ./internal/kbx ./internal/app` 和 `git diff --check` 通过。
- Server race 下的 `TestDeferredPlanningApproveContinuationUsesCoderExecuteSystem`、`TestSelectionExplainWebSocketLaneGuardsAndSequentialStreams`、`TestProxyWebSocketHTTPSSEObserverTerminatesInvalidEventWithLocalTimeContractError`、`TestStartRunRegistersIndependentAgentAndTeamRuns` 失败；在未修改基线副本上四项均复现。其中 proxy_ws_handler.go 的 WebSocket 并发写被 race detector 捕获，其余涉及异步断言或临时目录清理时序，未扩展本次 KBX 修改去修复这些独立问题。不能把整套 race 验收记为通过。
- config 回归有一项基线失败：`TestLoadDefaults` 期望 default WriteRoots 包含 `@workspace`，实际是 `@chat,@temp`。在 `git archive HEAD` 生成的未修改基线副本中同样复现，未在本次修改权限默认值。

完整 opt-in 真实文档测试目前会因期货运营库失败而返回失败；这是保留的验收阻塞，不是全部通过。


## Builtin 同步与发布验证

2026-10-04，在 Platform Git worktree 中省略 `--builtins-root` / `--connectors-root`，实际运行 `bash scripts/sync-local-builtins.sh --target darwin/arm64` 成功；自动找到主仓库旁的两个项目。源码仅在隔离目录编译，KBX 版本 1.0.0，commit c5ff7599ea4ca86851dbb749d1179fb1b76125b7。

`make release-program PROGRAM_TARGET_MATRIX=darwin/arm64` 成功，产物 `dist/release/agent-platform-v0.4.28-darwin-arm64.tar.gz` 通过 verify-program-bundle。归档含 bin/kbx、licenses/kbx/LICENSE、THIRD-PARTY-LICENSES.txt，不含旧 kbase-lance-engine。KBX payload SHA-256 为 `126260614a3d518c1b4afc8850a3d77478d1b519cf86e98cd06b35641ddf2078`。

使用新 cache 中的 KBX 执行 TestLiveChunkAndFilterContract 通过：单文件返回 5 个独立 chunk，目录前缀边界、扩展名大小写、证据回读和行分页通过，本地 mock embedding 合约通过。builtins、prepare-local-builtins-lock、verify-program-bundle、render-program-manifest 测试通过；新增测试覆盖 worktree 默认路径、显式覆盖、旧 cache 拒绝和 archive 许可证的缺失/空内容/校验和异常。Shell 语法、Python 编译检查及 diff 空白检查通过。

同步对 Poppler 同版本不同 SHA、dbx/httpx 本地 commit 与正式 lock 不匹配保留了既有告警与拒绝回写规则；本地 cache 和服务包构建成功。未验证 Windows PowerShell 和其他架构；Linux/Windows ARM64 尚无 KBX 发行支持。此项完成不改变 singleton update 协议仍待接通的状态。
