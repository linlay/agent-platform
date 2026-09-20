# WebApp 能力接入

## 职责与当前阶段

SDK 功能命名空间统一使用单数：`assistant`、`skill`、`connector`、`artifact`、`desktop`、`automation`、`kanban`。复数只用于返回的集合字段，不用于功能分组。

| 分组 | 职责 | Platform 当前边界 |
| --- | --- | --- |
| assistant | 调用 Agent、订阅运行事件 | 复用 query；尚无可信 appId Run 归属 |
| skill | 查询可用技能、选择已获准技能 | 沿用 Agent catalog 与 mustUseSkills 校验 |
| connector | 执行已授权连接器 CLI/MCP | 复用现有连接器凭据和按连接器/adapter 的短期执行授权 |
| artifact | 获取已发布产物元数据和内容 | 仅显式授权 Chat 中的 manifest 产物 |
| desktop | 原生界面、连接器登录交互 | 可信宿主调用现有认证管理器 |
| automation | 应用后台调度 | 本阶段未实现专属归属及持久授权，不得透传全局 CRUD |
| kanban | 看板业务查询 | 本阶段未实现 WebApp 专属入口 |

以上是职责契约；Desktop SDK 的具体实现与测试由 Desktop 仓库维护。WebApp 不持有 Platform JWT、连接器 token、OAuth 回调或凭据目录，不实现连接器登录页。需要登录时由页面用户操作调用 `desktop.authenticateConnector({connectorId})`，Desktop 打开受管认证界面，Platform 负责会话和凭据。认证失败或没有 Desktop 能力时返回错误。普通调用失败不自动弹窗、不自动重复执行。

## 可信宿主与短期 grant

`/api/desktop/*` 即使普通 API 允许匿名访问也强制 JWT。宿主入口要求已验证的 `scope=app`、非空 deviceId 和 `desktop-user:<64位十六进制摘要>` subject；请求体不能选择账号。

- `POST /api/desktop/webapp/grants`：`{version:2,appId,execution:[{connectorId,adapter}],chatIds?}`，返回 `{grantId,token,appId,expiresAt}`；expiresAt 为 epoch milliseconds。
- `DELETE /api/desktop/webapp/grants?grantId=...`：同 subject 撤销。
- `GET/POST/PUT/DELETE /api/desktop/connector/auth?id=...`：当前连接器状态、发起认证、提交受管凭据、退出；具体认证参数复用既有连接器接口。
- `GET /api/desktop/connector/auth?id=...&sessionId=...`：只查询该次会话；不替换为新的登录会话。
- `POST /api/desktop/connector/auth/cancel?id=...&sessionId=...`：取消指定会话。

grant 存活 15 分钟，仅保存在内存中；重启失效，撤销取消关联操作。Desktop 持有 token 并代理请求，页面不得获取 token。主体、应用、连接器执行集合、Chat 集合创建后冻结。最多 1024 个活动 grant，每个最多 64 个连接器/adapter 组合、128 个 Chat，不支持通配符。

Platform 检查 Chat 存在及现有 principal 引用权限；应用与 Chat 的关联目前由可信 Desktop 提供，尚无服务端持久化应用所有权模型。不能以此 grant 推导全局历史、其他应用产物或后台 automation 权限。

## 连接器凭据

连接器仅使用当前部署的一套凭据。Desktop 登录、WebApp 操作与既有 Agent/管理接口复用同一个认证管理器及 `<runtime>/.state/connectors/<connectorId>`；CLI 的 configEnv 继续指向其 `config` 子目录。退出登录对这些调用共同生效。

不按应用或用户 subject 创建额外凭据目录，不要求资源包新增个人配置支持声明。应用 grant 只限制可执行连接器/adapter 和 Chat，不选择连接器账号。现有连接器包的启动器及 configEnv 约定保持不变。

## 通用连接器执行 v2

不再读取 operations.json，也不注册 Platform 内置业务 profile。WebApp 按连接器和 adapter 获得执行许可，直接传递 CLI argv 或 MCP 原生工具名与参数。SQL、字段解析、身份文本提取与提醒文案归应用；新增连接器只配置安装、启动、认证，不逐条转换业务命令。

CLI 从连接器既有 command、BinDir 或已安装全局命令解析入口，复用现有准备状态和 configEnv。macOS/Unix 直接 exec 程序或已安装启动脚本，调用参数不拼接 shell 字符串；Node 入口由受管 Node 启动。Windows 原生 exe 直接启动，标准 Node cmd shim 解析为 Node+脚本再传 argv，不经过 cmd /c；不支持的脚本明确拒绝。禁止应用传 executable、cwd、env。固定启动目录来自包，环境只保留基本运行变量、受管 bin 与当前连接器的认证绑定，不继承 Platform JWT 和其他云凭据。通用 CLI 执行不是 OS 沙箱，不能把连接器支持的凭据查看或文件功能假称为被隔离。

