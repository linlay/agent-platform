# WebApp 能力接入

## 职责与当前阶段

SDK 功能命名空间统一使用单数：`assistant`、`skill`、`connector`、`artifact`、`desktop`、`automation`、`kanban`。复数只用于返回的集合字段，不用于功能分组。

| 分组 | 职责 | Platform 当前边界 |
| --- | --- | --- |
| assistant | 调用 Agent、订阅运行事件 | 复用 query；尚无可信 appId Run 归属 |
| skill | 查询可用技能、选择已获准技能 | 沿用 Agent catalog 与 mustUseSkills 校验 |
| connector | 查询并执行明确声明的业务操作 | 本文新增的个人凭据、短期授权和只读 operation |
| artifact | 获取已发布产物元数据和内容 | 仅显式授权 Chat 中的 manifest 产物 |
| desktop | 原生界面、连接器登录交互 | 可信宿主调用个人授权入口 |
| automation | 应用后台调度 | 本阶段未实现专属归属及持久授权，不得透传全局 CRUD |
| kanban | 看板业务查询 | 本阶段未实现 WebApp 专属入口 |

以上是职责契约；Desktop SDK 的具体实现与测试由 Desktop 仓库维护。WebApp 不持有 Platform JWT、连接器 token、OAuth 回调或凭据目录，不实现连接器登录页。需要登录时由页面用户操作调用 `desktop.authenticateConnector({connectorId})`，Desktop 打开受管认证界面，Platform 负责会话和凭据。认证失败或没有 Desktop 能力时返回错误。普通调用失败不自动弹窗、不自动重复执行。

## 可信宿主与短期 grant

`/api/desktop/*` 即使普通 API 允许匿名访问也强制 JWT。个人入口要求已验证的 `scope=app`、非空 deviceId 和 `desktop-user:<64位十六进制摘要>` subject；请求体不能选择账号。

- `POST /api/desktop/webapp/grants`：`{appId,operations:{connectorId:[operationId]},chatIds?}`，返回 `{grantId,token,appId,expiresAt}`；expiresAt 为 epoch milliseconds。
- `DELETE /api/desktop/webapp/grants?grantId=...`：同 subject 撤销。
- `GET/POST/PUT/DELETE /api/desktop/connector/auth?id=...`：个人连接器状态、发起认证、提交受管凭据、退出；具体认证参数复用既有连接器接口。
- `GET /api/desktop/connector/auth?id=...&sessionId=...`：只查询该次会话；不替换为新的登录会话。
- `POST /api/desktop/connector/auth/cancel?id=...&sessionId=...`：取消指定会话。

grant 存活 15 分钟，仅保存在内存中；重启失效，撤销取消关联操作。Desktop 持有 token 并代理请求，页面不得获取 token。主体、应用、操作集合、Chat 集合创建后冻结。最多 1024 个活动 grant，每个最多 64 个连接器、每连接器 128 个明确操作、128 个 Chat，不支持通配符。

Platform 检查 Chat 存在及现有 principal 引用权限；应用与 Chat 的关联目前由可信 Desktop 提供，尚无服务端持久化应用所有权模型。不能以此 grant 推导全局历史、其他应用产物或后台 automation 权限。

## 个人凭据

新入口使用 `<persistentRoot>/users/<sha256(subject)>/bindings/default/<connectorId>`，其中 persistentRoot 通常为 `.state/connectors`。连接器安装、准备和资源包仍为部署级；个人凭据不回退到部署凭据，个人退出不删除部署凭据。

CLI 包必须显式声明 `cli.json` 的 `platform.personalConfig: true`，并有唯一 configEnv 映射；启动器必须尊重注入的独立配置目录。旧包不自动获得个人调用能力。个人入口不接受依赖部署 identity-file 的 oneid-token。既有 Agent 调用和管理端授权保持原有部署级行为，此次不是全局凭据迁移。

## 连接器 operation

连接器包根目录可包含 `operations.json`，版本固定为 1。每项包含 `operationId`、`description`、`effect:"read"`、对象类型的 `inputSchema/outputSchema` 和一个执行映射。没有声明的 CLI/Skill/MCP tool 不成为 WebApp 能力。

