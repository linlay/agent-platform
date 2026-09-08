# WorkBuddy 连接器打包

## 分发方式与认证方式分开

`type: cli` 表示组件通过命令行调用，不表示 ZIP 内已经包含真实 CLI。检查分发内容时，应区分：

| 分发方式 | ZIP 内容 | 使用前准备 |
| --- | --- | --- |
| 自带 CLI | 真实程序及其依赖，例如随 Platform 分发的 builtin.dbx、builtin.httpx | 按宿主平台选择已有程序 |
| 安装命令型 | cli.json 中的 runtime/init/versionCheck、认证命令、技能，可带启动器 | 下载并安装 CLI，再完成授权 |
| 远程 MCP 定义型 | connector.json、mcp.json、可选技能 | 连接远端服务并配置认证，不安装 CLI |

这批 WorkBuddy 来源中，腾讯会议、企业微信、飞书均为**安装命令型**。此前腾讯会议和企业微信包里的 `bin` 也只是 Platform 启动器，不能据此判断已内置 CLI。飞书 npm 包本身还是安装引导包，首次执行时由官方安装器下载并校验对应平台的原生 CLI。

生成的 `package-info.json` 与合集 `index.json` 用 `cliDelivery`、`bundledCLI`、`launcherIncluded` 描述分发方式，供人和打包工具读取；不增加另一套运行配置。三个安装包均为 `bundledCLI:false`，不带 Node.js、node_modules、已下载二进制或凭据。Platform 仍只根据 connector.json、cli.json、mcp.json 执行。

## 十个包

连接器版本以来源市场 `.codebuddy-connector/connectors.json` 中对应条目的 `version` 为准，存在时原样保留；未声明或为空时统一使用约定的 `0.1.0`。当前这十个来源条目均未声明版本，因此十个 ZIP 的连接器版本均为 `0.1.0`。Platform 适配、补图标及重新打包不自行递增版本，也不把 npm CLI、Skill 或 WorkBuddy 客户端版本当作连接器版本。每包 `package-info.json`、README 和合集索引同步记录该版本。

| id | 名称 | 分发 | 认证配置 | 技能数 |
| --- | --- | --- | --- | --- |
| tmeet | 腾讯会议 | npm 安装 + 启动器 | null；受管 CLI 浏览器授权 | 1 |
| wecom | 企业微信 | npm 安装 + 启动器 | null；受管 CLI 扫码 | 14 |
| feishu | 飞书 | npm 引导安装 + 启动器 | null；应用配置与用户登录两步 | 28 |
| tencent-docs | 腾讯文档 | 远程 MCP | mcp；OAuth 发现、动态注册、PKCE | 0 |
| tdx-connector | 通达信 | 远程 MCP | mcp；OAuth 发现、动态注册、PKCE | 1 |
| westock-mcp | 腾讯自选股 | 远程 MCP | mcp；OAuth 发现、动态注册、PKCE | 1 |
| qq-mail | QQ 邮箱 | 远程 MCP | mcp；显式资源标识与 OAuth 发现 | 1 |
| github | GitHub | 远程 MCP | token；GitHub PAT Bearer | 1 |
| kdocs | 金山文档 | 远程 MCP | token；金山文档 MCP Bearer Token | 1 |
| tencent-map | 腾讯地图 | 远程 MCP | token；WebService Key 查询参数 | 0 |

本表的认证字段沿用新的 `token / oneid-token / oauth / mcp / null` 契约。`null` 表示组件自己负责认证；这三个 CLI 通过显式 `cli.json.platform` 接入已有受管生命周期。认证字段不表示离线可用或已登录。

