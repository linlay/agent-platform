# Shell 命令审批：剩余工作计划

> 2026-11-01 合并：原 `shell-command-analysis-implementation.md` 已并入本文并删除，剩余工作只看这一份。第 0 节是接手入口和按提交排的清单；第 1–8 节是背景、设计合同与已定决策；第 9 节是验证与完成定义；附录是回归样例、评审意见摘要和证据。
> 代码现状：Shell 审查最近一次改动是 `bbed0bbf`（C1 开工基线为 `b8ca2ad6`）。S1、S2（含两轮复审补丁和选项补丁）已完成，S0 完成首批，S3 的 C1–C5 已完成，C6（S4）的 C6a–C6g 已完成；S5–S8 未开始，下一步可独立推进 C11u（Unix 资源），Windows 的 C7–C10 仍须逐步满足原生验收。Windows 相关结论都没有在原生 Windows 上端到端运行过。
> 已完成工作的过程记录在 git 历史中（提交见 §0.1）；本文只保留后续还要用到的事实与证据（附录 C）。

## 0. 接手入口

### 0.1 已完成

| 步骤 | 提交 | 内容 |
| --- | --- | --- |
| lsof 与 curl/wget 空设备局部修复 | `9731155e` | 见附录 C.1；curl/wget 的 `/dev/null` 特例在 S6 删除 |
| S1 工具专用 LaunchSpec | `5554545f` | `hostshell.ResolveTool` 生成工具专用启动合同，与 Terminal、Git Bash 开关分开；Unix 默认参数不变；精确批准绑定完整启动调用，启动前复核不一致返回 `bash_launch_changed` |
| S2 未知方言合同 | `5554545f`；复审补丁 `ef0b41f4`、`fd11f873` | 非系统 Bash、PowerShell、cmd、MSYS、登录/交互模板整条走 6.6；文本检查分两级；系统 Bash 冻结启动时 PATH 选中的那一项；本轮规则批准只绑定 Shell 身份、模板与方言 |
| S2 补丁：选项与隐式配置 | `addef42c` | UnknownOption 追加单次 complex；curl `-K`、wget `--config` 与非空的 `CURL_HOME`/`WGETRC`/`RIPGREP_CONFIG_PATH`/`TAR_OPTIONS` 走 opaque。默认配置文件探测、`HOME` 改向留到 S4/S6 |
| S0 首批基线 | `addef42c`、`2fe8bb52` | `bash_baseline_test.go`；三档 387 行规范化快照（迁移前生成，已在迁移前源码上复核） |
| S3 首批迁移 | `2fe8bb52` | `bashast`、`bashsec` → `syntax/bash`；`shellanalysis` → `commands` 与 `unwrap/command.go`；`types.Invocation`/`Effects`；参数与重定向目标保留字面量来源（`WordSpan.Literal`、`Redirect.TargetWord`；C6b 已将参数来源带过包装，尚未用于新资源抵消） |
| C1 / S3 整体搬迁 | `0c794567` | 9 个 Bash 文件、对应测试与快照移到 shellreview 根包；5 个通用路径辅助函数导出；中立文本检查移到 commandtext，消除经 config 的传递依赖。函数体与权限行为保持，验证见附录 C.3 |
| C2 / S3 plan | `115e7ef4` | 决策、合并、启动绑定及批准移到 plan；连接器调用数据归 types；SecurityReview 留根包函数，调用方与现有测试同步。319 个函数/方法及 3 个结构的 AST 一致，387 行快照不变 |
| C3 / S3 identity + unwrap | `11b404ae` | 身份、脚本与连接器核验移到 identity；包装到 unwrap；交替调度留根包；执行事实与探针合同归 types。测试改用显式系统快照，另加隔离回归 |
| C4 / S3 resources | `f00f0da5`；容器边界修复 `93ee79a9` | 资源、Git 缓存、glob、旧重定向与摘要移到 resources，使用窄输入；独立补上缺少容器摘要探针时禁止 Host 同名路径回读，新增六种环境组合的回归 |
| C5 / S3 policy 与入口 | `058e808a` | 根入口、policy 映射/硬限制/凭据抵消完成；脚本证明与路径事实归各自层；旧 Bash 入口文件删除，公开 HITL 合同保持。1608 个固定环境完整计划差分一致；S3 依赖门槛通过，下一批 C6 |
| C6a / S4 首批结构 | `a9cb5c94` | 中立选项合同进入 types/options.go，共享解析与规格辅助进入兄弟 options 包；sed、awk、jq、find、pdftotext 分文件。45 个原声明 AST 一致，1608 个完整计划差分一致；C6 尚未完成 |
| C6b / S4 包装参数来源 | `fa09efee` | BashExecution.Words 与包装后的 Argv 对齐，保留源码范围、原 argv 下标与来源类别；env -S 生成词非字面，直接附加参数独立保留来源；旧 env 入口已删除。1608 个完整计划差分一致；registry 与细化事实尚未完成 |
| C6c / S4 名单与网络文件 | `c6294b4d` | 共享名字、身份类别、安装捕获与硬阻断收敛到纯数据 registry 叶子；curl/wget/rg 各文件；部署白名单独立，原有认可范围不扩张；1608 个完整计划差分一致 |
| C6d / S4 结构化事实 | `51cfce5b` | Effects 改为 Ops/Unknowns/Deps/SSHAgent；ResolveIdentity 返回命令域与来源，Analyze 同时核对 Bash 方言；选项解析按下标和 UTF-8 值范围保留来源，根包已消费新事实；默认配置的资源核验仍归 C11u |
| C6e / S4 多目标行为修复 | `6b3fcee1` | cp/mv/ln 各文件；源、目标、替换对象、目录内派生目标、相对软链接与硬链接源分别保护；guest 类型失败不读 Host；三档只读漏检在旧代码红、当前绿；旧快照不变 |
| C6f / S4 包装差异 | `44f1ad38` | 两套非 env 包装语法合到 unwrap/wrappers.go；执行与仅查找硬阻断候选的用途显式区分，env 共用 Env；1764 个旧新案例保留全部旧硬阻断，另补 36 个阻断；两套审批对象仍留到 C12 |
| C6g / S4 命令语义审计 | `bbed0bbf` | 未知选项保留危险事实并保护可见子树；Git 输出/配置/连续 -C 与仓库子树、源的读写分别核验；Wget 请求体来源、派生输出目录与 printf 状态变更补回归；387 行旧快照不变，原生 Windows 未启用 |
| 设计决定 | 本文 §8.1 | 原待确认第 1、2、3、4、6 项，以及 2026-11-01 的 Windows 补充决定；第 5、7 项已有落地建议，待原生验证（§8.2） |

### 0.2 当前代码入口

- 启动合同与系统 Bash 判定：`internal/hostshell/tool.go`（`ResolveTool`、`classifyToolShell`、`toolSystemShells`）。
- 文本检查：`internal/commandtext/text.go`（`CheckText`、`CheckHidden`、`CheckAll`）、`syntax/bash/prechecks.go`（`CheckRawText`，Bash 引号外）；Bash 解析与旧 bashsec 审查均在 `syntax/bash`。
- 身份与脚本入口：`internal/shellreview/identity/{identity.go,paths.go,scripts.go,connector.go}`；根包 `execution.go` 保留身份→可信包装→重新确认身份的交替调度，`unwrap/{execution.go,wrappers.go,env.go}` 共用包装语法；syntax/bash 只消费不授予身份信任的硬阻断候选。临时依赖边与两套审批对象保持到 C12。
- 名称与身份类别：`internal/shellreview/registry/registry.go`；`commands/registry.go` 只按其 ID 聚合分析函数，命令名不再各存一份。
- 中立调用与效果：`internal/shellreview/types/invocation.go`、`effects.go`、`command.go`、`source.go`（Op/Target/Unknown/Dep、域/来源/方言与选项值范围）；执行事实与环境探针合同：`types/execution.go`；包装后来源：`BashExecution.Words`/`types.SourceWord` 与 `unwrap/{env.go,source.go}`；来源判定：`syntax/bash/provenance.go`；命令语义：`commands/`；共享包装辅助函数：`unwrap/command.go`；选项合同与通用 POSIX 算法：`types/options.go`、`options/`。
- 根包调度：`internal/shellreview/review.go`；按方言选择文本检查与 cwd 默认值：`input.go`；外围语法审查：`bash_connector.go`（`SecurityReview`）。
- 政策映射与硬限制：`policy/{mapping.go,hardlimits.go}`；旧 curl/wget 空设备局部规则暂留 `policy/legacy.go`，C11u 删除。
- 资源入口：`internal/shellreview/resources/{gitstate.go,paths.go,targets.go,fingerprint.go,redirects.go}`；旧空设备分类保留到 C11u。
- 决策与批准：`internal/shellreview/plan/{plan.go,combine.go,approval.go}`；启动绑定：`plan/launch.go`（精确批准用 `Fingerprint`，规则用 `RuleFingerprint`）；跨层连接器调用结构在 `types/connector.go`。
- 行为基线：`internal/shellreview/bash_baseline_test.go`、`bash_snapshot_test.go` 与 `testdata/bash-review-baseline.txt`。

### 0.3 剩余工作（按提交顺序）

规则：
- 每行可按编译边界拆为多个提交，每个提交都能编译；结构迁移和行为变化不混在同一提交。
- 迁移旧入口时，同一提交改完所有调用方并删除旧入口，不保留转发别名。两套授权对象在 S7 之前按现有合同共存，纯搬家时不改形状。
- 387 行快照只在行为变化经审核后才更新（附录 C.3）。
- Windows 的额外合同未通过原生验证前，不设置 SyntaxSupported=true；未通过的能力保持 6.6，或保持对应目标 Unresolved。
- S0 剩余的名单审计贯穿 C1–C6：`bash_snapshot_test.go` 已显式覆盖当前 ordinaryCommands、shellBuiltins、wrapperNames 和解释器/build/opaque 示例，连同连接器、技能、自写脚本及 readonly/多要求案例，得到三档 387 行规范化快照（迁移前生成）。排序叶子并去掉机器相关摘要，比较决策、规则类别、作用域、叶子数量和 Shell 安全审查；现有测试继续覆盖单次、规则、跨 cwd/环境的批准作用域。C6 已完成命令元数据、语法硬阻断与严格模式名单的统一核对；配置默认白名单仍独立。registry/testdata/pre-registry.json 固定了迁移前各名单；原本只有身份条目而没有分析器的命令继续 opaque，不因收敛名单而自动通行。后续行为变化继续单独回归，不能把 387 行写成完整语法或平台验收。

| 批次 / 原阶段 | 要做的具体内容与文件 | 前置 | 完成门槛与失败处理 |
| --- | --- | --- | --- |
| C1 / S3 整体搬迁（已完成） | 9 个 Bash 文件及 Bash 测试/快照已移到 shellreview 根包；通用路径包导出 5 个函数；中立文本检查移到 commandtext，调用方已同步（细节见下） | 已完成 | accesspolicy 的全部生产依赖不含任何 shellreview；387 行快照逐字节不变；Windows 交叉编译通过，不代表原生验收 |
| C2 / S3 plan（已完成） | `plan/` 已承接 BashPlan、构造函数、合并、批准登记/查询/消费、规则、元数据及启动绑定；连接器调用数据移到 types；SecurityReview 改为根包函数，调用方同步 | C1 已完成 | 无环；精确/规则/多叶子/一次性批准保持；tools/llm 通过 plan（import 别名 shellplan）使用公开合同；无旧转发别名，快照不变 |
| C3 / S3 identity + unwrap（已完成） | 身份、PATH/容器身份校验、解释器/脚本与连接器入口移到 identity；包装算法移到 unwrap/execution.go；根包 execution.go 保留交替调度，旧 bash_execution.go 删除；跨层执行/探针合同先移到 types，测试显式传系统安装快照 | C2 已完成 | 假 env/bash 不展开；原连接器/技能/自写脚本合同保留；生产安装表保持私有且只返回副本，无测试全局改写；原 syntax/bash 包装算法不动，差异留 C6 |
| C4 / S3 resources（已完成） | resources/{gitstate.go,paths.go,targets.go,fingerprint.go,redirects.go} 承接资源检查、缓存与旧分类；摘要只接收执行版本字段和探针；Git 只接收容器标志；另提交容器缺失探针的 Host 回退修复 | C3 已完成 | 旧设备/路径规则、缓存键/TTL 与 glob 限额不变；容器缺失/失败探针均不读取 Host 同名文件，真实容器运行仍待目标验收 |
| C5 / S3 policy 与入口（已完成） | review.go 承接根包调度；policy 映射路径、动作、凭据与硬限制；脚本证明检查归 identity，opaque 路径事实归 resources；旧入口文件删除，HITL 公开合同保持 | C4 已完成 | S3 包图门槛通过；根入口无特殊命令字面量；unwrap 为纯标准库，resources 不依赖 plan；387 行快照不变，1608 个固定环境完整计划差分一致 |
| C6 / S4 命令与事实（已完成） | registry 为唯一命令定义来源；复杂 NativeCLI 各文件；共享算法到兄弟 options/；建立 Op/Unknown/Dep、Origin、Target/SourceWord；包装展开后每个参数仍能对应回原 WordSpan，`env -S` 拆出的一律非字面；合并两套包装的差异独立审阅 | C5 | 名单审计完成；Read/Output 与 Create/Delete/Rename/Chmod/Link 不混；复制/移动/链接的源、目标、被替换对象各自核验；未知保留已知危险事实；native/cmdlet/内建不按名字混用；新增行为逐项测试，不重刷旧快照掩盖变化 |
| C7 / S5a Windows 安装与环境信任 | hostshell/powershell.go：可信安装入口、canonical/摘要、引擎/配置依赖及启动代码注入环境；确认商店别名与安装更新策略 | C6；可先做隔离原型 | 未证明信任时 helper 零启动；不能先运行假 pwsh/用户模块再辨身份。原生检查所支持运行时的装载入口，无法覆盖则 6.6 |
| C8 / S5b helper 与版本 | syntax/powershell/{helper.ps1,helper.go,profile.go,ast.go,positions.go}、tools/powershell_helper.go；一次进程完成受限解析/元数据；版本能力与 UTF-16→UTF-8；协议/预算/并发/取消 | C7 | Go 假 helper 故障覆盖；实际 5.1 与部署固定的 7.6 patch 的协议/AST/版本证据；7.4/7.5、Preview、未知版本走 6.6，不套用已知 profile；helper 解析前缀加用户命令的整段文本，含 `using` 的命令不启动 helper；本批不自动启用命令免审批 |
| C9 / S5c 模块与执行一致性 | identity/powershell{,_modules}.go；前置核验内置依赖；两端按绝对路径导入同一组可信模块，helper 关闭自动加载、执行端保留；别名/正式名/参数元数据；执行端前置守卫 | C8 | 未知模块初始化标记不出现；helper 与执行端模块/身份相同；5.1/7.6 实测"PATH 有外部程序时不自动加载"；守卫失败用户源码零执行；行为变化与 S3 分开提交 |
| C10 / S5d 原生 argv 与启用 | syntax/powershell/native_argv.go；独立 argv 回显 exe；Legacy/Standard/Windows 模式及可信 exe 身份；仅已验证 git.exe/rg.exe/curl.exe 等子集接共享分析器 | C9、原生 Windows fixture | 对每个已支持精确 profile 证明 argv 一致；脚本宿主/批处理/未覆盖参数不借用 exe 合同；命令和版本范围明确后才启用对应分析，其他仍为 Exec/6.6 |
| C11u / S6 Unix 资源与空设备 | resources 资源三态与 Unix/容器设备身份；ShellRedirect 和 Argument 抵消分开；删除两处旧特例 | C6，不等 C7–C10 | Linux/macOS真实 Lstat、实际容器数字探针；tee/curl/cat 的空设备规则通用；opaque/unknown 命令的字面重定向不新增路径要求；cp 等替换目标与 rm/chmod/rename 不豁免；探针失败 Unresolved/readonly 优先 |
| C11w / S6 Windows 资源 | FileSystem/provider、Path/LiteralPath、盘符/UNC/junction/ADS、重定向与 $null；NUL 专门身份原型 | C10 | 各路径类别按实际 Windows 证明；未证明目标 Unresolved；NUL 未完成不阻塞其他已验证能力，不能声称整个 Windows S6 已完成 |
| C12 / S7 统一审批 | 合并 syntax/bash 原安全审查与访问审查，移除临时 syntax/bash→unwrap 边；tools/llm 只调用一个入口；统一批准消费 | C6、C11u；Windows 接入仅用已验收 C10/C11w 范围 | 保持 HITL 公开形状；一次批准只执行一次；并发兄弟调用不能借用；拒绝/取消/未启动不消耗批准；精确/规则/重试/恢复/Mailbox 代批全覆盖；任何已知硬阻断不降级 |
| C13a / S8a 只读与输出 | commands/powershell 的 params.go/common.go，加 Get-Location、Test-Path、Get-Content、Get-ChildItem、Write-Output、Select-Object、Select-String、Measure-Object、Sort-Object 各文件（后四个只支持常量形式） | C10、C11w、C12 | 每个实际模块版本参数集、缩写、provider/管道、三档权限测试通过，逐命令启用 |
| C13b / S8b 会话 cwd | set_location.go；只跟踪可信 FileSystem 常量目标，分支/失败和后续相对路径纳入 | C13a | 不沿用错误 cwd；未建模状态操作不让后续命令使用旧身份/环境快照 |
| C13c / S8c 文件修改 | New-Item、Set-Content、Add-Content、Out-File、Tee-Object、Copy-Item、Move-Item、Remove-Item 各文件 | C13b | 复制源/目标/替换对象、链接、递归/删除与 Output 分类正确；readonly/管理员 block 不可被 WhatIf/Confirm/Force 放宽 |
| C13d / S8d 网络 | Invoke-WebRequest、Invoke-RestMethod 各文件 | C13c | 各实际版本的方法、Body/InFile/OutFile、内容解析及动态参数单独验收；与 curl.exe 区分 |
| C14 / 总验收与文档 | 全仓回归、Linux/macOS基线、原生 Windows逐版本逐命令矩阵、真实容器；按 §0.6 更新现行文档 | 已交付批次 | 明列已分析子集、仍需审批项、未支持版本/路径；未完成项保留，不能只因文件齐全删除计划或宣称全部支持；完成整个 S8 后才执行 §0.6 收尾 |

