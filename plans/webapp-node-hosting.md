# Platform 容器内集成 Node.js，托管 Desktop WebApp

## 背景

Desktop 在本机托管 WebApp：前端静态资源加可选的 Node 后端，由 Electron 主进程启动、经本地网关分发，并通过 `/__desktop/*` SDK 调用 Desktop 能力（梳理见 `/workspace/docs/zenmind/desktop-webapp-hosting.md`）。云端以 compose 方式部署 Platform 时，没有 Desktop 可以承载这些 WebApp。

Frank 希望 Platform 镜像内置 Node 运行时，在 compose 模式下像 Desktop 一样直接启动和管理 WebApp，前端和后端都支持；已明确不采用「后端放独立容器」。

本文记录现状、已定需求、问题清单、各项的可选方案与利弊、实测记录、待定问题、改动面和测试。本文不给结论或推荐。行号以写作时的 main（`5aef3bea`）为准；Desktop 侧行号以 zenmind-desktop `d65eaa23` 为准。

## 现状

### 部署与加固

- compose：`scripts/release-assets/image/compose.yml`
  - `init: true`（:10）、`user: "10001:10001"`（:13）、`read_only: true`（:14）
  - `cap_drop: ALL`（:15-16）、`no-new-privileges`（:17-18）
  - 只发布 `AP_BIND_ADDRESS:AP_PORT → 容器内 8080`（:20-21）
  - `env_file: .env`（:22-23），会进入 Platform 进程环境
  - tmpfs：`/tmp`（1777，exec）、`/home/agent`（0700，exec）（:26-28）
  - bind 挂载：`/opt/configs` 只读，`/opt/runtime`、`/opt/workspaces` 可写（:29-39）
  - 整容器限额：mem/cpus/pids（:40-42）
- 默认值 `scripts/release-assets/image/env.example`：4g、2 CPU、1024 pids、tmp 1g、home 512m，`AP_BIND_ADDRESS=127.0.0.1`，以及 `AP_CHAT_RESOURCE_TICKET_SECRET`。
- `scripts/release-assets/image/DEPLOY.md`：数据目录需对 UID 10001 可写（:50）、需要 flock（:52）、对外由本机 TLS 反代转发（:61）。
- 镜像 `Dockerfile`
  - 运行时基于 Debian，只装 ca-certificates（:38-39）
  - `chown -R 10001:10001 /opt`（:42）；二进制和 builtins 均 `--chown=10001`（:46,48-49）
  - `USER 10001:10001`（:64）
  - **不含 Node**
- Ingress：`scripts/release-assets/ingress-nginx/agent-platform-ingress.conf.template:10-59`
  - 按 `$host` 映射 upstream，未知 Host 返回 444
  - 重写转发头，支持 WS 升级，关闭缓冲，3600s 超时，100MB 请求体
  - 新增站点：`ap-ingress.sh add <name> <port>`（`INGRESS.md:42-60`）
- 部署设计：`docs/云端部署与持续任务架构设计.md`
  - §5.1：一个 owner 一个实例
  - :207：同 UID 代码无法保证读不到凭证
  - :231-232：子域名与路径前缀
  - §6：泛域名证书只覆盖一级
  - :240：转发头
  - :244：不可信内容放独立内容域名

### Platform 侧相关代码

- 运行模式只有 `standalone|desktop`：`internal/config/config.go:88-99`，`cmd/agent-platform/main.go:165`。
- 目录：`internal/config/config_defaults.go`
  - `StateDir = <runtime>/.state`（:21）
  - `AgentsDir`（:25）、`AutomationsDir`（:28）
  - `Auth.Enabled` 默认 true（:122-123）
- 热加载：AgentsDir 由 watcher 热加载（`docs/智能体配置说明.md:329`），automations 同样（`docs/自动化.md:76`）。写入这些目录会直接改变 Platform 行为。
- 受保护路径：`internal/accesspolicy/protected.go:19-35` 只覆盖 StateDir、身份文件、RU 预览目录和 Desktop secrets。它只在工具和 bash 分析里生效，不是 OS 级保护，对 Platform 拉起的任意子进程无效。
- 子进程环境：
  - 终端会话传入完整 `os.Environ()`：`internal/server/ws_terminal_session.go:51`
  - Bash 工具和连接器 CLI 用 `internal/shellenv` 白名单：`internal/tools/tool_bash.go:469`、`internal/connectorauth/cli.go:235`
  - stdio MCP 默认继承 `os.Environ()`；`IsolatedEnvironment` 时只保留 PATH/TMP/LANG 等：`internal/mcp/client.go:468-476`
  - kbx、memoryworker 同样继承：`internal/kbx/client.go:85-87`、`internal/memoryworker/client.go:117-118`
