# WebApp 托管

## 当前实现范围

完整设计见 [Linux Docker Compose WebApp 托管方案](../plans/webapp-node-hosting.md)。当前代码已接入部署选择、严格配置、owner/Host 边界、Node 制品与隔离探测，以及 owner ZIP 上传、预检、安装、停止状态更新和卸载事务。静态/Node 网关、应用 SDK 和进程生命周期继续按后续批次实现，不能把安装管理视为完整托管交付。

共享 Manifest 已补齐 Linux 目标及合法/非法输入 fixture，builder 同步离线纯 JS 包约束；Desktop 仍拒绝安装 Linux 目标包。Go 安装器嵌入 Desktop 生成的 schema 并消费同一 fixture；`scripts/sync-webapp-contracts.py --check --desktop-root <Desktop源码根>` 检查复制漂移，不在生产环境读取相邻源码。小批次边界与验证限制见计划第 21 节。

## 安装事务与数据所有权

`internal/webapp` 独占安装代码、期望状态和应用授权；`app.New` 只在合法启用后创建服务，并持有运行根下的实例独占锁。Server 只校验 owner、来源和请求形状，不解析包或执行安装。前端与后端能力仍由后续网关和桥接模块负责。

上传是有界 ZIP 字节流，不接受服务器路径。预检不执行代码、不创建安装记录；提交绑定上传摘要与当前安装 revision。解包拒绝穿越、反斜杠、大小写碰撞、链接、特殊文件、超量展开、秘密文件和原生载荷；Compose 子集拒绝其他平台、非 Node launcher 与执行链环境覆盖。

首次安装固定 stopped。更新当前要求先 stopped，保留安装身份、授权、用户配置和持久数据；运行中更新随生命周期批次接入。只读代码在同一文件系统的 staging 与 app 之间改名，journal 在切换前持久化，安装记录、代码摘要及目录同步完成后才提交。中断的未提交事务恢复原代码与记录；已提交事务只重做清理。恢复结果不确定时停止后续 mutation，保留现场，不再叠加安装。

卸载使用独立删除 journal，持久数据默认保留，只有 owner 显式 `purgeData:true` 才删除。重新安装生成新的调用收据 namespace，正常更新保持原安装身份。代码回滚不撤销应用业务数据迁移。

该能力只针对 Linux Compose。Desktop 继续使用 Electron 原有宿主；macOS、Windows 本机及 Linux 裸机运行 Platform 不自动启用，也不从 PATH 查找 Node。

## 开关与配置

RuntimeMode 仍是 standalone/desktop。新的独立启动选项为 `--webapp-host=disabled|compose`，默认 disabled；两份 Compose 模板选择 compose，但这不会自动打开功能。配置入口为 `configs/runtime.yml` 的 `webapps`，模板默认 `enabled: false`。

开启要求同时满足：

- 实际 Go 平台为 Linux，RuntimeMode 为 standalone，启动参数明确选择 compose。
- 鉴权开启，设置 issuer、audience、require-expiry，以及恰好一个非空 allowed-subjects owner。
- platform-origin 为不含路径、查询或端口的规范 HTTPS origin；content-domain 为另一注册域名下的内容子域；instance-slug 是不超过 45 字符的 DNS label。
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
| hostingAvailable | 当前固定 false，不能据此认为安装/托管已实现 |
| stage | 未组装安装服务时 foundation；安装服务就绪时 installation |
| issues | 有界诊断码，不包含环境、凭证或任意进程输出 |

接口响应 no-store。平台 Host 来自固定 platform-origin，不采信 X-Forwarded-Host；未知或应用内容 Host 在平台 mux 前拒绝，不能借 `/api/admin/*`、`/healthz` 或 `/ws` 进入原 handler。loopback peer 对精确 loopback:平台端口的 GET /healthz 单独放行。来自内容域的 Origin 也不能进入平台 API/主 WS。

## Node 与 Linux 探测

Dockerfile 的独立构建阶段锁定 Node 官方制品版本和 SHA-256，只复制 node、LICENSE 与摘要，不复制 npm、头文件和编译器到运行层。Node/helper 及父目录为 root 所有；主 Go 程序仍 CGO_ENABLED=0。该阶段仅支持 linux/amd64，不向其他 program 包扩展 Node 分发。

辅助程序使用 C，在构建阶段编译。当前只支持 `--version` 和 `--probe`，尚不支持运行应用。探测在一次性子进程施加 Landlock，实际检查：

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