**C6（S4）已完成，下一步可独立推进 C11u**。S3 的 C1–C5、S4 的 C6a–C6g 均见 §0.1；Windows 的 C7–C10 不与 Unix 空设备工作互相等待。C6 只承诺当前 Bash 子集与新增保守回退，不表示 PowerShell 或所有 CLI 选项已经支持。

实现边界：
- `registry/` 是不含分析函数的只读名称/身份元数据叶子，syntax、identity、unwrap 共享它；`commands/registry.go` 通过稳定 ID 连接各命令文件。这样统一名字来源，又不让 syntax 导入命令分析器。部署白名单不从 registry 生成，未登记/缺少分析器的项继续 opaque。
- `Effects` 的公开合同只含 Ops/Unknowns/Deps/SSHAgent；命令文件内部的 `effectDraft` 是累加器，不再流入策略层。Read/Output/Create/Delete/Rename/Chmod/Link 已区分，未知选项追加约束而保留已知危险操作。复制、移动、链接的操作包含各目标角色，移动/链接源的读取与写入约束分别映射。
- `SourceWord` 保留原 WordSpan、原 argv 下标、来源类别与提取值的 UTF-8 范围；值范围按解析下标绑定，不按字符串相等查找。变量、glob、brace、无来源输入和 env -S 生成值不会恢复成字面。
- `CommandInput` 显式携带命令域、身份来源和方言；目前仅 Bash 已核验来源进入 Unix 分析器。PowerShell 的 cmdlet/别名、同名函数和未经原生 argv 验证的 exe 不匹配。默认配置文件/脚本依赖已输出，但完整存在性、可写性与摘要资源核验仍是 C11u/C11w，§0.5 的限制仍在；不可因新增 Dep 就声称漏洞已经消除。
- 包装使用共享语法，执行展开保持保守；语法侧只检查候选，不授予权限。`command -p`、未知选项、nice 附加值形式等保留有意的用途差异；timeout `--` 同时保留历史候选并检查 duration 后命令。原有硬阻断不降级，两套批准仍由 C12 合并。


**C1 的迁移范围**（已完成）。整体移出必须排在拆分身份、资源和 plan 之前：只要 `bash_review.go` 还在 `accesspolicy`，先移其中任何一块都会形成 `accesspolicy → shellreview/x → accesspolicy` 循环（§5.3）。这一步保留原函数体，只调整包、import 与五个共享函数名；共享 cwd 函数移到通用路径包：

- 移动的文件：`bash.go`、`bash_review.go`、`bash_execution.go`、`bash_connector.go`、`connector_invocation.go`、`bash_environment.go`、`bash_launch.go`、`git_state.go`、`glob.go`。`grant_scope.go`（`RuleReusable`）也被 llm 的通用 HITL 使用，留在 `accesspolicy`，S7 再定归属。
- 先不拆出 `plan/`：`bashPlan`、`combineBashPlans`、`bindBashLaunch` 与四个批准函数互相调用，现在拆要一并导出一批内部函数，等于在搬家时改接口。
- `accesspolicy` 需要导出的内部函数只有五个（2026-11-01 在 `95741e1a` 上复核）：`decisionForAction`、`outsideRootsReason`、`sessionAccessLevel`、`sessionWorkspaceEditingDisabled`，以及 `subtree.go` 也在用的 `resolveAgainstCwd`。最后一个保留在通用路径包并导出，不能随 bash.go 一起搬走后再反向导入；允许机械改名和调用限定，函数体保持原样。
- 调用方只有 `internal/tools`（`tool_bash.go`、`tool_sandbox_bash.go`、`bash_access_review.go` 及测试）与 `internal/llm`（`run_stream_*`、`mail_test.go`），`accesspolicy.BashPlan` 等改为 `shellreview.BashPlan`。
- 测试随代码移动：`bash_*_test.go`、快照测试与 `testdata/bash-review-baseline.txt`，以及 `capability_test.go`、`hardening_test.go`、`accesspolicy_test.go` 中的 Bash 用例；共享 fixture（`newCapabilityFixture`、`skillFixture`、`connectorFixture`、`fakeSystemPrograms`）放到新包的测试辅助文件。快照文件不重新生成。
- `control_readonly_test.go` 的 Bash 用例也随迁；纯路径用例留在 accesspolicy。迁移前 accesspolicy 与原 shellreview 各包的 111 个测试入口均保留，未重复测试名。
- 为满足传递依赖门槛，`syntax/text.go` 及测试原样移到 `internal/commandtext`；config、hostshell、Shell 审查和 tools 同步引用。原先 `accesspolicy → config → shellreview/syntax` 的隐藏依赖已移除，不留转发入口。
- 门槛：`go list -deps ./internal/accesspolicy` 不含任何 `shellreview` 包；快照逐字节不变。S3 的依赖方向门槛在这一步达到。

C11u 可在 C6 后与 Windows 原型分开推进；这表示依赖可独立满足，不要求派生子代理或自动并行修改同一文件。C12 的 Unix 范围可先交付，Windows 范围只有在各自前置门槛完成后接入。

版本验证的实际产物包括：部署 Shell 的真实版本/Edition、引擎与模块身份、LanguageMode、传参模式、影响语义的特性、AST/源范围样例、原生 argv 实测，以及各 cmdlet 参数/效果对照。记录在相应 testdata 中，完成时把结论补进附录 C；不同版本不只换同一 fixture 的标签。

本轮明确的后续决策默认值：采用受管模块一致性合同原型；按 20 个独立 cmdlet 范围分批交付；不强行安装或切换用户 PowerShell，不静默替换系统策略；未知版本/安装/环境保持现有保守路径。若原生证据否定某项假设，先更新对应合同和测试，再扩大支持，不绕过门槛。

### 0.4 目标环境验收（可并行）

- 原生 Windows：在 Windows 上运行 `internal/hostshell`、`internal/tools`、`internal/accesspolicy`、`internal/commandtext` 与 `internal/shellreview/...` 的测试；§2.1 样例需要单次人工；商店版 `pwsh` 应用执行别名的查找、链接解析、摘要与启动；中文/空格路径、UNC、junction；严格模式拒绝普通 Shell 调用。
- PowerShell 外部程序传参：5.1 与 7.6 各用无害的 argv 回显程序核对（S5 前置条件，§6.7）。
- 真实系统包升级：apt 原地替换、Homebrew 切换链接后无需重启仍按 Bash 分析，旧批准失效。目前只有模拟测试。
- 真实容器端到端：身份、路径、摘要、探针失败。
- 空设备：Linux/macOS 真实 Lstat 类型与主次设备号；真实容器固定 stat 路径、数字格式、不跟随链接、错误设备号与故障的三态处理；预审与启动前复审分别重新检查。模拟 resolver 或交叉编译不能替代这些验收。
- PowerShell helper：5.1/7.6 启动与 stdin 协议、UTF-8 原文往返、版本与参数合同、冷/热启动、并发/含排队预算、输出上限、取消及进程树清理；AMSI/EDR 受阻时走 6.6。测试模块不得在审批前加载。
- Windows 空设备：`$null` AST 语法丢弃与 `'$null'` 文件路径分开；裸 `NUL` 的身份确认和实际打开行为未通过原生验收前不启用豁免，其他保留设备写法首批 Unresolved。

### 0.5 已知限制（当前保留）

- Bash 文本检查按 AST 确认引用数据；heredoc 正文与注释按引号外处理，正文里的全角空格等会被拒绝。解析失败或超时时不提供引号豁免。
- 显式配置的 bash 绝对路径，如果不是启动时 PATH 选中的那一项，按未知方言处理。
- Platform 启动后新装的 Bash（包括装进更靠前 PATH 目录的），要重启后才按 Bash 分析。
- S5 之前的 Windows：ask/auto 档每条普通 Shell 调用都要单次人工审批；严格模式全部拒绝。
- 隐式配置（S6 之前）：`HOME`/`XDG_CONFIG_HOME` 改到可写位置、或 `HOME` 本身在写根内时，默认配置文件（`.curlrc`、`.wgetrc`）不审查；配置来源被批准时不绑定配置文件内容；`env -i`/`env -u` 清掉的配置变量仍按存在处理（多审不漏审）。
- Host 只做执行前检查，不隔离任意代码的内部访问；未知方言的人工批准不代表正文已经审查。

### 0.6 收尾

S8 与 C14 完成后同步更新现行文档：`AGENTS.md`（命令语义包名）、[工具目录权限](../docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](../docs/AccessPolicy与HITL改造.md)、[Runtime 模块边界](../docs/Runtime模块边界.md)、[Windows Git Bash](../docs/WindowsGitBash实施进度.md)（注明 Host Bash 仍为 PowerShell）。发布记录分别列明：已解析但仍需审批、具备精确语义可按政策通行、未支持而回退的环境。之后删除本文。

## 1. 目标

一条 Bash 工具调用是否需要审批，按固定的几层依次判断。每层只回答一个问题、只输出事实，只有策略层把事实变成 allow / auto / hitl / block。

要求：

- 每种特殊命令的知识（参数、拦截条件、过滤条件）只在它自己的文件里。
- "认识哪些命令"只有一份名单。
- 审批看的命令必须是实际执行的命令，Windows 也一样。
- 整个模块在一个目录下，子目录与层一一对应。

不变的边界：命令语义不决定授权；不按 GENERAL/CODER/KBASE/TEAM 分支；不执行用户命令来猜行为；Host 仍是执行前检查，不是逐次文件访问隔离。

## 2. 现状问题

### 2.1 Windows：审批看的和实际跑的不是同一条命令

Windows Host 的 Bash 工具默认用 `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command` 执行，但审查原先始终用 Bash 语法（`bashast`，基于 mvdan/sh）解析命令。两种语法的引号、转义和分隔规则不同：

```text
echo "\" ; Remove-Item -Recurse C:\x ; echo "\"
```

