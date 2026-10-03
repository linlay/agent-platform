# 连接器共享包与 Desktop 迁移

所有连接器的运行包统一放在 `<AP_RUNTIME_DIR>/ru-connectors/<id>/<contentDigest>/`。内置包和外部包遵循同一规则，同一内容版本在同一部署只安装一份；不直接执行发布来源，也不向每个 Agent 复制完整包。

```text
runtime/
├── connectors-center/<id>/                  # 外部原包
├── ru-connectors/<id>/<contentDigest>/      # 完整共享运行包
├── ru-agents/<agentKey>/connectors/<id>.json # 挂载引用
└── .state/
    ├── connectors/<id>/                    # 配置、凭证与 CLI 私有状态
    └── run-connectors/<runIdHash>.json      # 私有不可变运行挂载快照
```

CLI 内置来源为 Platform verified bundle 的 connectors 目录，其源码清单与技能位于相邻 `agent-platform-connectors/{dbx,httpx}/connector/`；native `builtin.platform-control` 与 `builtin.web-control` 的清单和技能各自完整保存在 `internal/resources/connectors/<id>/`，随程序内嵌。共享包包含清单、技能及其资源、适用的 bin/libs；运行引用和快照不保存凭据。模型通过 `@connectors/<id>/...` 访问当前 Agent 已挂载的包；Container 只读映射对应 `/connectors/<id>`。

## 安装、升级和回收

安装先复制到隐藏临时目录并校验内容，再以内容摘要原子发布。重复挂载校验并复用已有包。启动重建普通 ru-agents，但保留经过校验的共享包；共享目录直接初始化，不提供旧布局迁移。

Catalog 发布期间持有装配锁；当前挂载、活动 Run/Terminal 和 MCP session 保留包租约。连接器内容更新可以发布新的挂载快照，旧调用继续使用旧包。普通 Agent 文件的变更仍遵守活动运行目录租约。MCP 路由按 Agent、连接器组件及包版本隔离，共享文件不意味着共享 MCP 会话。

Run 的挂载快照保存在私有状态目录，持久引用位于共享根 `.run-pins`；等待用户输入时保留引用，以支持重启后的恢复。回收仅删除内容校验通过且没有引用、租约的版本，未知或被改动的目录保留。异常退出后没有完成终态对账的持久引用可能继续保留旧包，当前不会按时间强制清理它们。

共享包路径通过统一文件访问策略只读；未挂载包不获得文件读取或 CLI 专属免审权。Host Bash 仍遵循现有宿主执行模型，不提供操作系统级沙箱隔离。

## builtin.platform-control

内嵌 native/no_auth 平台控制连接器提供 catalog_query、catalog_manage、chat_query、chat_manage、platform_inspect 和七个 desktop_* 工具，使用统一 `{action,args}`。工具归属和运行时元数据分别在 internal/connector/native.go 与 control_actions.go 维护。详见 [平台控制连接器](Platform控制工具设计.md)。

它与 builtin.web-control 可同时挂载，互不授予对方工具；都为技能读取加入 file_read，不自动增加 Bash。调用始终检查受信任挂载。Catalog/Chat 限普通 native main root；KBASE 原生根 Run 可以使用平台工具，ACP 不执行这些工具。Standalone 仅显示五个本地平台工具。

启动原子发布内嵌包并持有共享租约，Agent 只保存引用。原来的 builtin.desktop 已退役，历史工具记录不改写；迁移后的 Desktop 动作通过原有反向请求发送，确认规则保持由 Desktop 负责。

## builtin.web-control

网页控制只处理“按地址打开并控制网页”。它不管理 Website 条目，也不负责 WebApp 的安装、打包和发布；Website 与 WebApp 的页面在其 Copilot Run 的授权范围内同样由这些工具操作。

模型只面对两种标识：

- **url**：用于 `workpanel_open`；只有文件预览使用它调用 `workpanel_close`，单个网页通过 `surfaceId` 关闭。网页以 `http://` 或 `https://` 开头；文件预览以 `@workspace/` 或 `@chat/` 开头并且必须位于当前 Workspace 内。其他写法（裸相对路径、无协议的主机名、绝对路径、`file://`）一律在发送前拒绝并给出改写提示，不做猜测。
- **surfaceId**：一个正在运行的网页。每个网页就是一个独立 surface，导航和刷新不改变它。

WorkPanel 条目 ID 和页面容器 ID 只在 Platform 内部使用，不出现在工具参数和结果中。

| 分类 | 工具 | 说明 |
| --- | --- | --- |
| WorkPanel | `workpanel_state` | 当前 Chat 面板中打开的条目 |
| WorkPanel | `workpanel_open` | 打开网页或预览文件，是打开新页面的唯一入口；网页返回 `surfaceId`；`reload` 在打开后刷新 |
| WorkPanel | `workpanel_close` | 按文件 `url` 严格匹配已记录的 Workspace 路径关闭预览，或 `all: true` 关闭整个面板；不接受网页 URL |
| Surface | `surface_list` / `surface_state` | 发现已授权网页、读取单页状态；`surface_state` 省略 `surfaceId` 读取所属应用的当前页 |
| Surface | `surface_navigate` | `goto` / `reload` / `back`，页面内导航 |
| Surface | `surface_activate` / `surface_close` | 显示、关闭一个网页 |
| Surface | `surface_screenshot` | PNG 截图，保存到当前 Chat 并返回引用名 |
| Surface | `surface_evaluate` | 执行脚本并返回值；大脚本用 `expressionFile` |
| Surface | `surface_click` / `surface_element` | 真实点击；按选择器填写、选择、聚焦、滚动 |
| Surface | `surface_cdp` | 没有专用工具的 CDP 方法（DOM、底层输入、Network） |
| AWCP | `awcp_manual` / `awcp_invoke` | 读取网站手册目录与章节、调用网站声明的业务动作 |

