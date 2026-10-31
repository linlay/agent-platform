# Linux Docker Compose WebApp 托管完整方案

状态：已开始实施，前置基础已接入，完整托管尚未实现；2026-10-31 根据当前仓库及相邻 Desktop 源码重新整理。

## 1. 目标与部署边界

在 Linux Docker Compose 部署中，由 Go Platform 在自己的容器内托管 WebApp：提供前端静态资源，为需要后端的应用启动内置 Node.js，通过应用专用域名代理 HTTP/WebSocket，并提供与 Desktop 尽量一致的 WebApp SDK。

**新增的 Platform Node.js 后端托管能力只在 Linux Compose 部署中启用。** Desktop 仍由 Electron 托管自己的 WebApp，Platform 不在 Desktop 模式中重复启动 Node；Linux 裸机启动也不自动获得该能力。

### 1.1 支持矩阵

| 部署形态 | 本次新增的 Platform WebApp 宿主 | Node.js 来源 |
| --- | --- | --- |
| 正式 Linux Compose | 显式开启后支持静态前端与可选 Node 后端 | Platform 镜像内固定版本 |
| 仓库开发 Compose | 显式开启且满足同一准入条件后支持 | 相同镜像构建链路 |
| Linux 裸机、普通二进制启动 | 不启用 | 不为本功能查找或安装 Node |
| macOS/Windows 本机 Platform | 不启用 | 不新增 Node 分发要求 |
| Desktop 内置 Platform，含未来 Linux Desktop | 不启用；WebApp 继续交给 Desktop 宿主 | Desktop 原有 Electron/运行时机制 |

这里的 Linux 指容器实际执行环境。实现不能通过操作系统为 Linux、目录位于 `/opt`、存在 Node 或发现 Docker 文件就自动开启托管。

### 1.2 已确认需求与本提案的取舍

已确认：镜像包含 Node；前后端均可托管；Node 与 Platform 位于同一容器；支持安装、启停、健康、日志和 Platform 重启后的期望状态恢复；兼容 Desktop 包与 SDK；新增能力仅面向 Linux Compose。

下表是本提案建议采用的实现基线，不能解释为用户此前已确认这些细节：

| 议题 | 本方案采用的设计 |
| --- | --- |
| RuntimeMode | 保留 `standalone / desktop`，增加独立启动选项标识 Compose WebApp 宿主 |
| Node | Node 24 LTS；构建时锁定精确补丁版本与校验摘要 |
| npm 与依赖 | 运行镜像只带 Node 运行所需内容；应用包预装依赖，安装及启动时不执行 npm |
| 首版应用范围 | owner 自建或审核过的可信应用；静态前端、纯 JS Node 后端 |
| 平台 | 首版 linux/amd64，与当前完整镜像分发能力一致 |
| 路由 | 每应用独立 origin；应用内容使用独立注册域名 |
| 隔离 | 保留 Compose 加固，Node 启动必须成功施加 Landlock ABI ≥ 4 文件/TCP 策略；ABI ≥ 6 自动增加 IPC 作用域 |
| 内核不满足 | Node 托管不可用，不静默降低隔离；静态应用仍可使用 |
| 资源限制 | 整容器硬限额 + 应用准入、V8 堆限额与进程树监测；不承诺应用级硬配额 |
| 生命周期 | 每应用一个监督进程；退避重启；按期望状态重新启动，不接管旧 PID |
| 连接器 | WebApp 宿主负责应用授权，复用调用方中立的 connectorops，不伪造 deviceId |
| 安装入口 | WebClient 上传 ZIP 并审阅安装；Desktop 推送和 Agent 安装工具另行扩展 |

## 2. 当前事实与原方案修正

### 2.1 当前实现依据

- `Dockerfile`：Go 主程序仍为 `CGO_ENABLED=0`，Debian 运行层没有 Node；当前对 `/opt` 整树 chown 给 UID 10001。
- `scripts/release-assets/image/compose.yml`：`init: true`、UID 10001、只读根文件系统、`cap_drop: ALL`、`no-new-privileges`；仅映射 8080；runtime/workspaces 可写，configs 只读；限制整个容器的内存、CPU、pids。
- 根目录 `compose.yml` 是另一份开发模板，加固与停止宽限期不同，不能把正式模板的保证套到开发模板上。
- `internal/config/config.go`、`cmd/agent-platform/main.go`：RuntimeMode 只有 standalone/desktop。`resolvedRuntimeDescription` 基于路径的 compose/container 描述只是日志文案，不能用作功能准入。
- 当前没有 WebApp 长驻进程服务和应用反向代理。Container Hub 是 Agent 执行环境，不承担这里的 WebApp 宿主职责。
- `internal/accesspolicy/protected.go` 的工具路径限制不是 OS 沙箱，无法约束直接启动的 Node。
- `internal/server/server_auth.go` 可接受 `access_token` Cookie；现有 WS `CheckOrigin` 过宽；普通管理路由不能接受新的 WebApp 凭证。
- `internal/connectorops` 已提供中立执行服务与内存 grant；HTTP grant 创建仍要求已验证的 `scope=app + deviceId` 本地程序身份，普通浏览器 owner JWT 不能直接沿用该入口。
- 当前正式完整镜像链路为 linux/amd64；Node 官方存在 arm64 包，不代表 Platform 已支持完整 linux/arm64 发行。

Desktop 参照文件位于相邻 `zenmind-desktop`：

- `src/shared/webapp-manifest.ts`：Manifest v2、`webapp-` 加 16 位小写十六进制 ID；`frontend.routeConfig.backendPrefixes`；Node 命令为 `backend.command.type: electron-node`。当前 target 未包含 Linux。
- `src/main/modules/webs/webapps/launchers.ts`：结构化 argv、环境白名单、HOST/PORT、`WEBAPP_*` 和 `DESKTOP_ACTION_BRIDGE_*` 注入。
- 同目录 `runtime-health.ts`、`runtime-process.ts`、`gateway.ts`：健康、进程组停止、静态与后端分发。
- 同目录 `bridge-module.ts`：绝对路径 `/__desktop/*`；普通 action fetch 当前使用 `credentials: "omit"`，云端 Cookie 鉴权需要宿主提供适配后的 bridge。

### 2.2 不能继续沿用的表述

