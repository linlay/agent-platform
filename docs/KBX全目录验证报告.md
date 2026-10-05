# KBX 全目录知识库验证报告

日期：2026-10-05；环境：macOS arm64。

本次递归检查 `/Users/linlay/Documents/知识库测试用文档` 的全部 10 个一级子目录，以一级目录为独立知识库，覆盖其下所有层级。根目录直接存放的两份 DOCX 和一份 PDF 不属于本次子目录建库范围。

Platform 当前源码为 `aad4d4d1`。受管 KBX 为本机实际缓存版本 `0.1.0`，manifest commit `fea7cc7accade629e05a4da8c0d8bb7a2dc2eff9`，二进制 SHA-256 `8f3a70296e647d6e8d8d3919c0874ef62688ba7a83cff028b56a1c805952db2e`。这与 10 月 4 日验证记录中的 KBX 版本不同，本报告依据当前实际二进制实测，不沿用旧报告结论。

## 结论与覆盖情况

可以按一级目录建立独立知识库，并递归包含下级目录；当前结果不是“所有文件都成功”。9 个文档目录产生可检索索引：5 个支持的正文文件全部入库，4 个部分成功；另外 1 个目录是旧索引产物，不作为正文入库。共发布 **1,445 条文档记录**，这是各库条目之和，不是跨库去重数。

| 目录 | 本次入库 | 结果与缺失 |
|---|---:|---|
| 2026基金从业✅ | 232 PDF | 支持的正文全部入库，重试后成功 |
| AI建设文档 | 16 | 13 DOCX、2 PDF、1 PPTX，正文全部入库 |
| 冒烟文档 | 27 | 23 MD、4 PDF，正文全部入库 |
| 研发中心各条线述职 | 19 | 18 DOCX、1 PPTX，正文全部入库 |
| 秋而文档 | 479 | 453 MD、22 RT、3 YQ、1 TABLE；已入库，不代表自定义格式全部具备语义结构 |
| 参考规章制度 | 173 | 原库回滚；副本排除 1 份异常 DOCX，另 5 份扫描 PDF 失败、4 份旧 DOC 不支持 |
| 期货运营 | 186 | 原库回滚；副本排除 2 份日期元数据异常 DOCX，4 份旧 DOC 不支持 |
| 沪深300_2025年年报_PDF | 300 | **297 PDF + 3 CSV**；3 份 PDF 失败，不能写成 300 份年报成功 |
| 研发中心中期会议汇报人材料 | 13 | 原库回滚；副本排除 3 份 PPTX，视频与 ZIP 未入库 |
| kbase-基金从业 | 不建库 | 742 个旧 DB/Lance/manifest 等产物或辅助文件，不是原始知识正文 |

系统隐藏文件、AppleDouble 旁车、旧索引与 unsupported 文件不计入成功正文。详尽源文件清单和各扩展名数量在 `inventory.json`、`source-before.json`。

## 验证结果

- 9 个最终文档库共 45 组查询、225 次完整证据逐字回读，全部一致。另建 10 份年报的样本库，25 次回读通过；样本不计入上述 1,445 条覆盖数。
- 通过临时 Go fixture，将已建库数据库交给当前 Platform `kbx.Manager` 的真实 CLI runner，逐库验证 status、完整文件列表、查询及 5 条 chunk 回读：9 库共 45 条回读通过。fixture 显式路由已有数据库，并使用临时运行目录；这属于读取适配测试，不是正式 Agent/API 部署验收。
- `TestLiveChunkAndFilterContract` 通过：同文档多 chunk、目录边界、扩展名大小写、证据读取及行分页。
- `TestLiveVectorPrefilterAndLibraryIsolation` 通过：使用仅本机 HTTP mock 和人工文本，验证向量配置/鉴权/过滤/隔离。首次因沙箱禁止监听而失败，允许本机测试后重跑成功。
- 全部 **2,279 个源文件**的相对路径、SHA-256、字节大小和修改时间前后相同，源文档未变。该校验覆盖旧索引文件和根目录文件；根目录文件仅校验，不建库。
- 本次未修改生产源码、真实配置或正式 Agent，未运行完整仓库回归；测试代码归档在试验目录，未留在 `internal/kbx`。

## 年报大库性能与失败文件

