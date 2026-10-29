# AP_ID_TOKEN 一次性切换方案与实施记录

> 决策日期：2026-10-29。用户确定不做旧名称兼容，身份变量与模板直接统一为 `AP_ID_TOKEN`。
> 分析基线：`8a514534`（v0.5.28）。本文件取代之前的“旧模板兼容”建议。
> 当前为代码实施记录；测试和外部包验证结果见末尾，真实 SSO 业务验收单独记录。

## 1. 最终契约

- 平台仅提供 `AP_ID_TOKEN` 环境变量和 `${AP_ID_TOKEN}` 身份模板。
- 不提供 `AP_ACCESS_TOKEN` 或 `ONEID_TOKEN` 别名，不做旧模板归一，不做双变量注入，也不从旧环境变量回退读取身份。
- 旧名只保留在拒绝测试、迁移说明和历史发布包中，不保留旧 Go 常量或运行时兼容分支。
- `EnvAccessToken` 改为 `EnvIDToken`，身份文件源码与测试成组改为 `id_token*.go`。
- OAuth 的内部 `ACCESS_TOKEN`、`${OAUTH_ACCESS_TOKEN}`、OAuth HTTP 字段以及 Provider 的 `useSsoIdToken` 保持现有语义。
- Desktop 继续写 `sso-id-token.txt`，继续通过 `--sso-id-token-file` 传绝对路径，不新增启动参数、凭据副本或持久化迁移。

这是外部程序环境契约的直接变更：使用旧模板的包和直接读取旧变量的 CLI/SDK 必须同步升级。

## 2. 执行与模板矩阵

| 执行面 | 目标行为 |
| --- | --- |
| 已验证的单条直接 oneid Host CLI | 启动前读取一次身份，注入 `AP_ID_TOKEN`；自定义凭据 env 与默认变量使用同一快照 |
| oneid stdio MCP | 进程创建前使用当前身份；下次调用检测轮换并重建进程；身份不可用时拒绝新调用并 retire 旧 session |
| oneid HTTP MCP | 每次真实请求读取当前文件，为完整 HTTPS endpoint 生成 Bearer；动态 Header 与 Authorization 使用同一快照 |
| 普通 Bash、CLI lifecycle、Workspace Terminal、非 oneid MCP | 不继承、注入或允许用户覆盖 `AP_ID_TOKEN` |
| Container、Proxy/ACP、LSP、文件搜索、普通 sidecar | 不因改名扩大身份读取或注入范围 |
| Provider、KBX、memory worker | 沿现有 `provider.Credential()` 等授权路径读取身份，不改为依赖进程变量 |

| 声明位置 | 唯一合法身份模板 | 旧两个模板 |
| --- | --- | --- |
| cli.json env 值 | `${AP_ID_TOKEN}` | 拒绝 |
| mcp.json stdio env 值 | `${AP_ID_TOKEN}` | 拒绝 |
| HTTP 动态 Authorization | 省略，或精确 `Bearer ${AP_ID_TOKEN}` | 拒绝 |
| HTTP 其他动态 Header 值 | `${AP_ID_TOKEN}` | 拒绝 |
| URL、静态字段、command、args | 不增加身份模板展开，保留原声明限制 | 不转换、不提供身份 |

模板名称大小写敏感；保留环境变量键的拒绝与剥离大小写不敏感。Authorization 的 Header 名大小写不敏感，重复大小写变体冲突仍拒绝；值不宽松 trim，不接受静态 Bearer、前后空格或附加后缀。

CLI/stdio env 和普通动态 Header 保留原有字面前后缀组合能力；不新增 Shell 求值或递归模板展开。自定义 `SERVICE_TOKEN` 这类凭据映射只进入本连接器自己的子进程，不进入公开 CLI env、普通 Bash 或 Terminal。

## 3. 实施发现与处理

### 3.1 环境继承缺口

原 `shellenv.UnsafeOverride` 不识别身份变量。管理员将精确身份名、`AP_*` 或 `*` 加入 inherit-env 时，基础环境可能继承父进程身份值；默认名单测试不足以覆盖。