1. “Linux 就开启 Node”不成立，必须同时满足 Compose 显式选择和配置开关。
2. 原文列出的 Node 精确版本、大小、ABI 和测试结果属于原调研记录，不能充当本次发布锁定值或目标容器验收结论。
3. 扫描 ELF 可识别架构及部分动态链接依赖，但无法仅凭 `.node` 文件可靠证明全部 Node/Electron ABI、Node-API 及运行时兼容性。首版拒绝原生 addon，避免给出虚假的兼容保证。
4. subreaper 接管孤儿进程，不会自动杀掉所有后代；Pdeathsig 也不沿整个后代树继承。需要明确的监督、清理和容器恢复策略。
5. Landlock 文件权限、隐式 ptrace 限制、ABI6 信号/抽象 Unix socket 作用域是不同能力。不能把保护 `/proc` 的效果全部归因于 ABI6。
6. “Platform 不建立应用模型”应限定为连接器执行核心不持有应用模型。新的 WebApp 宿主需要管理自己的应用，但不能把 appId/deviceId 逻辑塞回 connectorops。

## 3. 总体架构与模块职责

```text
浏览器 / WebClient
        │ HTTPS
        ▼
宿主机 nginx：平台域名 + 应用内容域名
        │ 唯一公开入口：Platform 8080
        ▼
Go HTTP 入口：受信 Host 分类
        ├── 平台域名 → 原有 API + owner WebApp 管理 API
        ├── 应用域名 → 应用会话认证 → WebApp 网关
        │                                  ├── /__desktop/* → SDK 适配
        │                                  ├── backendPrefixes → Node HTTP/WS
        │                                  └── frontend.root → 静态文件/SPA
        └── loopback 专用内部调用 → 后端 token 认证 → SDK 适配

internal/webapp：安装、版本、授权、期望状态、生命周期
        │ 受管启动协议
        ▼
每应用 webapp-supervisor（同容器、无提权）
        └── 施加应用策略后 exec Node（127.0.0.1:分配端口）

SDK 适配 → Runtime / Chat 资源窄接口 / connectorops
```

建议职责划分：

| 模块 | 职责 |
| --- | --- |
| `internal/webapp` | Manifest、安装事务、版本租约、状态、应用授权及服务门面 |
| `internal/webapphost` | Linux 监督进程协议、端口、健康、重启、日志、隔离能力探测 |
| `internal/webappgateway` | Host 对应应用后的静态、HTTP/WS 代理与响应头处理 |
| `internal/webappbridge` | SDK action 映射、owner/应用授权复核、资源归属与窄服务调用 |
| `internal/server` | HTTP 解码、主体识别、路由适配与响应；不承载安装和守护业务 |
| `internal/app` | 组装服务、监听就绪后恢复、关闭与健康摘要 |
| `cmd/webapp-supervisor` | Linux 专用小型辅助程序，监督单应用并在单线程子进程施加策略 |

辅助程序采用独立 C 源码及 Linux 构建阶段，避免直接在 Go 多线程主进程中临时施加仅当前线程生效的限制。Go 主程序保持纯 Go 构建；C 编译器仅存在于镜像构建阶段。辅助程序不是另一容器，也不以 root 运行。

WebApp 生命周期不属于 Agent Run，不按 GENERAL/CODER/KBASE/TEAM/EXECUTOR 分支；应用启动也不隐式增加任何 Agent 工具。

## 4. 启用、配置与失败行为

### 4.1 显式部署选择

拟新增启动选项 `--webapp-host=disabled|compose`，默认 disabled。两份 Compose 模板显式传入 `--webapp-host=compose`；它独立于现有 `--runtime-mode`，不得由业务请求、YAML 或进程环境自动猜测。

这是部署契约，不是防伪证明：有权修改启动命令的部署管理员仍能手工传相同参数。本项目只支持和验收 Compose 链路，不试图从容器内部证明启动者确实是 Compose。

启用条件：

```text
通用 WebApp 宿主：
  GOOS == linux
  && --runtime-mode=standalone
  && --webapp-host=compose
  && runtime.webapps.enabled
  && owner 鉴权、HTTPS、公网域名及存储配置有效

Node 后端：
  通用宿主可用
  && 固定 Node / supervisor 校验通过
  && 实际容器中的 Landlock ABI >= 4 且文件/TCP 规则施加探测成功
```

未开启时，不创建 WebApp 目录，不开启监听/恢复任务，不注册可用的托管管理入口。非 Linux、Desktop 或缺少 Compose 启动选择时强行配置 `enabled: true`，启动时报告明确配置错误。

合法部署中 Node/内核运行条件不满足时，Platform 和静态应用继续提供服务，托管状态显示 `backendAvailable: false` 及诊断；已有 Node 应用标为 `blocked`，启动返回 503。禁止降级为不受限制的 Node。

### 4.2 配置归属

以下是拟新增结构，不是当前代码已支持的配置；默认值在实现时进入 `internal/config` 与模板共同维护：

```yaml
webapps:
  enabled: false
  platform-origin: https://demo.platform.example.com
  content-domain: apps.example.net
  instance-slug: demo
  max-running: 4
  backend:
    port-start: 20000
    port-end: 20127
    max-old-space-size-mb: 256
    rss-stop-mb: 512
    health-interval-seconds: 10
    unhealthy-threshold: 3
    restart:
      max-attempts: 5
      window-seconds: 600
      stable-reset-seconds: 300
  logs:
    max-size-mb: 10
    max-files: 3
```

安装包大小、解压后大小及文件数也设置服务端上限，并与 nginx 上传上限对齐。示例首版采用压缩包 100 MiB、解压 512 MiB、最多 20,000 个文件；最终默认进入同一配置事实源。

Node 与 supervisor 路径固定于镜像受管目录，不能由 Manifest 选择任意解释器。所有 WebApp 子目录从 RuntimeDir/有效 StateDir 派生；不增加 YAML `paths`，不增加任意 WebApp 目录环境变量。nginx 的域名、证书与路由属于部署配置，凭据不进入示例或应用包。

## 5. 镜像与包兼容

### 5.1 Node 分发

- 选择 Node 24 LTS，精确版本及 linux-x64 官方制品 SHA-256 随镜像源码锁定；构建不解析 latest。
- 仅复制 Node 运行时及确有需要的共享依赖和许可证，不携带 npm/corepack、头文件或编译工具链，不自动 strip 未验证的官方二进制。
- `/opt/node` 与 `/opt/libexec/webapp-supervisor` 及其父目录保持 root 所有、不可由 UID 10001 改写；将 Dockerfile 整树 chown 改为对数据目录的定点授权。
- Node 使用绝对路径启动，默认不注入 Agent Bash 的 PATH，不作为新增通用 Agent 工具能力分发。
- Node 制品和辅助程序均纳入镜像版本、校验和及许可证清单；如构建引入新源码/锁文件，显式更新 `.dockerignore` 白名单。
- 不改 macOS/Windows program 包，也不要求 Desktop 下载第二套 Node。arm64 等待完整 Linux 镜像依赖链支持后再开启。

### 5.2 Manifest v2 支持子集

首版不重新发明包格式。Platform 与 Desktop 共用契约及有效/无效 fixture，字段未知、大小写错误、旧字段仍严格拒绝。