年报库一次建库实测 **796.62 秒（约 13 分 17 秒）**；日志显示主要等待发生在全文索引写入阶段，不能仅凭日志进一步断言具体函数瓶颈。1 秒进程采样显示曾达到约 **3.0 GiB 峰值内存**；这是本机单次观察，不是容量保证。10 份按文件名排序选取的年报样本建库耗时约 30.7 秒，样本执行期间全量任务也在运行，不能据此线性外推。

失败的三份 PDF 为：

- `002532_天山铝业_2025年年度报告.pdf`：无文字层，需 OCR。
- `600886_国投电力_国投电力控股股份有限公司2025年年度报告.pdf`：约 74.15 MiB，超过 50 MiB 抽取限制。
- `601899_紫金矿业_紫金矿业集团股份有限公司2025年年度报告.pdf`：约 76.22 MiB，同上。

年报库原目录直接发布部分结果，未在源目录删除或拆分这些文件。包括副本、索引、日志及样本的整个试验目录当前约 4.8 GiB，不能当作纯索引大小或稳定压缩比。

## 方法与范围

- 使用 `collection add` 建立离线文本索引；分块为 3600 字符、重叠 540 字符。
- embedding、query expansion、reranker、graph 均显式关闭，真实文档没有送往外部模型。
- 每库执行 5 组查询（管理、风险、系统、工作、测试），每组最多 5 条；逐条使用 evidence ID 回读并比较完整文字。
- 原目录只读。部分库通过复制文档到独立试验目录、排除阻塞文件建立可用子集；这类结果必须标为部分成功。
- 独立原库、部分库、来源快照、命令输出、排除清单、测试脚本均保存在被 Git 忽略的 `build/kbx-assessment-20261005/`。试验库未注册为正式 Agent，也未迁入 Platform runtime。

## 主要阻塞

### Platform 维护链路尚未交付

`internal/app/app.go` 已构造 `kbx.Manager`，检索、列表和回读走新 KBX。`internal/kbx/manager.go` 中 `Start`、`ReconcileWatchers` 仍为空实现；refresh/wait 返回 unavailable。成功导入离线索引不等于生产平台已支持自动建库、文件变更刷新、断线重连和重启恢复。当前 status 仍会标记 stale/degraded。

### 期货运营：两份 Office 日期元数据使整库回滚

两份 DOCX 的 `docProps/custom.xml` 中 Created/filetime 缺少时区：

- `交易所规则/中金所/中国金融期货交易所风险准备金管理办法.docx`：`2026-04-27T09:14:34`。
- `交易所规则/中金所/合约细则/中国金融期货交易所中证1000股指期货合约交易细则.docx`：`2026-04-27T13:12:51`。

错误为 `metadata collection failed ... premature end of input`。在副本中排除两份后可建库 186 份（185 DOCX、1 XLSX）。另外 4 份旧 DOC 不支持，未算入成功数。建议 KBX 对非关键 Office 元数据采取可追踪的容错策略，避免阻断正文。

### 参考规章制度：损坏或格式不符的 DOCX 与扫描 PDF

`附件：《期货公司期货投资咨询业务试行办法》.doc.docx` 报 `invalid Zip archive: Could not find EOCD`，导致整库回滚。排除该副本后发布 173 份，另有 5 份 PDF 无文字层，需 OCR：

- `《元数据注册系统(MDR)　第1部分：框架》GBT 18391.1-2009 .pdf`
- `《证券及相关金融工具 交易所和市场识别码》GB_T 23696-2017.pdf`
- `金融标准化十四五规划(银发[2022]18号).pdf`
- `（WG1 通用基础）GBT 23696-2017 证券及相关金融工具交易所和市场识别码.pdf`
- `（WG1 通用基础）GBT 39601-2020 证券及相关金融工具 金融工具短名（FISN）.pdf`

另外 4 份旧 DOC 不支持。173 份中为 159 PDF、14 DOCX。

### 中期汇报：截图 PPTX、超大 PPTX、视频与压缩包

以下三份 PPTX 导致整库回滚：