处理：在低层 `shellenv` 定义唯一的 `EnvIDToken` 常量，`UnsafeOverride` 显式拒绝，继承过滤不受名单或通配符放宽。`agentconfig.EnvIDToken` 引用该常量。普通 Bash 与 CLI 公共基础环境在最终返回前再次剥离身份值。

### 3.2 同一 CLI 调用读取两次身份

原直接 CLI 先在 connectorauth 中解析模板，再在 tools 中重新读取文件注入默认变量。文件在两次读取间轮换会造成变量不一致。

处理：`ResolveEnvironmentWithRevision` 在 oneid 下始终读取一次非空身份，即使没有自定义 env 模板也不提前返回；用此快照解析自定义变量并返回 `AP_ID_TOKEN`。tools 删除第二次读取。文件缺失、空、非法或不可读时在启动进程前报错。

复核补齐：catalog 对所有带 CLI 组件的 oneid 包保留凭据绑定，不再仅以自定义凭据 env 非空为条件。无模板的直接 CLI 同样读取身份、缺身份拒绝启动，且只允许单条直接调用；纯 MCP 包不生成空的 CLI 凭据绑定。回归测试从真实 catalog 加载路径获取绑定后执行 CLI，不仅手工构造下游状态。

### 3.3 诊断表与实际环境分开校验

原 `hostshell.KnownVariables` 只是诊断变量表，不能当成实际进程剥离的证明。此次分别覆盖真实基础环境、审批变量表、Terminal 两层环境、MCP 子进程；诊断表移除新名的所有大小写变体。

### 3.4 保持执行授权边界

通用 `mcp.NewOperationClient` 对 oneid 的拒绝保持不变，不向 connectorops 或 VIEW 增加身份能力。CLI 的公共/lifecycle 环境不改为自动注入身份。已有旧 manifest 结构的读取范围不扩展，但其中的身份模板也只允许新名。

复核后明确 connectorops/VIEW 的 oneid 边界：CLI、HTTP MCP 和 stdio MCP 无论是否声明凭据模板、Desktop 是否登录，都在配置、身份状态检查和创建幂等收据前返回 `connector_oneid_unsupported`（HTTP 422）。这是对 CLI 无模板时旧有无身份执行行为的显式调整，也把 MCP 原有的执行拒绝提前，避免误报需要登录或把确定未启动的写入记成结果未知。通用执行目录保留连接器 ID 与 revision，但 `adapters`、`components` 为空数组；Agent 直接 CLI 和已挂载 MCP 的身份链路保持原合同。不清理已有未完成收据。

## 4. 模块改动

| 模块 | 改动 |
| --- | --- |
| `internal/shellenv` | 统一身份名称，阻止精确和通配继承 |
| `internal/agentconfig` | 新 Go 常量、新文件名，读取与受管注入只使用新名，修正普通 Host Shell 注入的过时注释 |
| `internal/runenv` | 新名硬拒绝，复用共享常量；不影响原有名称格式和 deny-keys 规则 |
| `internal/connector` | CLI 模板只声明新名，删除 ONEID_TOKEN 转换；旧 manifest 中删除旧身份模板迁移 |
| `internal/connectorauth` | 一次身份快照同时供应默认与自定义变量；状态检查读取新名；公共环境剥离新名 |
| `internal/tools` | 直接 CLI 删除重复读取；普通环境、审批表、Container 保留变量校验统一新名 |
| `internal/mcp` | 只校验和展开新名，删除 Authorization 旧模板归一，stdio/HTTP/状态错误同步改名 |
| `internal/hostshell`、`terminal`、`server` | 诊断与终端过滤新名；导入校验与运行时接受范围一致 |
| `cmd/agent-platform` | connector-manage 状态回归与新变量测试 |

`ReadSsoIdTokenFile`、`ReadIdentityEnvironment`、`WithIdentityEnvironment` 名称保留。凭据仍来自有效身份文件，不修改 Platform 全局环境，不写入原包、运行包或 `.state/connectors`。Windows 的 `FILE_SHARE_DELETE`、文件读取上限与原子替换行为保持原实现。

## 5. 已知外部包升级

仅发现 gxj-flowcenter 的当前源码和 1.0.0 发布包直接声明旧 Bearer 模板。