- Bash 语法：一个 `echo`，带一个字面量参数。`echo` 没有效果，审查结果为放行。
- PowerShell：`\` 不是转义符，`;` 分隔三条语句，`Remove-Item` 会执行。

解析结果已用 `go test -overlay` 探针在 macOS 上确认，未在 Windows 上端到端运行。

**处理状态（`5554545f`）**：Host Bash 工具改由 `hostshell.ResolveTool` 生成工具专用启动合同，按实际 Shell 与启动模板判定方言。Windows 的 PowerShell、cmd、MSYS，以及 Unix 上非系统 Bash、登录/交互模板，都不再交给 Bash 解析器，整条命令按未知方言审批（6.6）。这只是止血：正文里的删除、远端修改仍然识别不出来，要等 PowerShell AST（6.2）。

相关事实：

- 工具启动与 Terminal 分开；Git Bash 开关只影响 Terminal（见 [Windows Git Bash](../docs/WindowsGitBash实施进度.md)）。
- PowerShell 先按别名、函数、cmdlet 解析命令名，再找 exe。`cat`、`ls`、`rm`、`cp`、`mv`、`sort`、`tee` 是 cmdlet 别名；Windows PowerShell 5.1 的 `curl`、`wget` 是 `Invoke-WebRequest`。按名字套用 GNU 语义是错的身份。
- Windows 默认命令白名单是 `*`。
- 单条直接连接器调用不经 Shell，按审查得到的 argv 直接启动，不受此问题影响。

### 2.2 一个函数做了所有层的事（S3 已拆解）

重构前的 `reviewBashScript` 把 cwd、身份、opaque、凭据、路径、glob、Git、空设备特例、重定向与汇总混在一个循环里。C1–C5 已拆出 identity、unwrap、resources、policy、plan；根包 `review.go` 保留解析、层间调度、有界递归和结果汇总。旧 curl/wget 特例暂留 policy/legacy.go，命令名单与结构化事实已由 C6 统一；设备身份抵消仍在 C11u。

### 2.3 原有名单分散问题（C6 已收敛）

| 位置 | 名单 |
| --- | --- |
| `shellreview/identity/identity.go`、`scripts.go` | `ordinaryCommands`、`shellBuiltins`、`wrapperNames`、`captureSystemExecutables` 的额外名单、`IsInterpreter` |
| `shellreview/identity/identity.go` | `IsOpaqueCommand` |
| `shellreview/commands/specs.go`、`operands.go`（原 shellanalysis） | `specs` 与 `Operands` 的 switch（各有一份名字列表） |
| `shellreview/syntax/bash/bash_patterns.go`（原 bashsec） | `astDangerousCommands`、`zshDangerousCommands` |
| `tools/tool_bash.go` | `unsupportedBashCommands`（严格模式） |
| `config/config_defaults.go` | `defaultBashAllowedCommands` |

以上是迁移前位置；C6 后名字仅存在 registry，分析器以 ID 绑定。以下历史差异保持保守，不借重构自动扩大信任：

- `df`、`dbx`、`httpx` 在 `ordinaryCommands` 中但没有分析器，落到 `unknownOperands`，实际总是 opaque。
- `Get-ChildItem`、`Get-Content`、`Remove-Item` 是 cmdlet，`LookPath` 找不到，这几个条目永远不会生效。

### 2.4 两套审查、两套批准

- `syntax/bash`（原 `bashsec`）返回 `ReviewResult`，批准存在 `BashSecurityApprovals`；`shellreview` 返回 `BashPlan`，批准存在 `AccessPolicyApprovals`。`internal/llm` 为两者分别发审批请求、分别消费。
- 原先包装命令拆了两份，C6f 已合并参数语法并审阅用途差异；syntax/bash 只消费共享候选。两套审批仍留到 C12。
- `bash -c` 递归也是两份，各自 `maxDepth = 3`。

### 2.5 命令特例写在策略层，事实类型太粗

- curl/wget 输出到 `/dev/null` 的豁免和 `git -C` 的仓库目录判断原先写在 Bash 审查循环里；C5 后空设备特例暂留 policy/legacy.go，Git 目录事实已归 commands/git.go，通用化仍在 C6/C11u。
- 原 Effects 只有 Read/Write；C6d 已改为 Op/Unknown/Dep 并区分操作。旧 `/dev/null` 特判保持到 C11u，不能把事实细化写成设备身份已实现。
- 旧合同的 Incomplete/ExecutesCode 已退出公开 Effects；未知语法事实、执行/未知行为与已知危险操作分别输出和消费。

## 3. 分层流水线

### 3.1 总表

| 层 | 回答的问题 | 输出 | 失败时 |
| --- | --- | --- | --- |
| 0 启动与方言 | 实际由哪个 Shell、以什么模板执行 | LaunchSpec：Shell canonical 路径与摘要、模板、参数、有效环境、cwd、方言（已实现） | 方言或模板不支持：整条命令走未知方言合同（6.6） |
| 1 语法 | 拆成哪些调用？有没有无法表达的 Shell 行为 | 调用列表（argv、重定向、环境赋值、源位置） | 硬阻断，或 Unknown(syntax) |
| 2 身份 | 当前调用的 argv[0] 实际是哪个程序 | Identity（见 3.2） | 未知程序，即 Exec |
| 3 包装 | 已验证的包装命令背后跑什么 | 展开后的调用，回到第 2 层重新绑定身份 | Unknown(wrapper) |
| 4 语义 | 这组参数做什么 | Effects：Ops、Unknowns、Deps（见 4.3） | 由命令文件给出 Unknown |
| 5 资源 | 这些路径和依赖实际是什么 | 每项为 Known / Unresolved / HardViolation | Unresolved 为 Unknown(path)；HardViolation 直接阻断 |
| 6 策略 | 每个事实对应哪条政策 | 一组 Requirement（block / hitl / auto / allow） | — |
| 7 合并 | 整条命令怎么审批？批准绑定什么 | BashPlan（叶子要求、指纹、启动绑定） | — |

第 2、3 层是有界交替，不是两个单向阶段：先绑定身份，只有已验证的方言内建包装、系统包装或系统解释器才展开；展开出的子调用回到第 2 层重新绑定。若按名字展开 Workspace 里同名的假 `env`、`bash`，只审内层命令，就会漏掉实际执行的外层程序（R1）。现有 `analyzeBashExecution` 已先识别身份再展开，重构必须保持。

预审与启动前复审都完整运行 0–7 层，共用同一份 LaunchSpec。启动前重新解析 Shell，指纹不一致返回 `bash_launch_changed`；只有启动前复审消费一次性批准（已实现）。

### 3.2 各层合同

**0 启动与方言**（已实现）：`hostshell.ResolveTool` 生成 Bash 工具专用的 LaunchSpec，与 Terminal、Git Bash 开关分开；Unix 默认参数保持 `--noprofile --norc -o pipefail -c`。只有同时满足以下条件，才是 Bash 方言且进入语法分析：

- Shell 是系统 Bash：Platform 启动时搜索路径选中的那一项（链接本身），每次调用重新解析其链接目标并与实际执行的 canonical 路径比较。内容摘要只绑定批准；安装原地更新和符号链接切换后无需重启，之后在更靠前的 PATH 目录出现的同名程序不算系统 Bash（S2 复审补丁）。
- 模板只含已知的非交互参数，并且最后是一个 `-c {{command}}`。

其余都是不支持的方言或模板：Windows 的 PowerShell、cmd、bash.exe（MSYS），以及其他 Shell、登录/交互模板、附加源码的模板。容器使用容器内 bash，不经 LaunchSpec。单条直接连接器调用不经 Shell。

**1 语法**：每种方言一个解析器，输出相同的调用结构。Bash 使用 mvdan/sh；PowerShell 使用 PowerShell 自带的 AST 解析器（6.2）。参数与重定向目标保留原始源范围及是否为字面量，不能在变量、glob 或其他展开完成后仅凭结果字符串恢复来源；S3 保留来源，S4 将来源传到每个文件事实。Shell 级硬阻断归这一层：
- Bash：引号外的 Unicode 空白与零宽/格式字符（引号内是数据，原样保留）、IFS、`/proc/*/environ`、`eval`/`source`/`exec`/`trap`/`alias` 等改变 Shell 自身状态的内建命令、zsh 模块命令。`bashsec` 与严格模式的同类名单合并到这里。
- PowerShell：改变命令解析的命令（6.2）。
- 所有方言（包括未知方言），任何位置：控制字符、双向覆盖与隔离字符、无效 UTF-8。它们会让审批卡片显示的内容与实际字节不一致，与语法无关。
- 未知方言：无法区分引号，Unicode 空白与零宽/格式字符对整条命令检查。

**2 身份**：

| Identity 类型 | 判定 | 后续 |
| --- | --- | --- |
| ShellBuiltin | 方言内建命令（未被包装命令绕过） | 进入第 4 层 |
| System | canonical 路径等于启动时记录的系统安装 | registry 中有分析器则进入第 4 层，否则 opaque |
| PlatformBuiltin | rg、pdftotext、dbx、httpx 等平台内置程序的 canonical 路径 | 同上 |
| Cmdlet | PowerShell 中名称（含别名）解析为可信内置模块的 cmdlet | 以正式名查 registry，有分析器则进入第 4 层，否则 Exec |
| Connector | 已挂载连接器的冻结入口，路径与摘要核验通过 | 连接器规则，不走命令分析 |
| Interpreter | 系统解释器加脚本（含 shebang） | Exec；可能被技能脚本或自写脚本凭据抵消 |
| BuildTool | go、npm、make 等 | Exec |
| Shadowed | PowerShell 函数、外部脚本、非内置模块命令，或指向它们的别名 | Exec |
| Unknown | 其他 | Exec |

同名 Workspace 程序、PATH 前置目录中的同名程序都拿不到 System 身份。PowerShell 中只有解析为外部程序（Application）的名字才可能取得 System / PlatformBuiltin 身份（6.3）。

**3 包装**：只有一份实现。只展开第 2 层确认过的包装；包装不授予目标额外权限；外围的重定向、环境与 cwd 变化保留。`bash -c` 的字面量脚本回到第 1 层递归，深度上限只有一个常量；`xargs`、`find -exec` 的运行时目标无法静态确定，产出 Exec。

**4 语义**：命令文件是纯函数，不做 IO、不读部署凭据。输入 argv，输出事实。详见第 4 节。

**5 资源**：解析相对路径、symlink/junction、容器 guest 路径、glob；识别空设备；核验 Git 配置与 hooks；按本次调用的有效环境找到命令默认配置文件的实际路径，确认是否存在、Agent 能否免审批写入（8.1）；对脚本、入口和配置文件计算摘要。除第 0 层的启动身份核验和第 1 层的受信任解析器进程外，业务资源 IO 集中在这一层和第 2 层；命令分析器与政策映射不探测文件系统或容器。每个结果三选一（R6）：

| 结果 | 含义 | 去向 |
| --- | --- | --- |
| Known | 已确认的路径、设备、依赖状态 | 按事实查表 |
| Unresolved | 无法确定：不支持的路径形式、探针暂不可用 | Unknown(path)，走 complex |
| HardViolation | 已确认命中 readonly、平台保护目录、临时根逃逸、非法路径 | 直接阻断，任何档位与批准都不能放宽 |

探针失败不能擦掉已经发现的 HardViolation，也不能覆盖已识别的读写、删除和远端事实。

**空设备合同（原待确认第 3 项，设计已决定，S6 实现）**：

- Unix 只将去除语法引号后字面值严格等于 `/dev/null` 的操作数或重定向目标列为候选，不做 trim、home 展开或路径规整后匹配。`//dev/null`、`/dev/./null`、相对路径、变量或 glob 展开所得目标、指向空设备的符号链接不享受豁免，继续原有路径审查。`/dev/zero`、`/dev/full`、`/dev/tty`、`/dev/stdout`、`/dev/stderr`、`/dev/fd/*`、随机设备均不算空设备。
- Linux/macOS Host 用 `Lstat` 检查末级目标，不跟随符号链接；要求字符设备且 `Rdev` 主次设备号分别为 Linux `1:3`、macOS `3:2`。按 GOOS 隔离实现，检查函数可注入替身；其他 Unix GOOS 不授予空设备豁免。不在启动时冻结设备身份。
- 容器必须在实际执行容器内探测，复用现有只读策略探针通道。使用镜像中预先确定的受信任 stat 绝对路径，不按用户 PATH 搜索；输出数字字段 `%f|%t|%T`，校验文件类型位与十六进制设备号 `1:3`，不跟随末级链接，不依赖 `%F` 的本地化类型文本。探针路径、参数兼容性与不可免审批替换的入口在镜像验收时核实，不假定所有镜像都位于 `/usr/bin/stat`。
- 已确认是普通文件、符号链接或其他设备：无空设备豁免，继续路径审查；探针失败、超时、输出不合法或身份无法确认：`Unresolved`，保留可见路径要求并追加 Unknown(path)；已发现的 `HardViolation` 保持阻断。不得为降低延迟退回信任字面字符串。
- 预审与启动前复审分别重新检查；同一次审查、同一执行环境内可复用一次设备结果（含失败），不同审查、Run 或容器之间不缓存。这是执行前核验，不承诺消除核验后资源变化的竞争窗口。
- 按来源区分能抵消什么：
  - Shell 重定向目标（`>`、`>>`、`2>`、`&>`、`<` 等）由 Shell 自己打开，与程序怎样解析参数无关。字面候选经身份确认后即抵消该项读写路径要求，不看当前调用是否分析完整、是否 opaque。这样保持现状：opaque 或带未知选项的调用加 `2>/dev/null` 不额外产生路径要求，调用自身的 opaque/complex 要求照常。
  - 命令参数（argv）：只有当前调用分析完整时，命令分析器给出的 `Read`、`Output` 才能抵消。`Incomplete`/UnknownOption 的调用和 opaque 调用的可见参数不抵消（参数含义可能被未知选项改变），不将不完整状态扩大到独立兄弟调用。
- `Create/Delete/Rename/Chmod/Link` 与可能替换目标的 cp、mv、install、ln 目标操作不豁免；不能仅因目标是 `/dev/null` 就把目标操作降为 Output。
- readonly、平台保护、临时根逃逸和管理员显式 block 先于抵消；执行代码、删除、上传、远端修改及其他目标的要求各自保留。Windows 的 `$null` 语法丢弃与 `NUL` 设备候选分开处理，见 6.5。

**6 策略**：见 3.3、3.4。

**7 合并**：沿用现有 BashPlan 的叶子要求、规则批准与精确批准语义。每个叶子关联调用、事实来源（argv 下标、规则）与启动绑定：

- 精确批准绑定完整调用：命令正文加 LaunchSpec 指纹。
- 本轮规则批准追加 Shell 身份（canonical 路径及摘要）、模板与方言指纹；不额外加入 cwd、完整环境或命令正文，原规则自身的目标/调用限制不变（S2 复审补丁，见 §8.1）。

S7 合并 `bashsec` 的批准时，保持 HITL 公开形状与规则语义，协议调整另行讨论。

### 3.3 策略层判断顺序

1. **硬阻断**（任何档位、任何批准都不能放宽）：Shell 级硬阻断、资源层 HardViolation、递归目标包含受保护子树、容器 glob、editing=false 时在 Workspace 内写入或执行 Exec、管理员显式 block。
2. **按合同抵消**：资源层确认的空设备只抵消该项 Read/Output 的路径要求：Shell 重定向不看调用的分析状态，命令参数要求当前调用分析完整（3.2 空设备合同）；PowerShell AST 确认的 `$null` 重定向直接表示语法丢弃输出，不当作文件设备探测（6.5）。连接器入口、本次选中技能脚本、本 Run 自写脚本的凭据只抵消对应调用的 Exec。各类抵消都不能删除兄弟要求（R8）。
3. **逐个事实查映射表**（3.4）。
4. **合并**：同一调用的多个要求各自成为叶子，规则批准只覆盖对应叶子。

### 3.4 事实 → 政策键

这是全模块唯一一张映射表，放在 `policy/mapping.go`。

| 事实 | 条件 | 政策键 |
| --- | --- | --- |
| Read | 越出读根 | `read-outside-roots` |
| Output / Create / Delete / Rename / Chmod / Link | 越出写根 | `write-outside-roots` |
| 同上 | 在写根内 | `bash-write-in-write-roots` |
| 写入 Git 可执行配置路径 | — | `executable-config`（由路径规则判定，现状） |
| Read / Output | 字面候选经第 5 层确认为空设备，未命中硬阻断；来自命令参数时还要求当前调用分析完整，Shell 重定向不要求 | 仅抵消该项路径要求 |
| Delete（递归）/ Discard（丢弃未提交修改） | — | `destructive` |
| RemoteMutation | — | `remote-mutation` |
| Exec、UnknownBehavior、Dep 核验失败 | 无凭据抵消 | `bash-opaque-command` |
| UnknownOption | — | `bash-complex-filesystem`，单次批准；可见文件仍升级为写入（8.1） |
| Dep(ConfigFile) | 第 5 层确认：不存在，或存在但 Agent 免审批写不到 | 无要求（8.1） |
| Dep(ConfigFile) | 存在且 Agent 免审批可写 | 同 UnknownBehavior |
| Unknown(syntax / wrapper / path)、动态 cwd、动态值 | — | `bash-complex-filesystem` |
| 不支持的方言或启动模板 | — | 未知方言合同（6.6，已实现） |
| HardViolation | — | block |

默认三档（`internal/config/config_defaults.go`；最后一行是代码中的固定合同，不是配置项）：

| 政策键 | ask_for_approval | auto_approve | full_access |
| --- | --- | --- | --- |
| read-outside-roots | hitl | auto | allow |
| write-outside-roots | hitl | hitl | allow |
| bash-write-in-write-roots | allow | allow | allow |
| bash-opaque-command / bash-complex-filesystem | hitl | auto | allow |
| destructive / executable-config / remote-mutation | hitl | hitl | allow |
| 未知方言 | 单次人工 | 单次人工 | 按 `bash-complex-filesystem` |

### 3.5 不变式

1. 命令文件只产出事实，永远不返回 allow 或 hitl。
2. 命令文件只能增加要求。要求只能按 3.3 第 2 步的两类合同、在各自范围内抵消，不能删除兄弟要求。
3. Unknown 与已识别的事实同时保留：未知参数之前识别出的删除、上传不会丢。
4. 命令名单只有 registry 一处；身份层、opaque 判定、解释器列表都从 registry 读。部署白名单 `bash.allowed-commands` 仍独立，登记分析器不等于默认允许。
5. 方言或启动模板不支持时，整条命令走未知方言合同，不经过任何"简单命令"快速放行（已实现）。
6. 既有的连接器、技能脚本、自写脚本凭据合同不变，重构不能扩大它们的豁免范围。
7. 预审与执行使用同一份 LaunchSpec。Shell、模板、环境或 cwd 变化使旧的单次批准失效；本轮路径规则只随 Shell 身份、模板或方言变化失效（已实现）。

## 4. 命令知识：registry 与命令文件

### 4.1 Registry 条目

C6 实际采用两部分：`registry.Entry{ID, Name, Flags}` 作为所有层共用的名称元数据叶子，`commands` 按 ID 聚合命令文件的解析函数。`types.CommandInput` 带入已经证明的 Domain/IdentitySource/Dialect，Analyze 必须同时匹配。下方是后续加入 Windows 能力合同的概念描述符，并非声称这些 Windows 字段或模块已经实现。

```go
type Command struct {
    Domain  CommandDomain            // ShellBuiltin / NativeCLI / PowerShellCmdlet；不能只按显示名称匹配
    Dialects []dialect.Dialect        // 原生 CLI 可经验证复用；cmdlet 与 Bash 内建保持不同语义域
    Names   []string                 // 名称与别名，如 awk/gawk/mawk/nawk
    Kind    Kind                     // Analyzed / ShellBuiltin / Wrapper / Interpreter / BuildTool
    Sources []Source                 // 允许的身份来源：System / PlatformBuiltin / Cmdlet，可多个
    Options *OptionSpec              // 声明表：带值/不带值/可选值，值是读文件、写文件、目录、URL 还是普通值
    Supports CapabilityRequirement   // OS、Shell/模块能力、原生 argv 合同与 CLI 实现范围
    Analyze func(p Parsed, e *types.Effects) // 声明表表达不了的条件；可为空
}
```

- 查找同时使用外层 Dialect、已确认的程序/模块身份、命令正式名和能力合同；同名程序不能仅按名称借用语义（R7）。经确认是同一原生 CLI 家族的 Windows `git.exe`、`rg.exe`、`curl.exe` 可在通过平台及 argv 验收后复用 NativeCLI 描述符；PowerShell 的 `curl` 别名若解析为 Invoke-WebRequest，只能进入该 cmdlet 的描述符。Windows `find.exe` 不因名字相同而使用 POSIX find 分析器。
- `Sources` 允许一个名字同时有系统安装与平台内置两种来源，身份层按实际来源核对。
- 简单命令只写 `Options`，由共享解析器得出文件操作数；有子命令、内嵌语言或特殊条件的命令写 `Analyze`。
- `Kind` 取代 `ordinaryCommands`、`shellBuiltins`、`wrapperNames`、`isInterpreter`、`isOpaqueCommand`。启动时记录系统安装路径的名单也从 registry 生成。
- cmdlet 只按正式名登记（`Remove-Item`），别名到 cmdlet 的解析归身份层（`identity/powershell.go`）。
- 中立类型（Invocation、Effects、Op、Unknown、Dep、证据，以及 CommandDescriptor/OptionSpec/Parsed 的数据合同）放在 `shellreview/types` 叶子包。`shellreview/options` 是共享解析算法的独立兄弟包，只依赖中立类型；不能把它放在 `commands/options.go` 后让子包反向导入父包。`commands` 根包聚合本包描述符和 `commands/powershell` 导出的描述符；PowerShell 子包只依赖 `types/options`，不导入 `commands` 或 registry，避免父子包循环（R7）。示例字段用于说明含义，最终命名随实现确定。

### 4.2 命令文件示例

"针对命令集的特殊拦截和过滤条件"都写在这里，以事实的形式出现：

```go
// commands/curl.go
var curl = Command{
    Domain:  NativeCLI,
    Dialects: []dialect.Dialect{dialect.Bash, dialect.PowerShell}, // PowerShell 还须通过身份与 argv 合同
    Names:   []string{"curl"},     // PS 5.1 的 curl 别名由身份层解析为 Invoke-WebRequest，不归这里
    Kind:    Analyzed,
    Sources: []Source{System},
    Options: curlOptions,          // -o/--output 为写文件，-T 为读文件，-d/-F 为远端数据……
    Analyze: func(p Parsed, e *types.Effects) {
        e.Output(p.Values("o", "output")...)            // 空设备由资源层识别，这里不判断
        if p.MethodNot("GET", "HEAD", "OPTIONS") || p.HasAny("d", "F", "T", "json") {
            e.RemoteMutation("curl:method-or-data")
        }
        if p.HasAny("K", "config") {
            e.UnknownBehavior("curl:-K")                // 配置文件内容不解析，交给审批
        }
        if !p.FirstIs("q", "disable") {                 // -q 只在第一个参数时停用 .curlrc
            e.ConfigFile("curl:curlrc", ".curlrc", "CURL_HOME", "XDG_CONFIG_HOME", "HOME")
        }                                               // 是否存在、能否免审批写入由资源层判断
    },
}
```

```go
// commands/lsof.go：查询选择器与输出标志视为只读
// -D（设备缓存）、+d/+D（目录遍历）、未登记选项 → UnknownBehavior
```

```go
// commands/git.go：子命令表决定 Read / RepoWrite / RemoteMutation / Discard，
// 并产出 Dep{GitConfig: read|write|network}，由资源层核验配置与 hooks；
// -C <dir> 在这里产出仓库目录，不再由策略层特判。
```

### 4.3 Effects

```go
type Effects struct {
    Ops      []Op
    Unknowns []Unknown
    Deps     []Dep
    SSHAgent bool // Git 网络操作可能使用 SSH agent，只有此时才传 SSH_AUTH_SOCK
}

type Op struct {
    Kind      OpKind   // Read / Output / Create / Delete / Rename / Chmod / Link / Exec / RemoteMutation / RepoWrite / Discard
    Targets   []Target // 文件类操作的目标；复制、移动、链接有多个
    Recursive bool
    Arg       int      // 证据：argv 下标
    Rule      string   // 证据：规则 ID，如 "curl:-o"
    Origin    Origin   // ShellRedirect / Argument / Implicit；决定空设备抵消条件
}

type Target struct {
    Path string // 原始操作数，由资源层解析
    Role Role   // Source / Destination / Replaced / LinkTarget
    Word SourceWord // 原始源码范围、常量类型、字面量标记、展开来源
}

type Unknown struct {
    Kind UnknownKind // Option（语法未知）/ Behavior（行为未知）/ Syntax / Wrapper / Path / Dialect
    Arg  int
    Rule string
}

type Dep struct {
    Kind  DepKind // GitConfig / ConfigFile / Script
    Check string  // 如 GitConfig 的 read / write / network
    Path  string
}
```

复制、移动、链接必须同时检查源、目标、被替换对象与递归范围（R7）。`Arg` 与 `Rule` 用于 HITL 展示原因，以及说明哪些输入变化会使批准失效。`Origin` 与来源必须跨 Bash 包装展开、PowerShell 参数绑定和选项值提取持续保留；例如 `--output=/dev/null` 的目标要指回原始参数及其值范围。`env -S` 生成的参数保守标为非字面；PowerShell `$null` 的语法丢弃是单独目标类型，不能伪造为字符串路径。字段名称在实现阶段确定，含义以本节为准。

C6 实现另有 `UnknownWrite`，用于保留不完整语法下的写约束，不代表普通字节输出，不能抵消为空设备；Target 的 Placement/Child 区分精确路径、放入目录和待确认的文件/目录形态。多目标定位保留 symlink/`..` 的物理解析顺序，guest 类型探针失败时检查保守候选，不读 Host 同名路径。CommandInput 的 Words 与去掉程序名后的 Args 对齐；SourceWord 的 Start/End 仍是整词范围，ValueStart/ValueEnd 是提取值在原始源码中的 UTF-8 字节范围。

### 4.4 新增或修改命令的检查清单

每个命令文件的注释或测试需要回答：

1. 按哪个方言登记？识别哪个实际程序、别名与身份来源？同名 Workspace/PATH 程序如何拿不到 System 身份？
2. 哪些参数带值、可选值？组合短参数和 `--` 怎么解释？
3. 哪些值是文件、目录、URL、表达式、脚本？各自是 Read、Output、Delete、Rename、Chmod、Link，还是递归范围？多目标操作的每个角色是什么？
4. 是否读取默认配置、环境、插件、hooks？由资源层核验，还是记为 UnknownBehavior？
5. 哪些参数会启动其他程序或让目标在运行时才确定？
6. 未知选项、缺失参数如何返回，是否保留已识别的危险事实？
7. Host 与容器、Unix 与 Windows 的行为是否一致，哪些未验证？
8. 回归用例：通过、需审批、硬阻断各至少一条。

### 4.5 Windows 命令与其他命令采用同一分工（补全方案）

Windows 不是在 Unix 分析器外加一个版本判断。需要同时交付三块：`syntax/powershell` 解析实际 PowerShell 语法，`identity` 证明命令真正指向哪个 cmdlet/程序，`commands/powershell` 按正式命令分别解释参数。三块最终都产出同一套中立事实，再共用 resources/policy/plan。

- 每个复杂 cmdlet 都有独立文件，不再用 `items.go`、`web.go` 集中堆分支。首期目标明确为 20 个 cmdlet 文件及 2 个共享辅助文件，测试另计；完整列表见 §5.1、逐命令效果见 §6.4。
- Windows 上运行的 `git.exe`、`rg.exe`、`curl.exe` 属于原生 CLI，验证身份、CLI 实现和 argv 合同后复用对应命令文件；同名别名和函数不能直接复用。`cmd.exe`、批处理、脚本宿主和未登记程序仍保守处理。
- 版本差异集中在 `syntax/powershell/profile.go` 和声明式能力数据中。cmdlet 文件通过能力/参数元数据判断是否支持，不自行散落 `if version >= ...`；同一参数在某版本缺失或含义未知时产生 Unknown，不能套用另一版本的规则。
- 单独存在 `.go` 文件不代表已支持。一个命令只有同时通过实际 Shell 版本、模块身份、参数集、Windows 路径和三档权限验收后，才可按语义分析；其余命令继续使用既有保守合同。

本节的 Windows 目录、helper、模块和命令支持范围仍为计划，尚未实现或启用；C6 增加的匹配门槛不能视为 PowerShell 已支持。

## 5. 目录结构

### 5.1 目标结构

```text
internal/commandtext/                # 配置、启动与审查共用的中立文本检查（C1 抽出，无方言依赖）
internal/hostshell/tool.go           # 第0层（已实现）：工具专用 LaunchSpec 与方言判定，与 Terminal 分开
internal/shellreview/                # Shell 审批唯一入口
  review.go                          # Review(Input) BashPlan：按层调用，不出现任何命令名
  input.go                           # 命令、cwd、环境、LaunchSpec、session、执行环境探针
  dialect/                           # 方言枚举（已建；叶子包，hostshell 也依赖它）
  types/                             # 已建 LaunchBinding/Invocation、Op/Target/Unknown/Dep、CommandInput 与 SourceWord
  syntax/
    bash/                            # 第1层 Bash：已迁入原 bashast + bashsec；两套审批对象尚未合并
    powershell/                      # 第1层 PowerShell（S5）
      helper.ps1                     # 固定 helper（go:embed）：解析与受限身份查询，不执行用户命令或自动加载待查模块
      helper.go                      # 内嵌脚本与协议编解码；经窄 runner 调用 tools 管理的 helper
      ast.go                         # AST 节点白名单 → 调用结构；硬阻断名单
      profile.go                     # Edition/精确版本/能力选择；不按 basename 推断支持
      native_argv.go                 # PowerShell 参数到目标 exe 的传参合同校验
      positions.go                   # UTF-16 源范围到 UTF-8 字节范围的验证和转换
      testdata/                      # 各实际版本的 AST、参数、源位置与协议 fixtures
  identity/                          # 第2层
    identity.go                      # Identity 类型与 Resolve 入口
    unix.go                          # PATH 解析、系统安装记录
    windows.go                       # PATHEXT 解析
    powershell.go                    # 别名 → 函数 → cmdlet → 外部程序；可信模块判定
    powershell_modules.go            # 固定受信任模块依赖与命令/参数元数据快照
    container.go                     # 容器内解析探针
    scripts.go                       # 解释器脚本识别、shebang
    connector.go                     # 连接器入口核验与直接调用
  unwrap/                            # 第3层：包装命令与 bash -c，只此一份；由 review.go 与身份交替调度
  registry/                          # C6 已建纯数据名称/身份类别叶子；无分析器或审批函数
  commands/                          # 第4层：已接入结构化事实与逐参数来源；Windows 子包仍待 C13
    registry.go                      # 描述符聚合；按方言、实际身份、正式名和能力合同匹配
    simple.go                        # echo printf pwd true false date sleep uname whoami id basename dirname which printenv test tr cd top free
    files.go                         # cat head tail wc file stat readlink realpath cut sort uniq du ls diff grep rg
    fileops.go                       # mkdir touch tee chmod rmdir rm
    cp.go  mv.go  ln.go  transfer.go   # 各自规格，共用多目标绑定
    curl.go  wget.go  rg.go  lsof.go  git.go  find.go
    sed.go   awk.go   jq.go    tar.go  pdftotext.go
    *_test.go                        # 每个命令文件各自的语义测试
    powershell/                      # cmdlet：一个正式命令一个文件；共享算法不含命令特判
      params.go                      # 参数集、缩写、位置、公共参数与版本元数据绑定
      common.go                      # 共享事实构造；不访问文件系统，不做审批
      get_location.go                # Get-Location
      test_path.go                   # Test-Path
      get_content.go                 # Get-Content
      get_child_item.go              # Get-ChildItem
      write_output.go                # Write-Output
      select_object.go               # Select-Object
      select_string.go               # Select-String
      measure_object.go              # Measure-Object
      sort_object.go                 # Sort-Object
      set_location.go                # Set-Location（含受限的静态 cwd 跟踪）
      new_item.go                    # New-Item
      set_content.go                 # Set-Content
      add_content.go                 # Add-Content
      out_file.go                    # Out-File
      tee_object.go                  # Tee-Object
      copy_item.go                   # Copy-Item
      move_item.go                   # Move-Item
      remove_item.go                 # Remove-Item
      invoke_web_request.go          # Invoke-WebRequest
      invoke_rest_method.go          # Invoke-RestMethod
      *_test.go                      # 每个 cmdlet 独立参数/效果/版本测试
  options/                           # 与 commands 为兄弟包，命令分析器共用
    options.go                       # 中立选项解析算法与来源处理
    posix.go                         # POSIX CLI 的组合短选项/长选项解析，保留值下标与字节范围
  resources/                         # 第5层
    result.go                        # Known / Unresolved / HardViolation
    paths.go                         # 相对路径、glob、容器目录
    targets.go                       # C6 已建多目标定位与 guest/Host 类型边界
    windows.go                       # 盘符、UNC、ADS、PowerShell provider 路径
    devices.go                       # 空设备身份：Unix /dev/null、Windows NUL；$null 为语法层丢弃输出
    gitstate.go                      # Git 配置与 hooks 核验
    fingerprint.go                   # 执行环境与内容版本指纹
  policy/                            # 第6层
    hardlimits.go                    # 3.3 第 1 步
    offsets.go                       # 3.3 第 2 步：空设备与凭据抵消
    legacy.go                        # C5 暂存旧 curl/wget 空设备规则；C11u 删除
    mapping.go                       # 3.4 映射表
  plan/                              # 第7层
    plan.go                          # BashPlan 与构造
    launch.go                        # 启动绑定（C2 已抽出）
    combine.go                       # 合并、待批准叶子、审批规则
    approval.go                      # 批准登记、查询、消费、来源与元数据

internal/hostshell/powershell.go      # 受信任安装入口、托管模板与环境装载风险准入；不执行用户命令
internal/tools/powershell_helper.go   # 一审一进程、全局并发、预算、管道限额、Job/取消清理
```

### 5.2 剩余迁移去向

| 现在位置 | 去向 | 批次 |
| --- | --- | --- |
| `syntax/bash` 的安全审查与 `BashSecurityApprovals` | 与访问审查合并为一个 plan | C12 |

已完成：工具启动规则移到 `hostshell`（`5554545f`）；`bashast`、`bashsec` → `syntax/bash`，`shellanalysis` → `commands` 与 `unwrap/command.go`，`Effects`/`FileAccess`/`GitCheck` 与调用结构 → `types`（`2fe8bb52`）；Bash 访问审查整体移到 shellreview 根包，中立文本检查移到 commandtext（C1，`0c794567`）；决策、合并、启动绑定及批准移到 `plan/`，连接器调用结构移到 `types/connector.go`（C2，`115e7ef4`）。

保留不动：`internal/accesspolicy` 只保留文件工具也要用的通用路径策略（`BuildPathPlan`、`BuildSubtreePlan`、档位、session 路径解析、保护目录）；`internal/hostshell` 负责启动合同；`internal/shellenv`、`internal/scriptstate`、`internal/skillsexec` 不变；进程启动、凭据注入与进程生命周期仍归 `tools`。

### 5.3 依赖方向

```text
hostshell ─────────────> shellreview/dialect, shellreview/types
tools / llm ───────────> shellreview
shellreview ───────────> syntax / identity / unwrap / commands / resources / policy / plan
上述各层 ──────────────> types / dialect / registry（中立数据叶子，不导入分析器）
commands（聚合）───────> 各命令分析器 / commands/powershell
命令分析器 ────────────> types / shellreview/options（C6 已建；纯函数）
identity ─────────────> unwrap（依赖标准库与 types/registry 数据叶子）
resources ────────────> identity（不依赖 syntax、policy、plan 或根包）
resources / policy / plan ────> accesspolicy（通用路径策略）
plan ─────────────────> types / contracts（保持现有批准上下文合同，不依赖根包或语法）
syntax/bash ───────────> mvdan/sh
syntax/powershell ─────> types / commandtext 文本检查 / 窄 HelperRunner
tools ────────────────> helper 进程生命周期 / processgroup（不反向依赖 tools）
config / hostshell / syntax ───> commandtext（中立叶子，只依赖标准库）
accesspolicy ──────────> 不依赖 shellreview（包括经 config 的传递依赖）
```

递归（`bash -c`、身份—包装交替）由 `review.go` 调度，子包不反向调用 review。收据登记与消费保持现有 contracts 上下文合同（纯分析器不接收 ExecutionContext）；S7 统一批准时再收窄收据接口。

当前临时边：`syntax/bash → unwrap` 在 C6 后只消费共享硬阻断候选与内层脚本文本；执行展开必须另有身份核验。该边随 C12 统一审查时移除。新增的 `syntax/bash → registry` 是只读名称元数据叶子，不导入命令语义、文件系统或策略，属于 C6 为统一名单所作的依赖细化；并非新增 syntax 到 identity/commands/resources 的调用。unwrap 使用标准库和 types/registry，cwd 解析仍由调用方注入。

`accesspolicy` 不依赖 `shellreview` 的门槛已在 C1 达到。Bash 代码先整体移出，再拆子包，避免经 BuildPathPlan 等路径策略形成反向循环。另发现 config 的 Shell 配置校验原先引用 shellreview/syntax，因此把中立文本校验及其测试原样移入 commandtext，并同步全部调用方；否则 go list 的传递依赖仍会包含 shellreview。C1–C6 已完成，下一批可推进 C11u 或按依赖推进 Windows 的 C7，见 §0.3。

C5 已把脚本证明核验移到 identity/provenance.go、可见 opaque 路径筛选移到 resources/operands.go；根包不再识别 cd/git/curl/wget 的字符串，原 cd/Git 目录知识分别在 commands/cd.go、git.go，C6 已从 registry 读取名字/类别，Git 仓库目录改由 Dep 携带。

C3 为避免子包反向依赖根包，先把 BashExecution、BashEnvironment 与临时根逃逸错误移到 types；C4 已迁移摘要与资源检查，具体容器探针的进程执行仍由 tools 管理。系统安装表在 identity 初始化时冻结；SystemPrograms 返回副本，可信调用方可经 BashEnvironment.SystemPrograms 显式提供本次快照，容器身份不使用 Host 快照。解析与交替调度留在根包，identity 不依赖 syntax 或根包；直接连接器调用由根包确认单条语法并分析后，调用 identity 的入口与参数边界核验。

## 6. Windows

已决定：Windows Host 继续使用 PowerShell，不改为 Git Bash；PowerShell 语法使用 PowerShell 自带的 AST 解析器。Git Bash 尚未启用，本期不设计 MSYS 方言。

### 6.1 方言矩阵

| 执行环境 | 实际执行 | 现状（`5554545f`） | 目标 |
| --- | --- | --- | --- |
| Unix/macOS Host，系统 Bash + 默认模板 | `bash --noprofile --norc -o pipefail -c` | Bash 语义分析 | 不变 |
| Unix 配置其他 Shell、非系统 Bash、登录/交互或附加源码模板 | 按配置 | 未知方言合同 | 不变，不计划支持 |
| 容器（含 Windows 宿主上的容器） | 容器内 bash | Bash 语义分析 | 不变 |
| Windows Host 默认 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command`，或配置的 `pwsh` | 未知方言合同 | S5 起按 PowerShell AST 审查 |
| Windows 配置 `cmd` | `cmd.exe /d /s /c` | 未知方言合同 | 不变，不支持 cmd 语法 |
| Windows 配置 `bash.exe` | MSYS bash | 未知方言合同 | 不变；Git Bash 启用时另行设计 |
| 单条直接连接器调用 | 不经 Shell，按 argv 启动 | 冻结 argv 合同 | 不变 |

### 6.2 PowerShell 语法：自带 AST 解析器

**做法**

- 先满足 §6.9 的安装与环境信任条件，再用与执行相同的 PowerShell 可执行文件解析。5.1 与 7.x 的语法不同，7.x 内部还存在传参、重定向和模块差异，必须选择 §6.8 的实际版本能力合同，不能只比较主版本或程序名。
- 平台自带一个固定 helper 脚本（`go:embed`），用 `[System.Management.Automation.Language.Parser]::ParseInput` 解析，受限身份查询按 6.3 执行；不执行被审查的命令，不在审批前自动加载待检查模块。命令文本经 stdin 传入，不拼接进 helper 脚本或命令行。
- `ParseInput` 不是无副作用的：解析结束时的符号解析（`PerformPostParseChecks` → `SymbolResolver.ResolveSymbols`）会对 `using module` 调用 `Get-Module -ListAvailable`，按名字搜索 PSModulePath、按路径读取模块文件，路径可以是 UNC（审批前访问网络，可能泄露 NTLM 凭据）。首版在 Go 端预检：命令文本中出现单词 `using`（不分大小写，不区分是否在字符串或注释内）就不启动 helper，整条走 6.6。原型在 5.1/7 上确认 `using` 不在首位、出现在字符串里时解析器怎样处理之后，才能收窄这条预检。依据：PowerShell 源码 `SymbolResolver.cs` 的 `VisitUsingStatement`/`GetModulesFromUsingModule`。
- helper 使用与最终执行相同的已冻结有效环境（PATH、PATHEXT、PSModulePath）与 cwd，采用同一受管模块初始化合同（§6.10），输出 JSON：真实能力、语句、调用、参数、重定向，以及每个命令名的解析结果（6.3）。helper 协议封装归 `syntax/powershell`，进程预算与生命周期归 tools；Go 端按下方白名单映射为与 Bash 相同的调用结构。
- 生命周期按下方固定合同执行；helper 普通故障进入 6.6，用户取消和启动绑定变化单独终止，不进入回退审批。
- helper 解析实际执行的整段文本（2026-11-01 确认）：平台前缀（UTF-8 输出设置、`$env:PATH=` 环境绑定、6.10 的模块导入与守卫）加用户命令。Go 端按源范围核对前缀语句与固定模板逐字一致，只分析其后的用户部分，源范围减去前缀长度后对应回用户命令。这样分析的就是实际执行的文本：例如用户命令开头的 `using`、`param` 单独出现时合法，放在前缀之后就是语法错误，helper 与执行端会得到同样的结果。前缀模板的转义另有单独测试。
- 预审与启动前复审都重新解析，不复用缓存的 AST。

**helper 生命周期（原待确认第 4 项，设计已决定，S5 实现）**：

1. 仅用于 Windows Host 的受支持 PowerShell 启动模板；容器 Bash 不使用它。一次审查启动一个 helper 进程，完成解析及获准的身份查询后退出；预审、启动前复审分别运行，不常驻、不缓存 AST 或身份。同一审查不为身份查询另起第二个 PowerShell。
2. 使用 LaunchSpec 对应的 canonical 可执行文件，并核验摘要，继承与执行一致的有效环境、cwd；不通过 basename 或另一套 PATH 重新选择程序。自定义登录/profile、附加源码等未验证启动模板继续走 6.6。
3. 参数固定为 `-NoProfile -NonInteractive -NoLogo -ExecutionPolicy Bypass -EncodedCommand <bootstrap>`。bootstrap 为固定短脚本，编码为 UTF-16LE 后 Base64；stdin 第一行传内嵌 helper 的 UTF-8 Base64，第二行传用户命令原始 UTF-8 字节的 Base64，写入后关闭 stdin。只有受平台控制的 helper 被构造并执行为 scriptblock，用户命令只能进入 Parser，不调用或求值。响应使用 `[Console]` 直接输出一行 UTF-8 JSON 的 Base64，避免管道格式化与 5.1 控制台编码差异。
4. 固定启动参数用于无临时脚本文件的传输，不承诺绕过组策略、AMSI、EDR 或语言模式限制。受阻、超时或启动失败时按第 8 条处理；不自动改用另一套启动方式。
5. 总预算 10 秒，从进入队列开始计时；全局最多同时运行 4 个 helper，等待可取消。stdout 编码后上限 1 MiB（包含行结束符），stderr 上限 64 KiB，命中上限即终止进程树；解码 JSON 上限 768 KiB。命令 UTF-8 原文上限 1 MiB，进入队列前检查；helper 源码不占命令限额。AST 最多输出 256 个调用，遍历深度最多 64；超出按不支持处理，不能静默截断。
6. 用现有 Windows `processgroup.Run` 在挂起状态加入 Job 后恢复运行，隐藏窗口。退出、超时、超限、取消及协议失败均清理整棵进程树，释放并发槽；父请求取消直接返回取消，不申请未知方言批准。
7. 响应协议固定 `v:1`，含真实 `psVersion`、`psEdition`、解析错误、语句结构及身份结果。Go 严格解码 Base64、UTF-8 与 JSON，只接收一个完整响应，拒绝未知字段、重复字段、尾随内容及无效源范围。LaunchBinding 当前尚无实际 PowerShell 版本字段，S5 需补充版本与执行身份绑定；只接受原生 Windows 验证过的 Desktop 5.1 / Core 7.6 LTS 的具体版本与参数形态（6.8），不能只按程序名认为版本匹配。
8. helper 不可用、启动失败、超时、非零退出、输出超限、协议不合法、版本不支持、用户命令有解析错误：整条命令走 6.6，提供稳定的失败类别与简短原因，不退回 Bash 分析。实际 Shell/模板/摘要/环境/cwd 绑定变化遵循原有 `bash_launch_changed` 合同，旧批准失效，不能通过回退继续消费旧批准；已发现的文本或资源硬阻断始终保留。
9. 记录排队耗时、进程耗时、实际版本、失败类别，不记录命令正文、环境值或原始诊断载荷。分别测 5.1/7 冷启动、热启动及完整工具调用的 helper 总开销；p95 超过 1 秒时触发常驻设计评估，不自动切换。常驻需另行解决环境指纹隔离、新 runspace、请求关联和清理，不能在当前实现中预设安全等价。

此生命周期合同不关闭第 5 项模块与身份规则；该项未经原生 Windows 验证前，不恢复相关命令的免审批。编码与启动参数依据 [PowerShell CLI](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_powershell_exe?view=powershell-5.1)，设备数字输出依据 [GNU stat](https://www.gnu.org/software/coreutils/manual/html_node/stat-invocation.html)；文档与交叉编译不能替代目标环境验收。

**允许的节点**（白名单，其他节点一律 Unknown(syntax)）：

| 节点 | 映射 |
| --- | --- |
| 语句列表、`;`、换行；PowerShell 7 的 `&&`、`\|\|` | 多个调用，所有分支都审查 |
| 管道中的 CommandAst | 一个调用；命令名必须是常量；不能把上游对象绑定的路径误认为已知常量（6.4） |
| 常量参数：裸词、单引号字符串、不含变量和子表达式的双引号字符串、数字 | 参数 |
| CommandParameterAst（`-Recurse`、`-Path:value`） | 参数，保留原文交给命令语义层匹配；`$true/$false` 仅在已支持 cmdlet 的布尔参数值位置识别为布尔常量 |
| `&` 调用常量命令名 | 同 CommandAst |
| 文件重定向，目标为常量或 `$null` | 常量目标为 Output；AST 确认的 `$null` 为语法丢弃输出（6.5） |
| 流合并重定向（`2>&1`） | 中性 |
| `cd` / `Set-Location` / `sl` 加常量路径 | 先证明是可信 Set-Location，再由其独立分析器记录 cwd；未接入时不能按名字更新或沿用后续旧 cwd |

**Unknown(syntax)**，即整条命令 complex，与 Bash 的非简单命令相同：变量与 `$env:` 引用（AST 的 `$null` 重定向及受支持 cmdlet 布尔参数位置的 `$true/$false` 除外）、子表达式 `$()`、可展开字符串中的变量或子表达式、脚本块 `{}`、splatting `@x`、.NET 成员调用与类型字面量（`[IO.File]::WriteAllText`）、赋值语句、函数与类定义、dot-source `.`、`--%` 停止解析符、`-EncodedCommand`。

白名单要检查整个 ScriptBlockAst，不只遍历语句列表：`UsingStatements`、`ScriptRequirements`（`#Requires`，其中 `-Modules` 会在第一条语句之前导入模块，也就早于执行端前置守卫）、`ParamBlock`、`DynamicParamBlock`，以及 begin/process/end/clean 命名块，只要非空，整条就走 6.6。这些都不在语句列表里，只看语句会漏掉。

**硬阻断**：改变后续命令解析的命令。它们会让同一条命令里后面的调用身份判断出错，与 Bash 的 `alias`、`source` 同类：`Set-Alias`、`New-Alias`（及别名 `sal`、`nal`）、`Import-Module`（`ipmo`）、对 `Alias:`、`Function:` 驱动器的写入。

**外部程序参数**：AST 只给出 PowerShell 层面的参数，不能证明 exe 实际收到相同的 argv。5.1、7.0–7.2、7.3+ 与各原生传参模式分别处理，具体合同见 6.8、6.11：

- 参数含 `"`、为空字符串或含需要转义的字符时为 Unknown(syntax)。
- 首批只分析经 5.1 与 7 实测、确认 argv 一致的外部程序参数形态（6.7）。
- `.cmd`、`.bat` 与 cmd.exe 脚本入口不继承普通 exe 的分析假设。

### 6.3 PowerShell 身份

要求（R4）：

- 身份查询不能执行待检查的未知模块代码。按精确名称调用 `Get-Command` 可能触发自动导入，而自动导入会运行模块脚本（[Get-Command 文档](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/get-command?view=powershell-7.5)），所以 helper 不能在允许任意自动加载时直接用它查待审命令；只按 6.10 安全枚举已加载且已核验的命令。
- helper 与执行进程按同一列表、同一顺序从绝对路径导入可信内置模块，并在执行进程中核对冻结的解析结果（6.10）。helper 关闭自动加载，执行端保留（2026-11-01 确认）：按 PowerShell 的查找规则，已加载命令和 PATH 中的外部程序都先于自动加载，两端对已分析命令的解析结果相同；只有两端都找不到的名字才会在执行端触发自动加载，这类命令本来就按 Exec 审查。
- 身份记录至少包含：Shell 路径与版本、环境与 cwd 指纹、模块的真实路径（不只是模块名）、正式命令名、外部程序 canonical 路径。
- 无法确认的项一律 Unknown，即 Exec，不按名称授予 Cmdlet 或 System 身份。

解析顺序与 PowerShell 一致：别名 → 函数 → cmdlet → 外部程序。

- 别名或名称指向可信内置模块（`Microsoft.PowerShell.Management`、`Microsoft.PowerShell.Utility`、`Microsoft.PowerShell.Core` 等，按真实安装路径核对）的 cmdlet，身份为 Cmdlet，以正式名查 registry（`rm` → `Remove-Item`，PS 5.1 的 `curl` → `Invoke-WebRequest`）。
- Application（按 PATH/PATHEXT 找到的 exe）走与 Unix 相同的 System / PlatformBuiltin 判定。
- 函数、`.ps1` 外部脚本、非内置模块命令为 Shadowed，即 Exec。

落地建议（原待确认第 5 项，需按 6.9–6.10 原生验证后启用）：

- 两端使用同一固定的受管模块合同：只把预先核验的内置模块按绝对路径导入；helper 关闭自动加载，执行端保留（理由见 6.10）。未知模块命令保守处理，不能以模块名赋予可信身份。
- 别名表与已加载命令从实际干净会话的安全枚举取得；外部程序先按 PowerShell 自身的类型和优先级分类，再用 Go 的 canonical/PATHEXT 结果交叉核对，不能单独用 LookPath 证明命令是 Application。
- 未加载模块的清单只做保守候选识别，不能因为清单声称导出某名字就授予 Cmdlet 身份，也不执行清单内容。命令实际元数据、模块依赖、执行端守卫与版本一起冻结。
- 原生实测不符合预期时保持 6.6，不改变两端模块加载规则来强行恢复免审批。该建议是新运行合同的设计，S3 纯搬迁不引入它。

### 6.4 PowerShell 命令语义（cmdlet）

- S5：没有对应分析器的 cmdlet 为 Exec；只有实际 Application 身份、Windows CLI 实现及 argv 合同均通过验收的外部程序，才可使用共享分析器。不能笼统宣称所有 `git status`、`rg`、`curl.exe` 已恢复分析。
- S8：在 `commands/powershell/` 逐命令接入下表。先实现 9 个只读/输出与管道过滤命令，再实现 Set-Location，之后文件修改、最后网络。每个文件保存自己的描述符、参数声明和特殊逻辑；共用以下绑定规则：
  - 参数名大小写不敏感；前缀缩写按该实际模块版本的完整参数元数据确认唯一性，不能只在分析器已登记参数子集中判断唯一。有歧义、未知或动态参数为 Unknown(option)。
  - 位置参数；`-Force:$false` 形式。
  - 分清真实公共参数和各命令支持的 ShouldProcess 参数（如 `-WhatIf/-Confirm`），不能认为每个 cmdlet 都有这些参数；不因 `-WhatIf` 或 `-Confirm:$false` 删除原本识别出的写入、删除、远端要求。
  - `-Path` 支持通配符，`-LiteralPath` 不支持。
- 参数绑定不能通过实际调用 cmdlet、`Invoke-Expression`、用户 scriptblock、动态参数生成器或未授权的 provider 来探测；这些过程本身可能有副作用。只使用已核验的静态元数据和常量输入。
- 管道的对象输入（如 `Get-ChildItem | Remove-Item`）可能给后续 cmdlet 提供路径，不能把“没有显式路径参数”当作无文件效果；无法静态绑定的目标产生 Unknown，并保留已识别的删除/递归等事实。

| 文件/正式命令 | 必须描述的效果与边界 | 顺序 |
| --- | --- | --- |
| `get_location.go` / Get-Location | 读取当前可信 cwd，不授予新目录权限 | S8a |
| `test_path.go` / Test-Path | FileSystem 路径元数据读取；provider/动态参数不能当普通文件 | S8a |
| `get_content.go` / Get-Content | 输入文件读取；`-Path`/`-LiteralPath`、范围与编码按版本元数据；Stream/ADS 未支持时 Unresolved | S8a |
| `get_child_item.go` / Get-ChildItem | 目录与递归读取；过滤条件不替代真实来源范围检查 | S8a |
| `write_output.go` / Write-Output | 仅对常量输入产生输出，无文件写入；不能执行输入对象或 scriptblock 来推断 | S8a |
| `select_object.go` / Select-Object | 只处理管道对象，无文件效果；只支持常量 -First/-Last/-Skip/-Index/-Unique 与常量属性名；scriptblock 与计算属性（哈希表）为 Unknown | S8a |
| `select_string.go` / Select-String | -Path（支持通配符）/-LiteralPath 为读取，模式与开关为常量；只处理管道输入时无文件效果 | S8a |
| `measure_object.go` / Measure-Object | 统计管道输入，无文件效果；常量 -Property/-Line/-Word/-Character/-Sum 等 | S8a |
| `sort_object.go` / Sort-Object | 只处理管道对象，无文件效果；常量 -Property/-Descending/-Unique；-Top/-Bottom 等版本相关参数按实际元数据确认；scriptblock 为 Unknown | S8a |
| `set_location.go` / Set-Location | 仅跟踪经身份确认的 FileSystem 常量目标；后续命令保留可能 cwd，失败分支不丢失 | S8b |
| `new_item.go` / New-Item | Create；符号/硬链接为 Link，位置、链接目标分别记录；其他 provider 不外推 | S8c |
| `set_content.go` / Set-Content | Output/覆盖；目标、编码、值来源分别保留；未知 provider 参数不能视为纯输出 | S8c |
| `add_content.go` / Add-Content | Output/追加；目标与来源、未知参数不丢失 | S8c |
| `out_file.go` / Out-File | Output；Append/NoClobber/Force 与版本编码差异不放宽目录权限 | S8c |
| `tee_object.go` / Tee-Object | 文件目标产生 Output；变量目标是会话状态，不误认为文件；动态目标保守处理 | S8c |
| `copy_item.go` / Copy-Item | 源 Read、目标写入/Create/Replaced 及递归范围；不把替换目标降为可豁免 Output；ToSession/FromSession 额外记远端/未知行为 | S8c |
| `move_item.go` / Move-Item | 源与目标 Rename、被替换对象及相关删除范围，权限分别核验 | S8c |
| `remove_item.go` / Remove-Item | Delete；递归产生 destructive；Force/WhatIf 不覆盖硬阻断 | S8c |
| `invoke_web_request.go` / Invoke-WebRequest | 方法、Body、InFile、OutFile 分别为远端修改、读取、输出；5.1 的内容解析等版本特有行为须单独核实，无法证明无额外代码行为时保留 UnknownBehavior | S8d |
| `invoke_rest_method.go` / Invoke-RestMethod | 依据实际参数集描述方法、Body/InFile、OutFile；不假定与 IWR 每个版本参数一致 | S8d |

没有列入的 cmdlet（包括 Start-Process、Invoke-Command、ForEach-Object 等）不因名称、别名或文件存在就获得已知语义；继续按 Exec/Unknown 处理。明确修改身份/模块的操作仍按 6.2 硬阻断。无法保持后续 cwd/身份正确性的状态变化，不能继续用旧快照分析后续命令，见 6.10。
- 每个 cmdlet 都要核验真实类型与来源，名称带 `Get-` 不等于免审批。

### 6.5 Windows 资源

- PowerShell 的 `$null` 重定向是语法丢弃输出：仅 AST 明确识别的自动变量 `$null` 在重定向目标位置适用，不查询文件身份；单引号字符串 `'$null'`、变量展开为同名字符串及作为外部程序普通参数的 `$null` 不适用。规则不能外推为 Bash 的输入重定向语法。
- Windows 设备首批仅接受去除语法引号后的裸 `NUL`（大小写不敏感、不带目录或扩展名）作为候选。其实际打开行为、非修改式身份确认方法与重定向/外部程序参数差异，必须在原生 Windows 5.1/7 验收后才能启用路径豁免；只有字符设备类型不足以证明是空设备。未实现或未通过验收时为 `Unresolved`，不能按程序名或保留名字符串直接豁免。
- `NUL.txt`、`C:\x\NUL`、`\\.\NUL`、扩展路径或其他保留设备名（`CON`、`PRN`、`AUX`、`COM1`…）首批统一 `Unresolved`，不进入普通文件快速放行。Windows 保留名语义受实际路径形式与 API 影响，不宣称任意写法都已经确认指向设备。
- 路径：盘符绝对 `C:\`、盘符相对 `C:foo`、UNC `\\server\share`、`\\?\` 前缀、大小写不敏感、8.3 短名、ADS `file.txt:stream`、junction。无法确定为普通路径时为 Unresolved。
- PowerShell provider 路径：`Env:`、`Alias:`、`Function:`、`Variable:`、`HKLM:`、`HKCU:`、`Cert:`、`WSMan:` 不是文件系统，一律 Unresolved；`Alias:`、`Function:` 的写入为 6.2 的硬阻断。`FileSystem::C:\x` 这类 provider 限定路径首批也按 Unresolved 处理。

### 6.6 未知方言合同（已实现，`5554545f`）

适用于 6.1 中"未知方言合同"的所有行。单条直接连接器调用除外，它按冻结 argv 合同执行。

| 档位 | 处理 |
| --- | --- |
| ask_for_approval | 每条命令单次人工批准，不可本轮复用 |
| auto_approve | 每条命令单次人工批准，不可本轮复用（R3：不自动运行） |
| full_access | 按 `bash-complex-filesystem` 配置，默认 allow |

所有档位都保持：

- 管理员显式 block 生效。
- cwd 读审查与 editing=false 的执行限制优先于批准。
- 严格命令模式（`shell-features-enabled: false`）直接拒绝。
- Mailbox 上级代批不能放宽为自动批准。
- 不再对正文做 Bash 安全分析。

这只阻止 Bash 误判，识别不出正文里的删除和远端修改。人工批准只表示用户批准了这次完整调用，不代表正文已经审查过，也不提供脚本内部访问隔离。PowerShell 在 S5 后改为按 AST 审查；cmd、MSYS 和其他 Shell 长期保持此合同。

### 6.7 Windows 验收

- 2.1 的样例：当前需要单次人工（单测覆盖，未在 Windows 实测）；S5 后解析为三条语句，`Remove-Item` 为 Exec。
- `Set-Alias rg Remove-Item; rg ...` 硬阻断。
- PowerShell 别名（`rm`、`cp`、`curl`）不得取得 GNU 命令的语义。
- 带初始化标记的测试模块，不得在审批前被 helper 加载。
- 同名模块、别名、PATH 差异下身份判断正确；启动前环境或解析目标变化，旧批准失效。
- helper 不可用、解析错误、超时：整条命令走未知方言合同。
- 用 powershell.exe 与 pwsh 分别解析 `a && b`，结果与各自的执行行为一致。
- 商店安装的 pwsh 应用执行别名需验证 LookPath、EvalSymlinks、内容摘要及实际启动，失败时保守拒绝；当前仅列入验收，未在原生 Windows 验证。
- 5.1/7 分别验证 `-EncodedCommand` 下 stdin 两行协议与中文、引号、换行命令的逐字节往返；冷/热启动、含排队的总超时、并发上限、取消与进程树清理。非 Windows 使用 Go 假 helper 覆盖进程和协议失败，不能替代原生验收。
- 企业策略、AMSI/EDR 阻止 bootstrap 或 helper 时，记录目标环境与失败类别，确认进入 6.6；带初始化标记的测试模块不得在审批前被加载。测试诊断不得泄露命令正文或环境值。
- `$null`、`'$null'`、裸 `NUL`、带目录或扩展名的保留设备写法分别验收；Unix 符号链接与 cp 目标用例见附录 A，不把 Windows 的待验收结论写成已交付。
- PowerShell 5.1 与 7 各用无害的 argv 回显程序，核对引号、空参数、空格、反斜杠、尾随斜杠和 Unicode。
- 直接连接器调用在三档下行为不变。
- 原生 Windows 运行：中文/空格路径、跨盘、UNC、junction、`NUL` 输出；交叉编译与 macOS 模拟不算验收。
- 破坏性样例只解析，或在隔离临时目录用无害替身运行。

### 6.8 PowerShell 版本：精确记录，按能力选实现

“支持 PowerShell”必须细分为语法、调用身份、原生 argv、cmdlet 参数和重定向能力。版本不是 `powershell`/`pwsh` 二选一，也不能只检查 major=7。

| 版本/Edition | 需要区分的行为 | 首版处理 |
| --- | --- | --- |
| Windows PowerShell Desktop 5.1 | 不支持 7 的管道链运算符；原生传参采用旧行为；输出/模块参数可能与 7 不同 | 支持：独立能力档案和 5.1 fixture；只有已验证参数与命令放行到精确语义 |
| PowerShell Core 7.6 LTS（支持到 2028-11-14） | 7 的语法；原生传参有 Legacy/Standard/Windows 三种模式（Windows 默认为 Windows 模式，对批处理等入口仍按旧规则）；原生 stdout 重定向/管道保持字节 | 支持：部署固定的 7.6 patch 版本，记录实际传参模式与目标程序类别 |
| Core 7.0–7.5（7.0–7.3 已停止支持，7.4 LTS 与 7.5 于 2026-11-10 停止支持）、Preview、7.7 及以后、未知 Edition、受限或未经验证的运行环境 | 不承诺与 7.6 等价 | 6.6；不拿 7.6 的解析或能力结果补位 |

首版只支持 Desktop 5.1 与 Core 7.6 LTS（2026-11-01 确认）。7.6 的新 patch 版本先在能力记录中验证再放行；7.6 停止支持之前评估下一个 LTS，同样逐版本验收。版本日期依据 [PowerShell 支持生命周期](https://learn.microsoft.com/powershell/scripting/install/powershell-support-lifecycle)。

事实依据：[PowerShell 原生传参说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5)描述 7.3 的改变；[重定向说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_redirection?view=powershell-7.5)描述 7.4 的字节流改变。两者不能合成一个“PowerShell 7”开关。

`types` 中增加中立 ShellProfile 数据，由 helper 在实际进程报告、Go 核验并冻结：

| 字段组 | 必须记录的内容 | 用途 |
| --- | --- | --- |
| 安装身份 | canonical Shell 路径、可执行文件摘要、实际 PSHOME、引擎程序集 canonical 路径与摘要、有关启动配置/模块依赖摘要 | 防止只有 launcher 相同、实际引擎或模块已变化 |
| 运行时身份 | PSVersion 的完整实际值（含 patch/可用 build 信息）、PSEdition、OS/进程架构、LanguageMode | 选择真实解析与能力，拒绝误用其他版本 |
| 行为能力 | NativeArgumentPassing 实际值或版本已证明的 legacy 状态、已启用且影响语义的实验特性、重定向能力、固定 UTF-8 前置设置与模块初始化合同版本 | 影响 argv、路径/tilde、编码及语法的字段进入审查 |
| 命令身份 | 正式名、类型、模块绝对路径/版本/依赖摘要、参数元数据摘要；原生程序 canonical 路径/实现身份/摘要 | 准确找到对应分析器，不把别名当 exe |
| 绑定 | 配置模板/平台前置源码摘要、有效环境指纹、canonical cwd、profile/依赖摘要 | 与精确批准及执行绑定，不展示敏感环境值 |

读取实际版本采用当前本地进程的 `$PSVersionTable`，不能相信命令名、PE 文件版本或远端/调用方提供的版本文本。该变量的内容见 [自动变量说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_automatic_variables?view=powershell-7.5)。helper 与执行端核对实际加载身份，不能只核对字符串版本。

实现不为每个版本复制所有 cmdlet 文件。`profile.go` 集中解析版本并选择经过测试的能力，`params.go` 用该模块实际静态元数据绑定参数；差异由声明式能力控制。新 patch 或模块版本变化会使旧批准失效，即使后续证明其能力兼容，也要重新审查本次调用。

预审和启动前复审均重新取得并比对 profile。精确批准绑定完整调用与全部相关行为依赖；可复用路径规则额外绑定 Shell 身份、模板/受管初始化和能力摘要，不因此加入无关 cwd 或完整环境值，保持已有路径复用合同。身份、能力或模板变化走 `bash_launch_changed`，不能消费旧批准。执行进程的固定前置守卫还要在用户源码开始前核对实际 profile 和命令身份；守卫失败时用户命令必须零执行并报错，不自动重试用户源码。

### 6.9 helper 启动之前的安装与环境信任

helper 在人工审批之前运行，因此启动它本身必须有可信依据。`ResolveTool` 当前对 Windows 只分类方言并保持 SyntaxSupported=false；它能解析路径和摘要，不等于该程序已可免审批执行。S5 必须新增以下前置门槛：

1. 从部署确认的系统/受管 PowerShell 安装入口识别候选，冻结选中入口而非任意同名 PATH 结果；每次重新校验 canonical 路径和文件。Windows 安装根通过受信任 OS/部署来源取得，不把本次 Agent 改写的 SystemRoot、ProgramFiles、PATH 当作安装证明。
2. 用户可写目录的伪 `pwsh.exe`、Workspace 程序、任意配置的同名脚本、未核实的商店执行别名都不能用于审批前探测。无法证明安装来源时不启动 helper，直接 6.6。系统安装信任不自动扩到该目录下任意程序。
3. 核验引擎/模块依赖及影响 .NET 启动的配置和环境。`DOTNET_STARTUP_HOOKS`、附加依赖/共享存储、CLR profiler 等能够在 bootstrap 之前装载代码的输入必须在首次 helper 启动前按已支持 profile 审查；不能等 helper 报告版本后才发现已经执行了注入代码。具体变量与装载入口清单由 Windows 原型和所支持运行时列全，未覆盖则回退。
4. 不允许只给 helper 清理这些输入而让用户命令用另一套环境。支持的受管合同对两端使用同一冻结环境；若部署需要未支持的装载环境，使用原有未知方言审批合同，不静默改环境来获得精确分析。
5. 只信任预先验证过的固定 helper/引导源码。探测加载的系统模块也必须在启动前按已知安装配置核验；未知模块不用于“试着解析”。失败、策略限制或执行身份变化均不尝试绕过系统限制。

这是支持 PowerShell 精确分析的条件，不是新增系统沙箱。对未知合同进行一次人工批准仍不提供脚本内部文件隔离。

### 6.10 模块、别名与执行端一致性：建议采用的受管合同

原待确认第 5 项在本轮给出具体落地建议；以下是 S5 原型和原生验收的目标，尚未生效。

**受管分析合同与现有未知合同分别冻结。** 受管合同按经预核验的绝对路径导入所需的可信内置模块；helper 与执行端使用同一模块列表、依赖摘要、顺序和前置源码。helper 关闭自动加载（`$PSModuleAutoLoadingPreference = 'None'`），执行端保留默认的自动加载（2026-11-01 确认）。理由：

- 如果执行端也关闭，没有预先导入的模块命令（Expand-Archive、Test-NetConnection、Get-NetTCPConnection、Get-CimInstance 等）会报"无法识别"，而 `Import-Module` 是硬阻断，补不回来。
- 保留自动加载不改变已分析命令的身份。[命令优先级](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_command_precedence?view=powershell-7.5)：已加载的命令先于自动加载；PATH 中找到外部程序时也不自动加载模块。两端预先导入的模块相同，所以 helper 认定的 cmdlet 和外部程序在执行端不变。7.x 的这条顺序有文档依据，5.1 需原生实测。
- 只有两端都找不到的名字才会在执行端触发自动加载。它在 helper 里没有身份，按 Exec 审查；它运行后可能导入同名函数，所以按下文规则，它之后的命令不再沿用旧身份快照。
选择受管合同属于 S5 的单独行为变化，不能混入 S3 搬家；未满足安装、版本或环境门槛时从一开始选择 6.6 的原有未知合同。不能拿一种合同的批准去执行另一种合同。

- 固定内置模块候选为 Core 和实际分析所需的 Management、Utility；以实际安装、版本、清单/二进制/脚本依赖的 canonical 路径与摘要确认，不仅比较 `Microsoft.PowerShell.*` 名字。先形成所支持安装的封闭依赖清单，再允许它们在审批前初始化。自定义 ScriptsToProcess、无法核验的 RequiredModules/程序集、可写依赖或初始化副作用不明确的安装不进入支持范围。
- helper 关闭自动加载后，只安全枚举默认别名、函数和已加载命令。不能精确 Get-Command 后让它为待检查命令自动导入未知模块；未加载的外部模块清单只能提供保守候选信息。PowerShell 的命令优先级与已加载状态有关，不能只用 Go 的 PATH 查找结果证明执行目标，见 [命令优先级](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_command_precedence?view=powershell-7.5)。
- cmdlet 必须确认类型、正式名、模块/程序集和实际参数元数据，再匹配 `commands/powershell/<name>.go`。默认别名以本次实际会话为准，不复制一张“所有 PowerShell 都一样”的表；PS 5.1 的 curl/wget、各平台的 ls/rm 等均作为回归案例。
- 对 Application，helper 的实际命令类型/解析目标与 Go 的 canonical、PATHEXT、安装来源和内容摘要同时核对。扩展名为 exe 也不能单独证明是可信程序；函数/脚本同名、模块限定名或缺省动词解析均不得误获已支持 cmdlet/CLI 身份。
- 执行进程采用相同可信初始化，在用户源码之前检查真实 ShellProfile、已加载模块集合和将使用的命令身份/参数元数据摘要。检查过程本身只操作已核验的内置命令或 .NET API，不加载待查未知模块、不调用用户命令。
- AST 确认会修改别名/函数/模块解析的操作保持硬阻断。尚未建模的函数、脚本或 cmdlet 可能改变当前会话状态；如果它出现在已分析命令之前，不能继续使用旧的 cwd/环境/命令身份快照。如果不能证明后续状态，整条调用退回 6.6，并保留已知硬阻断，不拼接“前半段未知、后半段已证明安全”的放行结果。
- Set-Location 的静态跟踪必须等 S8b 的正式身份与 FileSystem 参数分析器接入；S5 对含它的后续相对调用不沿用旧 cwd。helper 识别不了的命令（没有预先导入，PATH 中也没有）按 Exec 审查，执行时由自动加载照常找到并运行；它之后的命令按上一条处理。

验证必须包含带初始化标记的同名用户模块：仅做解析/身份审查不得创建标记；执行端运行已分析命令（预先导入的 cmdlet、PATH 中的外部程序）时也不得因同名命令加载它。另测 5.1 与 7.6 上"PATH 中有外部程序时不自动加载同名模块命令"这条顺序。若受管模块方案无法在实际 5.1/7 安装中满足这些条件，保留 6.6 并记录失败证据，不能宣布已恢复 Windows 自动语义分析。Windows 的 Full/Constrained/Restricted/NoLanguage 区别依据 [语言模式说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_language_modes?view=powershell-7.5)，不通过降级限制来运行 helper。

### 6.11 AST、原生 argv 与路径的交接合同

**源码范围**：PowerShell/.NET 的字符串偏移按 UTF-16 code unit 处理，Go 的现有 WordSpan 是 UTF-8 字节范围。`positions.go` 根据输入原文建立转换表，验证源范围、代理对边界与原文片段，不能直接把 helper offset 填进 Go Start/End。中文、组合字符、非 BMP Emoji、CRLF、空字符串和嵌套源范围都要有 fixture。未知 AST 节点或无效范围不得静默跳过。偏移与字符串编码依据 [PowerShell 7.4 的 Position.cs](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/System.Management.Automation/engine/parser/Position.cs) 和 [.NET 字符编码](https://learn.microsoft.com/en-us/dotnet/standard/base-types/character-encoding-introduction)，实际边界仍以原生 fixture 验证。

**原生 argv**：`native_argv.go` 只验证原生程序，不参与 cmdlet 参数绑定。记录实际 `$PSNativeCommandArgumentPassing`；Legacy、Standard、Windows 分别拥有实际测试合同。Windows 模式对某些程序/脚本仍采用 legacy 行为，不能把模式名直接等同于所有 exe 都是 Standard，依据 [原生传参模式](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_preference_variables?view=powershell-7.5)。

- 初版仅允许原生 argv 回显程序证实过的常量参数形态。空参数、嵌入引号、引号前反斜杠、数组展开、splatting、变量/子表达式和停止解析符等未覆盖形式保留 Unknown，不推测 argv。
- Windows 程序收到的是一整行命令行，由各自的运行时拆成 argv：MSVC/UCRT 与 mingw 的 C 运行时（curl.exe、Git for Windows）、Rust 标准库（rg.exe）、Go 运行时、.NET 的规则在引号和反斜杠上并不完全相同。一个回显程序只能证明它自己那种运行时。回显程序要按运行时各备一个，每个已登记的外部程序都要记录它属于哪一类；首版只放行所有类别结果一致的参数形态。
- cmdlet 绑定使用 §6.4 的参数集/元数据；不能把 cmdlet 参数简写直接交 POSIX options 算法，也不能把外部 exe 的 `-x:y` 当作 PowerShell 参数值语法重写。
- `.cmd/.bat`、cmd.exe、脚本宿主、`.ps1` 及其他解释器入口不继承一般 exe 的 argv 合同。已验证的单条直接连接器仍使用原有冻结 argv 路径，不经过该 Shell 分析。
- `env -S`、包装裁剪、选项附着值、PowerShell常量解码都保留来源链；不能因为最终字符串是 `/dev/null` 或 `NUL` 就补回字面标记。

**路径与重定向**：使用 Windows 路径语义和当前已证明的 FileSystem provider，不借用 Bash glob 或 Unix `/dev/*` 规则。`-Path` 的通配符语义与 `-LiteralPath` 分开；盘符相对路径、UNC、junction、ADS、保留设备和非 FileSystem provider 按 6.5 处理。无法确认的目标保持 Unresolved，已知 readonly/保护路径阻断优先。审批前不为了探测一个尚未准入的 UNC/provider 目标而连接远端或触发 provider 代码。

Shell 重定向为独立来源，不受原生命令是否 opaque/Incomplete 影响；`$null` 必须是 AST 的语法丢弃目标，`'$null'` 是文件路径。7.4 的字节流能力只说明输出内容如何传递，不授予路径写入权。`NUL` 设备身份仍是独立原生验收项；它未完成不阻塞其他已通过条件的 Windows 命令，保持该目标 Unresolved 即可。

### 6.12 Windows 验收矩阵与可交付声明

| 类别 | 必测案例 | 通过标准 |
| --- | --- | --- |
| 安装与前置加载 | Workspace/PATH 假 pwsh、可写模块、启动钩子环境、商店别名、安装更新 | 未证明可信时 helper 零启动；绑定变化用户命令零执行且旧批准不可用 |
| 版本/能力 | 实际 5.1、部署固定的 7.6 patch；7.4/7.5、Preview、未知 patch/Edition | 用实际解析器；声明未支持的版本走 6.6，不伪装成另一个版本 |
| 语法与偏移 | `&&/\|\|`、引号/反引号、CRLF、中文/Emoji、管道、数组、scriptblock | AST 与实际版本一致；源码来源逐字节对应，未知节点不丢失 |
| 解析期副作用 | `using module \\host\share\x.psd1`、`using module ./m.psm1`、`using assembly`、`#Requires -Modules`、param/begin 块 | 含 `using` 的命令不启动 helper；其余结构走 6.6；审批前没有文件或网络访问（用文件访问审计与网络抓包确认） |
| 身份与模块 | curl/curl.exe、rm 别名、同名函数/模块、PATH 阴影、模块限定调用、初始化标记 | 真实身份决定分析器；审批前不加载未知模块，执行端不切换模块合同 |
| 原生传参 | 每个受支持版本/模式下的常量、空值、空格、引号、反斜杠、Unicode、批处理入口 | 用独立 argv 回显 exe 记录实际边界；只对已证明形态启用分析 |
| cmdlet 参数 | 每个文件的参数集、唯一缩写、重复/缺失值、布尔值、公共参数、跨版本增删参数 | 与实际静态元数据一致；未知不擦除已知读写/删除/远端事实 |
| 资源与状态 | Path/LiteralPath、递归、provider、UNC/ADS/junction、Set-Location 后相对调用、对象管道路径 | 不沿用错误 cwd/参数来源，不把动态目标当无效果；硬阻断在三档都保留 |
| 输出与空设备 | 5.1/7 的文本及原生二进制重定向、`$null`/`'$null'`、NUL 各形式 | 内容能力与路径权限分别验收；没有 NUL 证据时仍为 Unresolved |
| 进程与故障 | 冷/热启动、含排队预算、并发 4、超限、取消、EDR/AMSI/LanguageMode 受限 | 进程树/槽位清理；故障走 6.6，取消结束，硬阻断与旧批准失效保持 |
| 审批与续接 | ask/auto/full、管理员 block、readonly、精确/规则/多叶子、并发/重试/Mailbox | 与既有公共合同一致；一次批准仅对应冻结调用，兄弟要求独立 |

非 Windows 的 Go 假 helper 测试只能证明协议和生命周期逻辑；Windows 交叉编译只能证明编译。原生 Windows 证明写入逐版本记录（Shell/引擎/模块版本与摘要、OS/架构、传参模式、命令/参数范围、执行结果、未覆盖项），不保存命令中的真实 token 或业务秘密。Linux CI 同时跑 387 行旧快照，macOS 的结果不能替代 Linux 的基线稳定性。

对外交付分三档说明：当前 `2fe8bb52` 仅是结构迁移；S5 只有通过矩阵的 PowerShell 外部程序子集获得分析；S8 按单个 cmdlet、模块与 ShellProfile 逐个启用。禁止用“helper 能启动”“major=7”“全部文件已生成”代替语义及执行一致性验收。

## 7. 实施原则

- 方言保护先行（已完成）。PowerShell AST（S5）不等两套审批合并（S7）；S7 的公开 HITL 协议默认保持稳定。
- 先细化事实（S4），再做通用空设备规则并删除 curl/wget 特例（S6）（R5）。
- 纯结构迁移与行为变化分开提交。行为快照使用规范化表示：路径、环境、叶子顺序规范化后比较；原始指纹只在固定 fixture 中逐值验证（R8）。
- 不能把尚未具备检查能力的环境标为已支持。
- S5 依赖 S3、S4，在这之前 Windows 两档每条命令都要人工审批。如果这段时间太长，可以先在现有结构上接入 AST、只放行外部程序，再随 S3/S4 迁移；但同样要先满足第 5 项模块/身份规则和原生 argv 验收（§6.2、§6.3）。

执行合同（原实施计划 §3，S7 前后都适用）：

- LaunchSpec 包含实际 Shell canonical 路径、方言与版本、命令模板/最终源码摘要、Args、有效环境指纹、cwd、执行环境标识。最终复核和进程创建共享同一份冻结值；敏感环境值不展示进卡片或日志，只参与必要的内部绑定。
- 每个叶子要求关联调用、效果/未知来源、Arg/Span 和规则；执行凭据只能匹配对应入口事实。
- 顶层以有界调度完成"绑定当前入口 → 验证是否可展开 → 展开并重新绑定"，不无条件按名称拆包装。
- `accesspolicy` 不反向导入 `shellreview`；`contracts` 不保存需要反向依赖自身的 plan 类型；收据登记/消费使用窄接口或中立数据，不把整个 ExecutionContext 交给纯分析器。

## 8. 决定事项

### 8.1 已决定

- Windows Host 继续使用 PowerShell，不改为 Git Bash。
- PowerShell 语法使用 PowerShell 自带的 AST 解析器，用与执行相同的可执行文件。
- Git Bash 尚未启用，本期不设计 MSYS 方言；Windows 上配置的 bash.exe 与 cmd 一样走未知方言合同。
- 未知方言合同按 6.6 执行：ask_for_approval 与 auto_approve 单次人工，full_access 按配置，严格模式拒绝，代批不放宽（已实现）。
- 工具启动与 Terminal 分开，Unix 默认启动参数不变（已实现）。
- 首批不新增 ps、ss、netstat，先完成已有命令归属与授权合同。
- 本轮规则批准只额外绑定 Shell 身份、模板与方言；相同目标可跨 cwd/环境复用路径规则，精确批准仍绑定完整调用（已实现）。
- 系统 Bash 是启动时搜索路径选中的那一项，每次重新解析链接；安装更新不需重启，后来出现的靠前同名程序不算（已实现）。
- 文本检查分两级：控制字符、双向覆盖/隔离、无效 UTF-8 在所有方言和位置硬拒绝；Unicode 空白与零宽/格式字符在 Bash 只查引号外，未知方言查整条（已实现）。
- 不认识的选项（原 8.2 第 1 项，2026-10-31）：命令分析给出 UnknownOption（含缺少选项值）时，追加 `bash-complex-filesystem`，只能单次批准、不可本轮复用；可见文件仍升级为写入，所以 auto 档下写根外的文件仍要人工；已识别的删除、上传等事实保留。
- 命令的隐式配置（原 8.2 第 2 项，2026-10-31）。不解析配置语言：
  - 显式配置文件（curl `-K/--config`、wget `--config`）记 UnknownBehavior，即 opaque；配置文件路径本身照旧按读取审查。
  - 命令文件声明自己读的配置：curl 是 `CURL_HOME`、`XDG_CONFIG_HOME`、`HOME` 下的 `.curlrc`；wget 是 `WGETRC` 与 `~/.wgetrc`；rg 是 `RIPGREP_CONFIG_PATH`；GNU tar 的 `TAR_OPTIONS` 是选项不是文件，非空即 UnknownBehavior。停用配置的选项（curl 首个参数 `-q/--disable`、rg 与 wget 的 `--no-config`）不记。Windows 的 `_curlrc` 等路径随 S5/S6 补。
  - 资源层按本次调用的有效环境（继承环境、run.env、命令内赋值与 export）求实际路径：不存在，无要求；探测失败，Unresolved；存在但 Agent 免审批写不到（写根外），视为用户自己的配置，无要求；存在且 Agent 免审批可写（写根内，或 `HOME` 等被改到可写位置），记 UnknownBehavior。
  - 记为 UnknownBehavior 并被批准时，批准绑定配置文件的内容摘要，内容变了旧批准失效。
- 默认白名单（原 8.2 第 6 项，2026-10-31）：`bash.allowed-commands` 维持现有部署政策，不由 registry 或"存在解析器"生成。它只在严格模式下检查，默认模式只要求名单非空；登记分析器不等于允许，未登记的允许项（如 `df`、`dbx`、`httpx`）照旧按 opaque 审查。
- 空设备身份（原 8.2 第 3 项，2026-10-31）：设计已决定，S6 实现与验收。Unix 仅认字面 `/dev/null` 并确认字符设备及主次设备号；容器必须探测，失败为 Unresolved，不因性能退回字符串信任。只抵消 Read/Output 的路径要求：Shell 重定向不看调用分析状态，命令参数要求调用分析完整；硬阻断优先。Windows `$null` 为 AST 语法丢弃，裸 `NUL` 仅为候选，身份确认与实际行为待原生验收；其余保留设备形式首批 Unresolved。完整合同见 3.2、3.3、6.5。
- PowerShell helper 生命周期（原 8.2 第 4 项，2026-10-31）：设计已决定，S5 实现与验收。一次审查一个进程，解析与受限身份查询合并，使用冻结启动身份、环境与 cwd；固定 bootstrap/stdin Base64 协议、10 秒含排队预算、并发 4、输出和 AST 上限按 6.2。普通故障走 6.6，取消直接结束、绑定变化使旧批准失效；模块规则第 5 项已在 6.9–6.10 给出具体建议但待验证，不以生命周期决定恢复免审批。
- Windows/PowerShell 补充决定（2026-11-01）：
  - helper 解析实际执行的整段文本（平台前缀加用户命令），Go 核对前缀后只分析用户部分（6.2）。
  - 命令文本含单词 `using` 时不启动 helper，整条走 6.6，因为 `ParseInput` 会在解析时对 `using module` 做模块发现（6.2）；白名单检查整个 ScriptBlockAst，含 using、`#Requires`、param 与命名块。
  - 模块：两端按绝对路径导入同一组可信模块；helper 关闭自动加载，执行端保留（6.10）。
  - 版本：首版只支持 Desktop 5.1 与 Core 7.6 LTS，其余走 6.6（6.8）。
  - 外部程序 argv 按目标程序的命令行解析运行时分类验证（6.11）。
  - 首批 cmdlet 为 20 个，S8a 补入 Select-Object、Select-String、Measure-Object、Sort-Object 的常量形式（6.4）。

### 8.2 原待确认项：本轮已给出落地建议，待验证

第 5、7 项保留原编号，2026-11-01 已补成具体实现建议（下表），不再留空方案。它们未通过原生环境验证，也不因此视为已经实现或已经批准改变当前运行合同。后续按 §0.3 先实现原型与验收；证据不满足时保持已有回退，不擅自扩大支持。

| # | 事项 | 建议 |
| --- | --- | --- |
| 5 | PowerShell 模块与身份规则 | 建议采用 6.9–6.10 的安装/环境前置准入、两端按绝对路径导入同一组可信模块（helper 关自动加载、执行端保留，2026-11-01 确认）、双端身份守卫；待原生验证后启用 |
| 7 | 首批 cmdlet | 6.4 明确 20 个独立文件（2026-11-01 补入 Select-Object、Select-String、Measure-Object、Sort-Object），按 S8a–d 分批；每个 Shell/模块版本独立验收，不以文件完成代表支持 |

另有一项未决的简化建议：§6.8 的 ShellProfile 要求每次审查对引擎程序集与模块依赖计算摘要。如果 §6.9 已限定安装在只有管理员可写的系统目录，这些摘要只能防住管理员，代价是每次审查读取数十 MB。可以改为启动时核验一次安装目录，每次审查只核对入口程序摘要与 `$PSVersionTable`。C7/C8 开始前决定。

## 9. 验证方案与完成定义

### 9.1 分层与性质测试

- 命令层：选项附着/分离、可选值、缺参、重复选项、`--`、表达式、配置依赖、多目标操作；已知危险事实与未知项同时保留。
- 身份/包装：真实系统入口、Workspace 同名程序、PATH 阴影、假解释器、包装中的 cwd/环境变化、递归上限。
- 资源层：canonical 路径、symlink/junction、临时根逃逸、readonly/平台保护、递归子树、空设备与伪装目标；不可解析与硬拒绝分开。
- 策略层：三档、editing=false、未知参数/方言政策、多个叶子、凭据只抵消对应执行要求。
- 授权层：toolID、参数、环境、cwd、程序/脚本/依赖版本变化；单次消费、规则范围、并发、取消、拒绝、未启动、恢复与代批。

快照使用稳定 fixture 和规范化表示。性质测试检查“新增未知不删除既有要求”“更换程序身份不继承语义”“对输出的豁免不能转给删除或其他叶子”。原始指纹只在受控固定上下文中逐值验证。

### 9.2 Windows 和真实环境

- PowerShell 5.1 与 7.6 分别执行无害 argv 回显辅助程序（按目标程序的命令行解析运行时各备一个，§6.11），核对引号、空参数、空格、反斜杠、尾随斜杠和 Unicode；将 `$PSNativeCommandArgumentPassing` 纳入合同。仅有 AST 并不能证明外部程序实际收到相同 argv。[官方参数解析说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5)
- `.cmd/.bat`、cmd.exe 和脚本入口不能继承普通 exe 分析假设；原有已核验连接器 shim 使用独立合同，不借此扩大普通调用授权。
- 分别验收真实容器中的身份、路径、摘要、空设备与探针失败；macOS 的模拟 resolver 和交叉编译不算通过。
- 破坏性样例只解析或在隔离临时目录使用无害替身，不直接执行文档中的删除命令。

### 9.3 交付检查

按改动范围运行语法/命令/资源/授权的相关测试；迁移与最终接入时运行 `go test ./...`。HITL 如需公开协议变更，单独核对 Server、WebClient 和 Mailbox 的读写与重评，不把“内部对象合并”当作前端已支持。

文档更新与发布说明见 §0.6。未通过的目标环境保持未完成状态。

## 附录 A：触发场景与回归样例

| 样例 | 原问题 | 期望 |
| --- | --- | --- |
| `lsof -nP -iTCP:8080 -iTCP:8081 -sTCP:LISTEN` | lsof 未登记，按 opaque 审批 | 只读查询，无需审批（已修复） |
| `curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8081/healthz` | `-o /dev/null` 被当成越界写入 | 无需审批（已修复，S6 改为通用规则） |
| `curl -X POST ...`、`curl -T file ...` | — | `remote-mutation`，空设备输出不豁免 |
| `tee /dev/null` | tee 的输出目标仍按写入审查 | S6 后去掉这一项路径要求，tee 的其他要求照常 |
| `cmd > /dev/null` | — | 重定向本身现已不产生要求；`cmd` 自己的要求（Exec、上传等）照常，不能笼统写成无需审批 |
| `rm /dev/null`、`chmod 600 /dev/null` | — | 不豁免 |
| `cat /dev/null`、`cat < /dev/null` | 直接读取参数与重定向规则不统一 | S6 后字面候选经身份确认可抵消：命令参数还要求调用完整，Shell 输入重定向不要求调用完整 |
| `echo ok > '/dev/null'`、`echo ok > "/dev/null"` | — | 去除语法引号仍为字面候选；S6 校验设备后移除该输出路径要求 |
| `echo ok > //dev/null`、`echo ok > /dev/./null`、`target=/dev/null; echo ok > "$target"` | 旧逻辑规整路径或丢失展开来源 | S6 不享受空设备豁免，继续路径审查 |
| Workspace 的 `null-link` 指向 `/dev/null`，随后 `tee null-link` | — | 符号链接不享受空设备豁免；canonical 路径审查照常 |
| `tee /dev/zero`，或 `/dev/null` 被换为普通文件/链接/错误设备号 | — | 已确认非空设备不豁免；相关路径与其他要求照常 |
| `cp x /dev/null`、`mv x /dev/null`、`install x /dev/null`、`ln x /dev/null` | Write 不足以区分输出与替换目标 | 目标不得按 Output 豁免，源、目标、替换/删除/链接事实分别审查 |
| 容器空设备探针超时、退出失败或输出异常 | — | Unresolved，保留可见事实并追加 complex；已有 readonly 等硬阻断不变 |
| 当前调用带未知选项，`/dev/null` 是它的参数（如 `tee --unknown /dev/null`） | — | 该参数不豁免，保留 complex 和可见路径；不影响独立兄弟调用的完整性 |
| `ls --unrecognized 2>/dev/null`、`python3 task.py 2>/dev/null` | 若按调用完整性限制重定向，opaque/不完整调用会多出写根外要求（auto 档转人工） | 重定向照常抵消，与现状一致；调用自身的 complex/opaque 要求保留 |
| `curl -F file=@/dev/null -o /dev/null URL` | — | 已确认空设备的读/输出路径可按合同抵消，上传/远端修改要求保留 |
| PowerShell `echo ok > $null` 与 `echo ok > '$null'` | 语法丢弃与文件路径混淆 | 前者由 AST 确认语法丢弃，后者是文件路径，其他命令要求继续审查 |
| Windows 裸 `NUL`、`NUL.txt`、`C:\x\NUL`、`\\.\NUL` | — | 裸名仅为候选，身份未验收前 Unresolved；其余写法首批 Unresolved |
| 同名 Workspace 程序 `./lsof`，假 `env` / `bash` 包装 | — | 不取得 System 身份，不按包装展开 |
| `cp a b`（`b` 已存在） | 只看最后一个操作数 | 源读、目标写、被替换对象都检查 |
| 2.1 的 PowerShell 样例（Windows Host） | 被当成单个 echo 放行 | 当前单次人工（已实现）；S5 后解析出 `Remove-Item` 为 Exec |
| `Set-Alias rg Remove-Item; rg -r . C:\x`（PowerShell） | — | 硬阻断 |
| `git status`（PowerShell，git 为 Application） | 当前单次人工 | 仅对应 ShellProfile、Windows git 实现及 argv 合同通过 C10 后使用共享语义；其余保持回退 |
| `rm -Recurse C:\x`（PowerShell，`rm` 为 Remove-Item 别名） | — | S5 为 Exec；S8 为 destructive |
| Unix 配置 `shell-executable: sh` 或登录模板 | — | 未知方言合同（已实现） |
| 含 ESC 序列或双向覆盖字符的命令（任何方言、引号内也算） | `5554545f` 对未知方言跳过全部文本检查 | 硬拒绝（已修复） |
| 提交信息里的组合 Emoji（`👨` U+200D `💻`）、连接器 JSON 里的全角空格 | `ef0b41f4` 曾在引号内也硬拒绝 | Bash 引号内原样保留；未知方言仍拒绝（已修复） |
| `echo` U+3000 `ok`（引号外全角空格）、`git` U+200B ` status`（引号外零宽空格） | — | Bash 与未知方言都硬拒绝 |
| 启动后在更靠前的 PATH 目录写入同名 `bash` | `ef0b41f4` 会当作系统 Bash 分析 | 未知方言合同（已修复） |

## 附录 B：评审意见 R1–R8 摘要

正文中的 R1–R8 引用指以下评审意见，均已并入对应章节。

| # | 意见 | 落在 |
| --- | --- | --- |
| R1 | 先确认程序身份再展开包装，否则同名假 `env`/`bash` 会让外层程序漏审 | §3.1 第 2、3 层交替；C3 |
| R2 | 工具启动不能直接复用 Terminal 的 `hostshell.Resolve`（Git Bash 开关、`-lc`） | 已在 S1 实现 |
| R3 | 未知方言在 auto 档不能自动运行，否则正文里的删除与远端修改无人审查 | 已在 S2 实现（§6.6） |
| R4 | helper 的身份查询不能触发模块加载；helper 与执行端的模块规则要一致并可核对 | §6.3、§6.10 |
| R5 | 通用空设备规则依赖细化后的 Output 等事实，应排在 S4 之后 | §7；C11u |
| R6 | 资源结果分 Known/Unresolved/HardViolation，探针失败不能擦除已发现的硬阻断 | §3.2 第 5 层 |
| R7 | 中立类型放叶子包；多目标操作带角色；命令按方言与身份查找 | §4.1、§4.3、§5.3 |
| R8 | 快照规范化后比较；各类凭据抵消不能删除兄弟要求 | §3.3、§3.5、§7 |

## 附录 C：已有修复与证据

### C.1 lsof 与 curl/wget 空设备（`9731155e`）

**lsof**：新增 `internal/shellanalysis/lsof.go`，接入分派、系统命令识别和 Unix/macOS 默认白名单。识别 `-a/-b/-h/-l/-n/-P/-R/-t/-v/-w` 标志与 `-c/-p/-u/-i/-s/-F` 参数（附着或分离形式）；普通操作数为读取，`--` 后按文件名处理；`-D`、`+d/+D`、未知选项和缺少必要参数回退到 opaque。只消费参数，不校验 PID、用户名、协议状态等值域。

**curl/wget 输出到 `/dev/null`**：`bash_review.go` 中的局部规则。命令为 curl/wget、分析完整、写入目标字符串精确为 `/dev/null`，且处于非 Windows Host 或容器时，`BuildPathPlan` 无错误且未阻断则跳过该输出的路径审批。普通文件、上传、远端修改和 readonly 继续检查。S6 由资源层空设备规则取代。

**验证**：`go test ./internal/shellanalysis ./internal/accesspolicy ./internal/config ./internal/tools ./internal/bashsec` 通过（部分命中缓存）。容器用例使用模拟解析环境；Host 用例在缺少程序时跳过；Windows Host 跳过这组 Unix 诊断用例。

### C.2 决策依据

- 2026-10-31 用 `go test -overlay` 临时测试做只读探针（未执行被审查命令），确认以下调用当时在 ask/auto 档都不需要审批，已由 `addef42c` 修复：`sort --compress-program=sh`（把数据当脚本执行）、`curl -K ./cfg`、`CURL_HOME=.`、`TAR_OPTIONS=--to-command=sh`（GNU tar；macOS 的 bsdtar 不读）、`RIPGREP_CONFIG_PATH`。
- 本机历史 Chat 去重后 8564 条 bash 命令：已分析且不执行代码的简单调用 3382 次；`addef42c` 后 Incomplete 且不执行代码的调用为 0，历史调用的审批结果不变。256 条用到 `/dev/null`，全部为字面写法；331 处 `/dev/null` 重定向中约 136 处跟在未登记程序后面，这是空设备合同区分"重定向"与"命令参数"的原因。
- 默认白名单 `bash.allowed-commands` 只在严格模式（`shell-features-enabled: false`）下由 `validateStrictCommand` 检查，默认模式只拒绝空名单。
- 2026-11-01 查证的 PowerShell 事实：`ParseInput` 结束时的符号解析会对 `using module` 调用 `Get-Module -ListAvailable`（源码 `SymbolResolver.cs`）；已加载命令与 PATH 中的外部程序都先于模块自动加载（about_Command_Precedence，5.1 待实测）；7.4 LTS 与 7.5 于 2026-11-10 停止支持，7.6 LTS 支持到 2028-11-14。

### C.3 快照与验证方法

- 快照：`internal/shellreview/bash_snapshot_test.go` 与 `testdata/bash-review-baseline.txt`，129 个示例 × 三档 = 387 行。它显式覆盖当前普通命令、内建、包装名单，以及解释器、build、opaque、连接器、技能脚本、Chat 自写脚本、readonly 与多叶子要求；比较决策、规则类别、作用域、复用属性和 Shell 语法审查。快照在迁移前生成，并已复制到迁移前源码树（`git archive`）上复核一致。
- 默认测试只读快照；`go test ./internal/shellreview -run '^TestBashReviewNormalizedBaseline$' -args -update-shell-baseline` 才更新（C1 已随代码移包）。行为变化必须先审核差异，不能用更新快照掩盖回归。快照目前只在 macOS 上跑过。
- 与改动前比较时，用 overlay 或 `git archive` 导出的源码树，不用 `git stash`（工作区常有其他未提交改动）。
- 常用检查：`go test ./...`；改动包的 `go vet`；`GOOS=windows GOARCH=amd64 go test -c` 交叉编译相关包（只证明能编译）；`git diff --check`。破坏性样例只解析，或在隔离临时目录用无害替身运行。
- C1（`0c794567`，macOS）：68 个迁移函数/方法与开工基线的 AST 函数体一致（仅规范化 5 个辅助函数名与文本包引用）；迁移范围原有 111 个测试入口全部保留且无重复。快照 SHA-256 为 `68322d37770f54d883ccb8864150f77f1b53d551edd92bd65d36bc502bf88ef8`，与迁移前相同；`go list -deps ./internal/accesspolicy` 无任何 shellreview 包。
- C1 验证通过：`go build ./...`、`go test -p 1 ./...`；accesspolicy、commandtext、shellreview 各包、config、hostshell、tools、llm 的测试与 vet；Windows amd64 的 shellreview/accesspolicy/tools 测试交叉编译。Linux 运行与原生 Windows 运行尚未验证。
- C1 并行全仓测试有失败记录：第一次 `TestInvokeHostBashStreamsOutputBeforeCompletion` 超过输出等待时限；第二次 `TestInvokeHostBashDoesNotWaitForBackgroundProcessOutput` 超过返回时限，且 `TestInterruptRunsDetachedSSEProxy` 未得到 interrupted。首个测试单独重复 5 次通过，随后串行全仓通过；原因尚未定位，不据此宣称并行全仓已稳定，也未调整测试时限或运行逻辑。

- C2（`115e7ef4`）：319 个函数/方法及 3 个跨包结构与 C1 的 AST 一致（仅规范化包限定、构造改名及 SecurityReview 方法转函数）；全仓 internal 的 4523 个既有 Test 入口计数不变，387 行快照 SHA-256 与 C1 相同。`go test -p 1 ./...`、相关 vet、macOS 构建、Linux amd64 交叉构建及 Windows amd64 的 shellreview/plan/tools 测试交叉编译通过。accesspolicy 无 shellreview 传递依赖，plan 不依赖根包或 syntax。

- C3（`11b404ae`）：45 个既有生产函数/方法经 AST 核对（规范化包限定/导出名、包装查询与连接器尾部提取；新增显式系统安装快照分支单独核对，默认行为保持）。4523 个旧 Test 入口全部保留，另加 1 个系统快照隔离回归；387 行快照 SHA-256 与 C1 相同。串行全仓测试、相关 vet、macOS 构建、Linux amd64 交叉构建及 Windows amd64 的 shellreview/tools 测试交叉编译通过。identity 不依赖根包或 syntax，types 只依赖标准库与 dialect，accesspolicy 依赖门槛保持。

- C4：结构提交 `f00f0da5` 的 15 个旧函数体 AST 一致（规范化导出名与 Git 容器布尔输入），原资源规则和缓存不变；独立修复 `93ee79a9` 以红绿回归复现并修正容器 nil 环境/缺少 Inspect 时读到 Host 文件的问题，同时覆盖 Host、成功及失败 guest 探针。串行全仓测试、相关 vet、Linux 交叉构建、Windows resources/tools 测试交叉编译通过；387 行快照 SHA-256 不变。该修复不代表真实容器端到端已验收。

- C5（`058e808a`）：使用 git archive 导出的 `93ee79a9` 与当前代码，在相同固定路径/安装快照/环境运行 67 个命令 × 三档 × 两种编辑状态 × 两种语法支持状态 × 两种 CDPATH = 1608 个案例；完整 BashPlan JSON（含精确指纹、原因、作用域、所有要求）逐字节一致。命令包括空命令、空参数 cd、多 cwd 分支、嵌套包装、未知选项、隐式配置、Git、glob、opaque、输出、重定向及危险操作；已有凭据测试另由全仓回归覆盖。串行全仓测试、相关 vet、macOS 构建、Linux amd64 交叉构建及 Windows amd64 shellreview/tools 测试交叉编译通过；387 行快照 SHA-256 保持。unwrap 为纯标准库，resources 不依赖 plan，identity/resources/policy/plan 均不依赖根包或 syntax；原 syntax/bash→unwrap 临时边保持到 C12。

- C6a（`a9cb5c94`）：45 个既有声明（包括全部选项规格）AST 一致；对 `058e808a` 的 1608 个固定环境完整计划差分逐字节一致，387 行快照 SHA-256 不变。串行全仓测试、相关 vet、Linux amd64 交叉构建及 Windows amd64 Shell 测试交叉编译通过；新增通用选项边界测试。命令 registry、包装来源、细化事实与包装差异仍未完成。

- C6b（`fa09efee`）：2012 组 env 输入与 14084 组包装输入的 argv、cwd、环境和成功状态与 `a9cb5c94` 一致；1608 个完整计划差分一致，387 行快照 SHA-256 不变。新增真实 Bash 源码范围、相同展开值不同来源、中文、嵌套包装、brace/glob、split 生成/转发与假包装回归；缺少来源时不从字符串猜字面。串行全仓复跑、相关 vet、Linux amd64 构建及 Windows amd64 Shell/unwrap 测试交叉编译通过。首次全仓的 `TestQuerySSEPersistsChatHistory` 缺少 run.complete，已在改动前 `a9cb5c94` 的 200 次运行中复现 3 次，Server 未修改；另一次并发包测试中快照出现解析复杂回退，串行复核通过，未更新快照。

- C6c–C6d（`c6294b4d`、`51cfce5b`）：对 `fa09efee` 的 1608 个固定环境完整计划逐字节一致。注册表测试固定迁移前身份、包装、捕获、严格模式与硬阻断名单；复杂命令分文件，选项/目标来源从下标传递。变量与字面同值、UTF-8、相连/独立选项、env -S 生成/附加词均有直接回归。
- C6e（`6b3fcee1`）：把三档复制/移动/链接只读测试放到 `51cfce5b` 的 git archive 源码后，真实复现 `cp sources/protected dest` 漏掉 `dest/protected`；当前全部阻断。另覆盖硬链接源、相对软链接、`-t`、未知选项、备份、复合 mkdir，以及 guest 缺失/失败类型探针不能读 Host。旧 387 行快照不变；此后新增行为不再宣称所有完整计划与旧版等价。
- C6f（`44f1ad38`）：1764 组实际语法审查结果对照旧源码，0 个旧硬阻断降级、36 个新增阻断，主要是 nohup/timeout 的 `--` 路径；历史 timeout 候选仍保留。已为 command -p/-v、未知选项、nice 附加值、timeout 信号/分隔符、来源对齐加入固定测试。
- C6g（`bbed0bbf`）：Wget 独立语义依据 [GNU Wget HTTP 参数](https://www.gnu.org/software/wget/manual/html_node/HTTP-Options.html) 与 [下载参数](https://www.gnu.org/software/wget/manual/html_node/Download-Options.html)；printf 状态变更依据 [Bash 内建文档](https://www.gnu.org/software/bash/manual/html_node/Bash-Builtins.html)。新增 Git/派生输出只读测试在 `51cfce5b` 源码上复现 `git diff --output=protected` 可进入审批而未硬阻断，当前三档全部硬阻断；未知参数仍保留远端修改/删除事实，显式安全输出不因无关只读子目录被阻断。Git 相对文件基于连续 -C 后的目录，源的读取与写入分别检查。
- C6 本轮验证：macOS 串行全仓 `go test -p 1 ./...`、相关 shellreview/tools/llm 测试与 vet、Linux amd64 `CGO_ENABLED=0 go build ./...`、Windows amd64 Shell/tools/llm 测试交叉编译通过。387 行快照 SHA-256 仍为 `68322d37770f54d883ccb8864150f77f1b53d551edd92bd65d36bc502bf88ef8`；未刷新文件；accesspolicy 无 shellreview 传递依赖。原生 Windows、Linux 上执行与真实容器完整链路仍需相应环境，不能把交叉编译写成通过这些验收。

### C.4 已修复的复审问题

- 对 `5554545f` 的复审：未知方言跳过全部文本检查；系统 Bash 升级后变成未知方言；本轮规则批准绑定了 cwd 与环境；相对路径的 `shell-executable`；Windows 严格模式说明。由 `ef0b41f4` 修复。
- 对 `ef0b41f4` 的复审：引号内的 Unicode 空白也被硬拒绝（改为两级检查）；启动后 PATH 前置目录的同名 `bash` 被当作系统 Bash（改为冻结链接）。由 `fd11f873` 修复。
- 对 `2fe8bb52` 的复审：结构迁移无问题；空设备合同按来源区分、S3 剩余顺序、S4 来源传递三处计划修正已并入 §3.2、§0.3、§5.3。
- 对 Windows 方案的复审（2026-11-01）：`using` 的解析期副作用、解析整段执行文本、执行端保留自动加载、版本范围、按运行时验证 argv、首批 20 个 cmdlet，已并入 §6 与 §8.1。

### C.5 待处理的小事

- 未合并的 `chore/dead-code-cleanup` 分支改过 `bashast/parser.go`、`bashsec/bash_security.go`、`shellanalysis/command.go`、`git.go`、`operands_test.go`，合并时要按新路径处理。
- `docs/工具目录权限.md` 的"Shell 语法硬阻断 仍然阻断"空格已在 C1 同步现行代码归属时修正。