- `04-叶召正-底账平台汇报/TakeFive底账平台汇报-截图版.pptx`：无可抽取文字。
- `10-丁荔-代码的“下沉”与“民主化”：我们是如何改变协作模式的.pptx`：超过 50 MiB 抽取限制。
- `11-班涛-Agent场景应用-智能办公汇报.pptx`：超过 50 MiB 抽取限制。

排除后可用 13 份（9 PPTX、2 HTML、1 PDF、1 MD）。20 个 MP4（含目录内资源文件）和 1 个 ZIP 不直接入库；视频需转写，ZIP 需先解包，截图需 OCR，超大文件需拆分或调整上游抽取策略。第三个 `.html` 文件是 `._` 资源旁车，未计为正文文档。

### 检索与表格边界

部分文件出现 `grid_only`、`needs_definition` 表格诊断。正文可搜索不代表所有单元格都能由普通查询召回，也不代表已验证财务表格计算。Platform 目前仅封装五个知识库工具，没有接入 KBX 原生 tsearch 表格查询协议。本轮不宣称验证表格数值问答、OCR、视频理解或真实向量模型的语义质量。

Platform 默认 include 只包含 MD/TXT/HTML/HTM/PDF/DOCX/PPTX。CLI 入库的 XLSX、CSV 及秋而目录中的 RT/YQ/TABLE，如要通过 Platform 检索，需显式扩展 Agent 的 include；本轮真实库适配测试采用了该扩展。

### PDF 抽取器启动探测

首轮基金、AI 和冒烟库曾报 `PDF extraction requires pdftotext`。独立执行受管 pdftotext 确认可用后，明确指定 `KBX_PDFTOTEXT` 并重试，全部相关 PDF 入库。首次探测失败的根因未确定，不能归因于文档损坏，也不应以退出码 0 判断全库成功；必须检查 indexed/failed/partial。

## 下一步建议

1. 优先补齐 Platform 与 KBX 的后台 update/重连/恢复契约，才可把这些测试库作为持续维护的正式知识库。
2. 修复 KBX 元数据容错与单文件失败隔离；保留错误清单，避免一份可选属性异常拖垮整库。
3. 对扫描件、旧 DOC、截图 PPT、视频及超大文档建立保留原件的预处理流程。
4. 按上述一级业务目录分库；`kbase-基金从业` 是旧索引产物，应从原始基金 PDF 重建，不能把 Lance/DB 文件当知识正文。
5. 接入获准的真实 embedding 模型后，另做有标准答案的语义检索与引用准确率测试。本轮证据回读一致性不等于问答准确率。

## 已保留产物与复用方法

本地试验根目录：`/Users/linlay/Project/zenmind/agent-platform/build/kbx-assessment-20261005`。

- `final-summary.json`：最终九库路径、文档数及格式；应读此文件，而不是首轮 `results.json` 中重试前的状态。
- `verification.json`：查询命中数量、逐字回读结果与示例引用。
- `platform-real.log`、`platform-annual.log`：Platform 真实库读取适配测试。
- `platform-contract.log`、`vector-contract.log`：受控合约测试（前者包含首次沙箱失败，后者为成功重跑）。
- 各库的 `build*.stderr`、`retry-build.stderr`：完整失败、回滚及重试过程。
- 三个 `partial-*` 目录的 `excluded.json`：明确排除文件；`source/` 为复制后的可用子集。
- `source-before.json`、`source-after.json`：源文件内容与属性校验。
- `run.py`、`retry.py`、`partial.py`、`retry-futures.py`、`annual-sample.py`、`verify.py` 和 `assessment_local_test.go.fixture`：本次试验脚本。构建脚本为本轮记录，重跑前应使用新的输出目录，避免重复注册 collection。

以下只读命令可直接复用已建成的基金库（其他库替换 `--kb` 为 `final-summary.json` 对应路径）：

```sh
cd /Users/linlay/Project/zenmind/agent-platform
build/builtins/darwin-arm64/bin/kbx \
  --kb 'build/kbx-assessment-20261005/01-2026基金从业✅/index.sqlite' \
  --config build/kbx-assessment-20261005/config.json \
  search '风险' -c workspace --agent --full
```

这批产物位于可清理的本地 build 目录，不属于正式分发或正式业务数据。正式落地前，应确定 runtime 存储、Agent workspace/include 和维护协议；尤其不要把部分库的复制目录误当成已与原文档自动同步。
