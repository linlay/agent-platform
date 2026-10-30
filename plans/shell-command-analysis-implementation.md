# Shell 命令审批：方案评审与实施计划

> 状态：实施中。首批 Host 启动绑定、基线与方言保护已实现，范围及验证见第 8 节；完整目录迁移与 PowerShell 语义仍未完成，未部署。
> 日期：2026-10-30。评审代码基线：`509b81a5`；原方案使用本次评审读取的版本，指纹见下行。
> 依据：[分层流水线与目录方案](shell-command-analysis.md)。本次读取版本 SHA-256：`3c4e8713b113a506c958acd285a7173cab85558c73e18291923fff08871e2092`。
> 2026-10-30 复审：R1–R8 已并入方案正文（层次、资源三态、registry、Windows 身份与 6.6 未知方言合同），第 5 节的待定项汇总在方案 §8.2；对 `5554545f` 的代码复审发现见 §8.4。

## 1. 评审结论与本次范围

总体方向可采用：在 `internal/shellreview/` 集中 Shell 审查，特殊命令按文件拆解，命令文件只产生事实，统一做资源解析、策略映射与授权汇总。

原方案已经超出最初两个命令的局部修复，包含 Windows 方言、包装与身份绑定、整个包迁移、事实模型和两套批准合并。应按下文依赖实施，不能把全目录搬迁当成已解决语义与安全问题。

本稿保留原方案明确的方向：Windows Host 继续使用 PowerShell；使用匹配执行器的 PowerShell AST；本期不支持 MSYS/cmd 方言；特殊命令使用独立文件；不增加 Agent mode 分支；Host 不承诺任意代码内部访问隔离。

需要修改或补齐的合同见第 2 节。涉及新增审批、阻断或 Windows 自动批准行为的建议，都应作为政策变化独立审核，不能夹在纯重构里默认生效。

## 2. 发现的问题及建议修正

### R1 / P1：包装拆解排在程序身份确认之前，可能信任同名伪装程序

原方案 §3.1–3.2 先解包装，再检查身份。若按命令名把 Workspace/PATH 中的假 `env`、`timeout` 或假 `bash` 当作系统包装拆开，可能只审查其看起来只读的内层命令，漏掉实际执行的外层程序。

当前 [analyzeBashExecution](../internal/accesspolicy/bash_execution.go) 是先调用 recognizedProgram，再允许对应包装解开；重构必须保持这个条件。

**修正**：语法解析后，对当前调用绑定身份；只有已验证的方言内建包装、系统包装或系统解释器才展开。展开得到的子调用重新绑定身份。包装与身份是有界交替过程，不能是两个互不依赖的单向阶段。

**验收**：真实系统 env/bash 继续可分析；同名脚本、伪造绝对路径、PATH/cwd 改变后不得继承包装或字面量递归能力。保留外围重定向、环境与 cwd 变化。

### R2 / P1：直接复用 hostshell 会改变启动合同

原方案 §3.2、§7 要求 Host Bash 改用 hostshell。但 [hostshell.Resolve](../internal/hostshell/shell.go) 在 GitBash.Enabled 时选择受管 Git Bash；UnmanagedInvocation 的 Unix 默认参数使用 `-lc`。当前 [tool_bash.go](../internal/tools/tool_bash.go) 使用 `--noprofile --norc -o pipefail -c`。

直接替换可能开启登录启动文件，改变 PATH、函数与配置；Windows 还可能与“继续使用 PowerShell”的决定冲突。

**修正**：先给 hostshell 增加调用用途/明确启动合同，区分工具调用与 Terminal。工具默认保持现有 Shell、启动参数和 UTF-8 行为，不通过 Terminal 的 Git Bash 开关隐式切换。Shell 可执行文件、完整 args 模板、有效环境、cwd、方言与版本组成冻结 LaunchSpec。

方言不能仅由可执行文件 basename 判断。自定义 `ShellArgs`、登录/交互标志、命令模板前后缀都可能影响实际执行，需要验证支持范围；不支持的启动形式走明确的未知或拒绝合同。

**验收**：迁移前后逐项比较 Executable/Args/Env/CWD；工具与 Terminal 分别测试；配置 ShellArgs 的启动语义不能悄悄变化。

### R3 / P1：阶段 0 闸门只能阻止 Bash 误判，不能完成 Windows 安全修复

