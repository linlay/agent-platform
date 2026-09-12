# HTTP 客户端与系统代理

Platform 通过 `internal/httpclient` 统一创建出站 HTTP 客户端。独立启动与 Desktop 启动使用相同的默认行为，Desktop 无需传入额外代理参数。该实现兼容 `CGO_ENABLED=0`。

## 配置

配置入口是 `<config-dir>/configs/runtime.yml` 的顶层 `http-proxy`。整个配置节可以省略，默认值见 `internal/config/config_defaults.go` 和 `configs/runtime.example.yml`。

```yaml
http-proxy:
  mode: auto
  system-refresh-interval: 15s
```

| 字段 | 含义 |
| --- | --- |
| `mode` | `auto` 自动解析；`direct` 强制直连；`fixed` 指定代理 |
| `url` | 仅 `fixed` 模式使用且必填，支持 `http://`、`https://`、`socks5://`、`socks5h://`；省略协议时按 HTTP 代理解释 |
| `bypass` | 仅 `fixed` 模式使用，可选字符串数组，规则遵循 Go `NO_PROXY` 语义 |
| `system-refresh-interval` | 系统设置缓存有效期，默认 `15s`，必须为正时长 |

例如：

```yaml
http-proxy:
  mode: fixed
  url: http://127.0.0.1:10809
  bypass: [localhost, "*.example.internal"]
```

修改 YAML 需要重启 Platform。系统代理开关、地址、端口和绕过规则会自动刷新，无需重启。含代理凭据的真实配置不得提交；日志不输出代理用户名或密码。

## 解析顺序

localhost、loopback 地址始终直连；专用内部通信客户端也始终直连。其他请求依次执行：

1. `direct` 直接返回直连；`fixed` 只使用配置的 `url` 与 `bypass`。
2. `auto` 先判断 `NO_PROXY`，命中后直接结束解析；即使没有同时设置环境代理也生效。
3. 根据目标协议选择 `HTTP_PROXY` 或 `HTTPS_PROXY`。支持对应小写变量，大写非空值优先。HTTP 与 HTTPS 独立解析，不以 `HTTP_PROXY` 代替缺失的 `HTTPS_PROXY`。
4. 当前协议未配置环境代理时，读取系统固定代理与系统绕过规则；对应 HTTP/HTTPS 代理未启用时可使用系统 SOCKS 代理。
5. Windows 存在自动代理配置且没有适用固定代理时，按请求 URL 调用 WinHTTP 解析 PAC/WPAD；只有系统明确返回直连才直连。
6. 没有适用代理或自动代理配置时直连。

`NO_PROXY` 支持域名、子域、IP、CIDR、可选端口和 `*`，复用 Go 的 `golang.org/x/net/http/httpproxy` 匹配器。仅系统代理被选用时才应用系统绕过规则。进程环境在工厂初始化时取快照；Agent/run 的环境变量不改变 Platform 自身 HTTP 路由。`ALL_PROXY` 不属于本次支持范围。

代理 URL 非法、系统读取失败、选中的代理连接失败时均返回错误，不尝试另一个来源或直连。系统读取错误也按缓存周期缓存，下次到期重试；不会拿旧代理或空设置掩盖错误。

## 平台适配与限制

| 平台 | 首期实现 |
| --- | --- |
| macOS | 调用系统自带 `/usr/sbin/scutil --proxy`，解析全局 HTTP、HTTPS、SOCKS、ExceptionsList 与 ExcludeSimpleHostnames |
| Windows | 无 CGO 调用 `WinHttpGetIEProxyConfigForCurrentUser` 读取当前进程用户的代理设置，通过 `WinHttpGetProxyForUrl` 执行 PAC 或 DHCP/DNS WPAD，并释放 API 返回的内存 |
| Linux / 容器及其他系统 | 显式配置与环境变量；没有额外的系统设置读取器 |

macOS 命令不经过 Shell，读取期限为 2 秒。系统快照在缓存到期后的首个请求中刷新，并发请求共用同一读取结果，不会每个请求都执行系统命令。工厂还提供 `Refresh()` 主动使缓存失效；目前没有新增 HTTP 管理接口。

系统绕过支持精确主机/IP、通配符、CIDR（包括 macOS 常见的 `169.254/16`）、可选协议与端口，以及 Windows `<local>` / macOS 简单主机名排除。匹配不会额外解析 DNS，CIDR 针对 URL 中的 IP 地址生效。macOS 的接口级 `__SCOPED__` 和补充配置暂不解析。