GitHub 与金山文档的原始目录条目声明 `server-side`，其 WorkBuddy 服务端授权代理和客户端身份不在源目录中，不能打进 ZIP。当前分别转换为独立 PAT/MCP Token 字段，不承诺复现原客户端的一键授权。GitHub 远程 MCP 不支持动态客户端注册；若采用浏览器 OAuth，部署方须注册自己的 GitHub App 或 OAuth App，详见 [官方集成说明](https://github.com/github/github-mcp-server/blob/main/docs/host-integration.md)。

腾讯地图原始 SSE 定义保存在 `upstream/mcp.json`，运行配置改用 [腾讯地图官方接入说明](https://lbs.qq.com/service/MCPServer/MCPServerGuide/userGuide) 中的 `https://mcp.map.qq.com/mcp?key=${TENCENT_MAP_KEY}&format=0`。Key 在实际 HTTP 请求中注入，包和目录响应保留占位符。

QQ 邮箱的 [资源元数据](https://api.mail.qq.com/.well-known/oauth-protected-resource) 声明 `resource=https://api.mail.qq.com`，实际调用地址是 `https://api.mail.qq.com/mcp`。包显式声明 `oauth.resource`，资源校验与凭据发送目的地分别绑定。腾讯自选股的资源元数据与调用地址相同。公开发现接口可访问不等于已验证用户账号权限。

## 安装定义保留与适配

`cli.json.init` 保留原始三个操作系统的 npm 安装命令。Platform 使用 `cli.json.platform` 的包名和固定版本安装到独立 prefix/cache，不运行全局 init shell：

| 连接器 | 固定版本 | 入口 | 独立配置变量 |
| --- | --- | --- | --- |
| 腾讯会议 | @tencentcloud/tmeet@1.0.16 | scripts/tmeet.js | TMEET_CLI_CONFIG_DIR |
| 企业微信 | @wecom/cli@1.2.0 | bin/wecom.js | WECOM_CLI_CONFIG_DIR |
| 飞书 | @larksuite/cli@1.0.94 | scripts/run.js | LARKSUITE_CLI_CONFIG_DIR |

这些版本用于可重复准备，不代表市场最新版本。飞书入口在缺少真实程序时会运行官方安装器，下载并校验原生程序，因此 Node.js/npm 的安装成功还不是最终 CLI 准备成功。飞书额外声明 `platform.nativeEntry:bin/lark-cli`（Windows 自动追加 `.exe`）；普通状态查询和退出不会启动缺少原生程序的安装引导器，下载安装只在显式准备阶段执行。

飞书的 `auth` 数组按顺序执行，每一步单独限制 `authUrlDomain`。`skipIf` 只看同一受管 CLI 命令的退出码，输出直接丢弃；检查超时终止流程。步骤结束后清除旧授权链接。最终执行 status，按 `statusMatchJson` 匹配 JSON 字段；支持直接 JSON 对象和 `ok:true,data:{...}` 信封，`ok:false` 不视为成功。JSON 中的 URL 转义先按 JSON 解码，保留原有 URL 查询编码；输出分块尚未完成的 URL 不向客户端暴露。没有实现任意 shell 或交互式终端脚本执行。

企业微信的危险默认目录删除命令仅保存在 `upstream/cli.json` 中供核对，不参与执行；有效配置使用 `logoutMode:delete-config`，仅清理本部署独立配置。原有 tmeet/wecom 技能适配继续保留，飞书追加 Platform 使用入口，GitHub 的 `skill/` 规范为 `skills/`。

## 品牌图标

十个包都包含品牌图标，补图标不改变上述版本规则。打包器从来源目录同级的 `icons/<id>.svg` 或 `icons/<id>.png` 读取 WorkBuddy 原始素材，保留原始字节，复制到包内 `assets/icon.svg` 或 `assets/icon.png`，并设置 `connector.json` 的 `icon` 字段。图标随 ZIP 离线安装，不依赖远程图片地址；`package-info.json` 和合集索引也记录图标路径。

Platform 校验图标并通过目录中的 `iconUrl` 提供受鉴权保护的资源接口，客户端读取图片后显示。必须使用支持图标字段的新版 Platform 与客户端；旧服务会因未知清单字段拒绝导入。同名旧包需覆盖导入新版 ZIP，只有升级前端不会为旧包自动补图标。图标约束与接口见 [连接器](连接器.md)。

## 生成与导入

```bash
python3 scripts/package-workbuddy-connectors.py \
  --source /path/to/workbuddy/connectors \
  --output build/connectors
```

输出十个独立 ZIP、`packages/<id>/` 展开包、README.md、index.json、sha256.json，以及 `workbuddy-platform-connectors.zip` 分发合集。合集应先解压，再分别导入内部十个 ZIP；它本身不是单个可导入连接器。

每包根目录包含 connector.json、组件 JSON、assets 图标、README.md、package-info.json 和可选 skills；upstream/ 保存原始组件 JSON 和对应目录条目，仅用于核对。Token 包附带空值 credentials.example.json，不包含真实值。ZIP 固定时间戳、文件排序和可执行权限；重复生成应得到相同 SHA-256。

通过 Platform 现有管理端导入，或使用：

```bash
agent-platform connector-manage import --runtime-dir /absolute/runtime /path/to/feishu.zip
```

导入只安装定义；CLI 下载准备、用户登录、远端 MCP tools/list 和业务权限是后续独立步骤。同名包显式覆盖，升级服务后才具备本次新增的多步 CLI、Token 查询参数及显式 OAuth 资源支持。当前账号授权仍需用户本人完成，包不会复制 WorkBuddy 登录态。

Agent 通过 `connectorConfig.connectors` 挂载连接器，技能自动导入，不放入 `skillConfig.skills`：

```yaml
connectorConfig:
  connectors:
    - feishu
    - github
```

持久化凭据留在 `.state/connectors/<id>`，运行包由 Platform 组装到 `ru-agents/<agentKey>/connectors/<id>`，不手工修改运行包。详细安装 API 与状态见 [连接器安装与授权](连接器安装与授权.md)。
