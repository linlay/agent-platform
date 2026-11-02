# Shell 命令审批：剩余工作

> 2026-11-02 精简。Unix/macOS Host 部分已全部完成（S1–S4、C1–C6、C11u、C12、C14u1，最后一笔 `9df6e45c`）。现状以 AGENTS.md、[工具目录权限](../docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](../docs/AccessPolicy与HITL改造.md) 为准。过程、评审意见与验证证据见 git 历史，精简前全文：`git show 6a490a32:plans/shell-command-analysis.md`。
> 本文只剩三块：Windows PowerShell 精确分析（C7–C10、C11w、C13）、真实 Container Hub 精确分析的前置合同（C14u2）、总验收与文档收尾（C14）。Windows 相关结论都没有在原生 Windows 上运行过。

## 0. 接手入口

### 0.1 现状

- Windows Host 默认以 `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command`（或配置的 `pwsh`）执行；cmd、MSYS `bash.exe` 也不经 Bash 解析。实际 Container Hub 以 `/bin/sh -lc` 执行。这些现在都走未知方言合同（§3）。
- 为什么不能用 Bash 语法审 PowerShell：`echo "\" ; Remove-Item -Recurse C:\x ; echo "\"` 在 Bash 里是一个 echo，在 PowerShell 里是三条语句。PowerShell 的 `cat/ls/rm/cp/mv/sort/tee` 是 cmdlet 别名，5.1 的 `curl/wget` 是 Invoke-WebRequest，按名字套 GNU 语义是错的身份。
- 已有分层。Windows 新增部分产出同一套中立事实（`types`），共用 resources/policy/plan：

| 层 | 现有代码 | Windows 新增 |
| --- | --- | --- |
| 0 启动与方言 | `internal/hostshell/tool.go`（`ResolveTool`） | `hostshell/powershell.go`：可信安装入口与环境准入 |
| 1 语法 | `shellreview/syntax/bash`；根包 `security_*.go` | `syntax/powershell/{helper.ps1,helper.go,ast.go,profile.go,native_argv.go,positions.go,testdata/}`；`tools/powershell_helper.go` 管 helper 进程 |
| 2 身份 / 3 包装 | `identity/`、`unwrap/`，根包 `execution.go` 交替调度 | `identity/{powershell.go,powershell_modules.go,windows.go}` |
| 4 语义 | `registry/`（唯一名单）、`commands/`、`options/`、`types/` | `commands/powershell/`：每个 cmdlet 一个文件，加 `params.go`、`common.go` |
| 5 资源 | `resources/` | `resources/windows.go`；`devices.go` 加 NUL |
| 6 策略 / 7 合并 | `policy/`、`plan/`；入口 `review.go`、`unified.go` | 不新增 |

- 行为基线在 `internal/shellreview/testdata/`：387 行原始快照（SHA-256 `68322d37770f54d883ccb8864150f77f1b53d551edd92bd65d36bc502bf88ef8`）、8 行 Unix 资源增量、18 行统一审批增量。只有 `go test ./internal/shellreview -run '^TestBashReviewNormalizedBaseline$' -args -update-shell-baseline` 才会更新。

### 0.2 规则

- 每批可拆成多个能编译的提交；结构迁移和行为变化不放同一提交。改旧入口时同一提交改完所有调用方并删除旧入口，不留转发别名。
- 快照只在行为变化经审核后更新。比较改动前后用 overlay 或 `git archive` 导出的源码树，不用 `git stash`。
- Windows 的额外合同未经原生验证前，不设置 SyntaxSupported=true；没通过的能力保持未知方言，或让对应目标保持 Unresolved。
- 命令文件只产出事实，不返回 allow/hitl。未知与已识别事实同时保留；空设备和连接器/技能/自写脚本凭据只抵消各自那一项，不删兄弟要求；硬阻断任何档位、任何批准都不放宽。
- 只有 `.go` 文件不代表支持。一个命令要同时通过实际 Shell 版本、模块身份、参数集、Windows 路径和三档权限验收才启用。交叉编译、macOS 模拟和 Go 假 helper 都不算原生验收。
- 默认取向：采用受管模块一致性合同原型；按 20 个 cmdlet 分批交付；不强行安装或切换用户的 PowerShell，不静默替换系统策略；未知版本、安装和环境保持现有保守路径。原生证据否定某项假设时，先改合同和测试，再扩大支持。
- 版本验证的产物（实际版本/Edition、引擎与模块身份、LanguageMode、传参模式、影响语义的特性、AST/源范围样例、原生 argv 实测、各 cmdlet 参数与效果对照）放进对应 testdata；不同版本不能只给同一份 fixture 换标签。

### 0.3 剩余批次

C7–C10 不需要等 Container Hub，C14u2 也不需要等 Windows。