MCP 仅使用包内已配置组件，按原生工具名调用，拒绝禁用工具和组件；返回原生 content/structuredContent/isError，不强制结构化对象结果。当前继续拒绝 oneid-token MCP，不把宿主 SSO 凭据交给 WebApp 执行器。

所有接口为 POST、使用短期 grant：

| 路径 | 请求 | 返回 |
| --- | --- | --- |
| `/api/webapp/connector/list` | `{}` | `{items:[{connectorId,name,packageVersion,adapters}]}` |
| `/api/webapp/connector/describe` | `{connectorId}` | `{connectorId,revision,adapters,components}` |
| `/api/webapp/connector/invoke` CLI | `{connectorId,adapter:"cli",args:string[],idempotencyKey?,credentialRevision?}` | `{invocationId,connectorId,adapter,credentialRevision,exitCode,stdout?,stderr?}` |
| 同上 MCP | `{connectorId,adapter:"mcp",component,toolName,arguments,idempotencyKey?,credentialRevision?}` | 相同身份字段及 `mcp` 原生结果 |

输入采用严格字段检查，adapter 参数不混用；CLI argv 最多 256 项，拒绝 NUL，保留中文、JSON、引号、换行和元字符原值。包锁、执行前指纹复核与授权复核继续生效；请求和结果有 1 MiB 上限，调用期限 30 秒。CLI 非零退出码、MCP isError 都是业务方必须检查的结果，不自动重试。返回前检查账号版本及授权；调用中账号变化或撤销后不交付结果。

## 执行授权和回执

Desktop 原生确认按应用、连接器和 adapter 记录，明确可读取数据、发送消息和修改数据，不推断通用命令的读写性。登录许可不授予执行，旧读取许可不升级。Desktop 仅允许页面使用此执行能力，后端 token 和 handler 双重拒绝。Platform 只接受可信宿主冻结的 execution 集合，不接受客户端在 invoke 中提权。

有副作用调用由应用提供稳定 idempotencyKey。Platform 在共享连接器状态根持久化 claim，按 subject/app/connector/新执行契约/key 摘要定位，并比较 adapter/args/component/toolName/arguments 的摘要。先独占落盘，再派发；已完成同请求返回结果（包括非零退出与 MCP 业务错误），不同参数冲突，未完成 claim 结果未知、不再派发。执行错误、超时、账号改变或收据保存失败均保持 claim。这不是外部 exactly-once。无 key 请求没有去重保证；SDK 和 Platform 均不自动重试。

旧 operation 收据保留不删除，不自动解释为新 argv 请求；升级沿用工作台每日发送的本地标记，不能换键自动重发。credentialRevision 为非凭据账号状态版本，可在一次业务流程的后续调用传回，防止查询人与群发送跨账号切换。

旧 grant 参数 operations/allowWrite、旧 invoke operationId/revision 被严格拒绝；grant 新版要求 version:2。Desktop、Platform、应用契约须同批升级，不能静默扩大权限。

## 已发布产物

同一 grant 还可访问显式 chatIds 的以下 POST 接口：

- `/api/webapp/artifact/list`：`{chatId,runId?,cursor?,limit?}`，默认 50、最多 100，返回 `{items,nextCursor?}`。
- `/api/webapp/artifact/get`：`{chatId,artifactId,runId?}`，返回元数据。
- `/api/webapp/artifact/read`：相同标识，成功返回文件字节，失败返回统一 JSON 错误。

元数据为 `{chatId,runId,artifactId,publishedAt,name,mimeType,sizeBytes,sha256}`，不含 URL 或磁盘路径。只读取 active Chat 的发布 manifest，不支持裸 artifactId 全局搜索、任意路径或归档 Chat。重名标识未指定足够 run 范围时报 `artifact_ambiguous`；读取检查真实文件大小与 SHA-256，变化或旧记录缺少摘要时返回 `artifact_changed`，不读取被替换的内容。

## 验证与限制

测试覆盖原生 argv、环境隔离、Windows/Unix 启动路径、MCP 文本及业务错误保留、授权冻结/撤销和回执去重。真实 WeCom 只读联调覆盖身份、文档与会议；群发送仅模拟验证。Windows 交叉编译和启动路径单元测试不等同于真实 Windows GUI 联调。应用持久所有权、后台执行 grant 与专属 automation 仍不在本期范围。
