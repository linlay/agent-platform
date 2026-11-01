# WebApp 托管

## 访问范围修订（2026-11-01）

用户确认先完成 `127.0.0.1` 本地端口访问，后续每应用独立域名另行实施。DNS/TLS/nginx 和公网内容域验收延期，详见计划顶部范围修订。当前新增网关仅提供每应用一个本地 origin，不在 Platform 主端口提供域名分流。

本地端口 Cookie 共享主机边界，不能等同于独立域名隔离；应用使用独立 Cookie 名并绑定实例代次，后端代理剥离宿主 Cookie。仅支持单 owner 审核的可信应用。Compose override 将前端端口范围发布在宿主机 `127.0.0.1`；Node/内部 SDK 端口不发布。远程浏览器需要 SSH 转发，后续域名接入会另做配置和验证。

## 当前实现范围

完整设计见 [Linux Docker Compose WebApp 托管方案](../plans/webapp-node-hosting.md)。当前代码已接入部署选择、严格配置、owner/Host 边界、Node 制品与隔离探测，以及 owner ZIP 上传、预检、安装、停止状态更新和卸载事务。静态/Node 网关、票据/会话、启停、最小 WebClient 管理和受限 Node 监督已接入；运行中更新、失败回退和配置/授权重建已接入；应用 SDK 继续按后续批次实现，不能把安装管理视为完整托管交付。

共享 Manifest 已补齐 Linux 目标及合法/非法输入 fixture，builder 同步离线纯 JS 包约束；Desktop 仍拒绝安装 Linux 目标包。Go 安装器嵌入 Desktop 生成的 schema 并消费同一 fixture；`scripts/sync-webapp-contracts.py --check --desktop-root <Desktop源码根>` 检查复制漂移，不在生产环境读取相邻源码。小批次边界与验证限制见计划第 21 节。

## 安装事务与数据所有权

`internal/webapp` 独占安装代码、期望状态和应用授权；`app.New` 只在合法启用后创建服务，并持有运行根下的实例独占锁。Server 只校验 owner、来源和请求形状，不解析包或执行安装。`internal/webappgateway` 独立监听本地应用端口，负责私有静态文件和会话；桥接模块与后端托管由后续批次接入。

上传是有界 ZIP 字节流，不接受服务器路径。预检不执行代码、不创建安装记录；提交绑定上传摘要与当前安装 revision。解包拒绝穿越、反斜杠、大小写碰撞、链接、特殊文件、超量展开、秘密文件和原生载荷；Compose 子集拒绝其他平台、非 Node launcher 与执行链环境覆盖。

首次安装固定 stopped。更新保留安装身份、授权、用户配置和持久数据。运行中更新先摘除访问并确认旧实例退出，安装 journal 保持 prepared，直到新实例健康才提交；失败则停止新实例、恢复原代码/记录与期望状态并重新启动。只读代码在同一文件系统的 staging 与 app 之间改名，journal 在切换前持久化，安装记录、代码摘要及目录同步完成后才提交。中断的未提交事务恢复原代码与记录；已提交事务只重做清理。恢复结果不确定时停止后续 mutation，保留现场，不再叠加安装。

卸载使用独立删除 journal，持久数据默认保留，只有 owner 显式 `purgeData:true` 才删除。重新安装生成新的调用收据 namespace，正常更新保持原安装身份。代码回滚不撤销应用业务数据迁移。

该能力只针对 Linux Compose。Desktop 继续使用 Electron 原有宿主；macOS、Windows 本机及 Linux 裸机运行 Platform 不自动启用，也不从 PATH 查找 Node。

## 开关与配置

RuntimeMode 仍是 standalone/desktop。新的独立启动选项为 `--webapp-host=disabled|compose`，默认 disabled；两份 Compose 模板选择 compose，但这不会自动打开功能。配置入口为 `configs/runtime.yml` 的 `webapps`，模板默认 `enabled: false`。

开启要求同时满足：

- 实际 Go 平台为 Linux，RuntimeMode 为 standalone，启动参数明确选择 compose。
- 鉴权开启，设置 issuer、audience、require-expiry，以及恰好一个非空 allowed-subjects owner。
- platform-origin 为规范 HTTPS origin，或本地 `http://127.0.0.1:<非特权端口>`；不含路径、查询和用户凭据。content-domain/instance-slug 保留给后续域名接入，本地模式不需要填写。
- resource.public-base-url 如有配置，与 platform-origin 一致。
- 配置限额合法；端口范围足以分配 max-running 个非特权端口。

