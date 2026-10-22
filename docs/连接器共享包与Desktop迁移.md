# 连接器共享包与 Desktop 迁移

所有连接器的运行包统一放在 `<AP_RUNTIME_DIR>/ru-connectors/<id>/<contentDigest>/`。内置包和外部包遵循同一规则，同一内容版本在同一部署只安装一份；不直接执行发布来源，也不向每个 Agent 复制完整包。

```text
runtime/
├── connectors-center/<id>/                  # 外部原包
├── ru-connectors/<id>/<contentDigest>/      # 完整共享运行包
├── ru-agents/<agentKey>/connectors/<id>.json # 挂载引用
└── .state/
    └── connectors/<id>/                    # 配置、凭证与 CLI 私有状态
```

CLI 内置来源为 Platform verified bundle 的 connectors 目录，其源码清单与技能位于相邻 `agent-platform-builtins/{dbx,httpx}/connector/`；native `builtin.platform-control` 与 `builtin.web-control` 的清单和技能各自完整保存在 `internal/resources/connectors/<id>/`，随程序内嵌。共享包包含清单、技能及其资源、适用的 bin/libs；运行引用和快照不保存凭据。模型通过 `@connectors/<id>/...` 访问当前 Agent 已挂载的包；Container 只读映射对应 `/connectors/<id>`。

## 安装、升级和回收

安装先复制到隐藏临时目录并校验内容，再以内容摘要原子发布。重复挂载校验并复用已有包。启动重建普通 ru-agents，但保留经过校验的共享包；共享目录直接初始化，不提供旧布局迁移。

Catalog 发布期间持有装配锁；当前挂载、活动 Run/Terminal 和 MCP session 保留包租约。连接器内容更新可以发布新的挂载快照，旧调用继续使用旧包。普通 Agent 文件的变更仍遵守活动运行目录租约。MCP 路由按 Agent、连接器组件及包版本隔离，共享文件不意味着共享 MCP 会话。

Run 的挂载快照只保存在 Platform 进程内存中，Run 结束即清除；持久引用位于共享根 `.run-pins`。等待用户输入时保留两者，以支持同进程续接；Platform 重启取消旧 Run 后释放其引用，快照随进程结束。旧的 `.state/run-connectors` 不再读取或写入，可手动删除。回收仅删除内容校验通过且没有引用、租约的版本，未知或被改动的目录保留。启动终态对账成功后释放已取消 Run 的持久引用；对账失败时保留引用，当前不会按时间强制清理它们。

MCP 绑定按挂载来源区分校验：当前 Agent 和活动运行的挂载包加载失败仍阻止绑定，错误包含 Agent key 和连接器 ID；仅来自历史 Run 持久引用的包加载失败时记录警告并跳过，不阻止 Platform 启动。该来源标记由运行时读取持久引用时赋予，不接受配置或持久 JSON 设置。等待中 Run 的引用与快照保留到该 Run 结束，同进程续接时仍严格校验原始包，不自动替换；Run 结束后两者一并删除，启动时取消历史 Run 并释放其持久引用；旧磁盘快照由部署方清理。有效的历史 MCP 包继续建立版本路由；后续 MCP 组件配置校验仍保持严格。

共享包路径通过统一文件访问策略只读；未挂载包不获得文件读取或 CLI 专属免审权。Host Bash 仍遵循现有宿主执行模型，不提供操作系统级沙箱隔离。

## builtin.platform-control

内嵌 native/no_auth 平台控制连接器提供 catalog_query、catalog_manage、chat_query、chat_manage、platform_inspect 和七个 desktop_* 工具，使用统一 `{action,args}`。工具归属和运行时元数据分别在 internal/connector/native.go 与 control_actions.go 维护。详见 [平台控制连接器](Platform控制工具设计.md)。

它与 builtin.web-control 可同时挂载，互不授予对方工具；技能读取所需 file_read 和 CLI 执行所需 Bash 均由 preset 或 Agent 显式声明。调用始终检查受信任挂载。Catalog/Chat 限普通 native main root；KBASE 原生根 Run 可以使用平台工具，ACP 不执行这些工具。Standalone 仅显示五个本地平台工具。

启动原子发布内嵌包并持有共享租约，Agent 只保存引用。原来的 builtin.desktop 已退役，历史工具记录不改写；迁移后的 Desktop 动作通过原有反向请求发送，确认规则保持由 Desktop 负责。

## builtin.web-control

网页控制只处理“按地址打开并控制网页”。它不管理 Website 条目，也不负责 WebApp 的安装、打包和发布；Website 与 WebApp 的页面在其 Copilot Run 的授权范围内同样由这些工具操作。