| 包属性 | Compose 规则 |
| --- | --- |
| schemaVersion、id/key/label/version | 沿用 v2 和现有 ID 规则 |
| `target: any` | 允许静态或完全不含平台依赖的纯 JS 包 |
| `target: linux-x64` | 新增枚举；仅当前 linux/amd64 镜像接受 |
| 其他 Desktop target | Platform 明确拒绝；不修改包内 target 冒充兼容 |
| frontend | 沿用 root/index、routeConfig.backendPrefixes/navigationFallback |
| `backend.command.type: electron-node` | 兼容为“宿主 Node 脚本”，Compose 映射到受管 Node；不设置 ELECTRON_RUN_AS_NODE |
| executable、python/java runtime | 首版返回 webapp_launcher_unsupported |
| backend.args | 作为 entry 后的应用参数，不得变成 Node 启动参数或 Shell |
| backend.env | 验证保留变量及危险注入项；不接受 NODE_OPTIONS、LD_PRELOAD 等执行链覆盖 |
| `.node`、原生辅助可执行文件、包内运行时 | 首版拒绝；不靠扩展名扫描宣称识别全部平台依赖 |
| appConfig/userConfig | 沿用结构、大小及敏感字段约束；不存平台密钥 |

`target: any` 是构建方的可移植声明，安装校验不能证明任意 JS 的所有运行时行为。CI Linux 启动测试与健康检查共同覆盖兼容性。

构建依赖在包外完成，应用 ZIP 携带产物与依赖；禁止安装时联网、npm ci、生命周期脚本及启动时补依赖。包内拒绝符号链接，因此采用扁平依赖或无链接 bundle；使用 pnpm 等工具的构建方必须先生成可独立分发产物。

### 5.3 SDK 兼容边界

保留 `/__desktop/bridge.js`、`/__desktop/actions/call`、app-config/user-config 路径及既有 action/args 和错误形状。路径名是兼容协议，不表示云端存在 Electron。

- 云端 bridge 由宿主发布，普通页面 action fetch 改为 `credentials: "same-origin"`；应用 session Cookie 只交给宿主 SDK 端点，不发给 Node。
- 明确传入 transport.url/token 的后端调用仍使用 Bearer，并保持 `credentials: "omit"`。
- SDK 响应维持 Desktop 的 `{ok, action, result/error}`；新管理 API 使用 Platform 的 `{code,msg,data}`，两种 envelope 不混用。
- capability 查询按宿主实际支持能力返回；旧 Manifest capability 字段不是授权事实源，不能将声明等同于 owner 同意。
- 首版支持配置读取，以及经 owner 授权的连接器执行、assistant.chat/execute、skill.read、artifact.read。对应 action 逐项复用现有服务和资源边界，未映射 action 明确返回 unsupported。
- 本机窗口、文件选择器、系统剪贴板、Desktop 下载目录、Tunnel 发布、本机浏览器控制及仅 Desktop 看板能力不伪装实现。
- 镜像测试包必须覆盖 SDK 前端调用和后端显式 transport，不能只用静态首页证明兼容。

## 6. 持久目录与安装事务

```text
<RuntimeDir>/webapps/<webappId>/versions/<contentDigest>/
  webapp.json + frontend/ + backend/ + 依赖      # 已发布代码，只读使用
<RuntimeDir>/webapp-data/<webappId>/             # 应用持久数据
<StateDir>/webapps/<webappId>/
  install.json                                # 当前版本、来源摘要与安装信息
  desired.json                                # running/stopped + revision
  policy.json                                 # owner 授权及应用资源关联
  user-config.json                            # 配置，仅指定文件可交给应用读取
  logs/                                       # stdout/stderr 轮转文件
<RuntimeDir>/.lock/webapps/                     # 服务实例及每应用 mutation 锁
/tmp/webapps/<webappId>/<generation>/           # 本次进程临时目录/HOME
```

应用不能读取整个自身 StateDir，其中有期望状态和授权。只授予代码只读、本应用数据读写、当前 generation 临时目录读写、指定 user-config 文件只读；日志由父进程捕获，不给应用日志目录写权。

### 6.1 安装与更新

1. owner 在 WebClient 上传 ZIP 到专用暂存入口；浏览器不能提交服务器任意 archivePath。
2. 对内容计算摘要、执行 Manifest/解包预检，返回名称、版本、target、后端类型、大小、申请能力和覆盖对象。
3. 提交安装时绑定同一暂存摘要与当前安装 revision；来源变化或并发更新返回冲突，不重新解释另一份字节。
4. 在正式应用目录外的同文件系统 staging 解包。拒绝绝对路径、`..`、反斜杠歧义、重复规范路径、符号链接、特殊文件及解压炸弹。
5. 预检和解包不执行包中任何代码。验证所有引用路径最终位于发布版本内；移除发布文件的写权限。
6. 获取应用锁，复核安装基准，将版本原子发布并同步写入安装记录；失败保留旧版本并清理 staging。
7. 首次安装默认 stopped，不隐式启动、打开或发布。敏感能力另存 owner 对具体能力/资源的同意。
8. 更新 running 应用时冻结旧版本，停止旧进程，启动并检查新版本；失败回到旧代码和原期望状态。首版接受短暂停机，不做双实例共享数据运行。

代码回滚不等于数据回滚。首版要求应用更新不执行不可逆数据迁移；确需迁移时先由 owner 备份数据并停机更新，平台不能声称自动撤销应用已写入的数据。

应用更新不自动扩大授权；新增能力需重新授权。更新失败/断电后的恢复依据 journal 与已同步的当前版本指针，不能把半解包目录当成已安装版本。

### 6.2 卸载与数据

卸载须经 owner 明确提交，先停止、撤销会话/token/grant、确认无活动租约，再移除代码与记录；持久数据默认保留，另有显式 `purgeData: true` 删除选项，并在审阅中展示数据范围。不得沿用未经确认的 Desktop 全量删除语义。

备份至少包含代码当前版本、webapp-data 与 StateDir 安装/授权记录；运行 PID、端口、token、临时目录不作为恢复依据。备份数据需停机或使用应用一致性机制，文件复制不等于数据库一致快照。

### 6.3 配置修订与回收

user-config 原子替换后，静态 SDK 读取新版本；运行中的 Node 通过受管重启重新获取指定文件的只读授权，避免 Landlock 仍绑定旧 inode。修改应用授权时先推进 policy revision、撤销旧 session/token/grant，再按 desiredState 重建运行实例，任何旧请求的授权 Check 均须失败。