`webapps` 拒绝未知字段、错误大小写、null、错误类型和越界限额。引号中的布尔/数字保留字符串类型，不沿用旧节的布尔强转。`webapps/` 存放代码和管理记录，`webapp-data/` 存放应用数据，都从 AP_RUNTIME_DIR 派生，不使用 StateDir；不提供 YAML paths 或新目录环境变量。

关闭时不运行探测、不创建 WebApp 数据目录、不注册诊断接口，也不改变现有 Host 行为。非法部署强制开启会阻止启动。合法 Linux Compose 中 Node/隔离前提不满足仅反映为诊断不可用，不把失败降级为无隔离执行。

## owner 诊断接口

`GET /api/admin/webapps/host` 只在启用后注册，要求普通 Platform JWT 经现有 verifier 验证，subject 必须等于唯一 owner；不接受 WebApp 或连接器执行凭证。

响应沿用 `{code,msg,data}`。data 字段：

| 字段 | 含义 |
| --- | --- |
| host / os / arch | 显式宿主、实际操作系统和架构 |
| nodeVersion / nodeReady | 锁定 Node 版本及受管制品检查结果 |
| supervisorReady | 独立探测辅助程序的完整性检查结果 |
| landlockAbi / isolationReady | 实测 ABI 与当前版本支持的核心限制是否生效 |
| isolationProfile / ipcScoped / deviceIoctlRestricted | 文件/TCP 或文件/TCP/IPC 档位，以及真实可用的增强能力 |
| warnings | ABI4/5 缺少 IPC、ABI4 缺少设备 ioctl 限制等非阻断提示 |
| backendPrerequisitesReady | 当前 linux/amd64 后端运行前提是否满足 |
| hostingAvailable | 已组装受限 Node runner 且前提满足时为 true；不代表完整 SDK 已交付 |
| stage | foundation / installation / static / backend；staticAvailable 独立报告静态可用性 |
| issues | 有界诊断码，不包含环境、凭证或任意进程输出 |

接口响应 no-store。平台 Host 来自固定 platform-origin，不采信 X-Forwarded-Host；未知或应用内容 Host 在平台 mux 前拒绝，不能借 `/api/admin/*`、`/healthz` 或 `/ws` 进入原 handler。loopback peer 对精确 loopback:平台端口的 GET /healthz 单独放行。来自内容域的 Origin 也不能进入平台 API/主 WS。

## Node 与 Linux 探测

Dockerfile 的独立构建阶段锁定 Node 官方制品版本和 SHA-256，只复制 node、LICENSE 与摘要，不复制 npm、头文件和编译器到运行层。Node/helper 及父目录为 root 所有；主 Go 程序仍 CGO_ENABLED=0。该阶段仅支持 linux/amd64，不向其他 program 包扩展 Node 分发。

辅助程序使用 C，在构建阶段编译。协议 v3 增加私有管道的 `--run`；旧 v2 仅探测 helper 不通过新宿主的版本准入。探测在一次性子进程施加 Landlock，实际检查：

- 放行文件可读，另一个文件不可读，放行文件仍不可写。
- 分配端口可 bind/connect，未授权 TCP bind/connect 被拒绝。
- ABI ≥ 6 时额外验证域外信号和抽象 Unix socket 访问被拒绝；ABI4/5 不宣称具有这些限制。

最低支持 ABI4：文件和 TCP 限制必须实际通过。ABI4 不传设备 ioctl 权限或 scoped 字段；ABI5 增加设备 ioctl 限制；ABI6 增加信号/抽象 socket 作用域。按实际 ABI 传入结构长度，探测协议为 v2，明确报告缺少的能力。低于 ABI4 或施加失败仍拒绝。检查 create/add/restrict 和拒绝结果，不凭 uname 判断。探测仅继承固定 PATH/LANG/LC_ALL，进程输出有界、5 秒超时；原始错误输出不回显。平台在已启用宿主下设置 dumpable=0，其余部署保持原行为。

正式和开发 Compose 使用 UID 10001、只读根、drop ALL、no-new-privileges 和 tmpfs；不增加特权或放宽 seccomp。`webapps/` 不论宿主是否开启都受普通工具保护；`webapp-data/` 不额外加保护，也不获得额外的工具授权。

## 验证状态

最终 `go test -p 2 ./...` 全量通过，Linux amd64 主程序已使用 CGO_ENABLED=0 交叉编译成功。初次高并发全量出现一个 connectorauth 登录用例超时，单例、整包和低并发全量复测通过，未放宽测试期限。

