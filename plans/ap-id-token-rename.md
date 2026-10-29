# 规划：保留变量 `AP_ACCESS_TOKEN` 重命名为 `AP_ID_TOKEN`

> 状态：讨论稿。本文只梳理问题、现状和可选方案，不给结论，不含工作量估算。
> 盘点基线：`main` @ `28c9c1b0`（v0.5.27）。

## 1. 背景和动机

- oneid 连接器模式复用 Desktop SSO 的 **ID token**。Platform 默认从 `<有效 StateDir>/identity/sso-id-token` 读取，启动参数 `--sso-id-token-file <absolute-path>` 可覆盖（Desktop 传入 `<Desktop dataRoot>/state/desktop/sso-id-token.txt`）。
- 这个 token 目前以平台保留变量 `AP_ACCESS_TOKEN` 的名义流转：
  - 注入给经验证的单条直接 oneid Host CLI 子进程；
  - 注入给 oneid 的 stdio MCP 进程；
  - HTTP MCP 用它生成 `Authorization: Bearer ...`。
- 周边命名已经统一到「ID token」：启动参数 `--sso-id-token-file`、配置字段 `SsoIdTokenFile`、Provider 字段 `useSsoIdToken`、函数 `ReadSsoIdTokenFile`、Desktop 文件 `sso-id-token.txt`。只有注入变量还叫 access token。
- 「access token」这个名字和 MCP OAuth 的访问令牌（内部 `ACCESS_TOKEN`、模板 `${OAUTH_ACCESS_TOKEN}`）以及 Desktop 的 canonical access token 容易混淆，读文档和写连接器包时都要额外解释。
- 动机：让变量名反映它的真实语义（ID token），即 `AP_ID_TOKEN`。

## 2. 现状盘点

`rg 'AP_ACCESS_TOKEN|ONEID_TOKEN|OAUTH_ACCESS_TOKEN'` 在仓库内共命中 **68 行 / 26 个文件**：Go 非测试代码 16 行，Go 测试 30 行，Markdown 22 行；`configs/`、`scripts/` 无命中。另有通过常量 `agentconfig.EnvAccessToken` 间接引用的位置：非测试代码 20 处 / 11 个文件，测试 13 处。

### 2.1 变量定义与保留名单校验

| 位置 | 内容 |
| --- | --- |
| `internal/agentconfig/env.go:24-26` | 定义 `EnvAccessToken = "AP_ACCESS_TOKEN"`；注释仍写「injected into ordinary Agent Host Bash」，与现行行为（普通 Host Shell 不注入）不符 |
| `internal/agentconfig/env.go:101` | 保留变量判定（大小写不敏感），供 `shellenv.UnsafeOverride` 等使用 |
| `internal/runenv/validation.go:15` | `run_env` 硬拒名单含 `"AP_ACCESS_TOKEN"` 字面量 |
| `internal/agentconfig/access_token.go` | `ReadIdentityEnvironment` 返回 `{EnvAccessToken: token}`；`WithIdentityEnvironment` 先剥离继承/伪造的同名变量（大小写不敏感）再注入 |

### 2.2 进程环境剥离（防泄漏）

| 位置 | 内容 |
| --- | --- |
| `internal/hostshell/context.go:58` | 普通 Host Shell 删除该变量 |
| `internal/terminal/environment.go:54` | Terminal 环境过滤 |
| `internal/server/ws_terminal_session.go:63,118` | Workspace Terminal 过滤与覆盖拒绝 |
| `internal/tools/bash_access_review.go:57` | Bash 访问审查排除该 key |

### 2.3 Host CLI 注入

| 位置 | 内容 |
| --- | --- |
| `internal/tools/tool_bash.go:549-558` | oneid 直接 CLI：`connectorauth.ResolveEnvironment` + `ReadIdentityEnvironment` 合并进子进程环境 |
| `internal/connectorauth/environment.go:89` | `AuthOneID` 分支读取身份环境 |
| `internal/connectorauth/manager.go:357` | 授权状态以 `identity[EnvAccessToken] != ""` 判定 authorized |

### 2.4 stdio MCP 注入

| 位置 | 内容 |
| --- | --- |
| `internal/mcp/client.go:429,457` | 进程创建前读取身份；错误文案 `oneid environment requires AP_ACCESS_TOKEN` |
| `internal/mcp/connectors.go:210` | mcp.json `env` 模板解析时把 `AP_ACCESS_TOKEN` 作为唯一可引用的身份变量 |