- 进程组：只有连接器 CLI 用了 `Setpgid` 加 `kill(-pgid, SIGKILL)`：`internal/connectorauth/process_unix.go:12-17`。`internal/processgroup/run_other.go` 在 unix 上只是 `cmd.Run()`。PTY 见 `internal/terminal/pty_unix.go:20-35`。stdio MCP 交给 SDK 的 `CommandTransport`（`internal/mcp/client.go:504-507`）。
- 没有长驻子进程的守护、重启框架。kbx 和 memoryworker 都是按次调用。
- 没有反向代理：仓库里没有 `httputil.ReverseProxy`。
- Container Hub 只有会话的 create/execute/stop：`internal/sandbox/client.go:51-93`，`docs/运行时和沙箱.md`。
- 鉴权：
  - `internal/server/server_auth.go:44`（withPrincipal）、:48（forceAuth 路由）、:71（接受 `access_token` cookie）
  - JWT 校验：`internal/server/security.go:93-140`
  - 资源票据（HS256）：`internal/server/security.go:280`
- 路由：`internal/server/server_routes.go:209-367`。`/api/admin/*`（:215 起）没有角色检查。
- WS：`internal/ws/handler.go:37` 的 CheckOrigin 恒为 true；:210-228 只从 bearer 子协议、Authorization 头或 `?token` 取 token，不读 cookie。
- 连接器授权：`docs/连接器执行协议.md`
  - :5「Platform 不接收 appId，不建立应用模型」
  - :11-19 cxg_ 授权（scope=app+deviceId，15 分钟，内存保存）
  - :49 部署级凭证
- `docs/连接器共享包与Desktop迁移.md:38`：Platform 不负责 WebApp 安装、打包、发布。

### Desktop 侧实现（可参照）

路径前缀为 `zenmind-desktop/src/main/modules/webs/webapps/`。

- 版本：Electron 42.11.4（`zenmind-desktop/package.json:83`），自带 Node 24.19.0（releases.electronjs.org）。
- 启动器 `launchers.ts`
  - :19-41 继承环境变量白名单
  - :135-159 注入 HOST/PORT/桥接地址和 token
  - :177-187 spawn，非 Windows 下 detached 独立进程组
  - :191-214 `electron-node` 用 `process.execPath` 加 `ELECTRON_RUN_AS_NODE=1`
- 端口与健康：`runtime-health.ts`
  - :34-39 先预留端口
  - :68-92 健康检查
- 生命周期：`runtime.ts:113-162,258-453`。只有手动 restart（:630），没有自动重启。
- 日志与停止：`runtime-process.ts`
  - :15-26 日志
  - :62-104 对进程组先 SIGTERM、超时再 SIGKILL
- 网关：`gateway.ts:574-670` 分发（含 WS）；:26-30 保留 `/__desktop/` 前缀。
- SDK：`bridge.mjs:1-4` 使用绝对路径 `/__desktop`。
- 能力与令牌：`capability-policy.ts:8-25`、`action-tokens.ts`。
- Manifest：`target` 里没有 linux（`webapp-manifest.ts:341`）。
- 构建约定：`zenmind-env/skills-center/webapp-builder/references/build-and-convert.md:51,161,164`。资源用相对 base，bridge 从绝对路径 `/__desktop/bridge.js` 引入。

## 目标功能（已定需求）

1. Platform 镜像内置 Node 运行时。
2. compose 模式下由 Platform 进程直接启动和管理 WebApp，包括安装、启停、健康检查、日志、随 Platform 重启恢复。
3. 同时支持前端静态资源和 Node 后端。前端由 Platform 提供；后端由 Platform 拉起，经 Platform 反代（含 WS）。
4. 不采用「后端放独立容器」。所有 WebApp 进程和 Platform 在同一个容器里。
5. 尽量兼容 Desktop 的 WebApp 包格式和 `/__desktop/*` SDK 语义。

## 问题清单

