# Shell 命令审批：分层流水线与目录方案

> 状态：待实施方案。除附录 A 记录的局部修复外，本文内容均未实现。
> 核对日期：2026-10-30。事实来源为本地源码、配置默认值与包测试；Windows 结论只在 macOS 上做过解析层探针，未在 Windows 上端到端运行。

## 1. 目标

一条 Bash 工具调用是否需要审批，按固定的几层依次判断。每层只回答一个问题、只输出事实，只有策略层把事实变成 allow / auto / hitl / block。

要求：

- 每种特殊命令的知识（参数、拦截条件、过滤条件）只在它自己的文件里。
- "认识哪些命令"只有一份名单。
- 审批看的命令必须是实际执行的命令，Windows 也一样。
- 整个模块在一个目录下，子目录与层一一对应。

不变的边界：命令语义不决定授权；不按 GENERAL/CODER/KBASE/TEAM 分支；不执行用户命令来猜行为；Host 仍是执行前检查，不是逐次文件访问隔离。

## 2. 现状问题

### 2.1 Windows：审批看的和实际跑的不是同一条命令（优先修）

Windows Host 的 Bash 工具默认用 `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command` 执行（`internal/tools/tool_bash.go` 的 `defaultHostShellExecutable`），但 `accesspolicy` 与 `bashsec` 始终用 Bash 语法（`bashast`，基于 mvdan/sh）解析命令。两种语法的引号、转义和分隔规则不同：

```text
echo "\" ; Remove-Item -Recurse C:\x ; echo "\"
```

