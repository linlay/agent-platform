# Windows 内置 Git Bash：实施进度

状态：**开发中，未完成端到端接入，不可发布，也不要在部署配置中开启。**

本次实现期间，工作区另一项任务持续修改 Host Bash、AccessPolicy 与 LLM 审批链路。
为避免覆盖其执行前复核、脚本来源和审批消费逻辑，Host 执行和路径安全接入暂缓。
本文件区分已实现部件和待完成能力，不替代最终功能文档。

## 已实现部件

- 相邻 `agent-platform-builtins/git-bash` 项目：独立包版本 `v1.1.0`；来源为 PortableGit
  `2.55.0.windows.5` Windows/amd64；原始上游版本、URL、SHA 与包版本分离。
  完整 source payload 保留 9,586 个文件；默认 build 校验来源后生成 `minimal-v1`
  精简发布树，当前 ZIP 为 2,978 个文件。裁剪沿用两轮候选规则，额外保留其中的
  许可证/版权文件；源 payload 数量不代表最终发布数量。
- 上游导入前验证 SHA；保留完整运行目录、DLL、配置、原始许可证、包版本清单。
  准备阶段完成可搬移 DLL 复制及虚拟设备目录创建，差异写入
  `platform-initialization.txt`；不复制准备机器的 hosts/services，也不执行安装器。
- 导入后的逐文件完整性清单、离线确定性 ZIP、同版本路径不可变校验；Shell/PowerShell
  同步脚本按 Windows/amd64 目标构建此组件，不在运行、普通构建或 release 时下载。
- `archive-tree` 固定输出 `libexec/git-bash/windows-amd64/`。Windows/amd64
  `StageCache` 缺少组件时失败，不受运行开关影响。实际完整包已通过独立临时 lock staging。
- 首次注册组件的本地 lock 引导逻辑；正式 lock 仍需原生 Windows、干净 Git commit、
  交互精确 `yes` 及稳定 dist 固化。macOS 没有写入正式 Windows target SHA。
- 配置解析 `bash.git-bash.enabled`，缺省 false；布尔值及未知字段严格校验。
  保留原 Shell 配置以供关闭后恢复。
- 公共 `internal/hostshell`：固定参数、受信任绝对路径、局部环境、路径转换辅助器；
  `internal/processgroup`：Windows 挂起启动、Job 分配、恢复及子进程树清理部件。
- Workspace Terminal 已接公共解析层，内部启动请求支持 args；Windows 继续 ConPTY。
  Host Bash **尚未接入**，因此目前不能承诺开关同时切换二者。
- `AP_GIT_BASH_EXE` 及 Shell 初始化/转换变量的配置与 run.env 保护。
- 相邻 `httpx` 升至 `v0.1.9`：Windows `from:shell` 固定使用注入入口和参数，
  无入口明确失败；嵌套 Shell 使用 Job Object；Unix `/bin/sh -lc` 保持不变。

## 待完成，均为发布阻断项

1. 合并稳定后的 Host Bash/审批入口：同一有效环境快照审核和执行，修订后重新审核，
   保留新加入的 authored-script 机制、精确审批消费、普通 pipe、tee 和 50ms 输出协议。
2. 接入 MSYS 路径权限检查：按实际可执行文件区分 MSYS 与原生参数；重定向单独解释；
   不可靠的文件访问不得自动放行。覆盖 readonly、KBASE、Skill、临时根与 junction。
   当前 cygpath 辅助器没有接入 AccessPolicy，不可视为安全适配已完成。
3. 补齐实际 Shell/原生文件参数规则的模型提示词、保护变量命令内修改检查、
   `.exe` 嵌入式代码识别及全部执行通道的环境隔离回归。
4. Windows 原生验收：无系统 Git、离线、中文/空格和搬移、Git/SSH、ConPTY 输入/缩放/
   Ctrl+C/关闭、超时及中断无残留、原生工具 argv/env/路径、跨盘/UNC/junction 权限。
   `httpx` 原生集成测试由 `AP_TEST_GIT_BASH_EXE` 显式启用，未设置时跳过。
5. 完成每个上游许可证对应的来源提供/分发审查；保留许可证和来源 URL 本身不等于
   所有再分发义务已经履行。
6. 同批完成 Platform、httpx 和 Windows builtin 正式 lock 发布；验收完成后才修改
   Windows 新安装模板默认值。当前未修改模板或任何用户真实配置。