- **Q1 Node 版本与体积**：选哪条 LTS 线；和 Desktop 的 Node 24 是否对齐；镜像增量多大。
- **Q2 依赖与只读根文件系统**：node_modules 来源；原生模块的平台和 ABI；离线安装；可写目录放哪。
- **Q3 进程托管**：spawn、端口、健康、重启、日志、停止（含 setsid 逃逸的后代）、Platform 重启后恢复。Platform 现在没有对应框架。
- **Q4 前端与路由**：静态资源；反代 backendPrefixes 与 WS；和 nginx 的配合；SDK 的绝对路径要求整站 origin；域名格式受泛域名证书限制。
- **Q5 同容器隔离**：现行加固下所有子进程都是 UID 10001。
  - 子进程默认能读写 `/opt/runtime` 全部内容，包括 .state、身份文件、AgentsDir、automations（热加载）。
  - 子进程能读同 UID 进程的 `/proc/<pid>/environ`，而 Platform 环境里有 env_file 的密钥（实测）。
  - 子进程不能建子 cgroup。
  - `PlatformProtectedPaths` 对子进程无效。
- **Q6 认证与 SDK**：后端 token 怎么下发；后端怎么调 Platform；`/api/admin/*` 无角色检查；WS 不校验 Origin；cookie 作用域。
- **Q7 开关前提**：依赖主机内核特性（Landlock ABI）时，不满足怎么办。

## 各项可选方案与利弊

### 1. 镜像：Node 版本与体积

| 选项 | 利 | 弊 |
|---|---|---|
| Node 24 LTS（v24.22.2） | 与 Desktop 的 Electron 42 自带 Node 24.19.0 同主版本，应用行为一致；原生模块 ABI 137 | 权限模型没有网络开关 |
| Node 26 LTS（v26.13.0） | 权限模型有 `--allow-net`（v25 起） | 和 Desktop 主版本不一致；`--allow-net` 只能全开或全关，监听端口也要开，对服务端应用意义有限 |
| Node 22 LTS（v22.23.4） | 更老、更稳 | 和 Desktop 差两个大版本 |

- 体积（实测 v24.22.2 linux-x64）：下载 tar.xz 32MB，解压 208MB。
  - `bin/node` 122MB（strip 后 104MB）
  - `lib/node_modules`（npm/corepack）19MB
  - `include` 67MB
  - 只装运行时、去掉 include：增量约 122–141MB（含 npm 时取上限）；再 strip 可以更小
  - v26 的 linux-x64 tar.xz 是 34MB，解压后未实测
- 架构：官方有 glibc 的 linux-x64/arm64，Debian 运行镜像可以直接用（ldd 只依赖 libc/libstdc++/libm/libgcc_s）。
- 安装位置：/opt/node 归 root 只读，需要从 `Dockerfile:42` 的 `chown -R /opt` 里排除。否则在 read_only 之外的场景，UID 10001 可以改写运行时。
- 是否带 npm：
  - 不带：体积小，运行期也不能现装依赖
  - 带：多 19MB，但可以支持安装时 `npm ci`（见下节）

### 2. 依赖与可写目录

| 选项 | 利 | 弊 |
|---|---|---|
| A. 应用包自带 node_modules（构建期 `npm ci`） | 安装时离线、确定；不执行 npm 生命周期脚本 | 包更大；原生模块必须在构建期按目标平台编出 |
| B. 安装时由 Platform 执行 `npm ci --ignore-scripts` | 包小 | 需要出网和 npm 缓存目录；不跑脚本时部分原生模块不能用；跑脚本就要执行第三方代码 |
| C. 只允许纯 JS 依赖 | 最简单，没有 ABI 问题 | 限制应用能力 |

- 原生模块：安装时扫描 `.node` 文件，核对平台（linux、glibc、架构）和 ABI。Electron 的 ABI 和 Node 的不同，为 Desktop 编的 `.node` 不能直接用（细节待确认）。
- Manifest 的 `target` 需要加上 linux-x64/arm64，`runtime: electron-node` 在这边映射成 node（`webapp-manifest.ts:341`、`launchers.ts:191-214`）。
- 可写目录（都在可写的 bind 挂载 `/opt/runtime` 下，`compose.yml:34-36`），具体命名待定：
  - 代码：`/opt/runtime/webapps/<id>/`
  - 数据：`/opt/runtime/webapp-data/<id>/`
  - 状态和日志：`/opt/runtime/.state/webapps/`
  - 临时目录：`/tmp/webapps/<id>`（tmpfs，`compose.yml:27`）
  - npm 缓存（选项 B）：`/home/agent` tmpfs 或 `/tmp`

