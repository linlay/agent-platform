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

- **url**：用于 `workpanel_open` 或仅接受 HTTP(S) 的 `surface_open`；URL 不作为 Tab 身份；单个 Tab 通过 `surfaceId` 关闭。网页以 `http://` 或 `https://` 开头；文件预览以 `@workspace/` 或 `@chat/` 开头并且必须位于各自指向的根内。其他写法（裸相对路径、无协议的主机名、绝对路径、`file://`）一律在发送前拒绝并给出改写提示，不做猜测。
- **surfaceId**：一个已授权页面或 WorkPanel Tab，附带 `capabilities`。网页与 HTML 文件预览是可控页面，导航和刷新不改变身份；其他 Tab 使用 `workpanel-item:` 前缀，只能激活或关闭。能力分级见 [MCP 与工具交互](MCP与工具交互.md#tab-surfaceid-与能力)。

页面容器 ID 只在 Platform 内部使用；条目 ID 只以 `workpanel-item:` surfaceId 的形式出现。

| 分类 | 工具 | 说明 |
| --- | --- | --- |
| WorkPanel | `workpanel_state` | 当前 Chat 面板中打开的 Tab，每项带 `surfaceId` 与 `capabilities` |
| WorkPanel | `workpanel_open` | 在当前 Chat 的右侧工作面板打开网页或预览文件；返回 Tab 的 `surfaceId` 与 `capabilities`；`reload` 在打开后刷新 |
| WorkPanel | `workpanel_close` | 只接受 `all: true` 关闭整个面板；单个 Tab 用 `surface_close` |
| Surface | `surface_list` / `surface_state` | 发现已授权网页、读取单页状态；`surface_state` 省略 `surfaceId` 读取所属应用的当前页 |
| Surface | `surface_open` | 在当前 Run 绑定的浏览器打开 HTTP(S) 网页：Chrome 任务复用 live 工作区或按记住的授权懒连接，未连接时挂起等待用户连接（最长 5 分钟）；WorkPanel 任务改由右侧工作面板打开；返回 `surfaceId` 与 `provider` |
| Surface | `surface_observe` | 读取语义快照及 `observationId` / 元素 `ref`，只读 |
| Surface | `surface_navigate` | `goto` / `reload` / `back`，页面内导航 |
| Surface | `surface_activate` / `surface_close` | 显示、关闭一个 Tab 或页面 |
| Surface | `surface_viewport` | 仅 WorkPanel 页面及 HTML 预览支持模拟设备视口或 `reset` 恢复；Chrome 明确返回不支持，不切换环境 |
| Surface | `surface_screenshot` | PNG 截图，保存到当前 Chat 并返回引用名 |
| Surface | `surface_click` / `surface_element` | 按快照引用操作元素，两种 provider 均支持 fill/type/press/hover/check/uncheck/多选；保留有页面证据的选择器和坐标兼容模式 |
| Surface | `surface_scroll` / `surface_dialog` | 受控滚动和原生网页弹窗，保留动作结果与跟随观察 |
| Surface | `surface_upload` / `surface_downloads` | 网页与当前 Chat 文件之间的受限传递，不暴露字节或任意本机路径 |
| Surface | `surface_login` | 列出当前网页所属网站的已保存账号（脱敏），或让 Desktop 把账号密码填入登录表单；密码不进入工具结果 |
| AWCP | `awcp_manual` / `awcp_invoke` | 读取网站手册目录与章节、调用网站声明的业务动作 |

CDP 不单独成类：它是 Desktop 内部通道，每个页面操作都是参数固定的 surface 工具，模型不能发送脚本或原始 CDP 方法。Chrome 与 WorkPanel 的 AWCP 和标准控制并存，业务优先读取手册并使用匹配章节；仅协议缺失、有效手册没有匹配动作或用户任务明确允许标准界面操作时，使用对应 provider 的标准工具。未知 AWCP 写入结果不能转成点击重放。Chrome 与 WorkPanel 使用固定 AWCP 工具、语义快照及引用操作；现有应用 scope 保留原路径，普通 Chat 在 canonical Run 门禁后按冻结 backend 路由，不因 Chrome 失败借用 WorkPanel 或前台页面。

实现要点：

- 每个工具在 `internal/tools/tool_web_control.go` 映射到既有的反向请求（WorkPanel 动作、`desktop.cdp.call`、`desktop.awcp.manual` / `desktop.awcp.invoke`）。Chrome 新建及语义工具在 CDP 反向信封内使用 `Surface.openChrome` / `Surface.observe` / `Surface.click` / `Surface.interact`；Desktop 决定当前 Chat/Run 可访问的 provider、页面和引用，不新增 Query 字段或通用连接器执行参数。
- 普通 Chat 的 Surface 结果标明 `provider: workpanel | chrome`。Chrome 工作区由 Desktop 管理；用户开启记住授权时，新 Chrome Chat 可以延续已获授权的 live 工作区与页面，未连接时仍按记住授权懒连接或由用户在宿主恢复。工作区可连续，每个 Run 的 canonical Chat、根 owner、backend 与控制租约仍独立校验并冻结；模型不能从旧 Chat 历史或 URL 推断授权。同一工作区只有一个 Run 实际控制，交接先取消并排空旧请求，忙碌等待或由用户接管，不回退另一连接或 WorkPanel。Platform 不管理连接：收到 `browser_connection_required` 时，网页控制工具自行计时，以有界轮询 `Browser.awaitConnection` 等待用户在 Desktop 完成连接（单次 20 秒、总计 5 分钟，随 Run 取消），连上后把原调用重试一次；Desktop 只允许尚未派发页面动作的 Run 等待，因此重试不会重放页面写入。WorkPanel Run 调用 `surface_open` 时 Desktop 返回 `browser_backend_mismatch`，Platform 改用 `workpanel_open` 打开同一地址；网页加载错误不构成重新授权或重连的理由。所有新 Run 都重新发现页面、读取观察与 AWCP 手册；真实重连不复活旧 Surface，旧 refs/manual binding 不继承。Chrome 标准动作默认返回新观察供下一步直接使用，观察失败不改变原动作 outcome；未知业务写入和取消后的不确定结果先回读而不重放。共享浏览器工作区不改变 WorkPanel 的 Chat 归属、项目 Workspace、Chat 文件或凭据权限。
- 单个 Tab 通过 `workpanel_state`、`workpanel_open` 或 `surface_list` 取得身份后用 `surface_close` 关闭，不通过 URL 或文件名推断目标；`workpanel_close` 只关闭整个面板。
- `@workspace/` 与 `@chat/` 文件分别限定在 Workspace 和当前 Chat 根内；HTML 文件预览支持有界语义观察和类型化操作，不提供脚本或原始 CDP 入口。
- `workpanel_state` 返回种类、打开地址、标题、激活状态、`surfaceId` 与 `capabilities`；页面尚在注册时身份为 `null`，可重新读取。WorkPanel 的 `surface_viewport` 使用固定 `Surface.setViewport`，与 Desktop 配套发布。
- `awcp_invoke.paramsFile` 走标准文件读取 AccessPolicy 与审批。
- 只读工具（`workpanel_state`、`surface_list`、`surface_state`、`surface_observe`、`surface_screenshot`、`awcp_manual`）可通过只读执行策略，planning 仍受既有工具排除配置约束；`surface_open` 和引用 mutation 不获得只读豁免。
- Standalone 运行模式下会话只暴露 `workpanel_*`，且不支持文件预览；`surface_*` 与 `awcp_*` 需要 Desktop 运行模式。
- 传输层错误码沿用既有的 `desktop_action_*` / `desktop_cdp_*` 前缀。
- Platform 的工具定义、native 注册表和技能资源随二进制发布；新增 Surface 方法要求 Desktop 与 Platform 配套升级、重启并使用新 Run，不手改部署中的 `ru-connectors` 或 Agent 配置。

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