| 批次 | 内容 | 前置 | 完成门槛 |
| --- | --- | --- | --- |
| C7 安装与环境信任 | `hostshell/powershell.go`：可信安装入口、canonical 路径与摘要、引擎/配置依赖、能在启动前注入代码的环境（§2.4）；确认商店别名与安装更新策略 | 可先做隔离原型 | 未证明信任时 helper 零启动；不能先运行假 pwsh 或用户模块再判断身份。原生检查所支持运行时的全部装载入口，覆盖不了就走未知方言 |
| C8 helper 与版本 | §2.2、§2.3、§2.6：一次进程完成受限解析与元数据；版本能力；UTF-16→UTF-8；协议、预算、并发、取消 | C7 | Go 假 helper 覆盖故障；实际 5.1 与部署固定的 7.6 patch 的协议/AST/版本证据；7.4/7.5、Preview、未知版本走未知方言；含 `using` 的命令不启动 helper；本批不放开任何命令 |
| C9 模块与执行一致性 | §2.5：`identity/powershell{,_modules}.go`；两端按绝对路径导入同一组可信模块，helper 关自动加载、执行端保留；别名/正式名/参数元数据；执行端前置守卫 | C8 | 未知模块的初始化标记不出现；helper 与执行端模块和身份相同；5.1/7.6 实测“PATH 有外部程序时不自动加载同名模块命令”；守卫失败时用户源码零执行 |
| C10 原生 argv 与启用 | §2.7：`native_argv.go`；按目标程序的命令行解析运行时各备一个 argv 回显 exe；Legacy/Standard/Windows 传参模式；只有已验证的 git.exe/rg.exe/curl.exe 等子集接共享分析器，通过后接入现有统一 BashPlan | C9、原生 Windows fixture | 每个已支持 profile 都证明 argv 一致；批处理、脚本宿主和未覆盖参数不借用 exe 合同；其他仍为 Exec 或未知方言 |
| C11w Windows 资源 | §2.9：FileSystem provider、Path/LiteralPath、盘符/UNC/junction/ADS、重定向与 `$null`；NUL 身份原型；Windows 默认配置文件（如 `_curlrc`） | C10 | 各路径类别按实际 Windows 证明；未证明的目标为 Unresolved；NUL 未完成不阻塞其他已验证能力 |
| C13a 只读与输出 | `commands/powershell/` 的 `params.go`、`common.go`，加 9 个只读/输出 cmdlet（§2.8；后四个只支持常量形式） | C10、C11w | 每个实际模块版本的参数集、缩写、provider/管道、三档权限测试通过，逐命令启用 |
| C13b 会话 cwd | `set_location.go`：只跟踪可信 FileSystem 常量目标，分支/失败和后续相对路径纳入 | C13a | 不沿用错误 cwd；未建模的状态操作之后不用旧的身份/环境快照 |
| C13c 文件修改 | 8 个修改类 cmdlet | C13b | 复制源/目标/替换对象、链接、递归/删除与 Output 分类正确；WhatIf/Confirm/Force 不放宽 readonly 或管理员 block |
| C13d 网络 | Invoke-WebRequest、Invoke-RestMethod | C13c | 各实际版本的方法、Body/InFile/OutFile、内容解析和动态参数单独验收；与 curl.exe 区分 |
| C14u2 真实 Hub 前置合同 | §4 | C14u1（已完成）、真实 Hub 与镜像合同 | 缺证据时维持未知方言，不用本地 Docker 的 Bash 测试替代 |
| C14 总验收与文档 | 全仓回归、Linux/macOS 基线、原生 Windows 逐版本逐命令矩阵、真实容器；按 §6 收尾 | 以上批次 | 明列已分析子集、仍需审批项、未支持的版本和路径；未完成项保留，不因文件齐全就删除本文 |

所有接入都保持 HITL 公开形状：一次批准只执行一次，并发兄弟调用不能借用，拒绝/取消/未启动不消耗批准，已知硬阻断不降级。

## 1. 决定事项

已决定：

- Windows Host 继续用 PowerShell，不改为 Git Bash。PowerShell 语法用 PowerShell 自带的 AST 解析器，且用与执行相同的可执行文件。
- 本期不设计 MSYS 方言；Windows 上配置的 `bash.exe` 和 cmd 一样长期走未知方言合同。
- helper 生命周期按 §2.3。
- helper 解析实际执行的整段文本（平台前缀加用户命令），Go 核对前缀后只分析用户部分。
- 命令文本含单词 `using` 时不启动 helper；白名单检查整个 ScriptBlockAst（using、`#Requires`、param 与命名块）。
- 模块：两端按绝对路径导入同一组可信模块；helper 关闭自动加载，执行端保留。
- 版本：首版只支持 Desktop 5.1 与 Core 7.6 LTS，其余走未知方言。
- 外部程序 argv 按目标程序的命令行解析运行时分类验证。
- 首批 20 个 cmdlet（§2.8）。
- Windows 空设备：`$null` 重定向是 AST 语法丢弃；裸 `NUL` 只是候选，原生验收前不豁免；其他保留设备写法首批 Unresolved。

有落地建议、原生验证后才启用：模块与身份规则（§2.4–2.5）；首批 cmdlet 逐个 Shell/模块版本验收（§2.8）。

