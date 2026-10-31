# WebApp 托管

## 当前实现范围

完整设计见 [Linux Docker Compose WebApp 托管方案](../plans/webapp-node-hosting.md)。当前落地的是前置基础：部署选择、严格配置、owner/Host 边界、镜像中的 Node 构建定义，以及 Linux 隔离能力探测。安装、静态/Node 网关、应用 SDK 和进程生命周期还未实现。

该能力只针对 Linux Compose。Desktop 继续使用 Electron 原有宿主；macOS、Windows 本机及 Linux 裸机运行 Platform 不自动启用，也不从 PATH 查找 Node。

## 开关与配置

RuntimeMode 仍是 standalone/desktop。新的独立启动选项为 `--webapp-host=disabled|compose`，默认 disabled；两份 Compose 模板选择 compose，但这不会自动打开功能。配置入口为 `configs/runtime.yml` 的 `webapps`，模板默认 `enabled: false`。

开启要求同时满足：

- 实际 Go 平台为 Linux，RuntimeMode 为 standalone，启动参数明确选择 compose。
- 鉴权开启，设置 issuer、audience、require-expiry，以及恰好一个非空 allowed-subjects owner。
- platform-origin 为不含路径、查询或端口的规范 HTTPS origin；content-domain 为另一注册域名下的内容子域；instance-slug 是不超过 45 字符的 DNS label。
- resource.public-base-url 如有配置，与 platform-origin 一致。
- 配置限额合法；端口范围足以分配 max-running 个非特权端口。

`webapps` 拒绝未知字段、错误大小写、null、错误类型和越界限额。引号中的布尔/数字保留字符串类型，不沿用旧节的布尔强转。目录固定从 AP_RUNTIME_DIR 派生 webapps/webapp-data，管理目录来自有效 StateDir/webapps；不提供 YAML paths 或新目录环境变量。

关闭时不运行探测、不创建 WebApp 数据目录、不注册诊断接口，也不改变现有 Host 行为。非法部署强制开启会阻止启动。合法 Linux Compose 中 Node/隔离前提不满足仅反映为诊断不可用，不把失败降级为无隔离执行。

## owner 诊断接口

`GET /api/admin/webapps/host` 只在启用后注册，要求普通 Platform JWT 经现有 verifier 验证，subject 必须等于唯一 owner；不接受 WebApp 或连接器执行凭证。

响应沿用 `{code,msg,data}`。data 字段：

| 字段 | 含义 |
| --- | --- |
| host / os / arch | 显式宿主、实际操作系统和架构 |
| nodeVersion / nodeReady | 锁定 Node 版本及受管制品检查结果 |
| supervisorReady | 独立探测辅助程序的完整性检查结果 |
| landlockAbi / isolationReady | 实测 ABI 与受限子进程实际拒绝行为 |
| backendPrerequisitesReady | 当前 linux/amd64 后端运行前提是否满足 |
| hostingAvailable | 当前固定 false，不能据此认为安装/托管已实现 |
| stage | 当前 foundation |
| issues | 有界诊断码，不包含环境、凭证或任意进程输出 |

接口响应 no-store。平台 Host 来自固定 platform-origin，不采信 X-Forwarded-Host；未知或应用内容 Host 在平台 mux 前拒绝，不能借 `/api/admin/*`、`/healthz` 或 `/ws` 进入原 handler。loopback peer 对精确 loopback:平台端口的 GET /healthz 单独放行。来自内容域的 Origin 也不能进入平台 API/主 WS。

## Node 与 Linux 探测

Dockerfile 的独立构建阶段锁定 Node 官方制品版本和 SHA-256，只复制 node、LICENSE 与摘要，不复制 npm、头文件和编译器到运行层。Node/helper 及父目录为 root 所有；主 Go 程序仍 CGO_ENABLED=0。该阶段仅支持 linux/amd64，不向其他 program 包扩展 Node 分发。

辅助程序使用 C，在构建阶段编译。当前只支持 `--version` 和 `--probe`，尚不支持运行应用。探测在一次性子进程施加 Landlock，实际检查：

- 放行文件可读，另一个文件不可读，放行文件仍不可写。
- 分配端口可 bind/connect，未授权 TCP bind/connect 被拒绝。
- 域外信号和抽象 Unix socket 访问被拒绝。

要求 ABI 至少 6，检查 create/add/restrict 和拒绝结果，不凭 uname 判断。探测仅继承固定 PATH/LANG/LC_ALL，进程输出有界、5 秒超时；原始错误输出不回显。平台在已启用宿主下设置 dumpable=0，其余部署保持原行为。

正式和开发 Compose 使用 UID 10001、只读根、drop ALL、no-new-privileges 和 tmpfs；不增加特权或放宽 seccomp。已发布代码和管理状态加入普通工具保护，持久数据不获得额外工具授权。

## 验证状态

最终 `go test -p 2 ./...` 全量通过，Linux amd64 主程序已使用 CGO_ENABLED=0 交叉编译成功。初次高并发全量出现一个 connectorauth 登录用例超时，单例、整包和低并发全量复测通过，未放宽测试期限。

本机已编译 linux/amd64 辅助程序。在 macOS ARM Docker Desktop 的 amd64 仿真、非 root/只读根/drop ALL/默认 seccomp 条件下，Landlock 返回 ENOSYS，探测明确失败而未降级。原生 ARM Linux 容器探测内核可提供 ABI 6；这不等于支持 Platform linux/arm64 发行，也不能替代原生 linux/amd64 验收。

锁定 Node 官方校验值已核对，但本次完整镜像构建受 Docker Hub token 访问超时阻断，不能宣称镜像已交付或目标服务器隔离已通过。后续须在网络可用的原生 Linux 环境执行完整构建及 Compose 集成验收。
