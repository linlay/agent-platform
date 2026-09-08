# VIEW 连接器与客户端接入契约

## 设计边界

连接器主类型为 `mcp | cli | view`，界面显示 MCP / CLI / VIEW。主类型决定必填组件文件，同一个包可以组合 `mcp.json`、`cli.json`、`view.json`。VIEW 提供展示模板：`usage: display` 渲染结果，`usage: form` 展示 HITL 表单。业务查询和执行由 Tool/MCP/CLI 完成，决定和提交仍由 HITL 控制面处理。纯 VIEW 包不自动增加 Bash 或 PATH。

VIEW 复用 ZIP 导入、原包编辑、Agent 挂载、运行目录组装和租约。`internal/view` 负责中立定义、资源读取、远端模板获取和快照；`internal/connector` 负责包校验；Server 将冻结的 Agent 挂载绑定到 QuerySession。Team 成员只使用自己的挂载。

## 包配置

```text
crm-views/
├── connector.json
├── view.json
└── views/
    ├── edit.html
    ├── card.html
    └── card.js
```

`connector.json`：

```json
{"id":"crm-views","name":"CRM Views","version":"1.0.0","type":"view","auth_mode":"none"}
```

`view.json`：

```json
{"views":{
  "edit":{"title":"编辑资料","renderer":"html","usage":["form"],"entry":"views/edit.html"},
  "card":{"title":"资料卡","renderer":"html","usage":["display"],"entry":"views/card.html","assets":["views/card.js"]}
}}
```

`renderer` 接受 `html | qlc`，与 HITL mode 无关。QLC 必须是 JSON 对象；HTML 必须是非空 UTF-8。`usage` 显式声明 `display`、`form` 或两者。入口和资源必须是 `views/` 下的规范 POSIX 路径，禁止绝对路径、回退段、查询串和逃出 views 的符号链接，`views` 本身也不能是链接。只发布显式声明的资源，完整 JSON 文档（含资源 base64）上限 8 MiB。

Agent 使用以下配置挂载：

```yaml
connectorConfig:
  connectors:
    - crm-views
```

工具 YAML 的展示元数据如下；MCP SDK 工具描述使用 `_meta.view`，部署级 `platform.tools` 覆盖使用同形 `view`：

```yaml
view:
  connectorId: crm-views
  key: card
```

实际工具结果保持原样，`tool.result` 单独增加展示引用。配置只允许 `connectorId/key`，`version/hash/renderer` 由服务端解析。HITL 命令规则的 subcommand 改为：

```yaml
match: update
level: 1
mode: form
view:
  connectorId: crm-views
  key: edit
```

自定义 VIEW 必须显式 `mode: form`，不能混用 `viewportType/viewportKey`。普通批准使用平台 `mode: approval` 对话框。自定义表单不进入自动批准、规则批准或批量批准合并；视图失败不会批准操作或改写结果。

## 远端模板

`remote` 与本地 `entry/assets` 互斥：

```json
{"views":{"edit":{"renderer":"html","usage":["form"],"remote":{"url":"https://views.example/rpc","key":"customer-edit","timeout":10000,"headers":{"Authorization":"Bearer ${VIEW_TOKEN}"}}}}}
```

URL 由管理员配置，API 调用者不能覆盖。Platform POST JSON-RPC `views/get`，`params` 仅含 `{"key":"customer-edit"}`，不发送 Chat、结果或表单数据。响应为 `{"jsonrpc":"2.0","id":"view","result":{"renderer":"html","html":"..."}}`；QLC 使用 `qlc` 对象，也接受相应类型的 `payload`。响应 renderer 若声明必须与配置一致。默认超时 10 秒，上限 60 秒，不跟随重定向。

请求头占位符只从 `.state/connectors/<id>/credentials.json` 替换一次，不读取进程环境。真实凭据不得写入包或 ZIP。公共响应不回显配置 URL、请求头、凭据或上游错误正文。当前 VIEW 不实现 OAuth 登录和刷新，混合包的 MCP OAuth 不自动授权 VIEW。

## HTTP、WS 和快照

HTTP：`GET /api/view?chatId=<chat>&connectorId=crm-views&key=card&usage=display[&hash=<sha256>]`。WS request type 为 `/api/view`，payload 使用相同字段。成功响应保持 `{code:0,msg:"success",data:...}`，data 为：

```json
{
  "view":{"connectorId":"crm-views","key":"card","version":"1.0.0","hash":"<64位小写SHA-256>","renderer":"html"},
  "entry":"views/card.html",
  "html":"<main>...</main>",
  "assets":[{"path":"views/card.js","mediaType":"text/javascript; charset=utf-8","data":"<base64>"}]
}
```

