# Windows 内置 Git Bash

Windows Git Bash 尚未完成端到端接入，不能作为已完成的运行能力发布或在部署配置中开启。打包开关与运行开关相互独立。

## 当前支持范围

- `bash.git-bash.enabled` 缺省为 false，严格校验布尔值和未知字段；关闭时沿用原 Shell 配置。
- `internal/hostshell` 提供受信任绝对路径、固定启动参数、局部环境与路径转换；`internal/processgroup` 提供 Windows Job 子进程树管理。
- Workspace Terminal 已使用公共 Shell 解析层，Windows 继续使用 ConPTY。Host Bash 尚未接入该解析层，不能承诺开关同时切换二者。
- `AP_GIT_BASH_EXE` 及 Shell 初始化、转换变量受配置与 run.env 保护。
- builtin 同步脚本从相邻 `agent-platform-builtins/git-bash` 项目准备并校验 Windows/amd64 包，固定布局为 `libexec/git-bash/windows-amd64/`；运行和 release 不下载。包版本、来源与摘要以构建清单和正式 lock 为准。
- 正式 lock 更新仍要求原生 Windows、干净 Git commit、交互确认及稳定 dist 固化，交叉构建不能写入 Windows target SHA。

## 未完成的接入与验收

- Host Bash 执行与审批尚未接入公共 Shell 解析层；需保持审批和执行环境一致、执行前复验、精确批准消费及实时输出协议。
- MSYS 路径转换尚未接入 AccessPolicy；不能把 cygpath 辅助器视为 readonly、KBASE、Skill、临时根或 junction 权限适配已经完成。
- Shell/原生参数提示词、命令内保护变量修改检查、`.exe` 嵌入式代码识别和执行通道环境隔离尚未完成接入验收。
- Windows 原生验收仍需覆盖无系统 Git、离线、中文/空格路径、搬移、Git/SSH、ConPTY、取消与进程清理、跨盘/UNC/junction；交叉编译不代表原生验收。
- 发布还需要完成上游来源提供与许可证义务审查，以及 Platform、httpx、Windows builtin 正式 lock 的配套验证。保留许可证和来源 URL 不等于完成再分发审查。

### 可选打包 Git Bash

macOS 原本不打包 Git Bash，保持现有流程且不增加排除标记。以下开关只控制 Windows/amd64 的 Git Bash 打包选择。

构建环境变量 `BUNDLE_GIT_BASH` 默认 `true`，仅接受 `true`/`false`（不区分大小写）。它独立于运行时 `bash.git-bash.enabled`，不写入 `.env` 或正式 builtin lock。

```powershell
$env:BUNDLE_GIT_BASH = "false"
# 已有完整 cache 时，可直接执行 Desktop 构建入口：
& C:\Project\desktop\scripts\build-builtin-services.ps1
# 或在 Platform 仓库仅生成 Platform 包：
make release ARCH=amd64
```

需要首次准备或更新其他 builtin 时，同一环境下执行 `scripts/sync-local-builtins.ps1`：关闭开关会跳过 Git Bash 来源复制、构建、临时 lock 解析与 staging，且不 promotion Git Bash 正式 lock 记录。其他 builtin 仍按既有流程处理。Shell 入口同样支持 `BUNDLE_GIT_BASH=false ./scripts/sync-local-builtins.sh`，也可用 `make release BUNDLE_GIT_BASH=false`。

release 关闭时直接从既有 cache 排除 Git Bash 文件、独立许可证/SBOM 目录及组件记录，不修改源 cache。生成的 `builtins.manifest.json` 使用 `gitBashExcluded: true` 明确记录主动排除；发布校验拒绝标记与组件冲突、残留 Git Bash 文件，以及默认开启却缺少组件的情况。sync 则会原子更新 cache 为本次选择的组件集；若重新开启后 cache 缺少 Git Bash，需先以 `true` 重新 sync。

恢复默认可用 `$env:BUNDLE_GIT_BASH = "true"` 或 `Remove-Item Env:BUNDLE_GIT_BASH`。不含 Git Bash 的包应保持运行时 `bash.git-bash.enabled: false`，使用已有 Shell 配置；错误开启会明确报组件缺失，不会自动切换到 PowerShell。当前默认运行配置无需改变。