### 3. 进程托管

- **可以照搬 Desktop 的**：
  - 环境变量白名单：`launchers.ts:19-41`
  - HOST/PORT 注入：`:135-159`
  - 进程组 SIGTERM→SIGKILL：`runtime-process.ts:62-104`
  - 健康检查语义：`runtime-health.ts:68-92`
  - 网关分发逻辑：`gateway.ts:574-670`
- **可以复用的 Platform 代码**：
  - `Setpgid` 加杀进程组：`internal/connectorauth/process_unix.go:12-17`
  - 环境变量白名单：`internal/shellenv`；隔离环境的写法：`internal/mcp/client.go:469-476`
  - PTY 与进程组：`internal/terminal/pty_unix.go`
  - 这些都不负责长期守护
- **需要新增的**：

| 方面 | 选项 | 说明 |
|---|---|---|
| 端口 | a. Platform 在 127.0.0.1:0 上监听拿到空闲端口后关闭，再交给子进程（同 `runtime-health.ts:34-39`） | 有短暂竞争窗口 |
| 端口 | b. 从固定区段分配，冲突就重试 | — |
| 端口 | c. 把已监听的 fd 传给子进程 | 需要应用支持 |
| 健康 | HTTP 路径或 TCP 连通，带启动超时 | Desktop 的超时和间隔可以照搬 |
| 重启 | a. 不自动重启（同 Desktop `runtime.ts:630`） | — |
| 重启 | b. 按退出码退避重启，超过次数标 failed | — |
| 日志 | a. stdout/stderr 写文件并按大小轮转 | — |
| 日志 | b. 进容器日志（`compose.yml:43-47`） | 会和 Platform 日志混在一起 |
| 停止 | a. 只杀进程组 | setsid 的后代会逃逸（实测） |
| 停止 | b. Platform 设 `PR_SET_CHILD_SUBREAPER`，停止时杀进程组并回收所有后代 | 实测孙进程 setsid 后会挂回 Platform |
| Platform 重启后恢复 | a. 子进程设 Pdeathsig，Platform 起来后按期望状态重新拉起 | — |
| Platform 重启后恢复 | b. 子进程脱离 Platform 继续运行，Platform 重连 | 与容器内 tini 和进程回收的配合复杂（`compose.yml:10`） |

### 4. 前端、反代与子域名

- 静态资源由 Platform 的 Go 代码直接提供（SPA 回退、缓存头）。
- 反代：新增 `httputil.ReverseProxy` 加 WS 升级；`/__desktop/*` 保留给 SDK（`gateway.ts:26-30`）。
- 分发方式：

| 选项 | 利 | 弊 |
|---|---|---|
| 按 Host（每个应用一个 origin） | 和 `/__desktop/bridge.js` 的绝对路径兼容（`build-and-convert.md:164`）；应用之间、应用和 Platform 之间按浏览器同源策略隔离 | 要配 DNS、证书和 nginx |
| 按路径前缀 `/apps/<id>/` | 不用改域名 | 绝对路径 `/__desktop` 和 `/` 下的资源会失效；所有应用和 Platform 同 origin，能读 Platform 的 cookie 和 localStorage（`server_auth.go:71`） |

- 子域名格式（受「泛域名只覆盖一级」限制，`云端部署与持续任务架构设计.md` §6）：

| 选项 | 说明 |
|---|---|
| `<app>.<inst>.域名` | 两级，要逐个签证书，或用 DNS-01 签 `*.<inst>.域名` |
| `<app>--<inst>.域名` | 一张泛域名证书就够 |
| 独立内容域名 `*.apps.域名` 或另一个注册域名 | 和 Platform 的 cookie 彻底分开（同文 :244） |

- nginx：在 map 里给应用域名指向同一个 AP_PORT，由 Platform 按 `Host` 或 `X-Forwarded-Host` 分发。模板已经具备 WS、超时和缓冲设置（`agent-platform-ingress.conf.template:10-59`）。可以复用 `ap-ingress.sh`（`INGRESS.md:42-60`），也可以加泛域名 server_name。

### 5. 同容器隔离（逐项）

前提：当前加固是 `user 10001`、`read_only`、`cap_drop ALL`、`no-new-privileges`，Docker 默认 seccomp（`compose.yml:13-18`）。

