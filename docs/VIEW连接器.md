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
{"id":"crm-views","name":"CRM Views","version":"1.0.0","type":"view","authMode":"none"}
```

`view.json`：

```json
{"views":{
  "edit":{"title":"编辑资料","renderer":"html","usage":["form"],"entry":"views/edit.html"},
  "card":{"title":"资料卡","description":"Show one customer profile card.","renderer":"html","usage":["display"],"entry":"views/card.html","assets":["views/card.js"],
          "dataSchema":{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}}
}}
```

`description` 与 `dataSchema` 是 display VIEW 面向模型的契约：`usage` 含 `display` 时 `description` 必填（≤1024 字符）；`dataSchema` 可选，仅限 display，序列化后 ≤4 KiB 且必须是可编译的 JSON Schema。两者只进入 prompt 目录和 Markdown 数据校验，不发送给 iframe。

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

实际工具结果保持原样，`tool.result` 单独增加展示引用。工具结果展示只接受连接器来源及其 `display` 用途；内置原生组件和审阅页仅用于 HITL，误用时返回 `viewError: invalid_view`，不请求模板。远端 MCP `tools/list` 的错误展示元数据只降级当前工具的展示，并保留工具列表和调用能力；本地 YAML、HITL 规则和部署级覆盖仍严格校验。配置只允许 `connectorId/key`，`version/hash/renderer` 由服务端解析。HITL 命令规则的 subcommand 改为：

```yaml
match: update
level: 1
mode: form
view:
  connectorId: crm-views
  key: edit