未决（C7/C8 开始前定）：§2.6 的 ShellProfile 要求每次审查对引擎程序集与模块依赖算摘要。如果 §2.4 已把安装限定在只有管理员可写的系统目录，这些摘要只防得住管理员，代价是每次审查读取数十 MB。可以改为启动时核验一次安装目录，每次审查只核对入口程序摘要与 `$PSVersionTable`。

## 2. Windows 设计合同

### 2.1 方言矩阵

| 执行环境 | 实际执行 | 现状 | 目标 |
| --- | --- | --- | --- |
| Windows Host 默认 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command`，或配置的 `pwsh` | 未知方言 | C8 起按 PowerShell AST 审查 |
| Windows 配置 cmd | `cmd.exe /d /s /c` | 未知方言 | 不变，不支持 cmd 语法 |
| Windows 配置 `bash.exe` | MSYS bash | 未知方言 | 不变；Git Bash 启用时另行设计 |
| 实际 Container Hub | `/bin/sh -lc` | 未知方言 | C14u2 |
| 单条直接连接器调用 | 不经 Shell，按 argv 启动 | 冻结 argv 合同 | 不变 |

### 2.2 PowerShell 语法

做法：

- 先满足 §2.4 的安装与环境信任，再用与执行相同的 PowerShell 可执行文件解析。按 §2.6 的实际版本能力选择实现，不只比较主版本或程序名。
- 平台内嵌固定 helper 脚本（`go:embed`），用 `[System.Management.Automation.Language.Parser]::ParseInput` 解析，身份查询按 §2.5。不执行被审查的命令，审批前不自动加载待查模块。命令文本经 stdin 传入，不拼进 helper 脚本或命令行。
- `ParseInput` 有副作用：解析结束时的符号解析（`SymbolResolver.ResolveSymbols`）会对 `using module` 调用 `Get-Module -ListAvailable`，按名字搜 PSModulePath、按路径读模块文件，路径可以是 UNC（审批前访问网络，可能泄露 NTLM 凭据）。所以 Go 端预检：文本里出现单词 `using`（不分大小写，不管是否在字符串或注释里）就不启动 helper，整条走未知方言。原型在 5.1/7 上确认解析器对非首位、字符串内的 `using` 怎样处理之后，才能收窄。
- helper 使用与执行相同的冻结环境（PATH、PATHEXT、PSModulePath）和 cwd，采用同一受管模块初始化合同，输出 JSON：真实能力、语句、调用、参数、重定向，以及每个命令名的解析结果。协议封装归 `syntax/powershell`，进程预算与生命周期归 tools。
- helper 解析平台前缀（UTF-8 输出设置、`$env:PATH=` 环境绑定、模块导入与守卫）加用户命令的整段文本。Go 按源范围核对前缀语句与固定模板逐字一致，只分析其后的用户部分，源范围减去前缀长度后对回用户命令。这样用户命令开头的 `using`、`param` 在 helper 与执行端得到同样的结果。前缀模板的转义单独测试。
- 预审与启动前复审都重新解析，不复用缓存的 AST。

允许的节点（白名单，其他节点一律 Unknown(syntax)）：

| 节点 | 映射 |
| --- | --- |
| 语句列表、`;`、换行；7 的 `&&`、`\|\|` | 多个调用，所有分支都审查 |
| 管道中的 CommandAst | 一个调用；命令名必须是常量；上游对象绑定的路径不当作已知常量 |
| 常量参数：裸词、单引号字符串、不含变量和子表达式的双引号字符串、数字 | 参数 |
| CommandParameterAst（`-Recurse`、`-Path:value`） | 参数，原文交给命令语义层；`$true/$false` 只在已支持 cmdlet 的布尔参数值位置认作布尔常量 |
| `&` 调用常量命令名 | 同 CommandAst |
| 文件重定向，目标为常量或 `$null` | 常量目标为 Output；AST 确认的 `$null` 为语法丢弃输出 |
| 流合并重定向（`2>&1`） | 中性 |
| `cd` / `Set-Location` / `sl` 加常量路径 | 先证明是可信 Set-Location，再由其分析器记录 cwd；未接入时不能按名字更新 cwd，也不能沿用旧 cwd |

Unknown(syntax)，即整条 complex：变量与 `$env:` 引用（`$null` 重定向和上述 `$true/$false` 除外）、`$()`、可展开字符串里的变量或子表达式、脚本块 `{}`、splatting `@x`、.NET 成员调用与类型字面量、赋值、函数与类定义、dot-source `.`、`--%`、`-EncodedCommand`。

白名单检查整个 ScriptBlockAst，不只语句列表：`UsingStatements`、`ScriptRequirements`（`#Requires -Modules` 会在第一条语句前导入模块，早于执行端守卫）、`ParamBlock`、`DynamicParamBlock`、begin/process/end/clean 命名块，任一非空就整条走未知方言。

硬阻断：改变后续命令解析的命令，与 Bash 的 `alias`、`source` 同类：`Set-Alias`、`New-Alias`（`sal`、`nal`）、`Import-Module`（`ipmo`）、对 `Alias:`、`Function:` 驱动器的写入。