版本回收至少保留当前版本和一个最近成功版本；活动租约和未完成安装事务引用的版本不可删。未认领上传/staging 默认 24 小时回收，generation 临时目录在确认进程退出后回收。磁盘不足时在 mutation 前拒绝安装，日志按限额轮转；应用持久数据仅按显式删除操作清理。

## 7. Node 启动与隔离

### 7.1 保留正式 Compose 加固

保持 UID 10001、只读根、drop ALL、no-new-privileges、init 与唯一对外端口。不给 root、SETUID/SETGID/SYS_ADMIN、宿主 Docker socket、可写 cgroupfs、自定义 unconfined profile。

开发 Compose 开启此功能时补齐相同安全前提和停止宽限期；否则仅作为已有开发服务运行，不能宣称通过 WebApp 部署验收。

### 7.2 supervisor 与 Node 权限

每应用 supervisor 负责生命周期，fork 后在执行 Node 的单线程子进程施加限制；supervisor 保留清理本应用后代所需权限，不把自身放进只允许 Node 访问的受限子域。

| 项目 | 执行规则 |
| --- | --- |
| 环境 | 从空环境构建白名单；不传 os.Environ、平台 JWT、AP_ID_TOKEN、provider key 或连接器凭据 |
| HOST/PORT | 固定 127.0.0.1 与已分配端口；禁止包覆盖 |
| WEBAPP_* | 由宿主生成；未开放目录类变量不注入，不能指向整个管理 StateDir |
| HOME/TMPDIR | 指向本 generation 临时目录；不复用共享 HOME 的秘密或缓存 |
| bridge | 注入 DESKTOP_ACTION_BRIDGE_URL/TOKEN 兼容变量；token 是应用实例最小权限凭证 |
| 文件规则 | 代码/Node/必要动态库/精确系统文件只读；仅本应用 data/tmp 可写；不放行整个 /opt、/etc、/tmp 或 /proc |
| 文件描述符 | supervisor 独占管理 control FD；Node 仅继承约定 stdio，关闭其他文件和 socket，防止已有 FD 绕过规则 |
| 网络 bind | Landlock 只放行分配的 TCP 监听端口；Node 配置仍必须使用 loopback |
| 网络 connect | 默认只放行 Platform 内部调用端口；确需外网时由 owner 配置额外 TCP 端口，不从包自动扩大 |
| IPC | ABI ≥ 6 启用 signal/abstract Unix socket scope；ABI 4/5 明确报告无该隔离，仅用于可信应用；不共享管理 socket |
| Node 权限 | 固定 --permission、文件白名单；不开 child_process、addon、WASI、inspector 等权限；必要 worker_threads 经兼容测试单独评估 |
| 执行参数 | 固定 Node 参数 + entry + 应用 args；shell=false，禁止 NODE_OPTIONS/动态加载器注入 |

ABI 探测必须实际执行 create/add/restrict 并验证拒绝行为；只看 uname、Docker 版本或 LSM 名单不够。只读根和 Landlock 同时生效，失败不能继续 exec Node。

必要系统读取路径按锁定 Node 与镜像测试得到，例如精确 CA、时区、DNS 文件和动态库；路径被符号链接转到数据区时也要拒绝或重新校验。Landlock 不控制所有元数据及所有网络操作，不能把路径白名单描述成完整命名空间隔离。

### 7.3 平台进程与信任范围

Compose 宿主启用时，Platform 及拿到敏感启动参数的 supervisor 尽早设 `PR_SET_DUMPABLE=0`；不在其他部署形态无条件修改。Node exec 后的进程间保护依赖受限域及实际 proc 访问检查，不能假设 exec 后自动保留 dumpable 设置。

普通工具受保护路径补充已发布 WebApp 代码和管理状态；数据访问沿用 owner/Agent 原信任边界，不凭此宣称任意 Agent Host 脚本已被隔离。AgentsDir/AutomationsDir 等已有只读保护继续保留，不在本次方案中顺便重做全平台权限模型。