## 当前验证范围

macOS 上通过：Git Bash 打包器、httpx 现有测试及 Unix 新回归；Platform 的
hostshell、builtins、terminal、agentconfig、runenv、prepare-local-builtins-lock
测试，以及新增 Git Bash 配置测试。实际 PortableGit 已校验导入并成功 staging。

Windows 交叉编译仅用于编译检查，不构成 Windows 原生验收。整个仓库存在并发改动，
没有宣称全仓测试通过。

## 本地产物记录

- 上游原始 SHA-256：`5aa8a20f6e9abb2c755f0e73c91c687701a46b309ad84a0ca6509380fa4ae290`。
- 包：相邻项目 `git-bash/dist/v1.1.0/git-bash_v1.1.0_windows_amd64.zip`。
- 当前包 SHA-256：`6217255a329beef15d95ad19be512973bff855db93241fa22d068339e066a8a8`。
- Windows 本地精简 ZIP 为 2,978 个文件、227 个目录条目、约 90.36 MiB。
  保留 Bash/Git、常用命令、SSH、LFS、凭据管理器及其运行依赖；不带编辑器、Perl、
  GPG、Tcl/Tk、Git GUI、mintty 和离线帮助。Office/PDF textconv 配置随转换器移除。
  重复 DLL 必须与保留的 bin 副本 SHA/大小一致，保留 PE 静态引用被移除 DLL 时构建失败。
- 新包已通过单元测试、真实 PE 静态依赖检查，以及中文/空格搬移路径下的 Bash、Git
  本地提交/clone/worktree、压缩解压冒烟测试；SSH、LFS、凭据管理器可启动。
  真实登录、网络及完整 ConPTY 验收仍未完成，不能据此视为全部发布阻断项已解决。
- 本次未更新 Platform cache 或 Desktop 最终包。需先执行 `scripts/sync-local-builtins.ps1`，
  再执行 Desktop 的 `scripts/build-builtin-services.ps1`；仅执行后者仍复用旧 cache。
  正式 lock promotion 继续遵守干净 Git commit 和原生 host 等既有约束。

继续实施前需先协调重叠文件的修改归属；不要覆盖、回滚或提交另一项任务的改动。

### 可选打包 Git Bash

macOS 原本不打包 Git Bash，保持现有流程且不增加排除标记。以下开关只控制 Windows/amd64 的 Git Bash 打包选择。

构建环境变量 `BUNDLE_GIT_BASH` 默认 `true`，仅接受 `true`/`false`（不区分大小写）。它独立于运行时 `bash.git-bash.enabled`，不写入 `.env` 或正式 builtin lock。

```powershell
$env:BUNDLE_GIT_BASH = "false"
# 已有完整 cache 时，可直接执行 Desktop 构建入口：
& C:\Project\zenmind\zenmind-desktop\scripts\build-builtin-services.ps1
# 或在 Platform 仓库仅生成 Platform 包：
make release ARCH=amd64
```

需要首次准备或更新其他 builtin 时，同一环境下执行 `scripts/sync-local-builtins.ps1`：关闭开关会跳过 Git Bash 来源复制、构建、临时 lock 解析与 staging，且不 promotion Git Bash 正式 lock 记录。其他 builtin 仍按既有流程处理。Shell 入口同样支持 `BUNDLE_GIT_BASH=false ./scripts/sync-local-builtins.sh`，也可用 `make release BUNDLE_GIT_BASH=false`。

release 关闭时直接从既有 cache 排除 Git Bash 文件、独立许可证/SBOM 目录及组件记录，不修改源 cache。生成的 `builtins.manifest.json` 使用 `gitBashExcluded: true` 明确记录主动排除；发布校验拒绝标记与组件冲突、残留 Git Bash 文件，以及默认开启却缺少组件的情况。sync 则会原子更新 cache 为本次选择的组件集；若重新开启后 cache 缺少 Git Bash，需先以 `true` 重新 sync。

恢复默认可用 `$env:BUNDLE_GIT_BASH = "true"` 或 `Remove-Item Env:BUNDLE_GIT_BASH`。不含 Git Bash 的包应保持运行时 `bash.git-bash.enabled: false`，使用已有 Shell 配置；错误开启会明确报组件缺失，不会自动切换到 PowerShell。当前默认运行配置无需改变。