请求没有 Agent、文件路径或远端 URL 选择器，Chat 确定 owner，沿用 Chat 资源鉴权。无 hash 时从该普通 Agent 当前挂载解析；Team 必须提供事件 hash。错误包括 400 `invalid_view`、403 `view_access_denied`、404 `view_not_found`、503 `view_unavailable`；Team 无 hash 为 400 `view_snapshot_required`。

`tool.result`、`awaiting.ask(mode=form)` 增加同形 `view`；失败时携带未解析引用和稳定 `viewError`。客户端保留原结果，表单失败仍允许拒绝，不能自动批准。Team 成员引用在对应 `forms[i].form.view`，错误在同级 `viewError`，外层路由不变。

事件发出前，服务端原子写入 `<chatId>/.views/<hash>.json`。同 Session 的同一 view/usage 首次成功后复用引用。工具消息持久化和冷回放保留引用；视图元数据不进入模型上下文。快照随 Chat 归档和恢复，连接器更新、解除挂载或删除后仍可凭 hash 读取。hash 绑定完整文档、版本与身份并在读取时校验，不允许跨 Chat 查找。

## WebClient 桥接

连接器管理增加 VIEW 过滤、组件列表和 `view.json` 编辑。工具结果卡片与 Markdown VIEW 块使用隔离 iframe。新 VIEW 统一 `sandbox="allow-scripts"`，不授予同源、弹窗、原生表单提交或宿主桥接权限。宿主内联声明资源并注入 CSP，限制资源来源、fetch/XHR、子框架及表单提交。应安装可信来源的视图包。

展示消息由宿主发送：`{type:"view_init" | "view_update",data:{view,payload}}`，payload 是工具结果或 Markdown 数据。展示 iframe 没有提交或执行消息处理器。

表单使用 `awaiting_init/awaiting_update`，data 含 `runId/awaitingId/view/mode/activeFormId/forms/form`。宿主按钮发送 `{type:"awaiting_collect",data:{runId,awaitingId,decision:"submit"}}`，模板读取字段后回复：

```json
{"type":"frontend_awaiting_submit","params":[{"id":"<activeFormId>","decision":"approve","form":{"name":"新名称"}}]}
```

只接受当前 iframe、当前等待项且宿主正在收集的响应；重复或未知表单 id 拒绝。客户端使用宿主保存的 `runId/awaitingId` 调用原 `/api/submit`，不接受模板改写路由。拒绝由宿主直接提交。Team 成员只接收自己的定义，宿主将返回参数包装到外层 `forms[i].form.params` 后沿用汇总提交协议。多表单模板应处理 `forms` 与 `activeFormId`。

QLC 当前采用 JSON 展示与 JSON 表单兜底，尚未实现专有 QLC 控件解释器。HTML 应打包为单入口与已声明资源，不支持隐式目录扫描、远端 CDN、CSS `@import` 或 JS 模块依赖解析。

Markdown 使用完整 fenced block：

````markdown
```view
{"view":{"connectorId":"crm-views","key":"card","hash":"<已取得的快照hash>"},"payload":{"name":"示例"}}
```
````

普通 Agent 的 Markdown 可省略 hash，按打开时当前挂载获取并产生快照；原文不会被改写，重新打开可能使用更新模板。固定历史版本需保存返回 hash。工具和 HITL 事件已经由服务端冻结。Team Markdown 必须使用已有快照 hash。

## 迁移与兼容范围

1. 把旧 `.html/.qlc` 复制进连接器 `views/`，关联资源一并复制并列入 `assets`，建立两个 JSON 清单，通过原 ZIP 导入入口安装。
2. 挂载到需要的 Agent；工具 `viewportType/viewportKey` 替换为 `view`，HITL 改为显式 `mode: form` 加 `view`。
3. 旧远端服务可暂用 `remote.protocol: "legacy-viewport"`，保持 `viewports/get` 与 `params.viewportKey`；认证移到部署凭据文件，包只保留占位符。
4. 客户端升级后验证展示、修改、拒绝与历史回放，再清理不再引用的旧文件。

不自动重写部署配置或历史。旧 `/api/viewport`、旧元数据和平台 builtin 对话框继续兼容。本次接入 Platform 与 WebClient，Desktop 仓库未修改。在线 Chat/Archive 回放支持 VIEW；独立会话 HTML 导出尚不内联 VIEW 模板，保留原始结果。
