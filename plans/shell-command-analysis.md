# Shell 命令审批：分层流水线与目录方案

> 状态：部分实施。启动合同与方言保护（实施计划 S0–S2）已在 `5554545f` 实现，复审补丁见 `ef0b41f4` 与实施计划 §8.6，选项与隐式配置补丁见实施计划 §8.8；S3 已迁入 Bash 解析/语法审查、命令语义与共享包装辅助函数，并保留字面量来源（实施计划 §8.10）。身份/资源/plan/policy 拆分、命令事实细化、PowerShell AST 和两套审批合并尚未完成。
> 核对日期：2026-10-31（原 §8.2 第 1、2、3、4、6 项已确认并移入 §8.1；第 3、4 项仅设计已决定，分别待 S6、S5 实现与目标环境验收）。进度、下一步与遗留项见[实施计划](shell-command-analysis-implementation.md)第 0 节；其评审意见 R1–R8 已并入本文。Windows 结论未在 Windows 上端到端运行。

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

### 2.2 一个函数做了所有层的事

`reviewBashScript`（`internal/accesspolicy/bash_review.go`）约 270 行，cwd 解析、容器目录、方言早返回、程序身份、连接器、opaque、技能/自写脚本豁免、路径、glob、子树、Git、curl 特例、重定向、cd 跟踪和连接器汇总都在一个循环里。新增一个特殊情况只能再加一个分支。

### 2.3 "认识哪些命令"分散在约 9 处

| 位置 | 名单 |
| --- | --- |
| `accesspolicy/bash_execution.go` | `ordinaryCommands`、`shellBuiltins`、`wrapperNames`、`captureSystemExecutables` 的额外名单、`isInterpreter` |
| `accesspolicy/bash.go` | `isOpaqueCommand` |
| `shellanalysis/specs.go`、`operands.go` | `specs` 与 `Operands` 的 switch（各有一份名字列表） |
| `bashsec/bash_patterns.go` | `astDangerousCommands`、`zshDangerousCommands` |
| `tools/tool_bash.go` | `unsupportedBashCommands`（严格模式） |
| `config/config_defaults.go` | `defaultBashAllowedCommands` |

已经出现不一致：

- `df`、`dbx`、`httpx` 在 `ordinaryCommands` 中但没有分析器，落到 `unknownOperands`，实际总是 opaque。
- `Get-ChildItem`、`Get-Content`、`Remove-Item` 是 cmdlet，`LookPath` 找不到，这几个条目永远不会生效。

### 2.4 两套审查、两套批准

- `bashsec` 返回 `ReviewResult`，批准存在 `BashSecurityApprovals`；`accesspolicy` 返回 `BashPlan`，批准存在 `AccessPolicyApprovals`。`internal/llm` 为两者分别发审批请求、分别消费。
- 包装命令拆了两份：`bashsec/bash_wrappers.go` 与 `accesspolicy.unwrapExecution`，规则不一致（例如 `command -p`）。
- `bash -c` 递归也是两份，各自 `maxDepth = 3`。

### 2.5 命令特例写在策略层，事实类型太粗

- curl/wget 输出到 `/dev/null` 的豁免和 `git -C` 的仓库目录判断写在 `bash_review.go` 里，其他命令无法复用。
- `Effects` 的文件效果只有 Read/Write，输出内容、删除、改权限无法区分，所以 `/dev/null` 只能按命令名特判。
- `Incomplete` 与 `ExecutesCode` 混用："尚不理解的参数"和"确定会执行额外代码"走同一个回退。

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

**5 资源**：解析相对路径、symlink/junction、容器 guest 路径、glob；识别空设备；核验 Git 配置与 hooks；按本次调用的有效环境找到命令默认配置文件的实际路径，确认是否存在、Agent 能否免审批写入（8.1）；对脚本、入口和配置文件计算摘要。只有这一层和第 2 层访问文件系统或容器探针。每个结果三选一（R6）：

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
- 仅抵消完整识别调用的 `Read`、`Output` 对应路径要求。`Incomplete`/UnknownOption 影响的当前调用不豁免，不将不完整状态扩大到独立兄弟调用。`Create/Delete/Rename/Chmod/Link` 与可能替换目标的 cp、mv、install、ln 目标操作不豁免；不能仅因目标是 `/dev/null` 就把目标操作降为 Output。
- readonly、平台保护、临时根逃逸和管理员显式 block 先于抵消；执行代码、删除、上传、远端修改及其他目标的要求各自保留。Windows 的 `$null` 语法丢弃与 `NUL` 设备候选分开处理，见 6.5。