| 项目 | 现行加固下能否做到 | 前提或代价 | 依据 |
|---|---|---|---|
| 独立 UID | 不能。没有权限时 setresuid 返回 EPERM | 要以 root 启动容器并加 SETUID/SETGID（no-new-privileges 不拦用权限切 UID）。非 root 用户加 cap_add 不会进有效权限集（Docker 不设 ambient，未实测）。代价：Platform 或一个辅助进程以 root 运行，被攻破后影响更大；数据目录属主和权限要改（`DEPLOY.md:50`） | 实测加文档 |
| 文件权限保护 /opt/runtime、.state、注册表、身份文件、workspaces、AgentsDir、automations | 同 UID 下不能 | 只在独立 UID 时才有意义；Landlock 可以替代 | `compose.yml:34-39`，`protected.go:19-35` |
| 环境变量白名单 | 能 | 子进程不继承 env_file，只给白名单加应用专用变量 | `launchers.ts:19-41`，`shellenv` |
| /proc/pid/environ 读取 | 同 UID 默认能读（实测读到了测试密钥） | Platform 自己设 `PR_SET_DUMPABLE=0` 后，environ 变成 root 0400，读取被拒（实测）。exec 会恢复子进程的 dumpable，子进程之间要靠 Landlock 作用域隔离 | 实测 |
| cgroup 子组 | 不能（cgroup 目录归 root，实测） | 要可写的 cgroupfs 或 cgroupns 加委派，需要放宽加固。不放宽时只有整容器限额（`compose.yml:40-42`） | 实测 |
| ulimit / prlimit | 能（实测 `--nproc=50 --nofile=256`） | NPROC 按 UID 计，和 Platform、智能体共用，不可靠。RLIMIT_AS 和 V8 的大块虚拟内存预留冲突（待确认）。内存可以用 `--max-old-space-size`，加 Platform 监控 RSS 后杀进程 | 实测加待确认 |
| OOM 优先级 | 子进程可以无权限调高自己的 oom_score_adj，让整容器 OOM 时先杀应用 | — | 未实测，依据 Linux 语义 |
| 只监听本机 | 部分能 | Landlock 网络规则能限制 bind 的端口，不限地址。容器只发布 8080（`compose.yml:21`），绑在 0.0.0.0 上的其他端口从容器外也到不了 | 实测 |
| 出网限制 | 部分能 | Landlock 网络规则（ABI4+）只按端口限 TCP connect，不限地址；UDP 不受控（ABI ≤6）。按地址限要 netns 或 iptables，需要放宽加固 | 实测加文档 |
| 受保护路径（OS 级） | 能，用 Landlock 文件白名单 | 内核要 ≥5.13；Platform 自带启动器施加规则 | 实测 |
| 受保护路径（智能体侧） | 能 | `protected.go` 要加 webapps 代码和数据目录，以及 AgentsDir、automations 是否纳入 | `protected.go:19-35` |
| Node `--permission` | 能（实测 v22.20） | 拦得住：白名单外的文件读取、child_process、addon（需 `--allow-addons`）。Node 24 不限网络。Node 文档的原话（是否「不防恶意代码」）待确认；可以作为额外一层 | 实测 |
| Landlock 作用域（ABI6） | 能（实测） | 受限进程读不到域外进程的 environ；信号和抽象 unix socket 的隔离按 ABI6 语义。内核约 ≥6.12（待确认） | 实测 |
| seccomp（子进程额外过滤） | 能，不需要权限，配合 no-new-privileges 施加 | 要维护系统调用清单 | 未实测 |
| bwrap / user namespace | box 上能用；Docker 默认 seccomp 在没有 CAP_SYS_ADMIN 时会拦命名空间相关的 clone/unshare | 要换自定义 seccomp profile 或 unconfined，等于放宽加固 | 实测（box）加文档；Docker 内未实测 |

几种组合的对比（中性列出）：