本方案仍是单 owner、可信应用模型：同 UID 的高权限 Agent 任意代码不受 Node 的 Landlock 约束；整容器资源共享；TCP 端口规则不能限制远端 IP，UDP 不能据此保证禁网。Node 权限模型只减少误用，不是运行恶意代码的安全边界。[Node 24 权限说明](https://github.com/nodejs/node/blob/v24.x/doc/api/permissions.md)

根据用户允许 ABI 4 的调整，最低要求为 ABI4 文件和 TCP 限制；ABI5 额外处理设备 ioctl，ABI6 自动增加信号/抽象 socket 作用域。ABI4/5 明确显示缺失的隔离能力，不能称为完整进程隔离。具体能力以实际施加和拒绝测试认定。[Linux 6.12 Landlock 文档](https://kernel.org/doc/html/v6.12/userspace-api/landlock.html)

## 8. 生命周期、恢复与资源

### 8.1 状态模型

期望状态仅 running/stopped，先持久化并 fsync 成功才返回受理成功。观察状态为 stopped / starting / running / unhealthy / stopping / backoff / failed / blocked。安装和更新是独立 mutation 状态，不冒充进程 running。

- start/stop 幂等；restart 为同应用串行的停止再启动。
- stopped 应用不提供前端、SDK 或后端业务访问，仅保留 owner 管理和诊断；静态应用的 start/stop 直接控制网关发布，无需 Node。
- 同一应用只允许一个 generation；状态改变用 revision 检测并发。
- 返回 version、generation、desiredState、observedState、健康、最近错误、退出码、重启次数和日志游标；公开 API 不回显 token、内部绝对路径或完整环境。
- 显式 stop 持久化 stopped 并取消已排队重启，防止进程退出回调又把应用拉起。

### 8.2 启动与健康

1. 校验当前版本、授权、宿主能力及 max-running 配额，租用版本与数据目录。
2. 在 20000–20127 分配端口；先探测再交给 Node 有竞争窗口，只有明确端口冲突才重新分配并有限重试。
3. 创建 generation、临时目录、最小环境与后端 token，启动 supervisor。
4. supervisor 建立进程组与 subreaper，完成父进程失联处理后启动受限 Node；通过私有控制通道报告施加策略及 exec 结果。
5. 只检查固定 loopback:分配端口。沿用 Manifest HTTP/TCP 健康类型和 startupTimeoutMs；HTTP 不跟随外部重定向、限制响应量。
6. 健康通过后网关才发布 backend target。静态应用直接就绪，不分配 Node 端口或 supervisor。

默认每 10 秒检查运行健康，连续 3 次失败先摘除代理，再进入停止与重启。starting/backoff/unhealthy 返回带重试提示的 503，不能把后端错误路径回退成 index.html。

### 8.3 重启与停止

非预期退出或健康失败按 1、2、4、8、16 秒加少量 jitter 退避；10 分钟内最多自动重试 5 次，连续健康 5 分钟重置失败计数。配置/隔离错误直接 blocked，包/入口错误直接 failed，不无限重试。手工 restart 可重置预算。

停止顺序：摘除新代理请求 → 关闭/限时排空已有 HTTP 和 WS → 撤销本 generation 后端 token、连接器 grant 并取消关联调用 → SIGTERM → 按 Manifest shutdownTimeoutMs 等待 → SIGKILL → wait/reap → 确认已登记后代退出 → 释放端口、临时目录与租约。

supervisor 逐步清理本应用进程组和已接管后代，使用 pidfd 或 PID+启动时间避免 PID 复用误杀。应用级 subreaper 避免主 Platform 全局 wait 与 os/exec、MCP、KBX 等现有子进程竞争回收。

必须测试 double-fork/setsid 后代。subreaper 不自动完成树清理，Pdeathsig 在 fork 子代会被清除，不能声称两者组合足以强制收容恶意进程树。[subreaper 语义](https://man7.org/linux/man-pages/man2/PR_SET_CHILD_SUBREAPER.2const.html)；[Pdeathsig 语义](https://man7.org/linux/man-pages/man2/PR_SET_PDEATHSIG.2const.html)

### 8.4 Platform 退出与恢复

- 正常退出：停止接收管理 mutation，全部应用并行摘除/停止；保留退出前 desiredState，不把所有 running 写成 stopped。
- supervisor 监听 Platform 私有控制管道 EOF 执行清理，Pdeathsig 只作辅助；不把 Go 创建子进程线程的存活假设成整个 Platform 存活。
- 正式和开发 Compose 停止宽限期与应用最长停止时间对齐；建议 45 秒容器宽限期，应用清理控制在 35 秒内，并行处理。
- Platform 异常退出采用 Compose 重建/重启容器的恢复路径，使容器运行时清除旧进程；不支持在同一容器里手工拉起第二个 Platform 接管旧进程。
- 新 Platform 取得实例独占锁，校验安装记录和版本摘要；等 HTTP 内部桥就绪，再按 desiredState 创建新 generation、端口与 token。
- 旧 PID、旧端口、旧 token 全部不接管；授权记录可保留，运行凭证必须重建。恢复只作用于 WebApp，不改变 Agent Run 重启后取消的现有合同。
- 监督失联或清理结果不确定时标 failed，阻止新 generation 并要求重启容器；不能重复启动两个数据写入者。

### 8.5 资源与日志

每应用默认 V8 old-space 256 MiB；512 MiB 是进程树 RSS 停止阈值，采样检测属于事后保护，不能称为内存硬限额。禁止 RLIMIT_AS 作为未经验证的 V8 内存限制，禁止用共享 UID 的 RLIMIT_NPROC 冒充每应用进程配额。

默认最多 4 个运行后端，记录进程树 RSS/CPU/退出原因；达到准入限制时拒绝启动。整个实例仍依靠 Compose memory/cpus/pids 硬限制，OOM 可能影响 Platform。可以给 Node 提高 oom_score_adj，但必须在目标容器验证后再纳入发布默认。

stdout/stderr 由宿主持续读取，每流 10 MiB、最多 3 个文件，避免长行与无换行输出导致内存增长；平台日志仅记生命周期摘要。日志展示按 owner 鉴权，不输出内部控制消息。平台自己的 token/JWT 不得被平台日志记录，但无法保证应用不把自身业务秘密写到 stdout。

## 9. 域名、入口分发与网关

### 9.1 域名规则

建议平台为 `demo.platform.example.com`，应用为 `demo--<appHex>.apps.example.net`。appHex 取规范 ID 的 16 位十六进制，避免可变 label/key 导致路由漂移。`*.apps.example.net` 是一层 wildcard；应用与平台示例使用不同注册域名，避免将同站 Cookie 策略当作安全隔离。

配置校验 Host 长度、字符、实例唯一性和平台/内容域冲突。应用 Host 只能映射到已安装应用，未知 Host 一律拒绝。owner 部署 DNS、内容域证书和 nginx 路由；安装不调用系统 nginx、不自动签证书。

nginx 将平台和本实例应用域名转发到原 AP_PORT，继续仅公开 8080，不暴露 Node 端口。多实例共享内容域时，按固定 instance-slug 的显式或有界匹配路由指向对应实例；不能把所有 wildcard 请求交给默认实例。

### 9.2 分发优先级

Host 分类必须在原有平台 API 路由和鉴权之前执行，之后命名空间完全分离：

1. 平台 Host：原有平台路由与 owner 管理 API；不允许借路径访问 Node 内部桥。
2. 应用 Host：宿主 session 兑换与保留路径、SDK、应用 backendPrefixes、静态前端。
3. Node 内部桥：精确内部路径 + loopback peer + 内部 Host + 受限 token；仅处理宿主 action。
4. 其他 Host：拒绝。现有健康检查 loopback `/healthz` 单独精确放行，不能因此放行全部平台 API。

应用 Host 的 `/api/admin/*` 即使与平台路由同名，也只能命中该应用声明的后端路由或返回应用 404，绝不能落到平台管理 handler。应用 token 到平台 Host 的普通 API 仍被拒绝。

Host 使用 nginx 重写后的 Host；客户端提交的 X-Forwarded-Host/Proto 不直接参与路由或票据生成。读取转发头时必须验证 peer 属于配置的代理集合，并剥离客户端原始值；公网 URL 优先从固定配置构建。

### 9.3 静态与后端

- backendPrefixes 按路径段匹配：`/api` 匹配 `/api`、`/api/x`，不匹配 `/apix`；保留应用原始路径与 query。
- `/__desktop` 及子路径由宿主保留，Manifest 不能覆盖；session 兑换路径同样属于宿主保留区。
- `httputil.ReverseProxy` target 只来自本 generation loopback 地址，不接受请求提供任意目标；出站使用内部直连客户端。
- 支持 WS Upgrade 与 SSE，设置连接、头读取、空闲/流期限及取消语义；停止时显式关闭 Upgrade 连接，不能只关闭 HTTP server。
- 请求转发移除宿主 session Cookie、平台授权头和所有伪造身份头，保留应用自身 Cookie/Authorization；SDK 请求不交给 Node。
- 后端响应不能设置宿主 session Cookie，含同名/保留前缀 Cookie 一律拒绝；应用 Cookie 仅限本 Host，禁止 Domain 扩散。
- 静态资源仅从 frontend.root 提供，规范化 URL 并防穿越；不公开 Manifest、backend、node_modules 或 data。
- SPA fallback 仅用于 GET/HEAD 且接受 HTML 的前端导航；缺失 asset、SDK、后端请求保持原错误。
- index、用户配置和 SDK action/session 响应使用 no-store/no-cache；有内容 hash 的静态文件才使用长期 immutable。
- 提供 nosniff、受控 frame-ancestors、Referrer-Policy；CSP 与实际脚本/网络需求匹配，不承诺阻止 Node 业务代码所有出网。

## 10. 页面认证、后端凭证与 CSRF

### 10.1 页面访问

首版应用仅 owner 私有访问，不因拥有域名就公开，也不引入匿名发布开关。

1. owner 在平台 WebClient 点击打开应用，平台核验现有 JWT、实例 owner 与应用访问权。
2. 创建一次性 launch ticket，绑定 subject、webappId、精确 origin、到期时间（建议 60 秒）和安全回跳路径，服务端只存摘要。
3. 用顶层 POST 表单向应用 Host 精确兑换端点提交票据；票据不进 URL、Referer 或访问日志。跨注册域兑换 POST 不依赖已有应用 Cookie，严格验证来源及票据。
4. 成功设置 `__Host-ap-webapp`：Secure、HttpOnly、Path=/、无 Domain、SameSite=Lax；303 到应用内部安全路径。
5. 应用会话为服务端随机凭证，绑定 owner、应用与授权 revision，建议闲置 30 分钟、最长 8 小时；停止、卸载、owner 登出和授权撤销时失效。容器重启后重新兑换。

未登录的顶层 HTML 导航可跳固定平台登录/打开入口；API/WS 返回 401，不重定向到 HTML。禁止任意 returnUrl/open redirect；鉴权错误不能被 SPA fallback 隐藏。

应用不能读取平台 access_token、平台 localStorage 或其他应用会话。WebClient 默认新标签打开；如需 iframe，不依赖第三方 Cookie 作为首版方案，单独验证浏览器兼容性。

### 10.2 CSRF 与 WS

Cookie 认证的 SDK 写请求检查精确 Origin、Fetch Metadata 和 CSRF token；云端 SDK 从已认证宿主 bootstrap 获取并自动提交 CSRF 值。应用业务写请求在网关校验精确同源 Origin 和 Fetch Metadata，沿用已有业务请求格式；需要更细业务权限时由应用执行自身校验。launch-ticket 兑换使用独立一次性票据校验，不要求预先取得 CSRF token。

页面 WS Upgrade 同时核验应用 session、归属和精确 Origin，不能继承 `CheckOrigin: true`；WS URL 不携带后端 token。无 Origin 的程序客户端使用明确 Bearer 协议，不能以“缺少 Origin”绕过 Cookie 通道校验。

平台现有主 WS 入口一并阻止应用内容域借 owner 凭据跨域建立连接，保留受信 WebClient/Desktop 协议；不能把应用 origin 批量加入平台 CORS 白名单。

### 10.3 后端 token

每 generation 生成独立不可猜测的 `wapp_` 不透明 token，由内存摘要索引校验；绑定 webappId、generation、owner、授权 revision 和能力集合，带有效期并可立即撤销。token 不进入前端、Manifest、安装记录或日志。

首版通过环境注入兼容 DESKTOP_ACTION_BRIDGE_TOKEN，URL 指向 loopback 专用内部路径。token 使用宿主管理的租约：建议 5 分钟，由 supervisor 存活且应用健康的观测每 30 秒在服务端续期；应用请求本身不能续期。健康 generation 无需因 token 续租重启，generation 结束、监督失联或撤权立即失效；不能把固定环境 token 设计成 15 分钟后应用永久失效。该租约与连接器 cxg_ 的固定有效期相互独立。

后端只可调用内部桥明确 action。wapp_ 不是 JWT，也不是 cxg_；普通 `/api/*`、管理 API、签发 grant、凭据管理均不接受它。浏览器 session 也不能作为普通平台 JWT。

## 11. SDK 授权与连接器边界

### 11.1 宿主应用授权

宿主持有应用/owner/Chat/资源关联和 owner 同意的能力记录，桥接每次调用从 session/token 解析可信上下文，不采信请求体自报的 owner、appId、Workspace 或资源授权。

- assistant.chat/execute：调用现有 Runtime 服务，遵循目标 Agent、工具、accessLevel、HITL 和 outputSchema 合同；应用权限不能把人工步骤自动变成已批准。
- skill.read：按应用被授权资源集合和现有读取服务过滤，不暴露服务器任意路径。
- artifact.read：同时满足应用关联和当前 owner Chat 权限，复用 chatresource/发布产物校验，不借连接器 grant。
- 前端/后端来源不能改变资源所有权；后端 token 不具备页面登录或授权管理权限。
- 前后端 action 差异、流式返回、取消、上传等逐项形成契约测试，不以静态假数据代替实现。

### 11.2 连接器执行

现有 `/api/connectors/execution/grants` 仍只接受原有本地程序身份。云端不伪造 deviceId，不给 Node 下发 owner JWT，不让浏览器 owner 绕过接口既有身份条件。

新增受信主体是进程内 WebApp 宿主服务：验证 owner、应用、授权 revision 和连接器/adapter 同意范围后，通过 app 装配的窄接口调用现有 connectorops.Grants.Issue/Service。connectorops 只接收 Subject、Execution、Check、IdempotencyNamespace 等中立 Scope，不 import webapp。

cxg_ 如在宿主内部使用，仍遵守现有 15 分钟有效期；宿主按需续发最小 grant，不把 cxg_ 暴露给应用，以便每次调用继续执行应用侧授权。停止/撤权/卸载撤销相关 grant 并取消调用。

应用关联由宿主保存；幂等 namespace 使用符合当前 ID 规则的稳定安装标识，跨进程重启和更新保持不变，卸载后不复用旧安装身份。它仅划分收据，不选择账号或授予权限。

保留原有约束：部署级凭据、CLI/MCP 复核、无任意 executable/cwd/env、oneid 通用执行不支持、超时与结果限制、未知结果不自动重发。Node 重启、grant 刷新和 SDK 网络重试不能自动重放有副作用的 invoke。

相应更新《连接器执行协议》职责表述为“连接器执行模块不持有应用模型；调用方负责生命周期与授权”，并记录进程内宿主授权来源；不放宽既有 HTTP grant 身份检查。

## 12. 管理 API 与操作入口

首版新增独立 owner 管理入口，拟定如下；最终路由/DTO 写入 API 文档并严格校验未知字段：

| 方法/路径 | 用途 |
| --- | --- |
| `GET /api/admin/webapps` | 已安装列表、期望/观察状态、访问 URL |
| `GET /api/admin/webapps/host` | 开关、静态/后端可用性、Node 版本、隔离诊断 |
| `POST /api/admin/webapps/uploads` | 有界 ZIP 上传，返回暂存 ID 与摘要 |
| `POST /api/admin/webapps/preflight` | 包校验与安装审阅，不执行应用 |
| `POST /api/admin/webapps/install` | 暂存摘要、expectedRevision 原子安装/更新 |
| `GET /api/admin/webapps/{id}` | 详情、授权摘要、状态、版本 |
| `POST /api/admin/webapps/{id}/start`、`/stop`、`/restart` | 幂等受理，长操作返回 operationId |
| `GET /api/admin/webapps/operations/{operationId}` | owner 查询安装/启停结果 |
| `GET /api/admin/webapps/{id}/logs` | 有界游标读取，禁止任意 path 参数 |
| `PUT /api/admin/webapps/{id}/policy` | owner 审阅后修订能力和资源授权 |
| `PUT /api/admin/webapps/{id}/user-config` | 按 Manifest schema 保存，revision 防冲突 |
| `POST /api/admin/webapps/{id}/launch-ticket` | 创建一次性打开票据 |
| `DELETE /api/admin/webapps/{id}` | 卸载，单独声明 purgeData |

新入口无条件校验 owner 管理主体，不因位于 `/api/admin` 就认为权限已满足。宿主启用要求配置实例 owner（单一 allowed subject）、JWT issuer/audience/expiry 约束；owner 来自已验证 principal 与部署配置，不从 body 取得。

WebClient 提供应用列表、上传审阅、安装、启停、诊断、日志、授权和打开。Desktop 仍走已有管理流程；本期不改变 desktop_webapp 工具在 standalone 下行为，不把 Desktop 工具重定向到本服务。

不新增 Agent 自动安装/发布权限。以后提供 Agent 管理入口时，使用独立工具和审批合同，复用宿主服务，不默认挂入所有 Agent。

## 13. 实施阶段与交付门槛

### 阶段一：契约、部署选择与基础能力

完成支持矩阵、启动选项、配置/目录模型、Manifest v2 Linux target 和共享 fixture；镜像锁定 Node/helper；在真实正式加固 Compose 中跑通 Landlock 与只读根预检。

门槛：非 Compose/非 Linux/Desktop 不启用；默认关闭；静态与 Node 可用性分开报告；内核不支持有 blocked 诊断。此阶段不宣称整体交付。

### 阶段二：安装、静态网关与 owner 访问

完成 ZIP 事务、独立域名、owner API、票据兑换、session、CSRF、静态与用户配置；WebClient 能安装并打开静态应用。

门槛：路径/Host/Cookie 隔离通过，未知 Host 拒绝，应用域不能进入平台 API。静态阶段可单独验证，整体需求仍包含后端。

### 阶段三：Node 托管闭环

完成 supervisor、隔离、端口、健康、HTTP/WS/SSE、日志、退避、停止、崩溃与 Compose 恢复；覆盖更新失败回退和数据保护。

门槛：带 Node 的参考应用在真实部署中通过安装→启动→访问→更新→重启恢复→停止→卸载闭环。

### 阶段四：SDK 与现有服务接入

完成云端 bridge、应用授权、连接器中立 Scope、assistant/skill/artifact 适配、取消与幂等；Desktop 与 builder 同步 Linux 包契约。

门槛：已承诺 action 均有真实集成测试，未支持能力明确报告，应用凭证不能升级到管理身份。

### 阶段五：正式部署验证与文档

在目标服务器核验内核/LSM/Docker/seccomp、DNS/TLS、nginx 和资源行为，执行升级、停机、故障恢复演练。通过后才可将整个功能写为已交付。

不按“先取消隔离跑起来、以后补安全”的顺序上线 Node；可独立交付静态托管，但不能当作完成本计划。

## 14. 改动范围

| 仓库/位置 | 预期改动 |
| --- | --- |
| Platform cmd/agent-platform、internal/config | 宿主选项、配置校验、默认值、关闭语义 |
| Platform internal/app | 组装、HTTP 就绪后的恢复、关闭与可用性 |
| 新增 webapp/webapphost/webappgateway/webappbridge | 按第 3 节实现，不向 server 堆业务 |
| Platform cmd/webapp-supervisor | Linux C helper、受管启动/清理协议与沙箱 |
| Platform internal/server、internal/api、WS | Host 外层分发、owner API、认证/Origin 适配 |
| Platform internal/accesspolicy | 代码及管理目录保护，沿用既有只读规则 |
| Dockerfile、.dockerignore、镜像构建锁/smoke | Node/helper 固化、所有权、体积/许可/校验与镜像测试 |
| 根 compose.yml、release image Compose/env/DEPLOY | 显式选择、加固、停止宽限期、启用说明 |
| ingress 模板、ap-ingress.sh、INGRESS | 内容域证书、实例应用 Host 路由与固定代理头 |
| 相邻 agent-webclient | 安装审阅/管理 UI、票据打开、错误/日志展示 |
| 相邻 zenmind-desktop/contracts、shared Manifest | Linux target、契约一致；不让 Desktop Platform 接管托管 |
| 相邻 zenmind-env/webapp-builder | 纯 JS/Linux 产物、离线依赖和 SDK 构建说明 |

真实 configs、token、证书和应用数据不提交。本次只改方案，不顺带修改其他仓库或全平台终端/MCP 环境继承逻辑；后者单独立项。

## 15. 验收清单

| 类别 | 必须覆盖 |
| --- | --- |
| 部署边界 | Linux 裸机关闭、非 Linux 拒绝强开、Desktop 不重复启动、两份 Compose 显式选择、关闭不建目录 |
| 镜像 | Node/helper 精确校验、UID 10001 不可改程序、无 npm/编译器、read_only 启动、增量体积实测 |
| Manifest | v2 fixture 一致、Linux target、any 纯 JS、非法 ID/未知字段、launcher/addon 拒绝、保留 env |
| 安装 | ZIP 穿越/链接/重复/膨胀、摘要变化、revision 并发、断电恢复、更新回退、数据不误删 |
| 生命周期 | 启停幂等、端口抢占、启动超时、健康失败、退出码、退避耗尽、停止取消重启、日志长行/轮转 |
| 进程树 | double-fork/setsid 清理、supervisor 崩溃、Platform SIGTERM/SIGKILL、管道 EOF、容器重启无旧写入者 |
| 隔离 | 真实加固容器中拒读平台/其他应用文件、拒改代码/授权、拒读敏感 proc、域外信号拒绝、无继承 FD 绕过 |
| 隔离失败 | Landlock 缺失/ABI 不足/seccomp 拒绝/施加失败都不 exec Node，静态/平台可诊断 |
| 网络 | 非分配 TCP bind/connect 失败；明确 IP/UDP 限制；仅固定 loopback target 被代理 |
| 路由 | 未知/伪造 Host、代理头伪造、路径段匹配、SPA 不吞后端错误、WS/SSE、停止断开 Upgrade |
| 浏览器认证 | 跨注册域 launch POST、票据一次性/过期/串应用拒绝、安全回跳、host-only Cookie、撤销、CSRF |
| WS/主平台 | 错 Origin 拒绝、应用域不能跨连主 WS、token 不进 URL、原 WebClient/Desktop 回归 |
| 代理凭证 | 宿主 Cookie 不到 Node、业务 Authorization 保留、Node 不能伪造宿主 Cookie、应用 token 不能调 admin |
| SDK | 页面 credentials、后端 transport、能力查询、配置、连接器、assistant、skill/artifact 资源复核 |
| 授权 | 不可伪造 owner/deviceId、HTTP grant 条件保持、跨应用/跨 generation 拒绝、撤权取消、cxg 续发 |
| 幂等 | 更新/重启不换安装 namespace、副作用不重放、未知结果保留、权限变化复核 |
| 资源 | max-running、RSS/日志增长、容器 OOM/磁盘满及恢复；不把监控阈值当硬隔离 |

实现测试以 Go 单测/集成为基础；Linux helper 与沙箱必须在实际 Compose 验证，HTTP 单测或 macOS 测试不能替代。发布前再执行仓库相应检查及完整 Go 回归。

## 16. 部署现场仍需确认的事实

这些是上线前验证输入，不能猜测为已通过：

1. 实际服务器架构、内核、启用 LSM、Landlock ABI 及 Docker/seccomp 下可用性。
2. owner 指定的平台公网 origin、独立内容注册域名、DNS 和证书；示例域名不是部署值。
3. 发布选定的 Node 24 精确补丁、官方 SHA、动态依赖、镜像体积和漏洞扫描。
4. 4 GiB/2 CPU/1024 pids 默认实例下，参考应用与 Agent 并发时资源和失败恢复表现。
5. 当前 Desktop SDK 完整 action 清单与每项服务映射，不能声明支持尚未接入的能力。

不再把部署事实与“选 A/B/C 哪个设计”混成待定问题；设计基线见第 1 节，变更基线同步调整实现和验收。

## 17. 调研记录与证据等级

原方案记录过在名为 box 的 Linux 6.12.94 环境中的测试：无 Docker、UID 1000、CapEff 0、Seccomp 0，使用 no-new-privs 模拟；观察到同 UID 读取 environ、dumpable 保护、无权切 UID/建 cgroup、Landlock ABI6 拒绝文件/端口访问、Node 权限拒绝部分访问，以及 subreaper 接管 setsid 后代。

原文还记录 Node v24.22.2 linux-x64 压缩包约 32 MB、解压约 208 MB，Node 文件约 122 MB。这些是历史调研者记录，本次未重新测量；不能外推为当前锁定版本大小、ABI、正式 Docker 容器或生产服务器验收结果。

方案整理时完成了仓库与相邻 Desktop 源码、官方机制文档核对，未访问目标服务器或执行隔离实测。后续开始实施后的容器结果另记在第 19 节，不能与原 box 调研记录混用。

技术机制参考：Node LTS 选型参照[官方发布周期](https://nodejs.org/en/about/previous-releases)；Node 权限、Landlock 和进程管理的限制已在对应设计段落附原始文档。

## 18. 后续需要同步的文档

- README.md、AGENTS.md：Compose 独有能力与模块入口，仅在实现并验证后更新交付描述。
- configs/runtime.example.yml：新增配置的唯一模板；不引入 paths。
- docs/云端部署与持续任务架构设计.md：同容器托管、域名、单 owner 信任边界。
- docs/运行时和沙箱.md：WebApp 与 Agent sandbox 分工、Landlock/资源限制。
- docs/API与协议.md：owner 管理、票据、session、内部桥与 SDK 独立 envelope。
- docs/连接器执行协议.md：执行核心中立性、受信宿主进程内授权，保留 HTTP grant 条件。
- docs/连接器共享包与Desktop迁移.md：Desktop 与 Linux Compose 各自托管职责。
- scripts/release-assets/image/DEPLOY.md、INGRESS.md：探测、开关、内容域/证书、升级/回滚/备份。
- Desktop WebApp 契约与 builder 文档：Linux target、Node 子集、纯 JS/离线包和云端不支持能力。

本方案的完成标准是 Linux Compose 中前端与可选 Node 后端的完整托管闭环；其他 Platform 部署不承担新增 Node 宿主职责。

## 19. 实施进度（2026-10-31）

首批基础已接入代码：独立 --webapp-host 启动选项、runtime.webapps 严格配置和 owner/域名/部署条件校验、冻结派生目录、owner 前置诊断接口、平台 Host 边界及内容域 Origin 拒绝、受管 Node/helper 的完整性与能力检查。两份 Compose 模板显式选择宿主，开发模板补齐加固；Dockerfile 增加锁定 Node v24.22.2 的独立构建阶段。辅助程序当前仅实现 --probe/--version，没有运行应用的入口。

相关 Go 回归和最终 `go test -p 2 ./...` 全量通过；初次全量有一个 connectorauth 登录用例超时，单例、整包及低并发全量复测均通过，未修改其超时时间。主程序完成 CGO_ENABLED=0、linux/amd64 交叉编译。linux/amd64 C 辅助程序已在既有编译镜像中通过 -Wall/-Wextra/-Werror 编译。在 macOS ARM Docker Desktop 的 amd64 仿真和正式加固条件下，Landlock 返回 ENOSYS，正确报告不可用；原生 ARM Linux 容器中 ABI 探测返回 6。这不代表支持 Platform arm64，也不代表原生 linux/amd64 完整隔离验收。

完整 Node 镜像阶段构建被 Docker Hub token 地址访问超时阻断，尚未生成并验证完整镜像。安装/更新事务、Manifest Linux fixture、网关、SDK、进程托管与恢复、WebClient 管理和生产目标验收均待后续实施；诊断明确返回 hostingAvailable:false。当前实现说明见 [WebApp 托管](../docs/WebApp托管.md)。

ABI 兼容调整：用户允许将最低版本设为 4。探测协议升级为 v2，按 ABI 选择文件权限位和 ruleset 结构长度；仅 ABI ≥ 6 执行域外信号及抽象 socket 拒绝测试，公开 isolationProfile/ipcScoped/deviceIoctlRestricted 和非阻断 warnings。低于 ABI4、规则施加或核心拒绝测试失败仍阻断后端前提，不提供无 Landlock 回退。

singapore02 已在正式加固参数的原生 amd64 容器中验证 ABI4 核心文件/TCP 限制成功，明确返回 ipcScoped:false 与 deviceIoctlRestricted:false。镜像发布 smoke 增加 Node 精确版本、helper 可执行性、运行时不可写及 npm 不存在检查，发布清单记录 Node 版本。