### 2.5 HTTP MCP Bearer 生成与模板归一

| 位置 | 内容 |
| --- | --- |
| `internal/mcp/connectors.go:204-215` | `headers.Authorization` 精确值 `Bearer ${ONEID_TOKEN}` 在内存归一为 `Bearer ${AP_ACCESS_TOKEN}`；显式 Authorization 只接受归一后的 `Bearer ${AP_ACCESS_TOKEN}`；错误文案 `oneid only supplies AP_ACCESS_TOKEN`、`omit it or use Bearer ${AP_ACCESS_TOKEN}` |
| `internal/mcp/client.go:634,653` | 每次请求读取身份生成 Bearer；错误文案 `oneid header requires AP_ACCESS_TOKEN` |

### 2.6 连接器清单模板解析（cli.json / 旧 auth_bindings）

| 位置 | 内容 |
| --- | --- |
| `internal/connector/cli_environment.go:21-22` | 对外模板名常量 `EnvOAuthAccessToken = "OAUTH_ACCESS_TOKEN"`、`EnvOneIDToken = "ONEID_TOKEN"` |
| `internal/connector/cli_environment.go:82-84` | 各 authMode 允许的模板变量：oneid 只允许 `${ONEID_TOKEN}` |
| `internal/connector/cli_environment.go:165-166` | 内存映射：`${OAUTH_ACCESS_TOKEN}`→`${ACCESS_TOKEN}`，`${ONEID_TOKEN}`→`${AP_ACCESS_TOKEN}` |
| `internal/connector/manifest_compat.go:277-282` | 旧 `auth_bindings` 迁移：非 cli 目标只允许 `${AP_ACCESS_TOKEN}`；cli 目标把 `${AP_ACCESS_TOKEN}` 改写成 `${ONEID_TOKEN}` |
| `internal/connectorauth/environment.go:45` | `${OAUTH_ACCESS_TOKEN}` 相关的组件数量校验（与本次改名无直接关系，列出供参照） |

当前模板词汇并不统一，改名时需要一起看：

| 位置 | `${ONEID_TOKEN}` | `${AP_ACCESS_TOKEN}` |
| --- | --- | --- |
| cli.json `env` | 接受 | **拒绝**（测试 `cli_environment_test.go:37` "old oneid name"） |
| mcp.json stdio `env` | **拒绝**（`connector_oneid_test.go:217`） | 接受 |
| mcp.json HTTP `headers.Authorization` | 仅精确值 `Bearer ${ONEID_TOKEN}` 接受并归一 | 仅精确值 `Bearer ${AP_ACCESS_TOKEN}` 接受 |
| mcp.json HTTP 其他 header | 拒绝（`connector_oneid_test.go:218`） | 接受（如 `X-SSO: ${AP_ACCESS_TOKEN}`，`connector_oneid_test.go:48`） |
| 旧 auth_bindings（迁移） | cli 目标的归一目标 | 非 cli 目标唯一允许 |

### 2.7 模型 Provider / KBX / 记忆调用

- `useSsoIdToken: true` 的 Provider 通过 `models.LoadModelRegistryWithSsoIdTokenFile`（`internal/app/app.go:179`、`internal/models/model_registry.go`）直接读 identity 文件，以 Bearer 发给 Provider `baseUrl`。
- KBX（`internal/kbx/manager.go:196`）和记忆 worker（`internal/memoryworker/config_sync.go:93`）走 `provider.Credential()`。
- 这条链路**不使用 `AP_ACCESS_TOKEN` 这个名字**，改名不直接影响它；但 `docs/配置化说明.md:607` 把它和 oneid 连接器放在一起描述，文档需要同步。

### 2.8 测试（直接字面量 30 行 + 常量引用 13 处）

- `cmd/agent-platform/connector_manage_test.go:129`
- `internal/agentconfig/access_token_test.go`、`internal/agentconfig/env_test.go:63`（含小写 `ap_access_token` 变体）
- `internal/connector/cli_environment_test.go:32-37,95-100`
- `internal/mcp/connector_oneid_test.go`（9 行：模板、伪造继承变量、拒绝用例）
- `internal/server/handler_connector_install_test.go:124`、`internal/server/ws_terminal_session_test.go:54-80`
- `internal/tools/tool_bash_test.go:904-973`、`internal/tools/access_hardening_test.go:73-94`、`internal/tools/tool_sandbox_bash_test.go:220`