原方案 §6.6 将不支持的方言映射为 complex，auto_approve 下自动允许。这样虽然不再把 PowerShell 误报为只读 echo，但未知正文里的删除和远端修改也无法产生各自的独立审批要求。

**修正建议**：优先交付方言判定与拒绝错误方言的快速放行；同时把 PowerShell AST 接入提前，不能等整包迁移与审批合并全部完成。是否允许 auto_approve 直接执行不支持的方言必须单独决定。

推荐 auto_approve 下未知方言不自动运行；这与原方案当前矩阵不同，是待审核政策建议。若保留原矩阵，发布说明应明确其为未知代码授权，不能宣称已保留正文里无法识别的 destructive/remote-mutation 检查。full_access 的处理也必须明确，不在结构迁移中改变。

**验收**：三档分别验证；预审与最终执行使用同一方言；helper 不可用与配置 cmd/bash.exe 时不得退回 Bash 的“简单命令”路径。

### R4 / P1：PowerShell helper 不止有解析问题，还存在身份查询副作用与进程间不一致

原方案 §6.3 将模块自动加载列为待核实。微软文档已确认：按精确名称使用 Get-Command 会自动导入模块；自动导入可运行模块脚本。这与只调用 ParseInput 的无执行语法分析不同。[Get-Command 官方文档](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/get-command?view=powershell-7.5)

此外，常驻 helper 与新启动的执行进程可能有不同的 PATH、PATHEXT、PSModulePath、cwd、已加载模块、别名和偏好变量。只使用相同 PowerShell 可执行文件不能证明命令身份相同。仅检查模块名称，也不足以证明模块来自可信安装路径。

**修正**：区分纯 AST 解析与身份探测；在 helper 中禁止未经授权的模块加载。执行端必须采用同样的模块/身份规则，或在执行进程中再次核对已冻结的解析结果；不能只在 helper 里禁用加载、执行端却继续默认加载。

身份绑定至少记录 Shell 路径/版本、环境与 cwd 指纹、模块真实路径/身份、正式命令名、外部程序 canonical 路径。无法确认的项返回未知，不按名称授予 cmdlet/System 身份。

**验收**：构造同名模块、别名和 PATH 差异；带初始化标记的测试模块不得在审批前被加载；启动前环境或解析目标改变后旧批准失效。常驻 helper 请求需有独立 ID、超时、输出上限与清理，避免不同 Run 的环境和状态串用。

### R5 / P1：实施顺序反了，通用空设备规则依赖事实细化

原方案 §7 阶段 3 实现所有命令的 Output 空设备豁免，阶段 4 才引入 Output/Delete/Chmod 等区分。当前 FileAccess 只有 Write，无法可靠完成前一步。

**修正**：先引入并迁移细粒度事实，保持原决策；然后实现空设备资源识别，最后删除 curl/wget 特例。拆主函数的纯结构变动与新增资源豁免分开提交。

**验收**：Output 到已确认空设备可以去掉该项路径要求；Delete/Chmod/Rename 不适用。若输出程序本身还有 Exec、上传或其他要求，仍须全部满足。因此原方案附录 B 的 `cmd > /dev/null` 不能笼统写成“无需审批”。

### R6 / P1：路径失败统一归 Unknown(path)，可能降级已有硬阻断

原方案 §3.1、§3.4 把无法解析资源映射为 Unknown(path) → complex；但临时根逃逸、非法路径及保护目录等不能因探测失败或 Unknown 回退而自动批准。

**修正**：资源结果区分 Known、Unresolved、HardViolation，保留结构化原因。已经确认的 readonly、保护目录、临时根逃逸等直接形成不可放宽事实，合并时最高优先；仅“不支持的方言/格式、探针暂不可用”等未确定项走未知政策。已识别的读写、删除和远端事实不得被未知覆盖。

**验收**：三档、精确批准、规则批准、脚本凭据均不能放宽 HardViolation；探针失败不能把已经发现的保护目标擦除。

### R7 / P2：目标包图与模型不足以直接实现，需先解决循环依赖和多目标操作

原方案将 Effects 放在 commands 根包，又让 commands/powershell 注册 cmdlet。如果子包引用父包 Effects，父包再汇总子包，会形成循环。语法、包装递归和 identity/commands 之间也需要明确依赖方向。