```

自定义 VIEW 必须显式 `mode: form`，旧 `viewportType/viewportKey` 已删除，不再接受。普通批准使用平台 `mode: approval` 对话框。自定义表单不进入自动批准、规则批准或批量批准合并；视图失败不会批准操作或改写结果。

## 远端模板

`remote` 与本地 `entry/assets` 互斥：

```json
{"views":{"edit":{"renderer":"html","usage":["form"],"remote":{"url":"https://views.example/rpc","key":"customer-edit","timeout":10000,"headers":{"Authorization":"Bearer ${VIEW_TOKEN}"}}}}}
```

URL 由管理员配置，API 调用者不能覆盖。Platform POST JSON-RPC `views/get`，`params` 仅含 `{"key":"customer-edit"}`，不发送 Chat、结果或表单数据。响应为 `{"jsonrpc":"2.0","id":"view","result":{"renderer":"html","html":"..."}}`；QLC 使用 `qlc` 对象，也接受相应类型的 `payload`。响应 renderer 若声明必须与配置一致。默认超时 10 秒，上限 60 秒，不跟随重定向。

请求头占位符只从 `.state/connectors/<id>/credentials.json` 替换一次，不读取进程环境。真实凭据不得写入包或 ZIP。公共响应不回显配置 URL、请求头、凭据或上游错误正文。当前 VIEW 不实现 OAuth 登录和刷新，混合包的 MCP OAuth 不自动授权 VIEW。

## HTTP、WS 和快照

HTTP：`GET /api/view?source=connector&chatId=<chat>&connectorId=crm-views&key=card&hash=<sha256>[&usage=display]`。WS request type 为 `/api/view`，payload 使用相同字段。成功响应保持 `{code:0,msg:"success",data:...}`，data 为：

```json
{
  "view":{"source":"connector","connectorId":"crm-views","key":"card","version":"1.0.0","hash":"<64位小写SHA-256>","renderer":"html"},
  "entry":"views/card.html",
  "html":"<main>...</main>",
  "assets":[{"path":"views/card.js","mediaType":"text/javascript; charset=utf-8","data":"<base64>"}]
}
```

请求没有 Agent、文件路径或远端 URL 选择器，Chat 确定 owner，沿用 Chat 资源鉴权。connector 请求必须携带事件已发布的 hash，只读取该 Chat（或其归档）的快照，不按当前挂载现场解析，因此历史不随连接器升级、解除挂载或删除而变化。错误包括 400 `invalid_view`、400 `view_snapshot_required`（缺 hash）、403 `view_access_denied`、404 `view_not_found`。

`tool.result`、`awaiting.ask(mode=form)` 与 `content.view` 携带同形 `view`；失败时携带未解析引用和稳定 `viewError`。客户端保留原结果，表单失败仍允许拒绝，不能自动批准。Team 成员的表单作为独立等待项发布，引用和 `viewError` 就在该事件上。

事件发出前，服务端原子写入 `<chatId>/.views/<hash>.json`，内容为完整 HTML 与声明资源。同 Session 的同一 view/usage 首次成功后复用引用；快照按内容寻址，同一模板版本在一个 Chat 内只存一份。工具消息与 assistant message 的 `views` 持久化和冷回放保留引用；视图元数据不进入模型上下文。快照随 Chat 归档和恢复，连接器更新、解除挂载或删除后仍可凭 hash 读取。hash 绑定完整文档、版本与身份并在读取时校验，不允许跨 Chat 查找。

## WebClient 桥接

连接器管理增加 VIEW 过滤、组件列表和 `view.json` 编辑。工具结果卡片与 Markdown VIEW 块使用隔离 iframe。新 VIEW 统一 `sandbox="allow-scripts"`，不授予同源、弹窗、原生表单提交或宿主桥接权限。宿主内联声明资源并注入 CSP，限制资源来源、fetch/XHR、子框架及表单提交。应安装可信来源的视图包。

展示消息由宿主发送：`{type:"view_init" | "view_update",data:{view,payload}}`，payload 是工具结果或 Markdown 数据。展示 iframe 没有提交或执行消息处理器。

宿主为 display VIEW 注入 `window.zenmindView` 适配脚本（不写入快照，随宿主协议演进）：`zenmindView.onData((payload, view) => …)` 接收 init/update，`zenmindView.resize()` 手动上报高度；脚本默认以 ResizeObserver 发送 `{type:"view_resize",height}`。宿主只接受来自该 iframe 的消息，高度限定在 48–1600px，首次上报前保持 320px。

表单使用 `awaiting_init/awaiting_update`，data 含 `runId/awaitingId/view/mode/timeout/form`，其中 `form` 为 `{title, data}`。宿主按钮发送 `{type:"awaiting_collect",data:{runId,awaitingId,decision:"submit"|"reject"}}`，模板读取字段后回复：

```json
{"type":"frontend_awaiting_submit","param":{"decision":"approve","data":{"name":"新名称"}}}
```

`decision` 与收集请求一致：`submit` 回 `approve`，`reject` 回 `reject`，两者都带当前 `data`。只接受当前 iframe、当前等待项且宿主正在收集的响应；重复或格式不符的回复拒绝。客户端使用宿主保存的 `runId/awaitingId` 调用 `/api/submit` 并提交 `param`，不接受模板改写路由。拒绝也先收集数据，让模型看到用户修改后的内容；视图加载失败或收集超时时，宿主直接提交不带数据的拒绝。一个等待项只有一张表单，模板不需要处理多表单。

QLC 当前采用 JSON 展示与 JSON 表单兜底，尚未实现专有 QLC 控件解释器。HTML 应打包为单入口与已声明资源，不支持隐式目录扫描、远端 CDN、CSS `@import` 或 JS 模块依赖解析。

## Markdown VIEW 块

助手正文用 info string 指定连接器 VIEW，正文只放数据：

````markdown
```view key=crm-views/card
{"name":"示例"}
```
````

语法（平台与 WebClient 按同一组 golden 用例 `internal/view/testdata/markdown_cases.json` 校验）：

- 栅栏从行首开始；恰好三个反引号加单词 `view`（不区分大小写），其后为空白或行尾。
- 属性只接受 `key=<connectorId>/<viewKey>`，两段均为小写 ID；重复、未知属性（含 `hash=`、`query=`）均为 `invalid_view`。不带 `/` 的内置 VIEW 不能用于 Markdown。
- 正文为一个 JSON 对象或数组，去除首尾空白后 ≤256 KiB；有 `dataSchema` 时按其校验，失败为 `view_data_invalid`。
- 单独一行、至少同样数量的同种栅栏字符闭合，允许尾随空白。其他代码块（``` 或 ~~~）内的 `view` 栅栏是代码，不是 VIEW；未闭合的块不渲染。
- `index` 为同一 content 内已闭合 VIEW 块的顺序号，非法块也占号，平台绑定与客户端分段靠它对齐。