```json
{
  "version": 1,
  "operations": [{
    "operationId": "item.list",
    "description": "读取条目",
    "effect": "read",
    "inputSchema": {"type":"object","additionalProperties":false},
    "outputSchema": {"type":"object","required":["items"],"properties":{"items":{"type":"array"}},"additionalProperties":false},
    "adapter": "cli",
    "cli": {"entry":"bin/client","args":["item","list"],"jsonFlag":"--json"}
  }]
}
```

CLI 只接受包内 bin 原生可执行文件，固定参数后追加 `--json` 与单个 JSON 参数，不通过 Shell。环境变量使用显式基本变量白名单，再注入个人 configEnv 和认证映射，不继承进程级云凭据或 AP_ACCESS_TOKEN。MCP 映射为 `adapter:"mcp",mcp:{component,tool}`，每次创建独立会话，拒绝禁用组件/工具，只接受结构化对象结果。

调用持有连接器跨进程操作锁，与资源包 mutation 串行。输入和输出均通过 Schema 校验，不加载远程 Schema；包及清单生成 revision，调用必须匹配。Schema 应明确约束字段及长度，并用字符串表示超出 JavaScript 安全整数范围的业务 ID。清单、输入、结果上限均为 1 MiB，单次调用期限 30 秒。写操作、任意命令、任意 MCP tool、模型路由和自动重试均未提供。

下列接口均为 POST，使用 grant 的 `Authorization: Bearer <token>`：

| 路径 | 请求 | 返回 data |
| --- | --- | --- |
| `/api/webapp/connector/list` | `{}` | `{items:[{connectorId,name,packageVersion,operationCount}]}` |
| `/api/webapp/connector/describe` | `{connectorId}` | `{connectorId,revision,operations}`，不暴露执行映射 |
| `/api/webapp/connector/invoke` | `{connectorId,operationId,revision,arguments}` | `{invocationId,connectorId,operationId,revision,status:"succeeded",output}` |

错误使用 `{code:<HTTP状态>,msg:<错误码>,data:{errorCode:<错误码>}}`。未授权为 `app_grant_required` / `operation_not_allowed`，缺少个人认证为 `connector_auth_required`（401），凭据变更为 `connector_auth_expired`（401），安装/状态不可用为 `connector_unavailable`（503），包正忙为 `connector_busy`（409），revision 变化为 `operation_revision_mismatch`（409），无效输入为 `invalid_arguments`（400），无效上游结果为 `invalid_upstream_response`（502）。故障不返回 stderr、凭据或宿主路径。

## 已发布产物

同一 grant 还可访问显式 chatIds 的以下 POST 接口：

- `/api/webapp/artifact/list`：`{chatId,runId?,cursor?,limit?}`，默认 50、最多 100，返回 `{items,nextCursor?}`。
- `/api/webapp/artifact/get`：`{chatId,artifactId,runId?}`，返回元数据。
- `/api/webapp/artifact/read`：相同标识，成功返回文件字节，失败返回统一 JSON 错误。

元数据为 `{chatId,runId,artifactId,publishedAt,name,mimeType,sizeBytes,sha256}`，不含 URL 或磁盘路径。只读取 active Chat 的发布 manifest，不支持裸 artifactId 全局搜索、任意路径或归档 Chat。重名标识未指定足够 run 范围时报 `artifact_ambiguous`；读取检查真实文件大小与 SHA-256，变化或旧记录缺少摘要时返回 `artifact_changed`，不读取被替换的内容。

## 验证与未完成项

自动测试覆盖个人账号/绑定隔离、部署凭据不回退、grant 冻结与撤销、受限 MCP 调用、CLI 单 JSON 参数及环境隔离、输入输出校验、产物跨 Chat 与摘要校验。测试使用临时目录和本地 mock，不使用真实账号。

WeCom 资源包适配在 env 仓库维护，需重新打包发布后才能获得新能力。真实 CLI 业务响应格式与登录联调尚未验证，不宣称已经完成真实日历/会议聚合。应用持久所有权、后台 grant、automation/kanban、写操作以及部署凭据向个人凭据迁移均未完成。