`Op` 只有一个 Path，尚未说明 Rename、Copy、链接等多目标操作如何表达。命令注册只有一个 Source，也难以同时表达系统与平台内置的同名安装；名称还必须按方言区分，不能混用 Bash 与 PowerShell 语义。

**修正**：新增标准库依赖的低层 types 包，统一 Invocation、Op、Unknown、Dep 和证据。命令根包仅聚合描述符；必要时将选项解析也放低层包。身份解析、资源探测都返回这些中立类型；递归由顶层调度，子包不反向调用父级 review。

多目标操作可以使用带 Role 的 Targets 或关联的多条事实；必须同时检查源、目标、被替换对象及递归范围。描述符按 Dialect + Name 查找，用身份允许集合或多个条目表达来源；部署白名单独立。

### R8 / P2：审批快照、批准兼容和“唯一减法”需要更准确的定义

原方案要求快照原始 Fingerprint 完全相同，但临时目录、系统入口和环境会影响它，跨机器保存原值不能稳定比较。原方案 §3.5 还称空设备是唯一减法，与 §3.3 的技能、自写脚本、连接器凭据抵消对应 Exec 不一致。

**修正**：行为快照规范化路径/环境和叶子顺序，对固定 fixture 单独验证指纹；另外测试改变命令、cwd、环境、程序、脚本和依赖版本必须使旧批准失效。对批准的兼容应定义为作用域、一次性消费和展示内容不变，而非所有重构都必须保持旧 hash。

命令分析只产生事实；资源确认与可信凭据可以按各自合同消除特定要求，均不得删掉兄弟要求。合并审批时先保留公开 HITL 形状和规则语义，再另行讨论协议调整。

## 3. 建议的包图与执行合同

沿用原方案一个目录、按层分子目录的方向，补上中立类型与注册依赖：

```text
hostshell ------------------------> shellreview/dialect
tools / llm ----------------------> shellreview
shellreview ----------------------> syntax / identity / unwrap / commands
                                    resources / policy / plan
上述各层 -------------------------> types / dialect（中立叶子包）
commands（聚合）------------------> 各命令分析器 / powershell 分析器
命令分析器 -----------------------> types / options（纯函数）
resources / policy ---------------> accesspolicy（公共路径策略）
```

constraints：accesspolicy 不反向导入 shellreview；contracts 不保存需要反向依赖自身的 plan 类型；启动、凭据注入和进程生命周期仍由 tools/既有服务拥有。收据注册/消费使用窄接口或中立数据，不把整个 ExecutionContext 分发给纯分析器。

LaunchSpec 建议包含实际 Shell canonical 路径、方言与版本、命令模板/最终源码摘要、Args、有效环境指纹、cwd、执行环境标识。最终复核和进程创建共享同一份冻结值；敏感环境值不展示进卡片或日志，只参与必要的内部绑定。

每个叶子要求关联 InvocationID、效果/未知来源、Arg/Span 和规则；执行凭据只能匹配对应入口事实。顶层以有界调度完成“绑定当前入口 → 验证是否可展开 → 展开并重新绑定”，而不是无条件按名称拆包装。

## 4. 可执行工作计划

以下以可单独验收的变更组织；每一步提交前必须可编译。迁移某个旧入口时，同一变更更新所有调用方并移除对应旧入口，不保留长期转发别名。既有两套授权对象可在后续合并之前暂按当前合同共存，不在纯搬家时提前改形状。