**6 策略**：见 3.3、3.4。

**7 合并**：沿用现有 BashPlan 的叶子要求、规则批准与精确批准语义。每个叶子关联调用、事实来源（argv 下标、规则）与启动绑定：

- 精确批准绑定完整调用：命令正文加 LaunchSpec 指纹。
- 本轮规则批准追加 Shell 身份（canonical 路径及摘要）、模板与方言指纹；不额外加入 cwd、完整环境或命令正文，原规则自身的目标/调用限制不变（S2 复审补丁，见 §8.1）。

S7 合并 `bashsec` 的批准时，保持 HITL 公开形状与规则语义，协议调整另行讨论。

### 3.3 策略层判断顺序

1. **硬阻断**（任何档位、任何批准都不能放宽）：Shell 级硬阻断、资源层 HardViolation、递归目标包含受保护子树、容器 glob、editing=false 时在 Workspace 内写入或执行 Exec、管理员显式 block。
2. **按合同抵消**：资源层确认的空设备只抵消完整识别调用中该 Read/Output 的路径要求；PowerShell AST 确认的 `$null` 重定向直接表示语法丢弃输出，不当作文件设备探测（6.5）。当前调用分析不完整时不享受空设备路径豁免。连接器入口、本次选中技能脚本、本 Run 自写脚本的凭据只抵消对应调用的 Exec。各类抵消都不能删除兄弟要求（R8）。
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
| Read / Output | 当前调用分析完整，字面候选经第 5 层确认为空设备，且未命中硬阻断 | 仅抵消该项路径要求 |
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

```go
type Command struct {
    Dialect dialect.Dialect          // 按方言登记：Bash 的 rm 与 PowerShell 的 Remove-Item 是不同条目
    Names   []string                 // 名称与别名，如 awk/gawk/mawk/nawk
    Kind    Kind                     // Analyzed / ShellBuiltin / Wrapper / Interpreter / BuildTool
    Sources []Source                 // 允许的身份来源：System / PlatformBuiltin / Cmdlet，可多个
    Options *OptionSpec              // 声明表：带值/不带值/可选值，值是读文件、写文件、目录、URL 还是普通值
    Analyze func(p Parsed, e *types.Effects) // 声明表表达不了的条件；可为空
}
```

- 按 Dialect + Name 查找；同名程序在不同方言下互不借用语义（R7）。
- `Sources` 允许一个名字同时有系统安装与平台内置两种来源，身份层按实际来源核对。
- 简单命令只写 `Options`，由共享解析器得出文件操作数；有子命令、内嵌语言或特殊条件的命令写 `Analyze`。
- `Kind` 取代 `ordinaryCommands`、`shellBuiltins`、`wrapperNames`、`isInterpreter`、`isOpaqueCommand`。启动时记录系统安装路径的名单也从 registry 生成。
- cmdlet 只按正式名登记（`Remove-Item`），别名到 cmdlet 的解析归身份层（`identity/powershell.go`）。
- 中立类型（Invocation、Effects、Op、Unknown、Dep、证据）放在只依赖标准库的 `shellreview/types`。`commands` 根包只聚合描述符，各命令分析器与 `commands/powershell` 只依赖 `types` 与 `options`，避免父子包循环（R7）。

### 4.2 命令文件示例

"针对命令集的特殊拦截和过滤条件"都写在这里，以事实的形式出现：