### 2.9 文档（22 行）

`AGENTS.md:190`（同一行两处）、`README.md:348`、`docs/运行时和沙箱.md:62,72`、`docs/鉴权与安全边界.md:82`、`docs/连接器.md:116,210`、`docs/MCP与工具交互.md:29`、`docs/Agent运行时组装.md:104,152`、`docs/连接器安装与授权.md:13,17,96,129,131`、`docs/手工测试用例.md:13,14,17,18`、`docs/智能体配置说明.md:602`、`docs/配置化说明.md:607`。

### 2.10 外部影响（兄弟项目与已发布包）

| 位置 | 依赖情况 |
| --- | --- |
| `zenmind-env/connectors-center/gxj-flowcenter/mcp.json:5` | `"Authorization": "Bearer ${AP_ACCESS_TOKEN}"` |
| `zenmind-env/market/connectors/gxj-flowcenter/1.0.0/universal/package/mcp.json:5` 及同目录 `gxj-flowcenter.zip` | 已发布 1.0.0 包内同样写死 `Bearer ${AP_ACCESS_TOKEN}` |
| `zenmind-env/market/connectors/index.json:286`、`market/connectors/README.md:16` | 市场说明文字提到 `AP_ACCESS_TOKEN` |
| `zenmind-env/skills-center/online-{pptx,docx,xlsx}/SKILL.md` | 说明「不注入 `AP_ACCESS_TOKEN`」，仅文字 |
| 其余市场包（kdocs、tencent-docs、github、qq-mail、feishu、westock-mcp、tdx-connector、wecom） | ZIP 内无命中 |
| `agent-platform-builtins`（dbx/httpx/kbx/memx 等 456 个文件，`strings` 检索） | 无命中 |
| `zenmind-desktop` | 不引用该变量名；只负责写 `sso-id-token.txt` 并通过 `--sso-id-token-file` 传路径（`src/main/infrastructure/filesystem/user-paths.ts:17`、manifest `ssoIdTokenFile: "v1"`） |
| `agent-webclient` | 无命中 |
| 第三方/客户自建 oneid 连接器、使用 oneid CLI 的 SDK | 仓库内不可见，**待确认**是否有直接读取 `AP_ACCESS_TOKEN` 的 CLI 或 SDK |

## 3. 需要决定的问题

### 3.1 是否保留 `AP_ACCESS_TOKEN` 作为过渡别名

- **A. 一次性切换，只认 `AP_ID_TOKEN`。** 语义最干净，代码路径最少；已发布的 gxj-flowcenter 1.0.0 和任何直接读 `AP_ACCESS_TOKEN` 的 CLI 会立刻失效，需要和包升级同步发布。
- **B. 过渡期同时注入新旧两个变量。** 旧 CLI/SDK 无感；同一 token 在子进程环境出现两份，泄漏面和剥离逻辑都要覆盖两个名字；需要定义何时停止注入旧名。
- **C. 只注入新名，但旧模板 `${AP_ACCESS_TOKEN}` 在解析时归一到新名。** 包内模板（mcp.json/cli.json）兼容，直接读进程环境变量的 CLI 不兼容。
- 衍生问题：过渡期如何界定（按 Platform 版本号、按日期、还是按市场包全部升级完成）；过渡期内加载旧名是否输出弃用告警，告警进日志还是进连接器状态接口。

### 3.2 保留变量校验是否同时拒绝新旧两个名字

- **A. 新旧都列为保留变量**（`agentconfig` 判定、`runenv` 硬拒名单、Terminal/Host Shell 剥离、`WithIdentityEnvironment` 剥离）。防止 Agent/Skill/`run_env` 用旧名伪造身份，安全上最稳；旧名会长期占用。
- **B. 只保留新名。** 名单简洁；旧名释放后，Agent 可以声明 `AP_ACCESS_TOKEN`，旧连接器若仍读旧名可能读到被伪造的值。
- **C. 过渡期 A，之后 B。** 需要在文档里写明释放时点。

### 3.3 mcp.json / cli.json 模板词汇