| 步骤 | 工作与交付物 | 依赖 | 验收门槛 |
| --- | --- | --- | --- |
| S0 合同与基线 | 关闭第 5 节的政策决策；列出命令/入口清单、规范化行为快照、授权作用域与 hard block 用例 | 无 | 当前可观察行为可解释；Windows 残余风险明确；不再有关键授权待定项 |
| S1 统一启动描述 | hostshell 增加工具用途的 LaunchSpec；tools 预审与启动共享它；方言与启动模板判定；接入批准失效指纹 | S0 | Unix 启动参数逐项一致；Windows 保持 PowerShell；Terminal/Git Bash 不被隐式改变；配置 ShellArgs 正确分类 |
| S2 方言保护 | 对缺少解析器的方言执行 S0 确定的策略；直接连接器仍按冻结 argv 合同验证；禁止错误 Bash 快速路径 | S1 | 三档、Host/容器、直接连接器与取消/重试用例；此步骤单独列为行为变化 |
| S3 中立类型与目录迁移 | 建 types/dialect 与层间接口；迁移既有 AST、安全检查、命令解析及相关调用方，尚不改授权结果 | S0，S1 | 包图无环；所有调用方可编译；规范化行为与已批准作用域一致；旧入口按迁移范围删除 |
| S4 统一命令、包装与事实 | registry 按方言登记；特殊命令独立文件；有界身份—包装展开；Op/Unknown/Dep 多目标与来源；Git -C 移到命令事实 | S3 | 假包装/假解释器不展开；未知不丢已知效果；复制/移动/链接各目标权限正确；名单一致性检查 |
| S5 PowerShell AST 与身份 | 固定 helper；绑定 Shell 版本、环境与模块规则；AST 白名单；首批只分析已证明 argv 一致的外部程序，cmdlet 保守处理 | S1，S2，S3，S4 | 原生 Windows 5.1/7 实测；helper 不在审批前加载测试模块；别名不套 GNU 语义；实际 argv 与分析一致；失败不降级 |
| S6 资源与通用输出 | 细化资源 Known/Unresolved/HardViolation；确认真实空设备；Output 统一规则；删除 curl/wget 名称特例 | S4；Windows 部分依赖 S5 | tee/curl 输出通用；rm/chmod/rename 不豁免；普通/伪装设备与 readonly 仍受限；真实容器校验 |
| S7 统一审批与调度接入 | bashsec/accesspolicy 的要求并入一个 plan；llm/tools 只调一个入口；保留 HITL 公开合同；移除旧批准分发/消费代码 | S4，S6；不阻塞 S5 的首次交付 | 一次批准只执行一次；并发兄弟调用不能借用；拒绝/取消/未启动不消耗错；重评、恢复与 Mailbox 代批覆盖 |
| S8 cmdlet 语义与收尾 | 按文件接入首批只读 cmdlet，再补文件与网络修改；公开参数/缩写/provider 等；清理重复入口；更新现行文档 | S5，S7 | 每个 cmdlet 独立语义与三档授权验收；原生 Windows 与真实容器完成各自范围；全仓测试通过 |

关键调整：S5 不等待整套审批合并完成；S6 在细化 Output 等事实之后；S7 的公开协议默认保持稳定。每个步骤可进一步拆提交，但不能把尚未具备检查能力的环境标为已支持。

首批不新增 ps/ss/netstat。先完成已有命令归属和授权合同，新的诊断命令作为后续独立变更处理，避免将语义扩展与大迁移混在一起。

## 5. 实施前需要明确的决定与建议默认值

| 决定 | 本稿建议 | 与原方案的关系 |
| --- | --- | --- |
| 未支持方言在 auto_approve 下怎样处理 | 不自动运行；采用显式人工或拒绝的受控回退，错误原因清晰 | 政策变化，需审核；原方案为自动通过 |
| UnknownOption 是否追加要求 | 追加与调用绑定的 complex 要求，同时保留已知效果 | 原方案待定；不能只升级已看到的文件 |
| curl -K/默认配置等隐式行为 | 首期记录依赖与 UnknownBehavior；不在首期重写完整配置语言 | 收敛首批范围；默认配置是否存在须探测，不能忽略 |
| 空设备身份 | 确认实际类型及空设备身份，不能仅以“字符设备”或字符串判断 | 原方案待定；不把 /dev/zero 等视为丢弃输出 |
| PowerShell helper 生命周期 | 先完成无状态请求合同与安全原型；基准数据支持后再选择常驻 | 原方案偏向常驻；正确性先于性能 |
| PowerShell 模块规则 | helper 与执行端采用一致且可验证的加载策略；不接受仅模块名或 helper 单边禁用 | 补齐原方案残余风险；未确认前不恢复对应免审批 |
| 默认白名单 | 首期保持现有部署政策，不由“存在解析器”直接生成 | 原方案待定；减少无关授权变化 |
| 首批 cmdlet | 先 Get-Location/Test-Path/Get-Content/Get-ChildItem/Write-Output，再文件修改与网络 | 原方案范围待定；每个命令需核验真实类型与来源，不因名称带 Get 就免审批 |

本表为评审阶段建议。首批已采用的方言政策见第 8 节；其余尚未实施的项不能视为当前配置或运行合同。

## 6. 验证方案与完成定义

### 6.1 分层与性质测试

