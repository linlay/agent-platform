# ACP 独立部署

Platform 可在 `standalone` 模式直接管理同机 ACP 插件。WebClient 沿用现有项目创建、聊天、配置、审批和历史接口，不新增安装入口，不修改 AGW。浏览器可以在另一台电脑；ACP stdio、项目目录、工具和凭据都在 Platform 所在机器。

## 启动与安装

管理员使用与 Platform 相同的操作系统账号执行命令。以下路径为部署示例，按实际位置替换；保留部署已有的认证设置。

```bash
AP_RUNTIME_DIR=/srv/zenmind/runtime ./agent-platform \
  --config-dir /srv/zenmind/config \
  --runtime-mode standalone \
  --agent-engine-node /opt/zenmind/node/bin/node \
  --port 8080
```

`--agent-engine-node` 是显式绝对路径，只用于独立 ACP 引擎；不会从 PATH 搜索，也不会运行 npm 安装。实际架构和 Node 版本必须匹配插件 Manifest 的 `host-managed` 依赖。现有 Codex/Claude 候选包要求 **24.19.0**，不能用任意 Node 22/24 代替；若服务器采用其他版本，须重新验证组合并构建对应 Manifest 的插件。只使用包内原生 executable 插件时可省略此参数。不要直接覆盖正在使用的 Node 二进制。

Platform 启动后，在另一终端执行：

```bash
./agent-platform agent-engine-manage list --runtime-dir /srv/zenmind/runtime
./agent-platform agent-engine-manage import --runtime-dir /srv/zenmind/runtime /srv/packages/zenmind-agent-codex-linux-arm64.zip
./agent-platform agent-engine-manage import --runtime-dir /srv/zenmind/runtime /srv/packages/zenmind-agent-claude-linux-arm64.zip
./agent-platform agent-engine-manage import --runtime-dir /srv/zenmind/runtime --overwrite /srv/packages/zenmind-agent-codex-linux-arm64.zip
./agent-platform agent-engine-manage disable --runtime-dir /srv/zenmind/runtime --id zenmind-agent-codex
./agent-platform agent-engine-manage enable --runtime-dir /srv/zenmind/runtime --id zenmind-agent-codex
./agent-platform agent-engine-manage uninstall --runtime-dir /srv/zenmind/runtime --id zenmind-agent-codex
```

ZIP 必须与服务器 OS/架构匹配。命令选项放在 ZIP 路径之前。首次导入默认启用；重复导入需显式 `--overwrite`，更新保留启用/禁用意图。`list` 输出引擎目录和排空状态。`--runtime-dir` 是 `ru-agents` 的父目录；默认布局即 `AP_RUNTIME_DIR`，采用自定义目录布局时以实际路径为准。

导入完成后，WebClient 现有创建项目窗口即可选择外部引擎，项目目录选服务器上的目录。无需重启 Platform；安装变更沿用现有目录失效通知。没有可用引擎时，standalone 的项目提示会引导联系服务器管理员。

## 安装事务与生命周期

`internal/agentengine` 复用现有 Manifest API 2、交付记录、摘要、不可变快照、候选验证和版本租约；新入口只负责本机安装事务与持久启用意图。归档不运行安装脚本；路径穿越、链接、特殊文件、异常体积、平台/依赖不匹配拒绝导入。候选验证失败不替换当前安装状态。

安装状态位于 `<runtime>/agent-engine-installations/`，其中 `installed.json` 保存完整已提交快照，`data/agent-engines/` 保存交付目录。启动重新校验并发布目录；损坏或缺失依赖明确使启动失败，不静默丢弃安装状态。旧版本交付保留到卸载，运行快照继续由已有租约回收；卸载保留 profile 和聊天绑定，以便同身份重新安装后显式恢复。

更新仅影响目标插件，新会话采用新快照；活动会话仍使用旧快照直到排空。禁用/卸载先停止目标插件的新准入，存在未释放租约时返回 `engine_busy`，已保存禁用意图。等待完成/取消及进程收尾后重试同一命令；空闲会话的异步退出也可能短暂返回 busy。另一插件不被强停。

共享 Node 升级是管理员协调操作：先禁用使用该 Node 的全部引擎，等待各项排空并重试成功；备份安装状态和 profile，卸载旧插件（profile/聊天保留），再停止 Platform。以新的 Node 绝对路径启动，导入与之匹配的插件，再恢复原会话。其他引擎在排空前不会被强停。故障时按相同流程恢复旧 Node 与旧插件包。不提供热替换 Node 的管理服务，也不能在线修改快照或放宽版本校验。保留不匹配的旧安装直接启动会被依赖校验拒绝，因此不能省略维护窗口的卸载/重装步骤。跨 Node 的完整部署升级流程仍待产品验收。

## 管理与认证边界

`management.json` 是仅部署账号可读的本机管理凭据，随 Platform 进程重建；命令自动读取，不需要复制 Token 到参数。管理接口 `/api/local/agent-plugins` 仅接受 loopback、专用 secret/host/epoch，拒绝浏览器 Origin、Sec-Fetch-Site、远端来源和普通应用 Token。它是 CLI 的本机通道，不是 WebClient 安装 API。一个 runtime 只能由一个独立安装管理实例持有；Desktop bootstrap 与 standalone 不可混用。