```go
// commands/curl.go
var curl = Command{
    Dialect: dialect.Bash,
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
}

type Target struct {
    Path string // 原始操作数，由资源层解析
    Role Role   // Source / Destination / Replaced / LinkTarget
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

复制、移动、链接必须同时检查源、目标、被替换对象与递归范围（R7）。`Arg` 与 `Rule` 用于 HITL 展示原因，以及说明哪些输入变化会使批准失效。字段名称在实现阶段确定，含义以本节为准。

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

## 5. 目录结构

### 5.1 目标结构

```text
internal/hostshell/tool.go           # 第0层（已实现）：工具专用 LaunchSpec 与方言判定，与 Terminal 分开
internal/shellreview/                # Shell 审批唯一入口
  review.go                          # Review(Input) BashPlan：按层调用，不出现任何命令名
  input.go                           # 命令、cwd、环境、LaunchSpec、session、执行环境探针
  dialect/                           # 方言枚举（已建；叶子包，hostshell 也依赖它）
  types/                             # 已建 LaunchBinding/Invocation/Effects 与来源类型；Op/Unknown/Dep 留 S4
  syntax/
    bash/                            # 第1层 Bash：已迁入原 bashast + bashsec；两套审批对象尚未合并
    powershell/                      # 第1层 PowerShell（S5）
      helper.ps1                     # 固定 helper（go:embed）：解析与受限身份查询，不执行用户命令或自动加载待查模块
      helper.go                      # helper 进程：启动、请求、超时、输出上限
      ast.go                         # AST 节点白名单 → 调用结构；硬阻断名单
    text.go                          # 文本检查：所有位置的控制/双向/编码检查；隐藏字符检查由调用方按引号规则选择范围
  identity/                          # 第2层
    identity.go                      # Identity 类型与 Resolve 入口
    unix.go                          # PATH 解析、系统安装记录
    windows.go                       # PATHEXT 解析
    powershell.go                    # 别名 → 函数 → cmdlet → 外部程序；可信模块判定
    container.go                     # 容器内解析探针
    scripts.go                       # 解释器脚本识别、shebang
    connector.go                     # 连接器入口核验与直接调用
  unwrap/                            # 第3层：包装命令与 bash -c，只此一份；由 review.go 与身份交替调度
  commands/                          # 第4层：已等价迁入原 shellanalysis 语义；registry、文件细拆与 Op 留 S4
    registry.go                      # 描述符聚合、Kind、按 Dialect + Name 查找
    options.go                       # 共享选项解析
    simple.go                        # echo printf pwd true false date sleep uname whoami id basename dirname which printenv test tr cd top free
    files.go                         # cat head tail wc file stat readlink realpath cut sort uniq du ls diff grep rg
    fileops.go                       # mkdir touch tee chmod rmdir rm cp mv ln
    curl.go  wget.go  lsof.go  git.go  find.go
    sed.go   awk.go   jq.go    tar.go  pdftotext.go
    *_test.go                        # 每个命令文件各自的语义测试
    powershell/                      # cmdlet 分析器（S8）
      params.go                      # 参数绑定：大小写、前缀缩写、位置参数、公共参数
      items.go                       # Get-ChildItem Get-Content Copy-Item Move-Item Remove-Item New-Item Set-Content Out-File …
      web.go                         # Invoke-WebRequest Invoke-RestMethod
  resources/                         # 第5层
    result.go                        # Known / Unresolved / HardViolation
    paths.go                         # 相对路径、glob、容器目录
    windows.go                       # 盘符、UNC、ADS、PowerShell provider 路径
    devices.go                       # 空设备身份：Unix /dev/null、Windows NUL；$null 为语法层丢弃输出
    gitstate.go                      # Git 配置与 hooks 核验
    fingerprint.go                   # 执行环境与内容版本指纹
  policy/                            # 第6层
    hardlimits.go                    # 3.3 第 1 步
    offsets.go                       # 3.3 第 2 步：空设备与凭据抵消
    mapping.go                       # 3.4 映射表
  plan/                              # 第7层
    plan.go                          # BashPlan 与构造
    launch.go                        # 启动绑定（现 accesspolicy/bash_launch.go）
    combine.go                       # 合并、待批准叶子、审批规则
    approval.go                      # 批准登记、查询、消费、来源与元数据