- Bash 语法：一个 `echo`，带一个字面量参数。`echo` 没有效果，审查结果为放行。
- PowerShell：`\` 不是转义符，`;` 分隔三条语句，`Remove-Item` 会执行。

解析结果已用 `go test -overlay` 探针在 macOS 上确认；"放行"由代码路径推出，未在 Windows 上端到端运行。

相关事实：

- Host Bash 没有接 `internal/hostshell`，`tool_bash.go` 另有一份 Shell 选择逻辑；Git Bash 目前只用于 Terminal（见 [Windows Git Bash](../docs/WindowsGitBash实施进度.md)）。
- PowerShell 先按别名、函数、cmdlet 解析命令名，再找 exe。`cat`、`ls`、`rm`、`cp`、`mv`、`sort`、`tee` 是 cmdlet 别名；Windows PowerShell 5.1 的 `curl`、`wget` 是 `Invoke-WebRequest`。按名字套用 GNU 语义是错的身份。
- Windows 默认命令白名单是 `*`。
- 单条直接连接器调用不经 Shell，按审查得到的 argv 直接启动，不受此问题影响。

### 2.2 一个函数做了所有层的事

`reviewBashScript`（`internal/accesspolicy/bash_review.go`）约 250 行，cwd 解析、容器目录、程序身份、连接器、opaque、技能/自写脚本豁免、路径、glob、子树、Git、curl 特例、重定向、cd 跟踪和连接器汇总都在一个循环里。新增一个特殊情况只能再加一个分支。

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
| 0 方言 | 实际由哪个 Shell 执行 | `bash` / `powershell` / `container-bash` / `direct`；其他（cmd、Windows 上配置的 bash.exe）为不支持 | 不支持的方言：整条命令 Unknown(dialect) |
| 1 语法 | 拆成哪些调用？有没有无法表达的 Shell 行为 | 调用列表（argv、重定向、环境赋值、源位置） | 硬阻断，或 Unknown(syntax) |
| 2 包装 | env、nohup、nice、timeout、stdbuf、command、`bash -c`、xargs 背后实际跑什么 | 展开后的调用与 cwd、环境变化 | Unknown(wrapper) |
| 3 身份 | argv[0] 实际是哪个程序 | Identity（见 3.2） | 未知程序 |
| 4 语义 | 这组参数做什么 | Effects：Ops、Unknowns、Deps（见 4.3） | 由命令文件给出 Unknown |
| 5 资源 | 这些路径和依赖实际是什么 | canonical 路径、空设备、glob 展开、Git 状态、脚本摘要 | 无法解析：Unknown(path) |
| 6 策略 | 每个事实对应哪条政策 | 一组 Requirement（block / hitl / auto / allow） | — |
| 7 合并 | 整条命令怎么审批？批准绑定什么 | BashPlan（叶子要求、指纹、展示原因） | — |

预审和启动前复审都完整运行 0–7 层；启动前复审使用实际子进程环境，只有这次复审消费一次性批准（与现状一致）。

### 3.2 各层合同

**0 方言**：由 `hostshell` 根据 Shell 配置、GOOS 和是否容器给出，Host Bash 的启动也使用同一结果，保证审查与执行方言一致。单条直接连接器调用的方言是 `direct`，不经 Shell。

**1 语法**：每种方言一个解析器，输出相同的调用结构。Bash 使用 mvdan/sh；PowerShell 使用 PowerShell 自带的 AST 解析器（6.2）。Shell 级硬阻断归这一层：
- Bash：控制字符、Unicode 空白、IFS、`/proc/*/environ`、`eval`/`source`/`exec`/`trap`/`alias` 等改变 Shell 自身状态的内建命令、zsh 模块命令。`bashsec` 与严格模式的同类名单合并到这里。
- PowerShell：改变命令解析的命令（6.2）。

**2 包装**：只有一份实现。包装命令不授予目标额外权限；`bash -c` 的字面量脚本回到第 1 层递归，深度上限只有一个常量；`xargs`、`find -exec` 的运行时目标无法静态确定，产出 Exec。

**3 身份**：

| Identity 类型 | 判定 | 后续 |
| --- | --- | --- |
| ShellBuiltin | 方言内建命令（未被包装命令绕过） | 进入第 4 层 |
| System | canonical 路径等于启动时记录的系统安装 | registry 中有分析器则进入第 4 层，否则 opaque |
| PlatformBuiltin | rg、pdftotext、dbx、httpx 等平台内置程序的 canonical 路径 | 同上 |
| Cmdlet | PowerShell 中名称（含别名）解析为内置模块的 cmdlet | 以正式名查 registry，有分析器则进入第 4 层，否则 Exec |
| Connector | 已挂载连接器的冻结入口，路径与摘要核验通过 | 连接器规则，不走命令分析 |
| Interpreter | 系统解释器加脚本（含 shebang） | Exec；可能被技能脚本或自写脚本凭据豁免 |
| BuildTool | go、npm、make 等 | Exec |
| Shadowed | PowerShell 函数、外部脚本、非内置模块命令，或指向它们的别名 | Exec |
| Unknown | 其他 | Exec |

同名 Workspace 程序、PATH 前置目录中的同名程序都拿不到 System 身份。PowerShell 中只有解析为外部程序（Application）的名字才可能取得 System / PlatformBuiltin 身份（6.3）。

**4 语义**：命令文件是纯函数，不做 IO、不读部署凭据。输入 argv，输出事实。详见第 4 节。

**5 资源**：解析相对路径、symlink/junction、容器 guest 路径、glob；识别空设备；核验 Git 配置与 hooks；对脚本和入口计算摘要。只有这一层和第 3 层访问文件系统或容器探针。

**6 策略**：见 3.3、3.4。

**7 合并**：沿用现有 BashPlan 的叶子要求、指纹、规则批准与精确批准语义；Bash 工具只有这一种审批对象（阶段 5 合并 `bashsec` 的批准）。

### 3.3 策略层判断顺序

1. **硬阻断**（任何档位、任何批准都不能放宽）：Shell 级硬阻断、readonly 与可信只读根、平台保护目录（StateDir、identity）、临时根逃逸、递归目标包含受保护子树、容器 glob、editing=false 时在 Workspace 内写入或执行 Exec。
2. **身份豁免**：连接器入口、本次选中技能脚本、本 Run 自写脚本，只抵消对应调用的 Exec，不抵消它可见的文件、远端或破坏性事实。
3. **逐个事实查映射表**（3.4）。
4. **合并**：同一调用的多个要求各自成为叶子，规则批准只覆盖对应叶子。

### 3.4 事实 → 政策键

这是全模块唯一一张映射表，放在 `policy/mapping.go`。

| 事实 | 条件 | 政策键 |
| --- | --- | --- |
| Read | 越出读根 | `read-outside-roots` |
| Output / Create / Delete / Rename / Chmod | 越出写根 | `write-outside-roots` |
| 同上 | 在写根内 | `bash-write-in-write-roots` |
| 写入 Git 可执行配置路径 | — | `executable-config`（由路径规则判定，现状） |
| Output | 目标经第 5 层确认为空设备 | 无要求 |
| Delete（递归）/ Discard（丢弃未提交修改） | — | `destructive` |
| RemoteMutation | — | `remote-mutation` |
| Exec、UnknownBehavior、Dep 核验失败 | 无身份豁免 | `bash-opaque-command` |
| UnknownOption | — | 可见文件升级为写入（现状；是否追加审批见待决定 1） |
| Unknown(syntax / wrapper / path / dialect)、动态 cwd、动态值 | — | `bash-complex-filesystem` |

默认三档（`internal/config/config_defaults.go`）：

| 政策键 | ask_for_approval | auto_approve | full_access |
| --- | --- | --- | --- |
| read-outside-roots | hitl | auto | allow |
| write-outside-roots | hitl | hitl | allow |
| bash-write-in-write-roots | allow | allow | allow |
| bash-opaque-command / bash-complex-filesystem | hitl | auto | allow |
| destructive / executable-config / remote-mutation | hitl | hitl | allow |

### 3.5 不变式

1. 命令文件只产出事实，永远不返回 allow 或 hitl。
2. 命令文件只能增加要求，不能减少。唯一的"减法"是第 5 层的空设备规则，且只对 Output 生效；Delete、Chmod、Rename 指向空设备不豁免。
3. Unknown 与已识别的事实同时保留：未知参数之前识别出的删除、上传不会丢。
4. 命令名单只有 registry 一处；身份层、opaque 判定、解释器列表都从 registry 读。部署白名单 `bash.allowed-commands` 仍独立，登记分析器不等于默认允许。
5. 方言没有解析器时，整条命令记为 Unknown(dialect)，不经过任何"简单命令"快速放行。
6. 既有的连接器、技能脚本、自写脚本凭据合同不变，重构不能扩大它们的豁免范围。

## 4. 命令知识：registry 与命令文件

### 4.1 Registry 条目

```go
type Command struct {
    Names   []string               // 名称与别名，如 awk/gawk/mawk/nawk
    Kind    Kind                   // Analyzed / ShellBuiltin / Wrapper / Interpreter / BuildTool
    Source  Source                 // System / PlatformBuiltin / Cmdlet
    Options *OptionSpec            // 声明表：带值/不带值/可选值，值是读文件、写文件、目录、URL 还是普通值
    Analyze func(p Parsed, e *Effects) // 声明表表达不了的条件；可为空
}
```

- 简单命令只写 `Options`，由共享解析器得出文件操作数。
- 有子命令、内嵌语言或特殊条件的命令写 `Analyze`。
- `Kind` 取代 `ordinaryCommands`、`shellBuiltins`、`wrapperNames`、`isInterpreter`、`isOpaqueCommand`。启动时记录系统安装路径的名单也从 registry 生成。
- cmdlet 只按正式名登记（`Remove-Item`），别名到 cmdlet 的解析归身份层（`identity/powershell.go`）。

### 4.2 命令文件示例

"针对命令集的特殊拦截和过滤条件"都写在这里，以事实的形式出现：

```go
// commands/curl.go
var curl = Command{
    Names:   []string{"curl"},     // PS 5.1 的 curl 别名由身份层解析为 Invoke-WebRequest，不归这里
    Kind:    Analyzed,
    Source:  System,
    Options: curlOptions,          // -o/--output 为写文件，-T 为读文件，-d/-F 为远端数据……
    Analyze: func(p Parsed, e *Effects) {
        e.Output(p.Values("o", "output")...)            // 空设备由资源层识别，这里不判断
        if p.MethodNot("GET", "HEAD", "OPTIONS") || p.HasAny("d", "F", "T", "json") {
            e.RemoteMutation("curl:method-or-data")
        }
        if p.HasAny("K", "config") {
            e.UnknownBehavior("curl:-K")                // 配置文件内容不解析，交给审批
        }
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
    Kind      OpKind // Read / Output / Create / Delete / Rename / Chmod / Exec / RemoteMutation / RepoWrite / Discard
    Path      string // 文件类操作的原始操作数，由资源层解析
    Recursive bool
    Arg       int    // 证据：argv 下标
    Rule      string // 证据：规则 ID，如 "curl:-o"
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

`Arg` 与 `Rule` 用于 HITL 展示原因，以及说明哪些输入变化会使批准失效。字段名称在实现阶段确定，含义以本节为准。

### 4.4 新增或修改命令的检查清单

每个命令文件的注释或测试需要回答：

1. 识别哪个实际程序与别名？同名 Workspace/PATH 程序如何拿不到 System 身份？
2. 哪些参数带值、可选值？组合短参数和 `--` 怎么解释？
3. 哪些值是文件、目录、URL、表达式、脚本？各自是 Read、Output、Delete、Rename、Chmod，还是递归范围？
4. 是否读取默认配置、环境、插件、hooks？由资源层核验，还是记为 UnknownBehavior？
5. 哪些参数会启动其他程序或让目标在运行时才确定？
6. 未知选项、缺失参数如何返回，是否保留已识别的危险事实？
7. Host 与容器、Unix 与 Windows 的行为是否一致，哪些未验证？
8. 回归用例：通过、需审批、硬阻断各至少一条。

## 5. 目录结构

### 5.1 目标结构

```text
internal/shellreview/              # Shell 审批唯一入口
  review.go                        # Review(Input) BashPlan：按层调用，不出现任何命令名
  input.go                         # 命令、cwd、环境、方言、session、执行环境探针
  dialect/                         # 第0层：方言枚举（叶子包，hostshell 也依赖它）
  syntax/
    invocation.go                  # 各方言共用的调用结构
    bash/                          # 第1层 Bash：现 bashast + bashsec 的 Shell 级检查
    powershell/                    # 第1层 PowerShell（阶段 6a）
      helper.ps1                   # 固定 helper 脚本（go:embed）：只解析，不执行
      helper.go                    # 常驻 helper 进程：启动、请求、超时、重启
      ast.go                       # AST 节点白名单 → 调用结构；硬阻断名单
  unwrap/                          # 第2层：包装命令与 bash -c，只此一份
  identity/                        # 第3层
    identity.go                    # Identity 类型与 Resolve 入口
    unix.go                        # PATH 解析、系统安装记录
    windows.go                     # PATHEXT 解析
    powershell.go                  # 别名 → 函数 → cmdlet → 外部程序；内置模块判定
    container.go                   # 容器内解析探针
    scripts.go                     # 解释器脚本识别、shebang
    connector.go                   # 连接器入口核验与直接调用
  commands/                        # 第4层：纯函数，无 IO
    registry.go                    # Command 表、Kind、Lookup
    effects.go                     # Op / Unknown / Dep
    options.go                     # 共享选项解析
    simple.go                      # echo printf pwd true false date sleep uname whoami id basename dirname which printenv test tr cd top free
    files.go                       # cat head tail wc file stat readlink realpath cut sort uniq du ls diff grep rg
    fileops.go                     # mkdir touch tee chmod rmdir rm cp mv ln
    curl.go  wget.go  lsof.go  git.go  find.go
    sed.go   awk.go   jq.go    tar.go  pdftotext.go
    *_test.go                      # 每个命令文件各自的语义测试
    powershell/                    # cmdlet 分析器（阶段 6b）
      params.go                    # 参数绑定：大小写、前缀缩写、位置参数、公共参数
      items.go                     # Get-ChildItem Get-Content Copy-Item Move-Item Remove-Item New-Item Set-Content Out-File …
      web.go                       # Invoke-WebRequest Invoke-RestMethod
  resources/                       # 第5层
    paths.go                       # 相对路径、glob、容器目录
    windows.go                     # 盘符、UNC、ADS、PowerShell provider 路径
    devices.go                     # 空设备：Unix /dev/null；Windows NUL、$null 及保留设备名
    gitstate.go                    # Git 配置与 hooks 核验
    fingerprint.go                 # 执行环境与内容版本指纹
  policy/                          # 第6层
    hardlimits.go                  # 3.3 第 1 步
    exemptions.go                  # 3.3 第 2 步
    mapping.go                     # 3.4 映射表
  plan/                            # 第7层
    plan.go                        # BashPlan 与构造
    combine.go                     # 合并、待批准叶子、审批规则
    approval.go                    # 批准登记、查询、消费、来源与元数据
```

### 5.2 现有代码去向

| 现有位置 | 去向 |
| --- | --- |
| `internal/bashast/*` | `syntax/bash/` |
| `internal/bashsec/bash_checks.go`、`bash_patterns.go`、`bash_security.go`、`bash_ast.go` | `syntax/bash/`（Shell 级硬阻断）；审批结果并入 `plan/`（阶段 5） |
| `internal/bashsec/bash_wrappers.go`、`accesspolicy.unwrapExecution`、`shellanalysis/command.go` | `unwrap/` |
| `accesspolicy/bash_execution.go` | `identity/`（身份）与 `unwrap/`（包装） |
| `accesspolicy/bash_connector.go`、`connector_invocation.go` | `identity/connector.go` |
| `internal/shellanalysis/*` | `commands/`（`operands.go` 的 switch 拆进各命令文件，`specs.go` 拆成各命令的 `Options`） |
| `accesspolicy/git_state.go`、`bash_environment.go`、Bash 用的 glob 展开 | `resources/` |
| `accesspolicy/bash_review.go` | `review.go`、`policy/`、`plan/` |
| `accesspolicy/bash.go` | `plan/`（BashPlan、批准）与 `resources/`（重定向分类） |
| `tools/tool_bash.go` 的 `resolveHostShellInvocation`、`unsupportedBashCommands` | 前者改用 `hostshell`；后者并入 `syntax/bash/` |

保留不动：`internal/accesspolicy` 只保留文件工具也要用的通用路径策略（`BuildPathPlan`、`BuildSubtreePlan`、档位、session 路径解析、保护目录）；`internal/hostshell` 负责启动并给出方言；`internal/shellenv`、`internal/scriptstate`、`internal/skillsexec` 不变。

旧包与旧 API 一次切换，调用方（`internal/tools`、`internal/llm`）在同一变更中改完，不保留转发别名。

### 5.3 依赖方向

```text
tools, llm ──> shellreview ──> syntax/*, unwrap, identity, commands, resources, policy, plan
                                     resources, policy ──> accesspolicy（通用路径策略）
hostshell ──> shellreview/dialect（叶子包）
commands：只依赖标准库
syntax/bash：只依赖 mvdan/sh
syntax/powershell：启动平台自带的 helper 进程，不执行被审查的命令
accesspolicy：不依赖 shellreview
```

## 6. Windows

已决定：Windows Host 继续使用 PowerShell，不改为 Git Bash；PowerShell 语法使用 PowerShell 自带的 AST 解析器。Git Bash 尚未启用，本期不设计 MSYS 方言。

### 6.1 方言矩阵

| 执行环境 | 实际执行 | 现状 | 目标 |
| --- | --- | --- | --- |
| Unix/macOS Host | `bash --noprofile --norc -o pipefail -c` | Bash 审查，一致 | 不变 |
| 容器（含 Windows 宿主上的容器） | 容器内 bash | Bash 审查，一致 | 不变 |
| Windows Host 默认 | `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command`，或配置的 `pwsh` | 按 Bash 审查，不一致 | 阶段 0 闸门；阶段 6a 起按 PowerShell AST 审查 |
| Windows 配置 `cmd` | `cmd.exe /d /s /c` | 按 Bash 审查，不一致 | 始终 Unknown(dialect)，不支持 cmd 语法 |
| Windows 配置 `bash.exe` | MSYS bash | Bash 语法一致，MSYS 路径未映射 | 始终 Unknown(dialect)；Git Bash 启用时另行设计 |
| 单条直接连接器调用 | 不经 Shell，按 argv 启动 | 一致 | 不变 |

### 6.2 PowerShell 语法：自带 AST 解析器

**做法**

- 用与执行相同的 PowerShell 可执行文件解析。`powershell.exe`（5.1）与 `pwsh`（7）语法不同，例如 `&&`、`||` 只有 7 支持，所以不能用一个版本代替另一个。
- 平台自带一个固定 helper 脚本（`go:embed`），以 `-NoProfile -NonInteractive` 启动，只调用 `[System.Management.Automation.Language.Parser]::ParseInput`，不执行被审查的命令。命令文本经 stdin 传入，不拼接进 helper 脚本或命令行。
- helper 输出 JSON：语句、调用、参数、重定向，以及每个命令名的解析结果（6.3）。Go 端按下方白名单映射为与 Bash 相同的调用结构。
- 常驻：每个 PowerShell 可执行文件一个 helper 进程，按行收发 JSON；超时或崩溃即重启。helper 不可用、有解析错误或输出不符合预期时，整条命令为 Unknown(dialect)。
- 解析对象是用户命令本身。平台前置的 UTF-8 输出设置和 `$env:PATH=` 环境绑定是固定模板，由单独测试覆盖其转义。
- 预审与启动前复审都重新解析，不复用缓存的 AST。

**允许的节点**（白名单，其他节点一律 Unknown(syntax)）：

| 节点 | 映射 |
| --- | --- |
| 语句列表、`;`、换行；PowerShell 7 的 `&&`、`||` | 多个调用，所有分支都审查 |
| 管道中的 CommandAst | 一个调用；命令名必须是常量 |
| 常量参数：裸词、单引号字符串、不含变量和子表达式的双引号字符串、数字 | 参数 |
| CommandParameterAst（`-Recurse`、`-Path:value`） | 参数，保留原文交给命令语义层匹配 |
| `&` 调用常量命令名 | 同 CommandAst |
| 文件重定向，目标为常量或 `$null` | Output；`$null` 为空设备 |
| 流合并重定向（`2>&1`） | 中性 |
| `cd` / `Set-Location` / `sl` 加常量路径 | 与 Bash 的 cd 相同，记录可能的 cwd |

**Unknown(syntax)**，即整条命令 complex，与 Bash 的非简单命令相同：变量与 `$env:` 引用（`$null` 重定向除外）、子表达式 `$()`、可展开字符串中的变量或子表达式、脚本块 `{}`、splatting `@x`、.NET 成员调用与类型字面量（`[IO.File]::WriteAllText`）、赋值语句、函数与类定义、dot-source `.`、`--%` 停止解析符、`-EncodedCommand`。

**硬阻断**：改变后续命令解析的命令。它们会让同一条命令里后面的调用身份判断出错，与 Bash 的 `alias`、`source` 同类：`Set-Alias`、`New-Alias`（及别名 `sal`、`nal`）、`Import-Module`（`ipmo`）、对 `Alias:`、`Function:` 驱动器的写入。

**外部程序参数**：PowerShell 5.1 与 7.3+ 向 exe 传参的引号规则不同。参数含 `"`、为空字符串或含需要转义的字符时为 Unknown(syntax)，避免审查的 argv 与 exe 实际收到的不一致。

### 6.3 PowerShell 身份

- helper 对每个命令名返回 PowerShell 的解析结果：命令类型（Alias / Function / Cmdlet / ExternalScript / Application）与所属模块。解析顺序与 PowerShell 一致：别名 → 函数 → cmdlet → 外部程序。
- 别名或名称指向内置模块（`Microsoft.PowerShell.Management`、`Microsoft.PowerShell.Utility`、`Microsoft.PowerShell.Core` 等）的 cmdlet，身份为 Cmdlet，以正式名查 registry（`rm` → `Remove-Item`，PS 5.1 的 `curl` → `Invoke-WebRequest`）。
- Application（按 PATH/PATHEXT 找到的 exe）走与 Unix 相同的 System / PlatformBuiltin 判定，按 registry 分析 argv（`curl.exe`、`git`、`rg`）。
- 函数、`.ps1` 外部脚本、非内置模块命令为 Shadowed，即 Exec。`.ps1` 以后可接入技能脚本、自写脚本凭据，不在首批。
- 待核实：helper 查询命令时模块自动加载是否会执行模块代码。若会，helper 禁用自动加载，并把"可能被模块命令遮蔽"列为验收项（待决定 7）。

### 6.4 PowerShell 命令语义（cmdlet）

- 阶段 6a：所有 cmdlet 为 Exec；外部程序按现有 registry 分析。这时 Windows 上 `git status`、`rg`、`curl.exe` 恢复与 Unix 相同的分析，cmdlet 按 opaque 审批。
- 阶段 6b：在 `commands/powershell/` 为常用 cmdlet 增加分析器。共享参数绑定规则：参数名大小写不敏感；可用唯一前缀缩写（`-Rec` 即 `-Recurse`，有歧义为 Unknown(option)）；位置参数；`-Force:$false` 形式；公共参数（`-WhatIf`、`-ErrorAction`、`-OutVariable` 等）；`-Path` 支持通配符，`-LiteralPath` 不支持。
- 首批候选（待决定 8）：
  - 只读：Get-ChildItem、Get-Content、Select-String、Test-Path、Get-Location、Write-Output、Get-Process、Get-NetTCPConnection（端口查询，对应 lsof 场景）。
  - 写入：New-Item、Set-Content、Add-Content、Out-File、Copy-Item、Move-Item、Remove-Item（`-Recurse` 为 destructive）。
  - 网络：Invoke-WebRequest、Invoke-RestMethod（非 GET/HEAD 方法、`-Body`、`-InFile` 为 RemoteMutation；`-OutFile` 为 Output）。

### 6.5 Windows 资源

- 空设备：`NUL`、PowerShell 的 `$null` 重定向。Windows 上任意目录下的保留设备名（`NUL`、`CON`、`PRN`、`AUX`、`COM1`…，含带扩展名的形式）都指向设备而不是普通文件；只有 `NUL` 视为空设备，其他保留名为 Unknown(path)。
- 路径：盘符绝对 `C:\`、盘符相对 `C:foo`、UNC `\\server\share`、`\\?\` 前缀、大小写不敏感、8.3 短名、ADS `file.txt:stream`、junction。无法确定为普通路径时为 Unknown(path)。
- PowerShell provider 路径：`Env:`、`Alias:`、`Function:`、`Variable:`、`HKLM:`、`HKCU:`、`Cert:`、`WSMan:` 不是文件系统，一律 Unknown(path)；`Alias:`、`Function:` 的写入为 6.2 的硬阻断。`FileSystem::C:\x` 这类 provider 限定路径首批也按 Unknown(path) 处理。

### 6.6 阶段 0 闸门

规则：阶段 6a 完成前，Windows Host（非容器）上除单条直接连接器调用外，整条命令按 Unknown(dialect) 处理，即 `bash-complex-filesystem`；同时保留 cwd 读审查与 editing=false 的执行限制（复用现有"非简单命令"分支）。阶段 6a 完成后，闸门只对 cmd 和 Windows 上配置的 bash.exe 保留。

可见变化：

| 档位 | 影响 |
| --- | --- |
| ask_for_approval | Windows Host 上几乎每条命令都需要审批 |
| auto_approve | 自动通过并记审计 |
| full_access | 不变 |

Unix、macOS、容器不受影响。

### 6.7 Windows 验收

- 2.1 的样例：阶段 0 后需要审批；阶段 6a 后解析为三条语句，`Remove-Item` 为 Exec。
- `Set-Alias rg Remove-Item; rg ...` 硬阻断。
- PowerShell 别名（`rm`、`cp`、`curl`）不得取得 GNU 命令的语义。
- helper 不可用、解析错误、超时：整条命令 Unknown(dialect)。
- 用 powershell.exe 与 pwsh 分别解析 `a && b`，结果与各自的执行行为一致。
- 外部程序参数含引号、空字符串：Unknown(syntax)。
- 直接连接器调用在三档下行为不变。
- 原生 Windows 运行：中文/空格路径、跨盘、UNC、junction、`NUL` 输出；交叉编译与 macOS 模拟不算验收。

## 7. 实施阶段

结构迁移、语义修正、政策调整分开提交。阶段 1 之前先生成"审批快照"：对现有测试中的命令与附录 B 的样例，记录当前的 Decision、RuleKey、Fingerprint 和叶子要求，作为后续阶段的对比基准。

| 阶段 | 内容 | 验收 | 可见行为变化 |
| --- | --- | --- | --- |
| 0 Windows 闸门 | 6.6 规则；`hostshell` 给出方言，Host Bash 审查读取它 | 6.7 第 1 条（阶段 0 部分）与直接连接器调用；Unix/容器快照不变 | 有，见 6.6 |
| 1 建目录、纯搬家 | 按第 5 节移动代码，调用方一次改完 | 全仓 `go test ./...`；审批快照完全一致 | 无 |
| 2 统一名单与包装 | registry 取代 2.3 的名单；`unwrap` 与 `bash -c` 递归各只留一份 | 审批快照一致；`df`、`dbx`、`httpx`、`Get-*` 的处理逐条列出并确认 | 仅确认过的条目 |
| 3 拆主函数 | `reviewBashScript` 按层拆开；curl 的空设备豁免改为资源层规则，覆盖所有命令的 Output 和重定向；`git -C` 移入 `git.go` | 审批快照一致；`tee /dev/null` 等新用例；`rm`/`chmod` 指向空设备不豁免 | 其他命令输出到空设备不再审批 |
| 4 事实细化 | Effects 改为 Ops / Unknowns / Deps 与证据；HITL 原因带参数位置 | 性质测试：增加未知项不丢已识别的删除/上传；更换程序身份不继承分析结果 | 视待决定 1 |
| 5 合并两套审批 | `bashsec` 的结果并入 BashPlan 叶子；`internal/llm` 只处理一种 Bash 审批 | HITL 请求、精确/规则批准消费、邮件代批、恢复用例 | HITL 请求形状可能变化，与 WebClient 同步 |
| 6a PowerShell 语法与身份 | helper 与 AST 白名单（6.2）；PowerShell 身份解析（6.3）；外部程序按 registry 分析，cmdlet 为 Exec；Host Bash 启动改用 `hostshell` 给出的同一方言与可执行文件 | 6.7 全部 | Windows 外部程序恢复与 Unix 相同的分析；阶段 0 闸门对 PowerShell 解除 |
| 6b PowerShell cmdlet 语义 | 共享参数绑定与首批 cmdlet 分析器（6.4） | 每个 cmdlet 的语义测试；缩写与歧义；provider 路径；`Remove-Item -Recurse` 为 destructive | 首批 cmdlet 不再一律 opaque |

实施完成后同步更新：`AGENTS.md`（命令语义包名）、[工具目录权限](../docs/工具目录权限.md)、[AccessPolicy 与 HITL 边界](../docs/AccessPolicy与HITL改造.md)、[Runtime 模块边界](../docs/Runtime模块边界.md)、[Windows Git Bash](../docs/WindowsGitBash实施进度.md)（注明 Host Bash 仍为 PowerShell）。

## 8. 决定事项

### 8.1 已决定

- Windows Host 继续使用 PowerShell，不改为 Git Bash。阶段 0 闸门接受 ask_for_approval 下审批增加，直到阶段 6a 完成。
- PowerShell 语法使用 PowerShell 自带的 AST 解析器，用与执行相同的可执行文件。
- Git Bash 尚未启用，本期不设计 MSYS 方言；Windows 上配置的 bash.exe 与 cmd 一样按 Unknown(dialect) 处理。

### 8.2 待决定

1. UnknownOption 是否追加 `bash-complex-filesystem` 审批？现状只把可见文件升级为写入，没有文件操作数时不审批。
2. 默认白名单是否由 registry 生成？生成时显式配置仍优先。
3. 首批是否新增 ps、ss、netstat 等查询命令？
4. curl `-K`、命令自身配置文件：解析内容，还是一律 UnknownBehavior？
5. 空设备确认：只认路径字符串，还是 Host 上额外确认为字符设备？容器内无法确认时如何回退？
6. PowerShell helper 常驻还是每次审查启动？建议常驻：PowerShell 5.1 冷启动通常要数百毫秒，而每次调用要解析两次（预审、启动前复审）。
7. helper 查询命令时若模块自动加载会执行模块代码：禁用自动加载并接受"模块命令遮蔽"的残余风险，还是改用其他方式判断遮蔽？
8. 阶段 6b 首批 cmdlet 范围（6.4 的候选是否全做）。

## 附录 A：已有局部修复（2026-10-30，commit 9731155e）

**lsof**：新增 `internal/shellanalysis/lsof.go`，接入分派、系统命令识别和 Unix/macOS 默认白名单。识别 `-a/-b/-h/-l/-n/-P/-R/-t/-v/-w` 标志与 `-c/-p/-u/-i/-s/-F` 参数（附着或分离形式）；普通操作数为读取，`--` 后按文件名处理；`-D`、`+d/+D`、未知选项和缺少必要参数回退到 opaque。只消费参数，不校验 PID、用户名、协议状态等值域。

**curl/wget 输出到 `/dev/null`**：`bash_review.go` 中的局部规则。命令为 curl/wget、分析完整、写入目标字符串精确为 `/dev/null`，且处于非 Windows Host 或容器时，`BuildPathPlan` 无错误且未阻断则跳过该输出的路径审批。普通文件、上传、远端修改和 readonly 继续检查。阶段 3 由资源层空设备规则取代。

**验证**：`go test ./internal/shellanalysis ./internal/accesspolicy ./internal/config ./internal/tools ./internal/bashsec` 通过（部分命中缓存）。新增 `lsof_test.go`、`bash_diagnostics_test.go`。容器用例使用模拟解析环境；Host 用例在缺少程序时跳过；Windows Host 跳过这组 Unix 诊断用例。未执行全仓测试、真实容器和跨平台验收。

## 附录 B：触发场景与回归样例

| 样例 | 原问题 | 期望 |
| --- | --- | --- |
| `lsof -nP -iTCP:8080 -iTCP:8081 -sTCP:LISTEN` | lsof 未登记，按 opaque 审批 | 只读查询，无需审批（已修复） |
| `curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8081/healthz` | `-o /dev/null` 被当成越界写入 | 无需审批（已修复，阶段 3 改为通用规则） |
| `curl -X POST ...`、`curl -T file ...` | — | `remote-mutation`，空设备输出不豁免 |
| `tee /dev/null`、`cmd > /dev/null` | tee 尚未豁免 | 阶段 3 后无需审批 |
| `rm /dev/null`、`chmod 600 /dev/null` | — | 不豁免 |
| 同名 Workspace 程序 `./lsof` | — | 不取得 System 身份 |
| 2.1 的 PowerShell 样例（Windows Host） | 被当成单个 echo 放行 | 阶段 0 后需要审批；阶段 6a 后解析出 `Remove-Item` 为 Exec |
| `Set-Alias rg Remove-Item; rg -r . C:\x`（PowerShell） | — | 硬阻断 |
| `git status`（PowerShell，git 为 Application） | 阶段 0 期间为 complex | 阶段 6a 后与 Unix 相同 |
| `rm -Recurse C:\x`（PowerShell，`rm` 为 Remove-Item 别名） | — | 阶段 6a 为 Exec；阶段 6b 为 destructive |