| 组合 | 加固要不要动 | 能拦住 | 拦不住 |
|---|---|---|---|
| C1：同 UID + 环境变量白名单 + Platform dumpable=0 + 进程回收者 | 不动 | 子进程读 Platform 的 environ | 读写 /opt/runtime 全部内容（含智能体和自动化热加载目录）、出网、资源争抢 |
| C2：C1 + Landlock（文件、TCP 端口、ABI6 作用域）+ prlimit + oom_score_adj + Node `--permission` | 不动；依赖主机内核 | 读写 Platform 状态、身份、注册表、智能体、自动化目录；读其他进程的 environ 和内存；绑定其他端口；开子进程（Node 层） | 对已放行端口的任意地址出网、UDP 出网、CPU/内存/pids 争抢（只能事后监控）、内核漏洞、用自己的 token 合法调用 Platform API |
| C3：C2 + 独立 UID（root 启动后降权） | 放宽 user/cap | 再多一层 UID 级的文件和信号隔离 | 出网和资源同 C2；Platform 侧以 root 运行带来新风险 |
| C4：C2 + 自定义 seccomp 或 bwrap/netns | 放宽 seccomp | 按地址限出网、更强的隔离 | 加固放宽，部署更复杂 |

无论哪种组合，Platform 里智能体的 bash 都不受应用的 Landlock 约束，以同 UID 仍能读应用的数据和 token。这和现在智能体能读 `/opt/runtime` 的信任模型一致（`云端部署与持续任务架构设计.md:207`）。

启动器的形态（待定）：
- a. Platform 自带 `webapp-exec` 子命令：Platform 重新执行自身，施加 prlimit、Landlock、oom_score_adj 后 execve node。Go 多线程下 Landlock 要对所有线程生效，或在单线程的子进程里施加；go-landlock 库能否采用待确认。
- b. 单独打一个 C 小程序进镜像。

### 6. 认证、开关、SDK（相对 Desktop 的变化）

只列一个容器带来的变化，其余沿用 Desktop 语义（`capability-policy.ts:8-25`、`action-tokens.ts`）。

- **页面鉴权**：浏览器访问应用子域名时如何证明是 owner。
  - 可选：Platform 的 JWT 加应用域专用短期 cookie；或从 Platform 页面跳转换票（复用资源票据 `security.go:280` 的思路）。
  - Platform 的 `access_token` cookie 不能设成父域（`server_auth.go:71`）。
- **后端 token 下发**，选项：
  - a. 启动时注入环境变量（同 Desktop `launchers.ts:135-159`）
  - b. 通过继承的 fd 或管道交给子进程，读完即关
  - 两种在 Landlock 作用域下都防得住其他应用；都防不住同 UID 的智能体 bash
- **后端调 Platform**：走 `127.0.0.1:8080`（容器内端口），Landlock 只放行这个端口。
  - 可选入口：新的内部路径前缀（只认 webapp token，并做 scope 校验）；或复用 `/api/desktop/*` 的 forceAuth 机制（`server_auth.go:48`）
  - `/api/admin/*` 没有角色检查（`server_routes.go:215` 起），webapp token 能否访问需要明确
- **WS**：`ws/handler.go:37` 的 CheckOrigin 恒为 true。来自应用 origin 的 WS 是否要校验 Origin 待定。
- **连接器**：cxg_ 授权是 scope=app+deviceId（`连接器执行协议.md:11-19`），云端没有 deviceId，映射方式待定；这也和「Platform 不建立应用模型」（:5）冲突。
- **开关前提**（可选的组合方式）：
  - compose 模式
  - `auth.enabled`（`config_defaults.go:122-123`）
  - 主机内核 Landlock ABI 达标（选 C2 及以上时）
  - 应用域名和 cookie 已隔离
  - 不满足时的行为：拒绝运行，或降级成「只允许可信应用」

### 7. 剩余风险

风险等级取决于应用来源：Frank 自己写的或来源可信时，大部分「需处理」项可以降级；token 被合法滥用和资源争抢，可信应用也会因 bug 触发。

| 风险 | 性质 | 说明 |
|---|---|---|
| 同 UID 的智能体 bash 能读应用数据和 token | 现状即如此 | 同信任模型（`云端部署与持续任务架构设计.md:207`） |
| 只有整容器资源限额 | 取决于选项 | 单 owner 单实例（§5.1），有监控兜底；OOM 可能波及 Platform |
| UDP 和已放行端口的任意地址出网 | 取决于应用来源 | 不放宽加固就限不住 |
| Node 权限模型不是主防线 | 已知限制 | 只作额外一层 |
| 内核不满足时同 UID 应用可以改写 AgentsDir、automations、身份文件 | 需处理 | 相当于拿到 Platform 权限（热加载：`智能体配置说明.md:329`、`自动化.md:76`） |
| Platform 不设 dumpable=0 时子进程能读到 env_file 密钥 | 需处理 | 实测会泄露 |
| webapp token 调用 `/api/admin/*` | 需处理 | 没有角色检查 |
| 应用 origin 和 cookie、WS Origin | 需处理 | `server_auth.go:71`、`ws/handler.go:37` |
| setsid 后代逃逸 | 需处理 | 实测 |
| 原生模块平台不匹配 | 需处理 | 安装期校验 |