Run 输出时由平台绑定：内容流每收到含换行的增量即扫描新闭合的块（流式期间末行须以换行结束才算闭合，content 结束时行尾即可），用**产生该内容的执行者**本 Run 冻结的挂载解析模板、写入快照并校验数据，随后发布 `content.view`；`content.snapshot.views` 汇总持久化。Team 成员使用自己的挂载，模型无需也不能填写 hash。原文不改写。ACP/PROXY/CHANNEL 不绑定，其 VIEW 块按普通代码块显示。旧 JSON 信封 `{"view":…,"payload":…}` 已删除，历史中的旧块因无绑定按代码块显示。

WebClient 只用绑定中的 hash 读取快照：块已闭合但绑定未到时显示加载中；content 结束仍无绑定则按代码块显示；`viewError` 时显示错误并折叠原始 JSON。

### Prompt 目录

display VIEW 通过 `configs/agent-prompt.yml` 的 `shared.view` 注入 system prompt，源码不提供任何默认文案：

```yaml
shared:
  view:
    instructions-prompt: |
      ...VIEW 用法规则...
    catalog-header: "Available views (key, purpose, data schema):"
```

注入规则：

1. 来源只取当前执行者冻结的连接器挂载，与绑定使用同一集合；Team 总控与成员各用自己的挂载。
2. 只列 `usage` 含 `display` 的连接器 VIEW，不列 form-only 与内置 VIEW。
3. 只对 Native Agent 生效，ACP/PROXY/CHANNEL 不注入。
4. `instructions-prompt` 与 `catalog-header` 必须同时配置，只配其一加载失败；都缺失时不注入。
5. 无可列 VIEW 时不注入。
6. section 为 `view-catalog`（`views.catalog`），紧跟 `skill-catalog`；内容为 instructions、header 与条目依次拼接。
7. 规划与执行 Run 内容相同，与 mode、editingMode 无关；目录计入 system-init 指纹。
8. Run 启动时随 Session 冻结，目录热重载只影响下一次 Run。
9. 条目按 `connectorId/viewKey` 字典序，字段为 `key/title/description/data`（结构标签由代码生成，description 折叠空白，data 为紧凑 JSON）；整段上限 32 KiB，超出从末尾整条移除并记录 warning。
10. 不提供 Agent 级覆盖；不需要某个 VIEW 就不挂载对应连接器。
11. description 与 dataSchema 来自包作者，信任等级同工具描述，不授予工具或权限。
12. 渲染与绑定不依赖 prompt：未注入时模型写出的 VIEW 块仍按上述规则绑定。

## 来源统一与硬切边界

所有实时事件统一携带 `view`。`source` 为 `builtin` 或 `connector`，`renderer` 为 `native`、`html` 或 `qlc`。配置只声明 `{key}` 或 `{connectorId,key}`，服务端解析来源与渲染器；连接器继续要求显式 `mode: form`。`mode=form` 使用单个 `form`，`planning` 与 `form` 提交单个 `param`，Team 成员等待项独立发布，见 [HITL 协议](HITL协议.md)；尚不支持模型生成 HTML。

内置引用示例：`{"source":"builtin","key":"platform_control_review","renderer":"html"}`。question、approval、planning 和 confirm_dialog 是客户端 native 组件，不通过接口获取 HTML。其他内置 HTML 从 `internal/resources/views/*.html` 自动发现，统一内联共享样式、脚本与尺寸桥。

HTTP/WS 统一 `/api/view`：内置请求 `source=builtin&key=<key>`，连接器请求 `source=connector&chatId=<chat>&connectorId=<id>&key=<key>&usage=form[&hash=<hash>]`。响应统一 `{code,msg,data:{view,html?,qlc?,entry?,assets?}}`。来源缺失或非法返回 400；未知内置 key 或请求 native 文档返回 404。内置请求不需要 Chat；连接器继续通过 Chat 鉴权，保留快照和归档读取。

Gateway 还需 Run 或 Chat 路由上下文，保留租户、Chat 权限与上游路由校验。Run 绑定按来源、连接器、key 和 hash 区分；Team 成员引用一并记录。

旧 `/api/viewport`、`viewportType/viewportKey` 配置、远端 `legacy-viewport` 协议均删除。远端只支持 `views/get`。旧 Markdown `viewport` 块与 `view` JSON 信封均不再触发模板加载，使用 `view key=<connectorId>/<viewKey>` 块；内置 VIEW 只用于 HITL。这是同批硬切，不提供旧事件、配置或快照迁移。

在线 Chat/Archive 回放保留 VIEW 引用。独立 HTML 导出仍不内联 VIEW 模板，不代表离线导出支持。