- 开发源码升级为 **1.0.1**，mcp.json 改为 `Bearer ${AP_ID_TOKEN}`。
- 新增 `market/connectors/gxj-flowcenter/1.0.1/universal/` 的源码和 ZIP，更新当前 index、校验和与 README。
- 1.0.0 历史文件不覆盖；它使用旧模板，在新 Platform 上不再受支持。
- 不重打包无关连接器，不修改已有包摘要，也不发布到远端市场。
- 当前 manifest 没有最低 Platform 版本协议，不凭空添加一个不会被安装器执行的字段；README/索引说明新版包要求支持 AP_ID_TOKEN 的 Platform。
- online-pptx/docx/xlsx 的说明同步新名；Desktop 身份文档同步正确的受限 CLI/MCP 注入边界。

| 包/程序 | 旧 Platform v0.5.28 | 新 Platform |
| --- | --- | --- |
| gxj-flowcenter 1.0.0，旧 Bearer 模板 | 支持 | 拒绝旧模板，须升级 |
| gxj-flowcenter 1.0.1，新 Bearer 模板 | 不识别新模板 | 支持 |
| CLI/SDK 只读新名 | 不获得身份 | 获得受管身份 |
| CLI/SDK 只读旧名 | 依旧版契约 | 不获得受管身份，须升级 |
| oneid HTTP 包省略 Authorization | 支持 | 支持；本次不要求所有包改为这种写法 |

这里只表示声明和身份传递协议；真实账号业务权限需单独验证。

## 6. 文档同步

同步 README、AGENTS 以及连接器、安装与授权、运行时组装、运行时和沙箱、鉴权边界、MCP、智能体配置、配置化说明和手工测试文档。

安装与授权文档统一说明只接受新名，消除原来两处 Authorization 模板描述的矛盾。AGENTS 仅更新当前事实与专题入口。旧名在正式文档中仅用于明确拒绝和升级说明，不再出现在正常示例中。

## 7. 验证与发布

必须覆盖：

1. 用户配置、动态 run_env、父环境及大小写变体不能覆盖新名；精确、前缀和全通配继承都无法放行。
2. 新名 CLI 模板属于凭据层，公共 env 无凭据；旧两名模板在 CLI、stdio、HTTP 和管理导入入口拒绝。
3. 直接 CLI 能得到新默认变量与自定义映射；文件轮换使用新值，缺失/为空/非法时无新进程启动；没有模板时同样拒绝无身份执行。
4. stdio helper 验证真实子进程环境、身份变化重建、未变化复用、注销后拒绝新调用及旧进程回收。
5. HTTP fake RoundTripper 验证当前 Bearer、自定义 Header、注销后不发送、完整 endpoint、query、端口及目的地限制。
6. Terminal、普通 Bash、审批表、非 oneid MCP 不获得身份；原包和 Platform 全局环境不被改写；状态目录没有 token 副本。
7. Provider、KBX、memory 和 OAuth 原路径回归；Windows 原子替换测试与目标平台构建。
8. 新市场包源码、ZIP 和索引摘要一致；历史包保持原字节，能够验证新包成功与旧模板拒绝。

发布时先更新使用旧名的连接器和可执行消费者，再与 Platform 同批切换；切换进程使旧 stdio 会话退出，不迁移未完成 Run。回滚需要同时恢复旧 Platform 与匹配的旧包/程序，不清空身份文件或连接器状态。

本次没有数据库、身份文件格式或凭据存储迁移。跨平台编译不代替 Windows 运行验收；单元测试与本地包校验不代表真实 SSO 账号业务验收。

## 8. 验证记录

2026-10-29 已完成：