## 实测记录

- **环境**：box（Linux，内核 6.12.94，LSM 有 capability、landlock、selinux、bpf；Seccomp 0；cgroup2 节点归 root；uid 1000，CapEff 0）。
  - box 上没有 Docker，没有复现加固容器
  - 用 `setpriv --no-new-privs` 模拟 no-new-privileges
  - 临时脚本放在 /tmp/wtest，测完已删除
- **结果**：

| 项目 | 结果 |
|---|---|
| 同 UID、NNP 下读 `/proc/<pid>/environ` | 可读，泄露了测试密钥；/proc/pid/maps 也可读 |
| 目标进程设 `prctl(PR_SET_DUMPABLE,0)` 后 | environ 变成 root 0400，读取被拒 |
| 没有权限时 setresuid(10002) | EPERM |
| `unshare -U -r`、`bwrap --unshare-all`（NNP） | box 上成功；Docker 默认 seccomp 下未实测 |
| 创建子 cgroup | 被拒（目录归 root） |
| prlimit `--nproc=50 --nofile=256` | 生效 |
| Landlock ABI 6（NNP，无权限） | 白名单外的文件读取被拒；bind 只允许放行的端口（18080 可以，18090 被拒）；connect 按端口限（443 被拒）、不能按地址限；受限进程读不到域外进程的 environ |
| Node v22.20 `--permission --allow-fs-read=<app>` | 白名单外读取和 child_process 都是 ERR_ACCESS_DENIED；监听 127.0.0.1 允许；addon 要 `--allow-addons` |
| `PR_SET_CHILD_SUBREAPER` 加 setsid 孙进程 | 孙进程的进程组变了（杀进程组杀不到），PPid 挂回回收者 |
| Node v24.22.2 linux-x64 | tar.xz 32MB，解压 208MB（node 122MB、npm 等 19MB、include 67MB），ABI 137，只依赖 glibc |

- **未实测 / 待确认**：
  - Docker 默认 seccomp 放行 landlock 系统调用：依据 moby#43991/#43199，20.10.18 起
  - Docker 默认 seccomp 拦截 userns 和 bwrap
  - 非 root 用户加 cap_add 不进有效权限集
  - oom_score_adj 能无权限调高
  - RLIMIT_AS 和 V8 的冲突
  - ABI6 对应的确切内核版本
  - 服务器内核版本
  - v26 解压后的体积
  - Node 权限模型文档的原话

## 可能的分阶段选项

不排先后。下面只是几种可以拆分的方式。

- **方式一：按信任级别拆**
  - 先做「只运行可信应用」：Node 运行时、安装校验、守护、静态加反代、子域名、环境变量白名单、dumpable=0、进程回收者、内部 API。
  - 再加 Landlock 文件、网络和作用域规则，prlimit，Node 权限，资源监控。
- **方式二：从一开始就带上 C2**：Landlock 不可用时整个功能拒绝运行。
- **方式三：先只托管纯前端**（没有 Node 后端），后端托管随后再做。
- C3/C4（独立 UID、自定义 seccomp）可以作为以后要运行不可信第三方应用时的单独议题。

## 待定问题

1. 生产服务器的主机内核版本，以及 Landlock ABI（≥4 才有网络规则，6 才有作用域）。Docker 版本是否 ≥20.10.18。
2. Node 版本：24 LTS（与 Desktop 一致）、26 LTS 还是 22 LTS；镜像是否带 npm。
3. 第一版是否只支持可信应用（Frank 自己写的或来源可信）。
4. 子域名格式：`<app>.<inst>`、`<app>--<inst>`，还是独立内容域名。
5. 内核不满足时：拒绝运行，还是允许降级成「只允许可信应用」。
6. 应用包是否必须自带 node_modules、禁止安装时联网（选项 A/B/C）。
7. 是否接受「只有整容器资源限额 + 监控」，不为子 cgroup 放宽加固。
8. 隔离组合选 C1/C2/C3/C4 中的哪个。
9. 自动重启策略（不重启，还是退避重启）；日志去向。
10. 后端 token 的下发方式（环境变量还是 fd）；后端调 Platform 的入口（新的内部前缀还是复用 `/api/desktop/*`）；webapp token 对 `/api/admin/*` 的访问范围。
11. 页面鉴权方式（应用域 cookie 还是换票）；WS 是否校验 Origin。
12. 连接器 cxg_ 授权在云端没有 deviceId 时如何映射；是否修改「Platform 不建立应用模型」的约定（`连接器执行协议.md:5`）。
13. WebApp 包从哪里来、怎么安装：Desktop 推送、WebClient 上传，还是智能体生成；与 `连接器共享包与Desktop迁移.md:38` 的边界。
14. 是否需要新的 RuntimeMode，还是在 standalone 下按配置开启（`config.go:88-99`）。