- 命令层：选项附着/分离、可选值、缺参、重复选项、`--`、表达式、配置依赖、多目标操作；已知危险事实与未知项同时保留。
- 身份/包装：真实系统入口、Workspace 同名程序、PATH 阴影、假解释器、包装中的 cwd/环境变化、递归上限。
- 资源层：canonical 路径、symlink/junction、临时根逃逸、readonly/平台保护、递归子树、空设备与伪装目标；不可解析与硬拒绝分开。
- 策略层：三档、editing=false、未知参数/方言政策、多个叶子、凭据只抵消对应执行要求。
- 授权层：toolID、参数、环境、cwd、程序/脚本/依赖版本变化；单次消费、规则范围、并发、取消、拒绝、未启动、恢复与代批。

快照使用稳定 fixture 和规范化表示。性质测试检查“新增未知不删除既有要求”“更换程序身份不继承语义”“对输出的豁免不能转给删除或其他叶子”。原始指纹只在受控固定上下文中逐值验证。

### 6.2 Windows 和真实环境

- PowerShell 5.1 与 7 分别执行无害 argv 回显辅助程序，核对引号、空参数、空格、反斜杠、尾随斜杠和 Unicode；将 `$PSNativeCommandArgumentPassing` 纳入合同。仅有 AST 并不能证明外部程序实际收到相同 argv。[官方参数解析说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5)
- `.cmd/.bat`、cmd.exe 和脚本入口不能继承普通 exe 分析假设；原有已核验连接器 shim 使用独立合同，不借此扩大普通调用授权。
- 分别验收真实容器中的身份、路径、摘要、空设备与探针失败；macOS 的模拟 resolver 和交叉编译不算通过。
- 破坏性样例只解析或在隔离临时目录使用无害替身，不直接执行文档中的删除命令。

### 6.3 交付检查

按改动范围运行语法/命令/资源/授权的相关测试；迁移与最终接入时运行 `go test ./...`。HITL 如需公开协议变更，单独核对 Server、WebClient 和 Mailbox 的读写与重评，不把“内部对象合并”当作前端已支持。

同步更新 AGENTS.md 的包名事实、工具目录权限、AccessPolicy/HITL、Runtime 边界和 Windows 说明。发布记录分别列明：已解析但仍需审批、具备精确语义可按政策通行、未支持而回退的环境。未通过的目标环境保持未完成状态。

## 7. 评审阶段证据及限制

- 阅读当前原方案与 Bash AST、命令语义、Host 启动、路径策略和授权代码；原方案及 Mailbox 等已有未提交改动均保留。
- 使用临时 Go overlay 做只解析探针，确认原方案 §2.1 的输入被 Bash 解析为一个 echo 调用；探针通过，没有执行输入命令，仓库测试源码未改动。
- 用 Microsoft Learn 确认 Get-Command 自动模块导入及 PowerShell 原生参数传递的相关行为；这些是设计合同依据，不代表已完成项目内 Windows 验证。
- 评审阶段没有 Windows 端到端测试，没有运行破坏性命令，没有重新运行全仓测试或部署。第 2 节的问题包括当时的代码事实与对拟议架构的风险推导，分别以“当前”“若按方案实施”说明；实际实施进度见下节。

## 8. 实施记录：首批启动合同与方言保护

### 8.1 已实现范围