模型只面对两种标识：

- **url**：只用于 `workpanel_open`，不作为 Tab 身份。网页以 `http://` 或 `https://` 开头；文件预览以 `@workspace/` 或 `@chat/` 开头并且必须位于所指根内。其他写法（裸相对路径、无协议的主机名、绝对路径、`file://`）一律在发送前拒绝并给出改写提示，不做猜测。
- **surfaceId**：WorkPanel 中的一个 Tab，附带 `capabilities`。网页与 HTML 文件预览是可控页面，导航和刷新不改变 surfaceId；其他 Tab 的 surfaceId 以 `workpanel-item:` 开头，只能激活或关闭。能力分级见 [MCP 与工具交互](MCP与工具交互.md#tab-surfaceid-与能力)。

页面容器 ID 只在 Platform 内部使用；条目 ID 只以 `workpanel-item:` surfaceId 的形式出现。

| 分类 | 工具 | 说明 |
| --- | --- | --- |
| WorkPanel | `workpanel_state` | 当前 Chat 面板中打开的 Tab，每项带 `surfaceId` 与 `capabilities` |
| WorkPanel | `workpanel_open` | 打开网页或预览文件，是打开新页面的唯一入口；返回该 Tab 的 `surfaceId` 与 `capabilities`；`reload` 在打开后刷新 |
| WorkPanel | `workpanel_close` | 只接受 `all: true`，关闭整个面板；单个 Tab 用 `surface_close` |
| Surface | `surface_list` / `surface_state` | 发现已授权网页、读取单页状态；`surface_state` 省略 `surfaceId` 读取所属应用的当前页 |
| Surface | `surface_navigate` | `goto` / `reload` / `back`，页面内导航 |
| Surface | `surface_activate` / `surface_close` | 显示、关闭一个 Tab 或页面 |
| Surface | `surface_viewport` | 模拟设备视口（宽高、像素比、移动端）或 `reset` 恢复，用于检查移动端布局 |
| Surface | `surface_screenshot` | PNG 截图，保存到当前 Chat 并返回引用名 |
| Surface | `surface_evaluate` | 执行脚本并返回值；大脚本用 `expressionFile` |
| Surface | `surface_click` / `surface_element` | 真实点击；按选择器填写、选择、聚焦、滚动 |
| Surface | `surface_login` | 列出当前网页所属网站的已保存账号（脱敏），或让 Desktop 把账号密码填入登录表单；密码不进入工具结果 |
| Surface | `surface_cdp` | 没有专用工具的 CDP 方法（DOM、底层输入、Network） |
| AWCP | `awcp_manual` / `awcp_invoke` | 读取网站手册目录与章节、调用网站声明的业务动作 |

CDP 不单独成类：它是通道，每个方法都作用在某个 surface 上。有明确用途的方法是参数固定的 surface 工具；`surface_cdp` 只保留没有专用工具的方法，方法名保持 CDP 原名，同一操作只有一个入口。读取或修改页面内容时先用 `awcp_manual`，页面没有手册或没有匹配章节才使用 surface 内容工具，该规则只写在技能中。

实现要点：

- 每个工具在 `internal/tools/tool_web_control.go` 映射到既有的反向请求（WorkPanel 动作、`desktop.cdp.call`、`desktop.awcp.manual` / `desktop.awcp.invoke`）；`surface_viewport` 使用 Desktop 新增的 `Surface.setViewport`，WorkPanel 条目结果新增 `surfaceId` 与 `capabilities`，Platform 与 Desktop 需同批发布。
- 单个 Tab 通过 `workpanel_state`、`workpanel_open` 或 `surface_list` 取得 surfaceId 后用 `surface_close` 关闭，不通过 URL 或文件名推断关闭目标；`workpanel_close` 只关闭整个面板。
- 文件预览沿用 Desktop 的规则：文件必须位于 Workspace 内。`@chat/` 只有在 Chat 目录位于 Workspace 内时可用；Chat 目录在 Workspace 之外的预览尚未实现，需要 Desktop 接受 Chat 目录作为可信根。
- `workpanel_state` 返回条目的种类、打开地址、标题、激活状态、`surfaceId` 与 `capabilities`；页面尚在注册时 `surfaceId` 为 `null`，重读即可。
- 验证 Agent 生成的 HTML 时用 `workpanel_open` 打开文件，再用 surface 工具读取、操作和 `surface_viewport` 改变视口，不另启外部浏览器；没有 Desktop WorkPanel 时如需外部浏览器，profile 必须放在 Chat 目录之外的临时目录并在结束时清理。
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