## 改动面

### 后端（agent-platform）

- `Dockerfile`：引入 Node（/opt/node，root 只读），`chown` 排除 /opt/node；可选是否带 npm 和 include。
- 新增 webapp 模块：
  - manifest 解析（兼容 Desktop 格式，加 linux target）
  - 安装和原生模块校验
  - 端口分配、健康检查、重启、日志轮转
  - 进程组、子进程回收者和 Pdeathsig
  - 期望状态持久化（`.state/webapps`）
- 启动器子命令或辅助程序：prlimit、Landlock、oom_score_adj、Node 参数。
- Platform 自身：`PR_SET_DUMPABLE=0`、`PR_SET_CHILD_SUBREAPER`。
- server：
  - 按 Host 分发、静态资源、ReverseProxy 加 WS
  - `/__desktop/*` SDK 端点
  - 页面鉴权，内部 API 前缀和 token scope
  - 应用 origin 的 WS Origin 校验（`ws/handler.go:37`）
- `accesspolicy/protected.go`：纳入 webapps 代码和数据目录（AgentsDir、automations 是否纳入待定）。
- 配置：`configs/runtime.example.yml` 增加 webapps 段（开关、域名格式、端口区段、限额）。
- 可选的独立修复：`ws_terminal_session.go:51` 改用环境变量白名单；stdio MCP 默认继承 `os.Environ()`（`mcp/client.go:468`）。

### 部署（scripts/release-assets）

- `image/compose.yml`、`env.example`：webapps 相关变量。选 C1/C2 时加固不变；选 C3/C4 时要改 user/cap/seccomp。
- `ingress-nginx` 模板和 `ap-ingress.sh`：应用子域名或泛域名。

### Desktop（zenmind-desktop）与 zenmind-env

- `webapp-manifest.ts:341`：target 加 linux。
- 是否由 Desktop 把应用推送或发布到 Platform（待定）。
- `webapp-builder` 构建约定：linux 原生模块、node_modules 打包方式。

## 测试

- 镜像：Node 版本、体积、/opt/node 不可写、read_only 下能启动。
- 托管：
  - 启动、健康检查、退避重启、超限后标 failed
  - 日志轮转
  - SIGTERM→SIGKILL
  - setsid 后代被回收
  - Platform 重启后恢复
  - 端口冲突
- 路由：静态资源和 SPA 回退、backendPrefixes、WS、`/__desktop/*`、未知 Host、nginx 联调。
- 隔离：在加固 compose 下逐项回归「实测记录」里的每一项：
  - environ 读取被拒
  - .state、AgentsDir、automations、身份文件的读写被拒
  - 非分配端口 bind 被拒
  - 非放行端口 connect 被拒
  - 子进程、addon 被拒
  - Landlock 不可用时按开关行为处理
- 认证：没有 token、跨应用 token、应用 token 访问 `/api/admin/*`、cookie 作用域、WS Origin。
- 安装：原生模块平台和 ABI 不匹配时拒绝安装；包内路径穿越。

## 需要更新的文档

- `docs/云端部署与持续任务架构设计.md`：WebApp 托管、域名与证书。
- `scripts/release-assets/image/DEPLOY.md`、`INGRESS.md`：内核要求、应用域名。
- `docs/运行时和沙箱.md`：同容器子进程隔离。
- `docs/API与协议.md`：webapp 内部 API 和 SDK 端点。
- `docs/连接器执行协议.md`：云端 WebApp 的授权映射。
- `docs/连接器共享包与Desktop迁移.md:38`：边界说明。
- `/workspace/docs/zenmind/desktop-webapp-hosting.md` 与 `webapp-builder` 构建约定：Linux 目标。