- S0：新增 `internal/accesspolicy/bash_baseline_test.go` 固定首批 Unix 行为基线，以决策及规则类别比较，避免保存机器相关路径 hash；补充三档方言回退、readonly、管理员 block、单次批准及独立叶子测试。完整命令清单与后续迁移基线仍需继续补齐。
- S1：新增 `internal/hostshell/tool.go` 的 ToolInvocation、ResolveTool 与 LaunchSpec。把工具启动规则从 tools 移入 hostshell，原 Terminal 入口及 Git Bash 开关不变，Unix 默认继续无 profile 的 Bash。Shell canonical 路径、文件内容 SHA-256、模板、实际参数、排序且按有效值去重的环境、canonical cwd 进入绑定；内容摘要用于绑定 Shell 版本，不启动 Shell 查询版本。
- 一次 Executor 调用仅取一次 run.env 快照，实际进程使用冻结的 Executable/Args/Env/CWD；启动前再次解析/摘要 Shell，变化时返回 `bash_launch_changed`，不消费旧批准。
- 精确 access/security 批准绑定完整启动调用；可复用路径规则额外绑定不含用户命令正文的启动合同，保留同合同下按路径复用的范围。过滤已批准叶子时保留启动绑定与方言状态。
- S2：已确认的系统 Bash 及受支持非交互模板使用原 Bash 语义分析；Windows PowerShell/cmd/MSYS、其他 Shell、登录/交互或带额外源码的模板、同名替代 Shell 使用整条未知调用合同，不再把其正文交给 Bash 安全分析作放行依据。
- 未支持语法时，ask_for_approval/auto_approve 强制单次人工；full_access 按 bash-complex-filesystem 配置决定。显式 block 始终保留；cwd 及 editing=false 的已知硬限制仍优先；严格命令模式直接拒绝未支持语法，预审不再请求无法执行的批准。Mailbox 代批不能因上级档位更高而放宽未知语法的人工要求。
- policy.shell 展示实际 Shell 路径、模板与方言；原因中也包含未支持启动形式，未输出环境值。已验证单条直接连接器使用原冻结 argv 合同，不依赖配置 Shell 的存在；没有为其他 Shell 调用增加连接器凭据。
- 新增 `internal/shellreview/dialect`、`types/LaunchBinding` 两个叶子包，仅奠定依赖边界；S3 的全目录迁移以及 S4–S8 尚未实现。

### 8.2 首批可见行为变化

| 场景 | 当前行为 |
| --- | --- |
| 系统 Bash + 默认工具模板 | 保持原命令语义和默认启动参数 |
| 配置 sh/zsh 等尚未接入的语法，或 Bash 登录/附加源码模板 | 受限两档要求本次人工批准；不按 Bash 猜测正文 |
| Windows Host 普通 Shell 调用 | 语法分析器接入前，受限两档不自动执行；目标环境端到端尚未验收 |
| 未知语法且严格命令模式 | 预审和执行均拒绝 |
| Shell、启动模板、环境或物理 cwd 改变 | 旧启动批准失效；原始命令字符串相同也不能复用 |
| 单条已核验直接连接器 | 不经过 Shell，保留原路径/摘要/argv/凭据合同 |

未知语法的人工授权只表示用户批准了该次完整调用，不证明正文没有文件、远端或破坏性行为；Host 任意代码内部访问仍不隔离。

### 8.3 验证与下一步

已补充启动模板迁移、同名 Shell 不执行探测、内容/环境/cwd 指纹、无效批准、直接连接器、组合要求单次消费、方言回退及 Mailbox 代批不能放宽人工要求的测试。2026-10-30 最终验证：

```bash
go test ./...
GOOS=windows GOARCH=amd64 go test -c -o /private/tmp/ap-hostshell-windows.test.exe ./internal/hostshell
GOOS=windows GOARCH=amd64 go test -c -o /private/tmp/ap-tools-windows.test.exe ./internal/tools
git diff --check
```

以上均通过，部分 Go 测试命中缓存。Windows 仅交叉编译，未在 Windows 执行测试；也未做本批真实容器端到端验收。未重启或部署运行实例。

下一批按 S3/S4 实施：建立中立 Invocation/Effects 合同，按命令拆迁现有分析器，统一注册及身份约束的有界包装展开。PowerShell AST 仍按 S5 提前接入，不等全部审批对象合并；真实 Windows 与容器验证未完成前保持相应状态为未验收。

### 8.4 复审发现（2026-10-30，针对 `5554545f`）

复审时在 macOS 上重跑 `go test ./internal/hostshell ./internal/accesspolicy ./internal/tools ./internal/llm`，全部通过。以下各项均由读代码得出，未在 Windows 上运行。