CDP 不单独成类：它是通道，每个方法都作用在某个 surface 上。有明确用途的方法是参数固定的 surface 工具；`surface_cdp` 只保留没有专用工具的方法，方法名保持 CDP 原名，同一操作只有一个入口。读取或修改页面内容时先用 `awcp_manual`，页面没有手册或没有匹配章节才使用 surface 内容工具，该规则只写在技能中。

实现要点：

- 每个工具在 `internal/tools/tool_web_control.go` 映射到既有的反向请求（WorkPanel 动作、`desktop.cdp.call`、`desktop.awcp.manual` / `desktop.awcp.invoke`），Desktop 协议与动作注册表没有变化。
- `workpanel_close` 关闭文件时由 Platform 读取面板状态，只接受唯一且完整匹配的已记录 Workspace 路径；不按文件名兜底，缺少路径或多项匹配返回 `workpanel_item_not_found`。单个网页先通过 `surface_list` 获取身份，再调用 `surface_close`，不通过 URL 推断关闭目标。
- 文件预览沿用 Desktop 的规则：文件必须位于 Workspace 内。`@chat/` 只有在 Chat 目录位于 Workspace 内时可用；Chat 目录在 Workspace 之外的预览尚未实现，需要 Desktop 接受 Chat 目录作为可信根。
- `workpanel_state` 返回条目的种类、打开地址、标题和激活状态，不返回对应的 `surfaceId`；网页的 `surfaceId` 通过 `workpanel_open` 结果或 `surface_list` 取得。
- `surface_cdp.paramsFile` 与 `surface_evaluate.expressionFile` 走标准文件读取 AccessPolicy 与审批。
- 5 个只读工具（`workpanel_state`、`surface_list`、`surface_state`、`surface_screenshot`、`awcp_manual`）在 planning/只读阶段可用。
- Standalone 运行模式下会话只暴露 `workpanel_*`，且不支持文件预览；`surface_*` 与 `awcp_*` 需要 Desktop 运行模式。
- 传输层错误码沿用既有的 `desktop_action_*` / `desktop_cdp_*` 前缀。

## 显式离线迁移

迁移工具默认只输出预览，应用前显式接受报告的新增工具：

| 旧配置 | 迁移结果 |
| --- | --- |
| builtin.desktop / platform_control / desktop_action / desktop-action | builtin.platform-control |
| builtin.desktop-web / desktop_cdp / desktop-cdp | builtin.web-control |
| 仅 builtin.desktop | 不额外授予 Web 权限 |
| 已挂载两个新连接器 | 不变 |

旧配置不在线隐式升级。无关 YAML 字节保持原样。旧 tools.yml 的 platform-control 配置段另行删除，run-env 保留。

```sh
go run ./cmd/migrate-desktop --runtime-dir /path/to/runtime
# 升级 Platform 发布包，停止该部署的 Platform 和配置编辑器后执行：
go run ./cmd/migrate-desktop --runtime-dir /path/to/runtime --apply --offline --allow-expansion
# 回滚时使用应用结果中的备份目录：
go run ./cmd/migrate-desktop --rollback /path/to/runtime/.desktop-migration-xxx --offline
```

应用前备份 Agent 文件，将旧 Desktop 技能目录移入备份，并校验预览与实际内容一致；回滚也检查后续修改，避免覆盖新编辑。迁移命令不提供 `--configure`，不处理 Desktop 连接配置状态。本次代码变更不自动迁移任何运行中的部署。

双版本回归覆盖技能资料范围与引用、共享复用、中文/英文目录、互斥选择、版本保留、Run 快照恢复、挂载检查、模拟 Desktop 调用及迁移保留版本。本次未执行 Windows 构建、真实 Desktop 反向调用或 Container Hub 联调，仍需目标环境验证。


## Runtime 锁目录

连接器锁统一位于 `<AP_RUNTIME_DIR>/.lock`，各用途使用独立路径：

```text
.lock/
├── shared-connector-layout.lock          # ru-connectors 初始化互斥
└── connectors/
    ├── assembly.lock                    # 装配共享锁 / 回收排他锁
    ├── install/<connectorId>.lock        # 共享运行包安装互斥
    ├── operations/<connectorId>.lock     # 来源修改、准备与授权操作互斥
    └── leases/<connectorId>/<digest>.lock # 版本使用共享锁 / 回收排他锁
```

锁目录及其子目录必须是真实目录，锁文件必须是普通文件。锁由操作系统管理，释放后保留文件，存在不代表正在占用；不得在进程运行时删除。macOS/Linux 使用 flock，Windows 使用 LockFileEx；Windows 不因点前缀自动设置隐藏属性。Git 忽略 `/.lock/`。

`ru-connectors/.shared-v1` 保留为当前布局标记。初始化只创建目录和标记，不根据标记缺失推断旧布局，也不备份、重命名或迁移现有目录；没有旧版 ru-connectors 兼容流程。

锁路径直接切换，不双锁、不回退旧路径。更新前停止同一 runtime 的全部 Platform 和管理进程，再启动统一使用新路径的版本。原位置已有的锁文件不自动删除，停机后可清理；运行包和 `.shared-v1` 保留。