业务用户继续使用现有 Platform 认证和项目权限。凭据复用读取的是 **服务器执行账号** 的配置，具体支持范围由插件负责：Codex/Claude 沿用各自已验证的本机文件配置复用，不会读取访问者电脑上的登录态。交互认证复用现有会话认证和受控终端装配；浏览器 OAuth 若回调服务器 `localhost`，远程用户须使用厂商支持的设备流或正确转发回调，不能把访问者电脑的 localhost 当作服务器。本轮不新增网关配置 UI，也不承诺任意 OAuth 远端登录闭环。

首版为单个可信部署边界；多个 WebClient 用户共用服务器的插件 profile 和 OS 执行身份，不是每用户独立厂商账号托管。管理凭据与安装目录纳入现有受保护路径；这不等于对受信插件进程实施完整 OS 沙箱。

## 平台与验证状态（2026-11-04）

- 已通过：Go 安装/更新/排空/失败回滚/重启恢复及并发归属测试；本机管理权限；App/CLI 装配；既有 ACP runtime 完整回归；共享契约校验和插件 `npm run verify`。
- 已通过：macOS 真实 Platform 进程 + 本机 CLI + ACP 测试程序（包内 executable 与显式 Node 两种入口），安装、现有 API 创建项目、HTTP Query、进程重启后同会话恢复、禁用/启用/卸载。
- 已通过：无网络 Linux arm64 容器内原生 ACP 测试程序的安装、正式 Query、目录与同会话恢复。它验证 Linux 宿主链路，不代表 Codex/Claude 模型调用已验收。
- Linux 构建目标已补 `linux-amd64` / `linux-arm64`，Codex/Claude 0.1.1 四份候选 ZIP 已构建成功（位于插件仓库 `build/<plugin-id>/0.1.1/<target>/`），各插件自行声明固定原生依赖；Claude 当前使用 glibc 包，Alpine/musl 不在本轮范围。
- 待实测：独立部署下真实 Codex/Claude 的账号、模型、文件审批、取消、异常强杀、后台子任务、双活动升级；远程 WebClient 浏览器闭环；Linux 双插件目标包使用匹配 Node 的真实产品运行；Windows 原生独立管理；跨 Node 升级。现有 Desktop 验收结果不替代这些组合。

本轮不改 AGW、不加 WebClient 安装页面，不恢复 YAML HTTP Bridge 或固定插件端口；市场下载、多人隔离与容器部署编排另列范围。

### 无认证开发环境的首次会话准备修复（2026-11-04）

`auth.enabled=false` 时普通 Query 没有登录主体；ACP 首次 prepare 曾强制要求主体，导致已注册引擎在浏览器显示不可用，接口实际为 403 `chat_access_denied`。现按普通 Query 的来源规则预留无认证会话；有主体的会话仍绑定原主体，匿名/其他主体不能访问私有会话，启用认证后缺登录仍返回 401。补齐真实 prepare 接口回归，不调整 AGW、插件注册或前端权限逻辑。

修复后已重启隔离开发 Platform，并经真实 WebClient 验证双插件新会话自动准备：Codex 返回 200 并显示 Mode/Model/Reasoning effort，Claude 返回 200 并显示 Mode/Model/Effort。该验证不发送模型消息，不替代完整聊天/审批产品验收。

### 新建空会话持续 engine_busy（2026-11-04）

本次根因是 Supervisor 默认最多 4 个 ACP 进程，已准备但从未发送消息的 Chat 长期占用进程；现场四个旧 Chat 都可 prepare，新的 Chat 全部返回 409，其中三个只是空准备会话。不是插件未注册，也不是认证失败。

修复在现有 Sessions 层：新准备遇到容量上限时，仅释放一个无操作、无在途交互、尚未发送消息且非子任务的准备会话，再尝试启动。保留持久绑定与确认配置；恢复不支持时仅对这种空会话重新创建，并重新应用配置。已提交/有历史上下文的会话不参与自动回收，活动配置/Prompt/审批不被打断。不改 AGW、插件协议或 UI，不提高进程上限。

验收通过：12 个空会话依次准备不再在第 5 个失败；回收后确认配置保留；配置操作中及四个有上下文会话均保持原实例。新增用例 race 连续 3 次通过，ACP 全包通过。重启隔离开发 Platform 后，真实 Codex/Claude 交替创建 6 个空会话和重新进入已回收空会话均 HTTP 200，进程不超过 4，原有消息的 Codex 会话 preparationId 保持不变；WebClient 自动准备显示配置，无模型消息发送。

限制仍在：如果 4 个名额都由已执行会话或活动操作占用，新会话仍会返回 engine_busy；这次没有开放有上下文会话的通用自动回收。对应策略与真实后台工作恢复仍须单独验证。证据位于 `build/qa/acp-standalone/acp-busy-*.log`。