| # | 级别 | 发现 | 建议 |
| --- | --- | --- | --- |
| 1 | P2 | 未知方言时 `BashPlan.SecurityReview` 直接返回 Allow，`reviewBashScript` 也在 `bashast` 预检之前返回，控制字符与 Unicode 空白检查都不再运行。Windows 现在每条命令都靠人读审批卡片，ESC 序列、零宽或双向控制字符可以让卡片显示的内容与实际字节不一致。另外，零宽与双向控制字符（U+200B–U+200F、U+202A–U+202E、U+2066–U+2069）目前对 Bash 也不在检查范围内 | 把与语法无关的文本检查独立出来，对所有方言（含未知方言）执行，命中即硬阻断；归入 S2 的补丁，不等 S3 |
| 2 | P2 | `classifyToolShell` 要求 Shell 的 canonical 路径与内容摘要都等于 Platform 启动时冻结的值。apt 升级 bash、Homebrew 升级（Cellar 路径变化）后，所有 Bash 调用都转为未知方言，直到重启：ask/auto 每条命令都要人工审批。显式配置另一个 bash（如 `/usr/local/bin/bash`）也会一直走未知方言 | 方案 §8.2 第 9 项：调用时按启动 PATH 重新解析系统 Bash，比较 canonical 路径；摘要只进批准指纹 |
| 3 | P3 | 本轮规则批准的 RuleKey 追加了 `ContractFingerprint`，其中含 cwd 与完整环境。同一路径规则换个 cwd，或 run.env 变化后就要重新批准，比改动前收窄 | 方案 §8.2 第 8 项：规则只绑定 Shell 身份、模板与方言 |
| 4 | P3 | Shell 改为按工具环境的 PATH 解析（原先由 `exec.Command` 按 Platform 进程 PATH 解析），且 `hostenv.LookPath` 不接受 `./bash` 这类相对路径的 `shell-executable`，现在会返回 `bash_launch_unavailable`。Windows 上从商店安装的 `pwsh` 是应用执行别名（reparse point），`EvalSymlinks` 与读取内容是否成功未验证 | 相对路径要么文档写明不支持，要么在配置加载时拒绝；`pwsh` 别名列入 Windows 验收 |
| 5 | 说明 | Windows 上关闭 Shell 特性（`shell-features-enabled: false`）后，所有命令都被拒绝；以前简单的 PowerShell 命令可以按白名单执行 | 属于有意变化；在工具目录权限文档里写明 Windows 严格模式当前不可用 |

R1–R8 本次逐条核对，均成立，已并入方案。

### 8.5 S2 复审补丁（2026-10-30）

本批按 §8.4 修补代码，不推进 S3 或 PowerShell AST。相关进度如下：

| 复审项 | 本批处理 |
| --- | --- |
| 1：所有方言的原始文本检查 | 已新增 `shellreview/syntax.CheckText`，在未知方言回退、连接器投影和字符串裁剪之前检查；Bash 预检也复用同一入口。控制字符、Unicode 空白、零宽/双向及其他格式控制字符、无效 UTF-8 一律硬拒绝。普通空格、Tab、LF、CRLF 允许，孤立 CR 拒绝；引号与 heredoc 不提供豁免 |
| 2：系统 Bash 安装更新 | 已改为保存启动时的搜索路径，每次重新解析当前系统 Bash canonical 路径。文件原地替换和安装符号链接切换不取消语法支持；内容摘要仍使旧批准失效。工具环境的 PATH 不能改写系统参考路径 |
| 3：本轮路径规则的范围 | 新增 RuleFingerprint，额外绑定只包括 Shell canonical 路径、内容摘要、模板与方言。cwd/环境只参与精确启动批准；原路径规则的目标限制以及其他规则自身的调用限制保留 |
| 4：相对 Shell 路径与商店 pwsh | 配置 normalize 阶段拒绝相对 shell-executable，允许程序名或绝对路径；商店 pwsh 应用执行别名已列入原生 Windows 验收，尚未验证 |
| 5：Windows 严格模式 | 已在现行工具目录权限文档中说明：不支持的 Shell 语法在严格模式下拒绝，直接连接器例外仍沿用原合同 |

行为变化：此前允许的引号内全角空格、连接器 JSON 中的 Unicode 空白，现在也受统一文本检查约束。已将相关测试拆成普通中文载荷的正向用例与不可见/特殊空白载荷的拒绝用例，不因冻结 CLI 入口而跳过原始文本检查。

新增回归覆盖所有方言与三档、已有批准不能放宽文本硬拒绝、原始命令裁剪前检查、真实挂载连接器、安装原地替换/符号链接切换、PATH 阴影、同一绝对目标跨 cwd/环境复用及 Shell/目标变化不复用。Windows 应用执行别名、真实系统包升级及目标环境端到端行为仍需分别验收，模拟安装测试不等于现场升级验证。

本批最终验证：`go test ./...`、`git diff --check` 通过；`internal/tools` 与 `internal/config` 的 Windows amd64 测试交叉编译通过。部分测试命中缓存；没有执行原生 Windows 测试、重启或部署。当前工作区另有部署恢复相关改动，本批未修改那些文件。