外部程序参数：AST 只给出 PowerShell 层面的参数，不能证明 exe 收到相同 argv。参数含 `"`、为空或含需转义字符时为 Unknown(syntax)；首批只分析 5.1 与 7 实测 argv 一致的形态（§2.7）；`.cmd`、`.bat` 和 cmd.exe 入口不继承普通 exe 的假设。

### 2.3 helper 生命周期

1. 只用于 Windows Host 的受支持 PowerShell 启动模板。一次审查一个进程，完成解析和身份查询后退出；预审、复审各跑一次，不常驻，不缓存 AST 或身份，不为身份查询另起进程。
2. 使用 LaunchSpec 对应的 canonical 可执行文件并核验摘要，继承与执行一致的环境和 cwd；不按 basename 或另一套 PATH 重选程序。自定义登录/profile、附加源码等模板走未知方言。
3. 参数固定为 `-NoProfile -NonInteractive -NoLogo -ExecutionPolicy Bypass -EncodedCommand <bootstrap>`。bootstrap 是固定短脚本（UTF-16LE 后 Base64）；stdin 第一行传 helper 的 UTF-8 Base64，第二行传用户命令原始 UTF-8 的 Base64，写完关闭 stdin。只有平台的 helper 被构造为 scriptblock 执行，用户命令只进 Parser。响应用 `[Console]` 输出一行 UTF-8 JSON 的 Base64。
4. 这些参数只为免临时脚本文件，不承诺绕过组策略、AMSI、EDR 或语言模式。受阻按第 8 条处理，不换启动方式。
5. 总预算 10 秒，从入队起算；全局最多 4 个并发，等待可取消。stdout 编码后上限 1 MiB，stderr 64 KiB，超限终止进程树；解码 JSON 上限 768 KiB；命令 UTF-8 原文上限 1 MiB（入队前检查）。AST 最多 256 个调用、深度 64，超出按不支持处理，不截断。
6. 用 Windows `processgroup.Run` 在挂起状态加入 Job 后恢复，隐藏窗口。任何结束方式都清理整棵进程树并释放并发槽；父请求取消直接返回取消，不申请未知方言批准。
7. 协议 `v:1`，含真实 `psVersion`、`psEdition`、解析错误、语句结构和身份结果。Go 严格解码，只接受一个完整响应，拒绝未知/重复字段、尾随内容和无效源范围。LaunchBinding 需补实际 PowerShell 版本与执行身份字段。
8. helper 不可用、启动失败、超时、非零退出、输出超限、协议不合法、版本不支持、用户命令有解析错误：整条走未知方言，给出稳定失败类别，不退回 Bash 分析。Shell/模板/摘要/环境/cwd 变化按 `bash_launch_changed` 让旧批准失效；已发现的硬阻断保留。
9. 记录排队耗时、进程耗时、实际版本、失败类别，不记命令正文、环境值或原始诊断。分别测 5.1/7 冷热启动与完整调用开销；p95 超过 1 秒时评估常驻设计，不自动切换（常驻需另解决环境隔离、新 runspace、请求关联和清理）。

### 2.4 helper 启动前的安装与环境信任

helper 在人工审批之前运行，启动它本身必须有可信依据。`ResolveTool` 目前对 Windows 只分类方言，能解析路径和摘要不等于可以免审批执行。

1. 从部署确认的系统/受管 PowerShell 安装入口选候选，冻结选中入口，每次重新校验 canonical 路径和文件。安装根来自受信任的 OS/部署来源，不把 Agent 改写的 SystemRoot、ProgramFiles、PATH 当证明。
2. 用户可写目录的伪 `pwsh.exe`、Workspace 程序、同名脚本、未核实的商店执行别名都不能用于审批前探测；证明不了来源就不启动 helper。系统安装信任不扩到该目录下任意程序。
3. 审查能在 bootstrap 之前装载代码的输入：`DOTNET_STARTUP_HOOKS`、附加依赖/共享存储、CLR profiler 等，必须在首次启动 helper 前检查，不能等 helper 报告版本后才发现注入代码已执行。完整清单由原型和所支持运行时列全，覆盖不了就回退。
4. helper 和用户命令用同一份冻结环境，不能只给 helper 清理。部署需要未支持的装载环境时，走未知方言，不静默改环境换取精确分析。
5. 只信任固定的 helper/引导源码；探测加载的系统模块也在启动前核验。失败或策略限制时不尝试绕过。

这是精确分析的前提，不是新增沙箱。未知合同的人工批准不提供脚本内部文件隔离。

### 2.5 身份、模块与执行端一致性

解析顺序与 PowerShell 一致：别名 → 函数 → cmdlet → 外部程序。

