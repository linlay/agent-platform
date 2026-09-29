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

CLI 内置来源为 Platform verified bundle 的 connectors 目录，其源码清单与技能位于相邻 `agent-platform-connectors/{dbx,httpx}/connector/`；native `builtin.desktop` 与 `builtin.desktop-web` 的清单和技能入口保留在 `internal/resources/connectors/`，公共技能资料只维护一份，随程序内嵌。共享包包含清单、技能及其资源、适用的 bin/libs；运行引用和快照不保存凭据。模型通过 `@connectors/<id>/...` 访问当前 Agent 已挂载的包；Container 只读映射对应 `/connectors/<id>`。

## 安装、升级和回收

安装先复制到隐藏临时目录并校验内容，再以内容摘要原子发布。重复挂载校验并复用已有包。启动重建普通 ru-agents，但保留经过校验的共享包；共享目录直接初始化，不提供旧布局迁移。

Catalog 发布期间持有装配锁；当前挂载、活动 Run/Terminal 和 MCP session 保留包租约。连接器内容更新可以发布新的挂载快照，旧调用继续使用旧包。普通 Agent 文件的变更仍遵守活动运行目录租约。MCP 路由按 Agent、连接器组件及包版本隔离，共享文件不意味着共享 MCP 会话。

Run 的挂载快照保存在私有状态目录，持久引用位于共享根 `.run-pins`；等待用户输入时保留引用，以支持重启后的恢复。回收仅删除内容校验通过且没有引用、租约的版本，未知或被改动的目录保留。异常退出后没有完成终态对账的持久引用可能继续保留旧包，当前不会按时间强制清理它们。

共享包路径通过统一文件访问策略只读；未挂载包不获得文件读取或 CLI 专属免审权。Host Bash 仍遵循现有宿主执行模型，不提供操作系统级沙箱隔离。

## builtin.desktop

Agent 使用 `connectorConfig.connectors` 在两个内置连接器中选择一个：

| ID | 中文名称 | 英文名称 | 技能内容 |
| --- | --- | --- | --- |
| `builtin.desktop` | 桌面端 | Desktop | 全部 Desktop Action 与完整 CDP/AWCP |
| `builtin.desktop-web` | 桌面端（网页） | Desktop (Web) | 完整 WorkPanel、`desktop.web.*` 与完整 CDP/AWCP |

网页版保留 WorkPanel 本地文件预览、通用标签页和关闭面板；不提供 Website 条目管理、WebApp 安装/生命周期、设置、市场、服务、Agent/Skill 编辑、宠物等技能资料。两个包使用相同的 `desktop-action`、`desktop-cdp` 技能名，运行路径分别属于各自 `@connectors/<id>/skills/`，不跨包引用。

两者均声明相同的 `desktop.action`、`desktop.cdp`，自动接入 `desktop_action`、`desktop_cdp` 和技能读取所需的 `file_read`，不会自动增加 Bash。Platform/Desktop handler、动作白名单、审批、客户端归属与 mode 限制共用一套。网页版的差异是提供给模型的操作资料，不是额外权限隔离；知道其他有效动作名称的调用仍走现有执行规则。共享工具 Schema 不枚举业务域，要求读取当前挂载技能。

同一 Agent 不能同时挂载两版，关系只由各自 `connector.json.mutuallyExclusiveWith` 声明，不在源码按 Desktop ID 判定。通用校验支持内置和外部包、单向声明；YAML 装载后的包解析、源码编辑、连接器选择保存和冻结定义恢复均适用。选择接口失败时不保存冲突配置，返回 HTTP 400、`data.error.code=connector_selection_conflict`、`connectorId`、`conflictingConnectorIds` 和本地化消息。目录透传声明，WebClient 点击冲突项会显示冲突名称、提示先取消原选择，并在列表顶部保留错误；服务端兜底失败也可见，不自动替换已选项。该交互已由 WebClient 组件测试验证，真实 Desktop 环境仍需联调。

现有 `builtin.desktop` 配置继续使用全部技能，无需迁移。切换只影响新发布定义；活动 Run 和可恢复等待继续使用冻结包与连接器 ID。

### 技能装配与维护

完整源位于 `internal/resources/connectors/builtin.desktop/`；网页版目录只存自身清单、`desktop-action/SKILL.md` 与网页动作目录。`internal/connector/builtin.go` 在内嵌装配时选择公共 CDP 全目录、WorkPanel/web-surfaces references、图标及 native 定义，再装配网页版专有文件，生成自包含运行包。共享资料只在完整源修改一次，两版动作目录与入口分别维护；新增网页动作应同步网页版目录。测试检查公共文件字节一致、网页版动作范围和 Markdown 引用完整性。

启动原子安装两版到各自 `ru-connectors/<id>/<contentDigest>` 并持有租约，沿用共享、升级、回收和 Run 快照机制。执行授权从冻结挂载解析实际 ID，不把网页版重写成完整版。仅显式注册的两个内置 ID 可以使用这些 native 能力。

Agent 加载不再根据 `toolConfig.tools` 中出现 `desktop_action` / `desktop_cdp` 就强制要求声明特定连接器 ID。工具声明与连接器挂载各自解析，工具存在性沿用通用工具目录、模型工具过滤与调用路由；未注册工具调用返回 `tool_not_registered`，不通过工具名反推连接器配置。这里没有新增对全部工具的 catalog 硬校验，也不把远端 MCP 发现加入加载关键路径。

移除的是配置加载阶段的特殊绑定阻断，不会根据工具名自动挂载包、导入 Skill 或生成执行授权。当前 Desktop native handler 的受信任挂载、客户端归属和审批检查仍保留；只有工具声明但缺少运行授权时，Agent 可装载和聊天，实际 Desktop 调用仍返回工具错误。旧普通 Skill 引用的解析与保留名称规则不在此次调整范围内，不能据此保证所有旧配置都会变为 ready。

包声明和技能共享，实际工具仍由 Platform 经现有协议路由到 Desktop 客户端。已有参数 Schema、客户端归属、审批与 mode 限制继续生效；KBASE、ACP 不开放此能力。外部包不能伪造 builtin 命名空间或任意 native handler。

两版 Desktop 均明确声明 `auth_mode: "no_auth"`：挂载即具备调用资格，无需连接配置，不读写 connection.json。管理接口返回无需配置及不可执行认证操作的能力字段；实际客户端可用性、归属和审批在调用时检查。

## 显式离线迁移

迁移工具默认只输出预览。它将旧 Agent 的 Desktop 工具/普通技能引用转换为 builtin.desktop 挂载；已选择任一 Desktop 版本时保留该选择，不追加另一版，并报告新增的工具入口；原来仅使用一种 Desktop 工具的 Agent 会获得另一种入口，应用前须显式接受该变化。无关 YAML 内容保持原样。

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