- 核心身份、环境、连接器、MCP、工具、Terminal、命令行相关包回归通过；Server 中的身份模板导入和终端环境测试在当前工作区单独通过。
- `go test -race ./internal/agentconfig ./internal/shellenv ./internal/connectorauth ./internal/mcp` 通过。
- `make test-memory-integration` 通过，使用仓库受管 memx 二进制。
- 从 `8a514534` 创建临时源码副本，只覆盖本次 34 个 Go 源码/测试文件并移除原身份文件名；`go test -p 2 ./...` 全量通过。该验证不包含同工作区同时进行的 accessConfig 修改。
- `CGO_ENABLED=0` 的 macOS arm64/amd64、Linux amd64、Windows amd64/arm64 构建通过；Windows 身份文件测试交叉编译通过，未在 Windows 实机运行。
- 用实际候选 Platform 二进制导入新 1.0.1 ZIP 成功，导入旧 1.0.0 ZIP 得到 `oneid only supplies AP_ID_TOKEN` 且未发布包。
- 命令行授权状态随合成身份文件创建、轮换、删除在 authorized/unauthorized 间变化；父进程的伪造新变量不作为凭据，无连接器 credentials/oauth 文件副本。
- 新包源码、ZIP 与市场索引 SHA-256 一致；旧 1.0.0 ZIP 的 SHA-256 仍为 `d56711c4902e9339a4a5c97ce5d3db1b502a3622b103d4a6b089ed0197532f3f`。
- 生产 Go 源码中无旧身份变量名、旧模板别名或旧 Go 符号，旧名只存在于拒绝用例、迁移说明和历史包。`git diff --check` 通过。

当前工作区首次全量检查遇到 hostenv 探测计时和 Server 事件顺序用例失败，串行复查又遇到同时进行的 accessConfig 测试编译错误。最终 `go test -p 2 ./...` 复查时，hostenv、Server 及其他包均通过，仅 `internal/tools/TestNativeConnectorToolsDeferToTheirSkill` 失败：chat_send/accessLevel、chat_start/accessLevel 的说明均为 135 字符，超过该用例上限。本次未修改这些说明；本次身份改名在上述干净基线副本中全量通过。

本次没有重启运行中的 Platform，也没有发布远端市场。真实 SSO 业务调用、Windows 实机行为和客户私有 SDK 分发未验证。

### 8.1 复核缺陷修复验证

2026-10-29 补齐 catalog 无模板 oneid CLI 绑定、connectorops 的显式拒绝及终端测试 gofmt 后，在当前工作区验证：

- 新增 catalog 测试覆盖无模板、仅公开 env、凭据模板、纯 MCP 与非 oneid CLI；工具回归测试改用真实 catalog 生成的绑定，覆盖身份注入、缺身份拒绝启动和复合调用限制。新增测试在修复前复现缺陷，修复后通过。
- connectorops 测试覆盖有无模板、Desktop 登录与未登录、无幂等键与重复写请求：统一返回不支持错误，没有生成写入收据；无执行权限时仍先返回权限错误。后续扩展到所有 adapter，当前统一错误码为 `connector_oneid_unsupported`（422）。
- `go test ./internal/catalog ./internal/connectorops ./internal/connectorauth ./internal/viewquery ./internal/viewaction ./internal/tools ./internal/server` 全部通过。
- `go vet ./internal/catalog ./internal/connectorops ./internal/tools ./internal/server` 通过；本轮涉及的 Go 文件均通过 gofmt 检查。
- 提交前以 `99bd8543` 为基线，从暂存区导出仅包含 AP_ID_TOKEN 改名及本轮修复的独立源码快照；`go test -p 2 ./...` 全量通过，不依赖工作区未提交的 accessConfig 等其他功能改动。

### 8.2 通用执行 oneid 边界补齐

2026-10-29 将提前拒绝扩展到 MCP，统一错误码为 `connector_oneid_unsupported`（HTTP 422）；目录与 Describe 对 oneid 返回空的 `adapters`、`components`，保留 ID 和 revision。同步安装授权、执行协议与 VIEW 错误码表；不改变 Agent 的身份链路或包作者自定义 stdio env 映射。

- 回归测试覆盖无模板 CLI、带模板 CLI、HTTP MCP、stdio MCP 和 CLI/MCP 混合包；登录前后、无幂等键与重复写请求均提前拒绝，不生成写入收据，权限检查保持优先。非 oneid 包仍声明原有 adapter 与组件。
- 新增场景在修复前复现 MCP 的认证/不可用错误及能力声明不一致，修复后通过；VIEW 查询保留 422 和错误码，VIEW 写操作保留错误而不转换为结果未知。
- `go test ./internal/connectorops ./internal/connectorauth ./internal/mcp ./internal/viewquery ./internal/viewaction ./internal/server` 全部通过。
- `go vet ./internal/connectorops ./internal/viewaction ./internal/server`、本轮 Go 文件的 gofmt 和差异空白检查通过。