- 是否新增 `${AP_ID_TOKEN}` 作为模板名。
- `${ONEID_TOKEN}` 是否保留：它目前是 cli.json 的唯一合法写法和 HTTP Authorization 的写法之一，属于「与内部变量名解耦的对外模板名」。
- 选项：
  - **A. 对外统一只用 `${ONEID_TOKEN}`**，内部变量改名不暴露给包作者；stdio env 和其他 header 也改为接受 `${ONEID_TOKEN}`。
  - **B. 对外统一用 `${AP_ID_TOKEN}`**，模板名与进程变量名一致；`${ONEID_TOKEN}`、`${AP_ACCESS_TOKEN}` 作为兼容写法归一。
  - **C. 两者都作为正式写法**，保留现在的「多写法 + 归一」模式。
- 归一的方向和位置：只在内存归一、不改写原包这一原则是否保持；归一只针对精确值（现状）还是允许在 stdio env / 其他 header 中通用替换。
- 是否借此消除 2.6 表格中的不对称（cli.json 拒 `${AP_ACCESS_TOKEN}`、stdio env 拒 `${ONEID_TOKEN}`）。

### 3.4 HTTP Authorization 允许哪些模板

- **A. 只允许省略或一个规范值**（例如 `Bearer ${AP_ID_TOKEN}` 或 `Bearer ${ONEID_TOKEN}` 二选一）。
- **B. 允许规范值 + 若干兼容值**，全部归一到同一内存形式。
- **C. 只允许省略**，Authorization 完全由平台生成，显式声明一律拒绝。最不易出错，但会让现有显式声明的包（gxj-flowcenter）失效。
- 无论选哪个，「仅 HTTPS 完整 endpoint、禁止 URL/静态字段/拼接模板」的约束是否原样保持。

### 3.5 已发布第三方连接器包和 SDK 的兼容

- gxj-flowcenter 1.0.0 是否发新版本改模板；旧版本在新 Platform 上是否仍要能加载。
- 市场 `index.json` 是否需要声明最低 Platform 版本，避免新包装到旧 Platform 上（旧 Platform 不认识 `${AP_ID_TOKEN}`）。
- 新旧 Platform × 新旧包的兼容矩阵要不要写进文档。
- 是否需要在连接器加载/安装接口中返回「使用了弃用变量」的提示，方便包作者迁移。
- 仓库外的客户自建 oneid CLI/SDK 如何通知（发版说明、文档迁移章节）。

### 3.6 Desktop 侧是否需要配合

- 当前 Desktop 只负责写 token 文件和传 `--sso-id-token-file`，不感知变量名，盘点看不需要改代码。
- 待确认：Desktop 文档（如 `zenmind-desktop/docs/鉴权SSO与TokenBridge.md`）是否需要补一句 Platform 侧变量名变化；Desktop 内置/分发的连接器是否有不在本次检索范围内的副本。

### 3.7 相关的命名清理是否一起做

- Go 常量 `EnvAccessToken` 是否随之改为 `EnvIDToken`，文件 `access_token.go` 是否改名。
- `env.go` 中过时的注释（「injected into ordinary Agent Host Bash」）是否顺带修正。
- 授权状态文案「Desktop SSO is unavailable」等是否需要调整。

## 4. 涉及的改动面清单

- **定义与保留名单**：`internal/agentconfig/env.go`、`internal/agentconfig/access_token.go`（含 `_other`/`_windows` 变体检查）、`internal/runenv/validation.go`。
- **环境剥离**：`internal/hostshell/context.go`、`internal/terminal/environment.go`、`internal/server/ws_terminal_session.go`、`internal/tools/bash_access_review.go`。
- **CLI 注入**：`internal/tools/tool_bash.go`、`internal/connectorauth/environment.go`、`internal/connectorauth/manager.go`。
- **MCP**：`internal/mcp/connectors.go`（模板归一、校验、错误文案）、`internal/mcp/client.go`（stdio 注入、HTTP Bearer、错误文案）。
- **清单模板**：`internal/connector/cli_environment.go`、`internal/connector/manifest_compat.go`。
- **测试**：2.8 所列文件；若选择过渡别名，需要新增「新旧名并存/旧名伪造/旧模板归一」用例。
- **文档**：2.9 所列文件。
- **外部**：`zenmind-env` 的 gxj-flowcenter 源包、市场包 ZIP、`index.json`、市场 README、三个 online-* SKILL.md 说明文字。

## 5. 文档需要同步修正的地方