- 名称或别名指向可信内置模块（Core、Management、Utility，按真实安装路径与摘要核对，不只比较 `Microsoft.PowerShell.*` 名字）的 cmdlet，身份为 Cmdlet，以正式名查 registry（`rm` → `Remove-Item`，5.1 的 `curl` → `Invoke-WebRequest`）。cmdlet 只按正式名登记。
- Application（PATH/PATHEXT 找到的 exe）走与 Unix 相同的 System / PlatformBuiltin 判定；helper 报告的命令类型与 Go 的 canonical、PATHEXT、安装来源和摘要交叉核对，扩展名为 exe 不足以证明。Windows `find.exe` 不用 POSIX find 分析器。
- 函数、`.ps1`、非内置模块命令为 Shadowed，即 Exec。无法确认的项一律 Exec。
- 身份记录至少含 Shell 路径与版本、环境与 cwd 指纹、模块真实路径、正式命令名、外部程序 canonical 路径。

受管合同：

- helper 与执行端按同一列表、同一顺序从绝对路径导入可信内置模块。helper 设 `$PSModuleAutoLoadingPreference = 'None'`，执行端保留默认自动加载。理由：执行端若也关闭，没预先导入的模块命令（Expand-Archive、Get-CimInstance 等）会无法识别，而 `Import-Module` 是硬阻断，补不回来；按[命令优先级](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_command_precedence?view=powershell-7.5)，已加载命令和 PATH 中的外部程序都先于自动加载，所以两端对已分析命令的解析相同（7.x 有文档依据，5.1 需实测）。
- 只有两端都找不到的名字才会在执行端触发自动加载；它在 helper 里没有身份，按 Exec 审查；它可能导入同名函数，所以它之后的命令不再沿用旧身份快照。
- 身份查询不能执行待查模块代码：精确 `Get-Command` 可能触发自动导入，helper 只安全枚举默认别名、函数和已加载命令。未加载模块的清单只作保守候选，不授予 Cmdlet 身份，也不执行清单内容。
- 内置模块要先形成所支持安装的封闭依赖清单；自定义 ScriptsToProcess、无法核验的 RequiredModules/程序集、可写依赖不进入支持范围。
- 执行端在用户源码之前检查真实 ShellProfile、已加载模块集合和将用的命令身份/参数元数据摘要；只用已核验的内置命令或 .NET API。守卫失败用户命令零执行。
- 尚未建模的函数、脚本或 cmdlet 出现在已分析命令之前，又不能证明后续状态时，整条退回未知方言，不拼接“前半段未知、后半段安全”。
- 受管合同和未知合同分别冻结，一种合同的批准不能执行另一种。安装、版本或环境门槛不满足时从一开始选未知合同。
- 原生实测不符合预期时保持未知方言，不改两端加载规则来强行恢复免审批。语言模式（Full/Constrained/Restricted/NoLanguage）不通过降级来运行 helper。

### 2.6 版本

| 版本/Edition | 要区分的行为 | 首版 |
| --- | --- | --- |
| Windows PowerShell Desktop 5.1 | 无管道链运算符；旧原生传参；输出/模块参数可能不同 | 支持：独立能力档案与 fixture |
| PowerShell Core 7.6 LTS（支持到 2028-11-14） | 7 的语法；原生传参 Legacy/Standard/Windows 三种模式（Windows 默认 Windows 模式，批处理等仍按旧规则）；原生 stdout 重定向保持字节 | 支持：部署固定的 7.6 patch，记录实际传参模式与目标程序类别 |
| 7.0–7.5（7.4、7.5 于 2026-11-10 停止支持）、Preview、7.7 及以后、未知 Edition、受限环境 | 不承诺与 7.6 等价 | 未知方言 |