**Windows 已接入原生 PAC/WPAD 解析。** 不新增外部程序或依赖，保持 `CGO_ENABLED=0`。自动代理在已有显式配置、环境变量、系统绕过和适用固定代理之后执行；固定代理与 PAC 同时启用时仍优先固定代理。按完整请求 URL（移除用户信息及 fragment）解析，不按主机缓存路由结果，避免不同路径的 PAC 规则串用。系统配置继续按默认 15 秒周期刷新；每次自动解析创建独立 WinHTTP session，不由 Platform 持久缓存 PAC 脚本。

自动解析包含排队的调用等待上限为 10 秒，也遵守请求取消；进程最多同时保留 4 个原生解析调用。WinHTTP 同步调用可能在调用方超时后继续执行，由原 worker 回收 session 和返回内存，期间仍占用名额，避免无限堆积；不强制中断系统 PAC 执行。原生 session 的解析、连接、发送和接收超时分别设为 5 秒，这不是整个 PAC 执行的硬期限。

PAC 返回明确的 `DIRECT` 时直连；返回代理列表时选择第一个适用入口。解析错误、脚本下载失败、WPAD 发现失败及选中代理连接失败均报错，不隐式直连。暂不支持列表内代理故障切换，不自动重放模型 POST。PAC 下载不自动发送当前用户的 Windows 登录凭据，要求集成认证的 PAC 服务可能返回授权失败；这与模型请求经过代理时的认证是两个不同环节。

macOS 仍只支持固定系统代理与绕过，PAC/WPAD 尚未实现；只有自动代理时明确报错，可用显式固定代理、环境变量或 `mode: direct` 覆盖。

Windows 服务账户读取自身设置，不自动读取另一个已登录用户的配置。原生 PAC 集成测试位于 `internal/httpclient/autoproxy_windows_test.go`；本次 macOS 开发环境仅验证公共回归和 Windows 交叉编译，真实 Windows PAC 执行及企业网络 DHCP/DNS WPAD 仍需在目标系统验证。接口级代理与原生系统变更通知尚未实现。

## 客户端与生命周期

已接入的 HTTP 请求包括模型流、模型工具和图片请求、Web Fetch、KBASE Embedding、远程 MCP、连接器 OAuth、远端 VIEW/Viewport、产物推送，以及 HTTP Proxy Agent 与资源下载。

Container Hub、Identity/JWKS 和 provider registration、KBASE Lance 本机 sidecar、健康检查使用专用直连客户端。通用客户端也自动排除 loopback，避免本机模型或服务被代理。

客户端工厂共享连接池，克隆标准 Transport 并替换代理解析；不修改 `http.DefaultClient`、`http.DefaultTransport` 或 Platform 进程环境。MCP 按服务器克隆 Transport 时保留解析器、TLS 参数和连接超时覆盖。

工厂没有增加统一总超时。模型首响应与流空闲超时、Embedding 请求期限、MCP 工具期限以及各有限请求的原有总超时仍由原模块管理。刷新只影响后续请求；已有连接按所选代理分池，正在读取的 SSE 不会因刷新被取消。

这次统一的是 Platform 自身的 HTTP。Gateway/Proxy 的 WebSocket 拨号、Agent Bash、外部 CLI/stdio MCP、Container 内进程及 Rust sidecar 自己发起的请求不继承这层代理解析。独立 `connector-manage` 命令仍按其原有部署参数工作，不加载 runtime YAML；它创建的 HTTP 客户端使用默认自动解析。

## 诊断与验证

请求失败日志和错误包含 `source`、去除用户信息的 `proxy`、`stage`。阶段区分代理解析、代理连接、CONNECT/TLS、请求发送、响应头等待和响应体读取；HTTP 407 另记 `proxy-auth`。网络错误保留底层错误链供超时分类，普通日志不打印可能包含凭据的原始上游错误。路由选择另外提供 `slog.Debug` 记录。

回归测试位于 `internal/httpclient` 与 `internal/config/config_http_test.go`，覆盖优先级、单独 `NO_PROXY`、绕过、缓存并发/刷新/错误、macOS/Windows 设置解析、HTTP 代理、HTTPS CONNECT、SOCKS5 远端 DNS、无直连回退和刷新期间 SSE 保持。

接口依据：[Go ProxyFromEnvironment](https://pkg.go.dev/net/http#ProxyFromEnvironment)、macOS 本机 `scutil(8)` 手册、[WinHttpGetIEProxyConfigForCurrentUser](https://learn.microsoft.com/en-us/windows/win32/api/winhttp/nf-winhttp-winhttpgetieproxyconfigforcurrentuser)、[WinHttpGetProxyForUrl](https://learn.microsoft.com/en-us/windows/win32/api/winhttp/nf-winhttp-winhttpgetproxyforurl)。
