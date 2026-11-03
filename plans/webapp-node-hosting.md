# WebApp 托管：剩余工作

> 2026-11-02 精简。原计划批次 0–7（本地端口范围）已完成，并在原生 Linux 候选上验收。已实现范围、验证记录与限制以 [WebApp 托管](../docs/WebApp托管.md) 为准；原完整方案（含设计取舍与调研记录）：`git show 6a490a32:plans/webapp-node-hosting.md`。
> 剩下两件事：每应用独立域名、生产切换。两项都完成并同步文档后删除本文。

## 1. 独立域名

2026-11-01 用户确认先做每应用 `http://127.0.0.1:<port>` 本地 origin，独立域名延期。本地模式只靠不同 Cookie 名、绑定 generation 和精确 Origin 区分应用，不是跨注册域隔离；远程使用靠 SSH 转发。

要做：

- 域名规则：平台如 `demo.platform.example.com`，应用为 `<instance-slug>--<appHex>.<content-domain>`，appHex 取规范 ID 的 16 位十六进制（不用可变的 label/key）。内容域与平台使用不同注册域名，不能把同站 Cookie 策略当隔离。`webapps.content-domain`、`instance-slug` 字段已保留；配置校验 Host 长度、字符、实例唯一性和平台/内容域冲突。
- 入口分发：Host 分类先于平台路由和鉴权（平台 Host 与未知 Host 拒绝已实现）。应用 Host 只映射已安装应用；应用 Host 下的 `/api/admin/*` 只能命中应用后端或返回应用 404。只用 nginx 重写后的 Host；转发头仅在 peer 属于配置的代理集合时读取，并剥离客户端原值。多实例共享内容域时按 instance-slug 有界匹配，不交给默认实例。
- 会话：launch ticket 用顶层 POST 跨注册域兑换，票据不进 URL、Referer 或日志；成功后设 `__Host-ap-webapp`（Secure、HttpOnly、Path=/、无 Domain、SameSite=Lax），303 到应用内部安全路径。未登录的顶层 HTML 导航跳平台登录/打开入口，API/WS 返回 401；不接受任意 returnUrl。
- 平台边界：主 WS 和平台 API 继续拒绝内容域 Origin，不把应用 origin 批量加入平台 CORS。
- 登出：普通 Platform 现在没有统一登出回调，接入时让 owner 登出撤销应用会话。
- 部署：owner 负责 DNS、内容域 wildcard 证书和 nginx 路由；补 ingress 模板与说明（原计划中的 INGRESS.md 尚不存在）。nginx 上传上限与 `max-archive-mib` 对齐；仍只公开 8080，不暴露 Node 端口；安装不调用 nginx、不签证书。

验收：未知/伪造 Host、代理头伪造、应用域进不了平台 API 和主 WS、跨注册域 launch POST、票据一次性/过期/串应用拒绝、`__Host-` Cookie、撤销、CSRF；在真实 DNS/TLS/nginx 下做浏览器回归。

## 2. 生产切换

生产 singapore02 仍是 v0.5.31 镜像、`webapps.enabled: false`；托管功能只在独立的 `agent-platform-webapp-acceptance` Compose 项目验收过。

切换前确认：

1. 目标机内核/LSM/Landlock ABI 与 Docker/seccomp 可用性（已知 ABI4，ipcScoped、deviceIoctlRestricted 为 false）。
2. 发布选定的 Node 24 补丁版本、官方 SHA、镜像体积和漏洞扫描。
3. 默认实例（4 GiB / 2 CPU / 1024 pids）下，参考应用与 Agent 并发时的资源和故障恢复表现。
4. 按 `scripts/release-assets/image/DEPLOY.md` 停写、备份配置与 data、记录镜像摘要，保留回滚入口。

## 3. 文档收尾

- 独立域名完成后，更新 docs/WebApp托管.md、docs/API与协议.md、DEPLOY.md、README.md、AGENTS.md 中“域名延期”的表述。
- docs/运行时和沙箱.md 补 WebApp 宿主与 Agent sandbox 的分工；docs/云端部署与持续任务架构设计.md 补同容器托管与单 owner 信任边界（两份目前都没提 WebApp）。