本机已编译 linux/amd64 辅助程序。在 macOS ARM Docker Desktop 的 amd64 仿真、非 root/只读根/drop ALL/默认 seccomp 条件下，Landlock 返回 ENOSYS，探测明确失败而未降级。原生 ARM Linux 容器探测内核可提供 ABI 6；这不等于支持 Platform linux/arm64 发行，也不能替代原生 linux/amd64 验收。

首次本机构建受 Docker Hub/代理带宽影响，随后已在 singapore02 原生构建并验证完整 Node 镜像。正式 release-image smoke、独立候选容器、生产 Node/ABI4 检查、HTTPS 首页、健康检查和未登录 API 401 均通过；完整 WebApp 安装、网关、SDK 和应用生命周期仍未交付。

ABI4 的适用范围为单 owner、来源可信应用。它缺少信号/抽象 Unix socket 作用域和 ABI5 设备 ioctl 限制；与同 UID 其他进程共享容器资源，不能作为不可信应用沙箱。差异依据 [Linux 6.8](https://docs.kernel.org/6.8/userspace-api/landlock.html) 与 [Linux 6.12](https://docs.kernel.org/6.12/userspace-api/landlock.html) 官方文档。

2026-10-31 singapore02 原生 linux/amd64（内核 6.8.0-1029-aws）实测：在 UID10001、只读根、drop ALL、no-new-privileges、默认 seccomp 容器中，协议 v2 探测返回 abi:4、ready:true、ipcScoped:false、deviceIoctlRestricted:false；文件与 TCP 核心限制通过，缺失能力按事实保留。

## singapore02 部署记录（2026-10-31）

- 公网入口：https://webclient.zenmind.cc；WebClient 保留 nginx 容器，Node 内置于 Platform 容器。
- Platform 镜像：agent-platform:v0.5.31，代码 revision 6fdf03c3d045bccef79a975cd3950f8036008fe4；Node v24.22.2。
- WebClient 镜像：agent-webclient:v0.5.31-node-20261031；源码 7bbd0641，生产 bundle main.d5c367b5.js。
- 原生 ABI4 在候选与生产的 UID10001、只读根、drop ALL/no-new-privileges 条件下通过核心文件/TCP 拒绝测试；ipcScoped 和 deviceIoctlRestricted 均为 false。
- 线上 Compose：/docker/agent-platform/compose.deploy.yml 与 /docker/agent-webclient/compose.yml；实际部署信息以 /docker/agent-platform/deployment.json 为准。
- 离线发布包：/docker/.releases/agent-platform/v0.5.31-6fdf03c3，已复核 checksums.sha256。
- 切换前已停止写入并保存运行数据、配置与旧镜像；回滚入口：/docker/.backups/zenmind-node-20261031-f3934349/rollback.sh。
- 当前 webapps.enabled 仍为 false，hostingAvailable 的设计状态仍是 false；这次交付证明运行时和前置限制可用，不代表应用托管功能已完成。

## 私有静态应用和管理界面

WebClient `/webapps` 仅在普通 Platform 模式展示；Desktop 沿用自己的 WebApp 页面。ZIP 上传后先审阅，明确安装才提交；首次安装 stopped。列表和详情提供启停、打开、配置、owner 授权与卸载；数据清理默认不选中。运行中修改 Node 配置会重启到新 generation；静态配置原子生效。授权变化立即撤销旧访问并重建 generation。

静态 start 校验不可变代码摘要、实际 Manifest 与安装记录一致，以及用户配置；stop 先撤销当前 generation 访问，再排空文件租约。关闭 Platform 保留 desiredState；重启恢复生成新的 generation，旧会话不恢复。

打开返回一次性表单 POST action，不在 URL、历史或持久存储中保存票据。应用端精确核验平台 Origin；兑换后设置每应用独立名字的 host-only HttpOnly、SameSite=Lax Cookie。本地 HTTP 不设置 Secure，限于上述 loopback 部署边界。票据一分钟过期、单次兑换；会话闲置三十分钟、最长八小时，绑定应用、generation、policy revision。停止及显式 `/sessions/revoke` 撤销访问。当前普通 Platform 没有统一登出回调，外部 SSO 登出不等同于此撤销接口；后续鉴权接入需同步处理。

静态只读 `frontend.root`，不列目录、不公开 Manifest/后端文件；只对 HTML 导航做 SPA fallback。应用端口不能进入平台管理路由，Host 必须精确匹配本监听端口；请求不采信转发头。业务写入/WS 要求应用精确 Origin，SDK 保留路径暂不转发。

回归已覆盖 Go race、共享契约、设置修订、停止排空与恢复、真实 loopback HTTP Host 校验、会话/票据过期及撤销。WebClient 类型、边界、国际化、定向测试及独立缓存的生产 build 通过；浏览器 fixture 验证安装后 stopped 展示、启动交互与访问范围页面，无脚本错误。整个 server 包首次回归出现已有连接数时序用例失败，单独复测该例与 WebApp 用例通过，未放宽断言。

## 受限 Node 监督（批次 3）

每应用 runner 从空环境构建变量，固定镜像 Node、权限参数、HOST/PORT、只读代码和配置、可写本应用数据及 generation 临时根；不运行 shell/npm，不给 child_process/addon/inspector 权限。Landlock 施加失败不执行 Node，默认只允许分配端口 bind 和显式授权 TCP connect；必要动态库、ELF 加载器、CA、OpenSSL 配置及 DNS 文件精确放行，不放行整个 /etc、/opt、/tmp 或 /proc。ABI4 仍不具备进程信号/抽象 socket scope，也不限制远端 IP/UDP；可信应用边界不变。

监督使用 watcher 与内部 subreaper，私有父进程存活/状态管道不进入 Node。两层 helper 持有独占 generation 锁，Node exec 前关闭所有非 stdio 管理 FD；清理以子进程收养、pidfd 与当前父身份校验为依据，不接管旧 PID。确认锁释放和无存活后代前，不释放端口或替换目录。启动恢复先检查旧 generation 锁，再回收临时根，保留持久数据。

启动先返回 starting，固定 loopback 健康检查并核验监听 socket 由本 Node 持有后才发布。持续健康和 RSS 监测失败时撤销访问，再清理并有界退避；显式停止取消重启。RSS 只是监测阈值，整容器资源限额仍是硬边界。stdout/stderr 有界轮转，owner 可读取尾部与游标，不按行积累无换行输出。

代理仅使用当前 generation 的 loopback 地址，按 backendPrefixes 路径段匹配，保留业务凭据并剥离宿主 Cookie、已验证平台 Authorization 与伪造身份/转发头；后端不能设置宿主或 Domain Cookie。停止显式关闭 Upgrade 的后端连接，SSE 立即 flush，均不回落到静态 SPA。

2026-11-01 在 singapore02 原生 linux/amd64、既有 v0.5.31 Node 镜像上覆盖新 helper 并运行新交叉编译测试：UID10001、init、只读根、drop ALL、no-new-privileges、默认 seccomp、network none、1 GiB/1 CPU。文件/端口拒绝、最小环境、HTTP 代理凭据、日志、WS/SSE 停止、新 generation、数据保留、恢复及两层监督进程分别 SIGKILL 后重启通过。单独 C harness 验证 setsid/double-fork 后代回收，保留无关 sibling。该隔离候选不挂载生产数据，不替代完整新镜像/业务 SDK/资源压力验收。

## 更新、配置和受理操作（批次 4）

长操作由 owner API 返回 202 operationId，再读取 `/operations/<id>`；浏览器取消等待不撤销已经受理的操作，也不重放 mutation。操作记录有界、内存保存；容器重启后根据安装记录、journal 和 desiredState 核对结果，不能凭 operationId 不存在自动重试。关闭宿主先取消并排空受理任务，同时停止所有 generation，再释放实例锁。

更新整个过程持有同应用控制锁，停止/配置/卸载不能插入目录切换。准备新版本时对外观察为 updating，只有新进程健康且 journal 提交、备份清理完成才公开。journal 记录原 desiredState；恢复未提交更新时同时恢复原代码、安装身份和原期望状态。回退不会撤销应用数据写入。

Node 每代使用宿主写入的只读用户配置快照，与本代可写 tmp 分离。缺省字段按当前 Manifest 补齐、移除字段不再投影，但原保存数据保留，保证前端 SDK 与后端读取一致，也能回退旧版本。快照及 generation 临时目录仅在确认旧写入者退出后清理；授权与用户配置变更均不扩大应用包已有授权。

原生 Linux 加固候选已通过运行中更新、新版本健康失败回退、只读配置快照、配置/授权导致 generation 变化、重启预算耗尽和停止取消 backoff。该结果仍不替代实际 Compose SIGKILL/重启和完整新镜像验收，后者留在批次 7。