7.6 的新 patch 先在能力记录中验证再放行；7.6 停止支持前评估下一个 LTS。依据：[支持生命周期](https://learn.microsoft.com/powershell/scripting/install/powershell-support-lifecycle)、[原生传参](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5)、[重定向](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_redirection?view=powershell-7.5)。

`types` 增加中立 ShellProfile，由 helper 在实际进程报告、Go 核验并冻结：

| 字段组 | 内容 |
| --- | --- |
| 安装身份 | canonical Shell 路径与摘要、实际 PSHOME、引擎程序集路径与摘要、启动配置/模块依赖摘要（摘要粒度见 §1 未决项） |
| 运行时身份 | 完整 PSVersion、PSEdition、OS/进程架构、LanguageMode |
| 行为能力 | NativeArgumentPassing 实际值、影响语义的实验特性、重定向能力、UTF-8 前置设置与模块合同版本 |
| 命令身份 | 正式名、类型、模块路径/版本/依赖摘要、参数元数据摘要；原生程序 canonical 路径/实现/摘要 |
| 绑定 | 模板/前置源码摘要、有效环境指纹、canonical cwd |

- 版本取本地进程的 `$PSVersionTable`，不信命令名、PE 文件版本或调用方文本。
- `profile.go` 集中选择能力，`params.go` 用实际模块静态元数据绑定参数；cmdlet 文件不散落 `if version >= ...`，某版本缺失或含义未知的参数产生 Unknown。
- 预审和复审都重新取得并比对 profile。精确批准绑定完整调用与相关依赖；路径规则额外绑定 Shell 身份、模板/初始化和能力摘要，不加入无关 cwd 或完整环境。新 patch、模块版本或模板变化走 `bash_launch_changed`。

### 2.7 源码范围、原生 argv 与路径

源码范围：PowerShell/.NET 偏移按 UTF-16 code unit，Go 的 WordSpan 是 UTF-8 字节。`positions.go` 按原文建转换表，验证源范围、代理对边界和原文片段，不能直接把 helper offset 填进 Start/End。中文、组合字符、非 BMP Emoji、CRLF、空字符串、嵌套范围都要有 fixture；未知节点或无效范围不能静默跳过。

原生 argv：

- `native_argv.go` 只验证原生程序，不参与 cmdlet 参数绑定。记录实际 `$PSNativeCommandArgumentPassing`；Windows 模式对某些程序/脚本仍用 legacy 行为，不能把模式名等同于所有 exe 都是 Standard。
- 初版只允许回显程序证实过的常量形态；空参数、嵌入引号、引号前反斜杠、数组展开、splatting、变量/子表达式、停止解析符保留 Unknown。
- Windows 程序收到整行命令行，由各自运行时拆 argv：MSVC/UCRT 与 mingw（curl.exe、Git for Windows）、Rust（rg.exe）、Go、.NET 的引号/反斜杠规则不完全相同。回显程序按运行时各备一个，每个登记的外部程序记录所属类别；首版只放行所有类别结果一致的形态。
- 不把 cmdlet 参数简写交给 POSIX options 算法，也不把 exe 的 `-x:y` 当 PowerShell 参数语法重写。
- `.cmd/.bat`、cmd.exe、脚本宿主、`.ps1` 等不继承 exe 合同；直接连接器调用仍走原有冻结 argv 路径。
- 常量解码等环节保留来源链；不能因最终字符串是 `NUL` 就补回字面标记。

路径与重定向：用 Windows 路径语义和已证明的 FileSystem provider，不借 Bash glob 或 `/dev/*` 规则；`-Path` 通配符与 `-LiteralPath` 分开。审批前不为探测未准入的 UNC/provider 目标而连接远端或触发 provider 代码。Shell 重定向是独立来源，不受原生命令是否 opaque 影响；`$null` 必须是 AST 的语法丢弃目标，`'$null'` 是文件路径。7.4 起的字节流能力只说明内容怎样传递，不授予路径写入权。

### 2.8 cmdlet 语义

- C8–C10 阶段：没有分析器的 cmdlet 为 Exec；只有 Application 身份、Windows CLI 实现及 argv 合同都通过的外部程序才用共享分析器。不能笼统宣称 `git status`、`rg`、`curl.exe` 已恢复分析。
- C13 起逐命令接入下表。共用绑定规则：参数名大小写不敏感；前缀缩写按该模块版本的完整参数元数据确认唯一（不能只在已登记子集里判断），歧义、未知或动态参数为 Unknown(option)；位置参数；`-Force:$false` 形式；分清真实公共参数与各命令支持的 ShouldProcess 参数，`-WhatIf`/`-Confirm:$false` 不删除已识别的写入、删除、远端要求；`-Path` 支持通配符，`-LiteralPath` 不支持。
- 参数绑定不能靠实际调用 cmdlet、`Invoke-Expression`、用户 scriptblock、动态参数生成器或未授权 provider 来探测，只用已核验的静态元数据和常量输入。
- 管道对象输入（如 `Get-ChildItem | Remove-Item`）可能给后续 cmdlet 提供路径；无法静态绑定的目标产生 Unknown，并保留已识别的删除/递归事实。
- 每个 cmdlet 都核验真实类型与来源，名称带 `Get-` 不等于免审批。

| 文件 / 正式命令 | 效果与边界 | 批次 |
| --- | --- | --- |
| `get_location.go` / Get-Location | 读取当前可信 cwd，不授予新目录权限 | C13a |
| `test_path.go` / Test-Path | FileSystem 路径元数据读取；provider/动态参数不当普通文件 | C13a |
| `get_content.go` / Get-Content | 读取；`-Path`/`-LiteralPath`、范围与编码按版本元数据；Stream/ADS 未支持时 Unresolved | C13a |
| `get_child_item.go` / Get-ChildItem | 目录与递归读取；过滤条件不替代来源范围检查 | C13a |
| `write_output.go` / Write-Output | 仅常量输入，无文件写入；不执行输入对象或 scriptblock | C13a |
| `select_object.go` / Select-Object | 只处理管道对象；常量 -First/-Last/-Skip/-Index/-Unique 与属性名；scriptblock 与计算属性为 Unknown | C13a |
| `select_string.go` / Select-String | -Path/-LiteralPath 为读取，模式与开关为常量；只处理管道输入时无文件效果 | C13a |
| `measure_object.go` / Measure-Object | 统计管道输入；常量 -Property/-Line/-Word/-Character/-Sum 等 | C13a |
| `sort_object.go` / Sort-Object | 常量 -Property/-Descending/-Unique；-Top/-Bottom 按实际元数据确认；scriptblock 为 Unknown | C13a |
| `set_location.go` / Set-Location | 只跟踪经身份确认的 FileSystem 常量目标；失败分支不丢 | C13b |
| `new_item.go` / New-Item | Create；符号/硬链接为 Link，位置与链接目标分别记录；其他 provider 不外推 | C13c |
| `set_content.go` / Set-Content | Output/覆盖；目标、编码、值来源分别保留 | C13c |
| `add_content.go` / Add-Content | Output/追加；目标、来源、未知参数不丢 | C13c |
| `out_file.go` / Out-File | Output；Append/NoClobber/Force 与编码差异不放宽目录权限 | C13c |
| `tee_object.go` / Tee-Object | 文件目标为 Output；变量目标是会话状态；动态目标保守处理 | C13c |
| `copy_item.go` / Copy-Item | 源 Read、目标写入/Create/Replaced 及递归范围；替换目标不降为可豁免 Output；ToSession/FromSession 记远端/未知 | C13c |
| `move_item.go` / Move-Item | 源与目标 Rename、被替换对象及删除范围分别核验 | C13c |
| `remove_item.go` / Remove-Item | Delete；递归为 destructive；Force/WhatIf 不覆盖硬阻断 | C13c |
| `invoke_web_request.go` / Invoke-WebRequest | 方法、Body、InFile、OutFile 分别为远端修改、读取、输出；5.1 内容解析等版本行为单独核实，证明不了时保留 UnknownBehavior | C13d |
| `invoke_rest_method.go` / Invoke-RestMethod | 按实际参数集描述，不假定与 IWR 每个版本一致 | C13d |

未列入的 cmdlet（Start-Process、Invoke-Command、ForEach-Object 等）不因名称、别名或文件存在获得语义，继续 Exec/Unknown。

### 2.9 Windows 资源

- `$null` 重定向：只有 AST 明确识别的自动变量 `$null` 在重定向目标位置适用，不查文件身份；`'$null'`、变量展开成同名字符串、作为外部程序参数的 `$null` 都不适用。
- NUL：首批只把去引号后的裸 `NUL`（大小写不敏感、无目录无扩展名）当候选；实际打开行为、非修改式身份确认、重定向与参数差异在原生 5.1/7 验收后才启用豁免，只看字符设备类型不够。`NUL.txt`、`C:\x\NUL`、`\\.\NUL`、扩展路径和 `CON`、`PRN`、`AUX`、`COM1` 等首批 Unresolved。
- 路径：盘符绝对 `C:\`、盘符相对 `C:foo`、UNC、`\\?\` 前缀、大小写不敏感、8.3 短名、ADS `file.txt:stream`、junction；无法确定为普通路径时 Unresolved。
- provider 路径：`Env:`、`Alias:`、`Function:`、`Variable:`、`HKLM:`、`HKCU:`、`Cert:`、`WSMan:` 一律 Unresolved（`Alias:`、`Function:` 写入是硬阻断）；`FileSystem::C:\x` 首批也 Unresolved。

## 3. 未知方言合同（已实现，Windows 与真实 Hub 的回退）

| 档位 | 处理 |
| --- | --- |
| ask_for_approval / auto_approve | 每条命令单次人工批准，不可本轮复用 |
| full_access | 按 `bash-complex-filesystem` 配置，默认 allow |

所有档位：管理员显式 block 生效；cwd 读审查与 editing=false 的执行限制优先；严格命令模式拒绝；Mailbox 代批不能放宽为自动批准；不对正文做 Bash 分析。人工批准只表示用户批准了这次完整调用，不代表正文已审查，也不提供脚本内部隔离。

## 4. 真实 Container Hub（C14u2）

现状（C14u1，`9df6e45c`）：`sandbox/service.go` 实际发送 `/bin/sh -lc`。未证明 guest Shell 与登录初始化前，ask/auto 整条单次人工，严格模式拒绝，预审不建会话、不跑任何 guest 探针；执行环境冻结；明确未派发的类型化证据才退还批准，已送出 RPC 后的网络/服务错误不推断为未执行。

要做：在真实 Hub 上证明所用 Shell、初始化、解释器与工具身份；建立受管入口或可信服务端只读观测通道，不能让待审的登录 Shell 自证；完成后再逐项启用现有 Bash/资源分析（guest 资源代码已在隔离的已声明 Bash 合同中验证过）。

门槛：不用伪造的 readlink/stat/Shell 输出授予身份；实际 argv、环境、取消、会话回收与只读边界通过；缺少证据维持未知方言。

启用后仍要处理的 guest 限制：

- guest Git 配置/include/hooks 没有完整 guest 探针，保持 opaque，不拿 Host 同名文件当证明。
- guest 配置探针只接受不可改写、非链接的 `/usr/bin/{stat,sha256sum,readlink,getent}` GNU 兼容入口；只在 Bookworm uid 65534 实测过，其他镜像没有默认承诺。
- Hub 镜像统一保证 `/bin/bash` 后，才能采用与 Host 相同的默认参数（含 `pipefail`）。

## 5. 验收

### 5.1 Windows 矩阵

| 类别 | 必测案例 | 通过标准 |
| --- | --- | --- |
| 安装与前置加载 | Workspace/PATH 假 pwsh、可写模块、启动钩子环境、商店别名、安装更新 | 未证明可信时 helper 零启动；绑定变化时用户命令零执行、旧批准不可用 |
| 版本/能力 | 实际 5.1、部署固定的 7.6 patch；7.4/7.5、Preview、未知 patch/Edition | 用实际解析器；未支持版本走未知方言 |
| 语法与偏移 | `&&/\|\|`、引号/反引号、CRLF、中文/Emoji、管道、数组、scriptblock；`a && b` 分别交 powershell.exe 与 pwsh | AST 与实际版本一致；源码来源逐字节对应 |
| 解析期副作用 | `using module \\host\share\x.psd1`、`using module ./m.psm1`、`using assembly`、`#Requires -Modules`、param/begin 块 | 含 `using` 不启动 helper；其余结构走未知方言；文件访问审计与抓包确认审批前无访问 |
| 身份与模块 | curl/curl.exe、rm 别名、同名函数/模块、PATH 阴影、模块限定调用、带初始化标记的测试模块；`Set-Alias rg Remove-Item; rg ...` | 真实身份决定分析器；审批前不加载未知模块；Set-Alias 硬阻断 |
| 原生传参 | 各版本/模式下的常量、空值、空格、引号、反斜杠、尾随斜杠、Unicode、批处理入口 | argv 回显 exe 记录实际边界，只对已证明形态启用 |
| cmdlet 参数 | 参数集、唯一缩写、重复/缺失值、布尔值、公共参数、跨版本增删参数 | 与实际元数据一致；未知不擦除已知事实 |
| 资源与状态 | Path/LiteralPath、递归、provider、UNC/ADS/junction、中文/空格路径、跨盘、Set-Location 后相对调用、对象管道 | 不沿用错误 cwd 或来源；硬阻断三档保留 |
| 输出与空设备 | 文本及原生二进制重定向、`$null`/`'$null'`、NUL 各形式 | 内容能力与路径权限分别验收；无 NUL 证据仍 Unresolved |
| 进程与故障 | 冷/热启动、含排队预算、并发 4、超限、取消、EDR/AMSI/LanguageMode 受限；stdin 两行协议的中文/引号/换行逐字节往返 | 进程树与槽位清理；故障走未知方言；诊断不泄露命令正文或环境值 |
| 审批与续接 | ask/auto/full、管理员 block、readonly、精确/规则/多叶子、并发/重试/Mailbox；直接连接器调用 | 与现有公共合同一致；一次批准只对应冻结调用 |
| 原有测试 | 在 Windows 上跑 `internal/hostshell`、`tools`、`accesspolicy`、`commandtext`、`shellreview/...`；严格模式拒绝普通 Shell 调用 | 全部通过 |

原生证明按版本记录（Shell/引擎/模块版本与摘要、OS/架构、传参模式、命令/参数范围、结果、未覆盖项），不保存真实 token。破坏性样例只解析，或在隔离临时目录用无害替身运行。

对外交付分三档说明：C8–C10 只有通过矩阵的外部程序子集获得分析；C13 按单个 cmdlet、模块与 ShellProfile 逐个启用；禁止用“helper 能启动”“major=7”“文件已生成”代替语义与执行一致性验收。

### 5.2 其他目标环境

- 真实系统包升级：apt 原地替换、Homebrew 切换链接后不重启仍按 Bash 分析，旧批准失效（目前只有模拟测试）。
- 真实容器端到端：身份、路径、摘要、空设备、探针失败（随 C14u2）。
- Linux CI 跑 387 行快照与两个增量，macOS 结果不替代 Linux 基线。

## 6. 收尾

C13 与 C14 完成后更新：AGENTS.md（Shell 审查段）、[工具目录权限](../docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](../docs/AccessPolicy与HITL改造.md)、[Runtime 模块边界](../docs/Runtime模块边界.md)、[Windows Git Bash](../docs/WindowsGitBash实施进度.md)（注明 Host 工具仍为 PowerShell）。发布记录分别列出：已解析但仍需审批、具备精确语义可按政策通行、未支持而回退的环境。之后删除本文。

## 附录：Windows 回归样例

| 样例 | 期望 |
| --- | --- |
| `echo "\" ; Remove-Item -Recurse C:\x ; echo "\"`（Windows Host） | 现在单次人工；C8 后解析为三条语句，`Remove-Item` 为 Exec |
| `Set-Alias rg Remove-Item; rg -r . C:\x` | 硬阻断 |
| `git status`（git 为 Application） | 现在单次人工；对应 ShellProfile、Windows git 实现与 argv 合同通过 C10 后使用共享语义 |
| `rm -Recurse C:\x`（`rm` 为 Remove-Item 别名） | C8–C10 为 Exec；C13c 后为 destructive |
| `echo ok > $null` 与 `echo ok > '$null'` | 前者 AST 确认语法丢弃；后者是文件路径 |
| 裸 `NUL`、`NUL.txt`、`C:\x\NUL`、`\\.\NUL` | 裸名仅候选，未验收前 Unresolved；其余首批 Unresolved |