- **`docs/连接器安装与授权.md` 第 17 行与第 131 行矛盾**：
  - 第 17 行：`headers.Authorization` 支持 `Bearer ${ONEID_TOKEN}` 和 `Bearer ${AP_ACCESS_TOKEN}` 两种模板，也可省略。
  - 第 131 行：Authorization 若显式声明，「仅接受 `Bearer ${AP_ACCESS_TOKEN}`」。
  - 代码（`internal/mcp/connectors.go:204-215`）实际行为与第 17 行一致。无论改名方案如何，这两处需要统一成同一份规则，并与 3.4 的决定一致。
- 同文件第 13、96、129 行的变量名和模板说明。
- `docs/连接器.md:116`（cli.json 模板：oneid 引用 `${ONEID_TOKEN}`）与 `:210`（oneid 注入 `AP_ACCESS_TOKEN`）。
- `docs/MCP与工具交互.md:29`、`docs/Agent运行时组装.md:104,152`、`docs/运行时和沙箱.md:62,72`、`docs/鉴权与安全边界.md:82`、`docs/智能体配置说明.md:602`、`docs/配置化说明.md:607`、`README.md:348`、`AGENTS.md:190`。
- `docs/手工测试用例.md:13-18` identity 用例中的变量名。
- 若保留过渡别名，需要一节「迁移说明」：新旧名对照、过渡期、弃用告警、兼容矩阵。

## 6. 验证要点

参考 `docs/手工测试用例.md` 第 13–18 行 identity 用例，按新变量名逐条复核，并补充改名特有的检查：

1. 不传 `--sso-id-token-file`、identity 文件不存在时，普通 Agent Bash 中新旧变量名都不存在（`test -z "${AP_ID_TOKEN+x}"`，以及旧名）。
2. 写入/原子替换/删除 token 文件不重启 Platform：普通 Bash 始终无新旧变量；oneid CLI 下一次子进程拿到新值，删除后不复用旧值、不继承父进程值。只检查存在性或哈希，不打印原文。
3. `--sso-id-token-file <absolute-path>` 覆盖默认路径仍生效。
4. 父进程环境或 Agent runtime env 设置新名、旧名（含大小写变体）都不能绕过 identity 文件；Agent/Skill/`run_env` 声明时按 3.2 的决定报保留变量错误。
5. Workspace Terminal、`file_grep/file_glob`、Container、Proxy、ACP、非 oneid MCP、LSP、sidecar 不获得新旧任一变量。
6. oneid stdio MCP：按 3.1 的决定检查注入了哪些名字；`env` 中各模板写法的接受/拒绝符合 3.3；轮换文件后下次调用重建进程，删除后拒绝新调用并回收旧 session。
7. oneid HTTP MCP：`headers.Authorization` 省略、各允许模板、各拒绝模板（拼接、static 字段、URL）的行为符合 3.4；只向完整 HTTPS endpoint 发送最新 Bearer。
8. cli.json `env` 模板：各写法的接受/拒绝符合 3.3。
9. 旧 `auth_bindings` 迁移路径的产物使用新模板（或按决定保留旧模板）。
10. 已发布的 gxj-flowcenter 1.0.0 在新 Platform 上的加载结果符合 3.5 的兼容决定；`.state/connectors` 不产生 SSO 凭据副本。
11. `useSsoIdToken` Provider、KBX、记忆调用不受影响（回归）。
12. `connector-manage status` 对 oneid 连接器的授权状态判定不变。

## 7. 待定问题

1. 是否保留 `AP_ACCESS_TOKEN` 作为过渡别名；若保留，是同时注入还是仅模板归一，过渡期以什么为界。
2. 保留变量名单是否同时覆盖新旧两个名字，旧名何时释放。
3. 对外模板名用 `${AP_ID_TOKEN}`、`${ONEID_TOKEN}` 还是两者并存；是否借此消除 cli.json 与 stdio env 之间的模板不对称。
4. HTTP `headers.Authorization` 显式声明允许哪些值，还是只允许省略。
5. gxj-flowcenter 等已发布包是否发新版、市场是否声明最低 Platform 版本、旧包在新 Platform 上是否继续可用。
6. 仓库外是否存在直接读取 `AP_ACCESS_TOKEN` 的客户 CLI/SDK（待确认），以及通知方式。
7. Desktop 文档是否需要同步说明；Desktop 是否有本次检索范围外的连接器副本（待确认）。
8. Go 常量、文件名、过时注释、错误文案是否一并重命名/修正。
9. 弃用告警的形态（日志、连接器状态接口、安装接口返回）以及是否需要告警。