```

### 5.2 现有代码去向

| 现有位置 | 去向 |
| --- | --- |
| `tools/tool_bash.go` 的 `resolveHostShellInvocation` 等 | 已移到 `hostshell.ToolInvocation` / `ResolveTool`（`5554545f`） |
| `internal/bashast/*` | `syntax/bash/` |
| `internal/bashsec/bash_checks.go`、`bash_patterns.go`、`bash_security.go`、`bash_ast.go` | `syntax/bash/`（Shell 级硬阻断）与 `syntax/text.go`；审批结果并入 `plan/`（S7） |
| `internal/bashsec/bash_wrappers.go`、`accesspolicy.unwrapExecution`、`shellanalysis/command.go` | `unwrap/` |
| `accesspolicy/bash_execution.go` | `identity/`（身份）与 `unwrap/`（包装） |
| `accesspolicy/bash_connector.go`、`connector_invocation.go` | `identity/connector.go` |
| `internal/shellanalysis/*` | `commands/`（`operands.go` 的 switch 拆进各命令文件，`specs.go` 拆成各命令的 `Options`）；`Effects` 移到 `types/` |
| `accesspolicy/git_state.go`、`bash_environment.go`、Bash 用的 glob 展开 | `resources/` |
| `accesspolicy/bash_review.go` | `review.go`、`policy/`、`plan/` |
| `accesspolicy/bash.go`、`bash_launch.go` | `plan/`（BashPlan、启动绑定、批准）与 `resources/`（重定向分类） |
| `tools/tool_bash.go` 的 `unsupportedBashCommands` | `syntax/bash/` |

保留不动：`internal/accesspolicy` 只保留文件工具也要用的通用路径策略（`BuildPathPlan`、`BuildSubtreePlan`、档位、session 路径解析、保护目录）；`internal/hostshell` 负责启动合同；`internal/shellenv`、`internal/scriptstate`、`internal/skillsexec` 不变；进程启动、凭据注入与进程生命周期仍归 `tools`。

迁移某个旧入口时，同一变更改完所有调用方（`internal/tools`、`internal/llm`）并删除旧入口，不保留转发别名。两套授权对象在 S7 之前按现有合同共存，纯搬家时不改它们的形状。

S3 首批已完成：原 `bashast`、`bashsec` 已整包迁到 `syntax/bash`，原 `shellanalysis` 的命令语义移到 `commands`，`command.go` 的共享辅助函数移到 `unwrap`。原 `SimpleCommand` 改为 `types.Invocation`，`WordSpan/Redirect/EnvVar` 与原 `Effects/FileAccess/GitCheck` 一并移到 `types`；调用方已切换，旧包已删除，公开 ruleKey 和两套批准对象不变。`WordSpan.Literal` 与重定向 `TargetWord` 仅保存来源事实，尚不启用新空设备豁免；`Expansion` 保持原连接器投影含义。身份/资源/plan/policy 与两套包装逻辑尚未拆分或合并，不能将整个 S3 标记完成。

### 5.3 依赖方向

```text
hostshell ─────────────> shellreview/dialect, shellreview/types
tools / llm ───────────> shellreview
shellreview ───────────> syntax / identity / unwrap / commands / resources / policy / plan
上述各层 ──────────────> types / dialect（中立叶子包，只依赖标准库）
commands（聚合）───────> 各命令分析器 / commands/powershell
命令分析器 ────────────> types / options（纯函数）
resources / policy ────> accesspolicy（通用路径策略）
syntax/bash ───────────> mvdan/sh
syntax/powershell ─────> 启动平台自带的 helper 进程，不执行被审查的命令
accesspolicy ──────────> 不依赖 shellreview
```

递归（`bash -c`、身份—包装交替）由 `review.go` 调度，子包不反向调用 review。收据登记与消费使用窄接口，不把整个 ExecutionContext 交给纯分析器。

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

- 用与执行相同的 PowerShell 可执行文件解析。`powershell.exe`（5.1）与 `pwsh`（7）语法不同，例如 `&&`、`||` 只有 7 支持，所以不能用一个版本代替另一个。
- 平台自带一个固定 helper 脚本（`go:embed`），用 `[System.Management.Automation.Language.Parser]::ParseInput` 解析，受限身份查询按 6.3 执行；不执行被审查的命令，不在审批前自动加载待检查模块。命令文本经 stdin 传入，不拼接进 helper 脚本或命令行。
- helper 使用与执行相同的环境（PATH、PATHEXT、PSModulePath）与 cwd，输出 JSON：语句、调用、参数、重定向，以及每个命令名的解析结果（6.3）。Go 端按下方白名单映射为与 Bash 相同的调用结构。
- 生命周期按下方固定合同执行；helper 普通故障进入 6.6，用户取消和启动绑定变化单独终止，不进入回退审批。
- 解析对象是用户命令本身。平台前置的 UTF-8 输出设置和 `$env:PATH=` 环境绑定是固定模板，由单独测试覆盖其转义。
- 预审与启动前复审都重新解析，不复用缓存的 AST。

**helper 生命周期（原待确认第 4 项，设计已决定，S5 实现）**：

1. 仅用于 Windows Host 的受支持 PowerShell 启动模板；容器 Bash 不使用它。一次审查启动一个 helper 进程，完成解析及获准的身份查询后退出；预审、启动前复审分别运行，不常驻、不缓存 AST 或身份。同一审查不为身份查询另起第二个 PowerShell。
2. 使用 LaunchSpec 对应的 canonical 可执行文件，并核验摘要，继承与执行一致的有效环境、cwd；不通过 basename 或另一套 PATH 重新选择程序。自定义登录/profile、附加源码等未验证启动模板继续走 6.6。
3. 参数固定为 `-NoProfile -NonInteractive -NoLogo -ExecutionPolicy Bypass -EncodedCommand <bootstrap>`。bootstrap 为固定短脚本，编码为 UTF-16LE 后 Base64；stdin 第一行传内嵌 helper 的 UTF-8 Base64，第二行传用户命令原始 UTF-8 字节的 Base64，写入后关闭 stdin。只有受平台控制的 helper 被构造并执行为 scriptblock，用户命令只能进入 Parser，不调用或求值。响应使用 `[Console]` 直接输出一行 UTF-8 JSON 的 Base64，避免管道格式化与 5.1 控制台编码差异。
4. 固定启动参数用于无临时脚本文件的传输，不承诺绕过组策略、AMSI、EDR 或语言模式限制。受阻、超时或启动失败时按第 8 条处理；不自动改用另一套启动方式。
5. 总预算 10 秒，从进入队列开始计时；全局最多同时运行 4 个 helper，等待可取消。stdout 编码后上限 1 MiB（包含行结束符），stderr 上限 64 KiB，命中上限即终止进程树；解码 JSON 上限 768 KiB。命令 UTF-8 原文上限 1 MiB，进入队列前检查；helper 源码不占命令限额。AST 最多输出 256 个调用，遍历深度最多 64；超出按不支持处理，不能静默截断。
6. 用现有 Windows `processgroup.Run` 在挂起状态加入 Job 后恢复运行，隐藏窗口。退出、超时、超限、取消及协议失败均清理整棵进程树，释放并发槽；父请求取消直接返回取消，不申请未知方言批准。
7. 响应协议固定 `v:1`，含真实 `psVersion`、`psEdition`、解析错误、语句结构及身份结果。Go 严格解码 Base64、UTF-8 与 JSON，只接收一个完整响应，拒绝未知字段、重复字段、尾随内容及无效源范围。LaunchBinding 当前尚无实际 PowerShell 版本字段，S5 需补充版本与执行身份绑定；只接受原生 Windows 验证过的 Desktop 5.1 / Core 7 版本与参数形态，不能只按程序名认为版本匹配，也不能将所有 7.x 视为同一传参合同。
8. helper 不可用、启动失败、超时、非零退出、输出超限、协议不合法、版本不支持、用户命令有解析错误：整条命令走 6.6，提供稳定的失败类别与简短原因，不退回 Bash 分析。实际 Shell/模板/摘要/环境/cwd 绑定变化遵循原有 `bash_launch_changed` 合同，旧批准失效，不能通过回退继续消费旧批准；已发现的文本或资源硬阻断始终保留。
9. 记录排队耗时、进程耗时、实际版本、失败类别，不记录命令正文、环境值或原始诊断载荷。分别测 5.1/7 冷启动、热启动及完整工具调用的 helper 总开销；p95 超过 1 秒时触发常驻设计评估，不自动切换。常驻需另行解决环境指纹隔离、新 runspace、请求关联和清理，不能在当前实现中预设安全等价。

此生命周期合同不关闭第 5 项模块与身份规则；该项未经原生 Windows 验证前，不恢复相关命令的免审批。编码与启动参数依据 [PowerShell CLI](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_powershell_exe?view=powershell-5.1)，设备数字输出依据 [GNU stat](https://www.gnu.org/software/coreutils/manual/html_node/stat-invocation.html)；文档与交叉编译不能替代目标环境验收。

**允许的节点**（白名单，其他节点一律 Unknown(syntax)）：

| 节点 | 映射 |
| --- | --- |
| 语句列表、`;`、换行；PowerShell 7 的 `&&`、`||` | 多个调用，所有分支都审查 |
| 管道中的 CommandAst | 一个调用；命令名必须是常量 |
| 常量参数：裸词、单引号字符串、不含变量和子表达式的双引号字符串、数字 | 参数 |
| CommandParameterAst（`-Recurse`、`-Path:value`） | 参数，保留原文交给命令语义层匹配 |
| `&` 调用常量命令名 | 同 CommandAst |
| 文件重定向，目标为常量或 `$null` | 常量目标为 Output；AST 确认的 `$null` 为语法丢弃输出（6.5） |
| 流合并重定向（`2>&1`） | 中性 |
| `cd` / `Set-Location` / `sl` 加常量路径 | 与 Bash 的 cd 相同，记录可能的 cwd |

**Unknown(syntax)**，即整条命令 complex，与 Bash 的非简单命令相同：变量与 `$env:` 引用（`$null` 重定向除外）、子表达式 `$()`、可展开字符串中的变量或子表达式、脚本块 `{}`、splatting `@x`、.NET 成员调用与类型字面量（`[IO.File]::WriteAllText`）、赋值语句、函数与类定义、dot-source `.`、`--%` 停止解析符、`-EncodedCommand`。

**硬阻断**：改变后续命令解析的命令。它们会让同一条命令里后面的调用身份判断出错，与 Bash 的 `alias`、`source` 同类：`Set-Alias`、`New-Alias`（及别名 `sal`、`nal`）、`Import-Module`（`ipmo`）、对 `Alias:`、`Function:` 驱动器的写入。

**外部程序参数**：AST 只给出 PowerShell 层面的参数，不能证明 exe 实际收到相同的 argv。PowerShell 5.1 与 7.3+ 向 exe 传参的引号规则不同（受 `$PSNativeCommandArgumentPassing` 影响）：

- 参数含 `"`、为空字符串或含需要转义的字符时为 Unknown(syntax)。
- 首批只分析经 5.1 与 7 实测、确认 argv 一致的外部程序参数形态（6.7）。
- `.cmd`、`.bat` 与 cmd.exe 脚本入口不继承普通 exe 的分析假设。

### 6.3 PowerShell 身份

要求（R4）：

- 身份查询不能执行模块代码。按精确名称调用 `Get-Command` 会自动导入模块，而自动导入会运行模块脚本（[Get-Command 文档](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/get-command?view=powershell-7.5)），所以 helper 不能直接用它做身份查询。
- helper 与执行进程必须采用同一套模块加载规则：要么两端一致，要么在执行进程中核对冻结的解析结果。不能只在 helper 里禁用加载，执行端却继续默认加载。
- 身份记录至少包含：Shell 路径与版本、环境与 cwd 指纹、模块的真实路径（不只是模块名）、正式命令名、外部程序 canonical 路径。
- 无法确认的项一律 Unknown，即 Exec，不按名称授予 Cmdlet 或 System 身份。

解析顺序与 PowerShell 一致：别名 → 函数 → cmdlet → 外部程序。

- 别名或名称指向可信内置模块（`Microsoft.PowerShell.Management`、`Microsoft.PowerShell.Utility`、`Microsoft.PowerShell.Core` 等，按真实安装路径核对）的 cmdlet，身份为 Cmdlet，以正式名查 registry（`rm` → `Remove-Item`，PS 5.1 的 `curl` → `Invoke-WebRequest`）。
- Application（按 PATH/PATHEXT 找到的 exe）走与 Unix 相同的 System / PlatformBuiltin 判定。
- 函数、`.ps1` 外部脚本、非内置模块命令为 Shadowed，即 Exec。

候选做法（待确认 5，需在 Windows 上核实）：

- 别名表：用同一可执行文件、在 `-NoProfile` 的干净会话里读取默认别名表。
- 外部程序：在 Go 中按 PATHEXT 查找。
- 与模块导出命令同名：只读取模块清单判断，不导入模块。
- 自动加载与外部程序的优先顺序：在 Windows 上实测确认。

### 6.4 PowerShell 命令语义（cmdlet）

- S5：所有 cmdlet 为 Exec；外部程序按现有 registry 分析。这时 Windows 上 `git status`、`rg`、`curl.exe` 恢复与 Unix 相同的分析，cmdlet 按 opaque 审批。
- S8：在 `commands/powershell/` 为常用 cmdlet 增加分析器。共享参数绑定规则：
  - 参数名大小写不敏感；可用唯一前缀缩写（`-Rec` 即 `-Recurse`），有歧义为 Unknown(option)。
  - 位置参数；`-Force:$false` 形式。
  - 公共参数（`-WhatIf`、`-ErrorAction`、`-OutVariable` 等）。
  - `-Path` 支持通配符，`-LiteralPath` 不支持。
- 首批顺序：
  1. 只读：Get-Location、Test-Path、Get-Content、Get-ChildItem、Write-Output。
  2. 写入：New-Item、Set-Content、Add-Content、Out-File、Copy-Item、Move-Item、Remove-Item（`-Recurse` 为 destructive）。
  3. 网络：Invoke-WebRequest、Invoke-RestMethod。非 GET/HEAD 方法、`-Body`、`-InFile` 为 RemoteMutation；`-OutFile` 为 Output。
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
- `$null`、`'$null'`、裸 `NUL`、带目录或扩展名的保留设备写法分别验收；Unix 符号链接与 cp 目标用例见附录 B，不把 Windows 的待验收结论写成已交付。
- PowerShell 5.1 与 7 各用无害的 argv 回显程序，核对引号、空参数、空格、反斜杠、尾随斜杠和 Unicode。
- 直接连接器调用在三档下行为不变。
- 原生 Windows 运行：中文/空格路径、跨盘、UNC、junction、`NUL` 输出；交叉编译与 macOS 模拟不算验收。
- 破坏性样例只解析，或在隔离临时目录用无害替身运行。

## 7. 实施步骤

步骤、依赖与验收门槛以[实施计划 §4](shell-command-analysis-implementation.md#4-可执行工作计划) 的 S0–S8 为准；当前进度与下一步见实施计划第 0 节。本文不再另列阶段。顺序要点：

- 方言保护先行（已完成）。PowerShell AST（S5）不等两套审批合并（S7）。
- 先细化事实（S4），再做通用空设备规则并删除 curl/wget 特例（S6）（R5）。
- 纯结构迁移与行为变化分开提交。行为快照使用规范化表示：路径、环境、叶子顺序规范化后比较；原始指纹只在固定 fixture 中逐值验证（R8）。

S5 依赖 S3、S4，在这之前 Windows 两档每条命令都要人工审批。如果这段时间太长，可以先在现有结构上接入 AST，只放行外部程序，再随 S3/S4 迁移。

实施完成后同步更新：`AGENTS.md`（命令语义包名）、[工具目录权限](../docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](../docs/AccessPolicy与HITL改造.md)、[Runtime 模块边界](../docs/Runtime模块边界.md)、[Windows Git Bash](../docs/WindowsGitBash实施进度.md)（注明 Host Bash 仍为 PowerShell）。之后删除本文与实施计划。

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
- 空设备身份（原 8.2 第 3 项，2026-10-31）：设计已决定，S6 实现与验收。Unix 仅认字面 `/dev/null` 并确认字符设备及主次设备号；容器必须探测，失败为 Unresolved，不因性能退回字符串信任。仅抵消完整调用的 Read/Output 路径要求，硬阻断优先。Windows `$null` 为 AST 语法丢弃，裸 `NUL` 仅为候选，身份确认与实际行为待原生验收；其余保留设备形式首批 Unresolved。完整合同见 3.2、3.3、6.5。
- PowerShell helper 生命周期（原 8.2 第 4 项，2026-10-31）：设计已决定，S5 实现与验收。一次审查一个进程，解析与受限身份查询合并，使用冻结启动身份、环境与 cwd；固定 bootstrap/stdin Base64 协议、10 秒含排队预算、并发 4、输出和 AST 上限按 6.2。普通故障走 6.6，取消直接结束、绑定变化使旧批准失效；模块规则第 5 项仍待确认，不以生命周期决定恢复免审批。

### 8.2 待确认

S0 需要关闭以下事项；每项最晚在哪一步之前确认，见实施计划 §0.2。确认后移到 8.1，编号保持不变。

| # | 事项 | 建议 |
| --- | --- | --- |
| 5 | PowerShell 模块与身份规则 | helper 与执行端一致且可验证；候选做法见 6.3，需在 Windows 核实 |
| 7 | 首批 cmdlet | 按 6.4 的顺序，先只读，再文件修改，最后网络 |

## 附录 A：已有局部修复

### A.1 lsof 与 curl/wget 空设备（`9731155e`）

**lsof**：新增 `internal/shellanalysis/lsof.go`，接入分派、系统命令识别和 Unix/macOS 默认白名单。识别 `-a/-b/-h/-l/-n/-P/-R/-t/-v/-w` 标志与 `-c/-p/-u/-i/-s/-F` 参数（附着或分离形式）；普通操作数为读取，`--` 后按文件名处理；`-D`、`+d/+D`、未知选项和缺少必要参数回退到 opaque。只消费参数，不校验 PID、用户名、协议状态等值域。

**curl/wget 输出到 `/dev/null`**：`bash_review.go` 中的局部规则。命令为 curl/wget、分析完整、写入目标字符串精确为 `/dev/null`，且处于非 Windows Host 或容器时，`BuildPathPlan` 无错误且未阻断则跳过该输出的路径审批。普通文件、上传、远端修改和 readonly 继续检查。S6 由资源层空设备规则取代。

**验证**：`go test ./internal/shellanalysis ./internal/accesspolicy ./internal/config ./internal/tools ./internal/bashsec` 通过（部分命中缓存）。容器用例使用模拟解析环境；Host 用例在缺少程序时跳过；Windows Host 跳过这组 Unix 诊断用例。

### A.2 启动合同与方言保护（`5554545f`）

范围、可见行为变化与验证见实施计划 §8。

## 附录 B：触发场景与回归样例

| 样例 | 原问题 | 期望 |
| --- | --- | --- |
| `lsof -nP -iTCP:8080 -iTCP:8081 -sTCP:LISTEN` | lsof 未登记，按 opaque 审批 | 只读查询，无需审批（已修复） |
| `curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8081/healthz` | `-o /dev/null` 被当成越界写入 | 无需审批（已修复，S6 改为通用规则） |
| `curl -X POST ...`、`curl -T file ...` | — | `remote-mutation`，空设备输出不豁免 |
| `tee /dev/null` | tee 的输出目标仍按写入审查 | S6 后去掉这一项路径要求，tee 的其他要求照常 |
| `cmd > /dev/null` | — | 重定向本身现已不产生要求；`cmd` 自己的要求（Exec、上传等）照常，不能笼统写成无需审批 |
| `rm /dev/null`、`chmod 600 /dev/null` | — | 不豁免 |
| `cat /dev/null`、`cat < /dev/null` | 直接读取参数与重定向规则不统一 | S6 后仅在字面候选、身份确认且当前调用完整时移除该读取路径要求 |
| `echo ok > '/dev/null'`、`echo ok > "/dev/null"` | — | 去除语法引号仍为字面候选；S6 校验设备后移除该输出路径要求 |
| `echo ok > //dev/null`、`echo ok > /dev/./null`、`target=/dev/null; echo ok > "$target"` | 旧逻辑规整路径或丢失展开来源 | S6 不享受空设备豁免，继续路径审查 |
| Workspace 的 `null-link` 指向 `/dev/null`，随后 `tee null-link` | — | 符号链接不享受空设备豁免；canonical 路径审查照常 |
| `tee /dev/zero`，或 `/dev/null` 被换为普通文件/链接/错误设备号 | — | 已确认非空设备不豁免；相关路径与其他要求照常 |
| `cp x /dev/null`、`mv x /dev/null`、`install x /dev/null`、`ln x /dev/null` | Write 不足以区分输出与替换目标 | 目标不得按 Output 豁免，源、目标、替换/删除/链接事实分别审查 |
| 容器空设备探针超时、退出失败或输出异常 | — | Unresolved，保留可见事实并追加 complex；已有 readonly 等硬阻断不变 |
| 当前调用带未知选项且目标为 `/dev/null` | — | 当前调用不豁免，保留 complex 和可见路径；不影响独立兄弟调用的完整性 |
| `curl -F file=@/dev/null -o /dev/null URL` | — | 已确认空设备的读/输出路径可按合同抵消，上传/远端修改要求保留 |
| PowerShell `echo ok > $null` 与 `echo ok > '$null'` | 语法丢弃与文件路径混淆 | 前者由 AST 确认语法丢弃，后者是文件路径，其他命令要求继续审查 |
| Windows 裸 `NUL`、`NUL.txt`、`C:\x\NUL`、`\\.\NUL` | — | 裸名仅为候选，身份未验收前 Unresolved；其余写法首批 Unresolved |
| 同名 Workspace 程序 `./lsof`，假 `env` / `bash` 包装 | — | 不取得 System 身份，不按包装展开 |
| `cp a b`（`b` 已存在） | 只看最后一个操作数 | 源读、目标写、被替换对象都检查 |
| 2.1 的 PowerShell 样例（Windows Host） | 被当成单个 echo 放行 | 当前单次人工（已实现）；S5 后解析出 `Remove-Item` 为 Exec |
| `Set-Alias rg Remove-Item; rg -r . C:\x`（PowerShell） | — | 硬阻断 |
| `git status`（PowerShell，git 为 Application） | 当前单次人工 | S5 后与 Unix 相同 |
| `rm -Recurse C:\x`（PowerShell，`rm` 为 Remove-Item 别名） | — | S5 为 Exec；S8 为 destructive |
| Unix 配置 `shell-executable: sh` 或登录模板 | — | 未知方言合同（已实现） |
| 含 ESC 序列或双向覆盖字符的命令（任何方言、引号内也算） | `5554545f` 对未知方言跳过全部文本检查 | 硬拒绝（已修复） |
| 提交信息里的组合 Emoji（`👨` U+200D `💻`）、连接器 JSON 里的全角空格 | `ef0b41f4` 曾在引号内也硬拒绝 | Bash 引号内原样保留；未知方言仍拒绝（已修复） |
| `echo` U+3000 `ok`（引号外全角空格）、`git` U+200B ` status`（引号外零宽空格） | — | Bash 与未知方言都硬拒绝 |
| 启动后在更靠前的 PATH 目录写入同名 `bash` | `ef0b41f4` 会当作系统 Bash 分析 | 未知方言合同（已修复） |
