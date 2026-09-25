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

CLI 内置来源为 Platform verified bundle 的 connectors 目录，其源码清单与技能位于相邻 `agent-platform-connectors/{dbx,httpx}/connector/`；native `builtin.desktop` 的清单与技能保留在 `internal/resources/connectors/builtin.desktop/`，随程序内嵌。共享包包含清单、技能及其资源、适用的 bin/libs；运行引用和快照不保存凭据。模型通过 `@connectors/<id>/...` 访问当前 Agent 已挂载的包；Container 只读映射对应 `/connectors/<id>`。

## 安装、升级和回收

安装先复制到隐藏临时目录并校验内容，再以内容摘要原子发布。重复挂载校验并复用已有包。启动重建普通 ru-agents，但保留经过校验的共享包；旧平铺 ru-connectors 在首次转换时备份到 `.connector-layout-backup-*`。

Catalog 发布期间持有装配锁；当前挂载、活动 Run/Terminal 和 MCP session 保留包租约。连接器内容更新可以发布新的挂载快照，旧调用继续使用旧包。普通 Agent 文件的变更仍遵守活动运行目录租约。MCP 路由按 Agent、连接器组件及包版本隔离，共享文件不意味着共享 MCP 会话。

Run 的挂载快照保存在私有状态目录，持久引用位于共享根 `.run-pins`；等待用户输入时保留引用，以支持重启后的恢复。回收仅删除内容校验通过且没有引用、租约的版本，未知或被改动的目录保留。异常退出后没有完成终态对账的持久引用可能继续保留旧包，当前不会按时间强制清理它们。

共享包路径通过统一文件访问策略只读；未挂载包不获得文件读取或 CLI 专属免审权。Host Bash 仍遵循现有宿主执行模型，不提供操作系统级沙箱隔离。

## builtin.desktop

Agent 使用 `connectorConfig.connectors` 挂载 `builtin.desktop`。它的 native 声明仅接受平台注册的 `desktop.action`、`desktop.cdp`，自动接入 `desktop_action`、`desktop_cdp` 和 `desktop-action`、`desktop-cdp` 技能，并提供读取技能所需的 file_read；不会自动增加 Bash。

包声明和技能共享，实际工具仍由 Platform 经现有协议路由到 Desktop 客户端。已有参数 Schema、客户端归属、审批与 mode 限制继续生效；KBASE、ACP 不开放此能力。外部包不能伪造 builtin 命名空间或任意 native handler。

配置状态使用 `.state/connectors/builtin.desktop/connection.json` 的 configured。Connect 标记配置完成，Disconnect 清除本地配置状态，Check 读取本地状态，不保存 Token；客户端离线不清除 configured。每次调用仍要求当前 Agent 挂载且已配置，并由原有执行链检查实际客户端能力和审批。

## 显式离线迁移

迁移工具默认只输出预览。它将旧 Agent 的 Desktop 工具/普通技能引用转换为 builtin.desktop 挂载，并报告新增的工具入口；原来仅使用一种 Desktop 工具的 Agent 会获得另一种入口，应用前须显式接受该变化。无关 YAML 内容保持原样。

```sh
go run ./cmd/migrate-desktop --runtime-dir /path/to/runtime
# 升级 Platform 发布包，停止该部署的 Platform 和配置编辑器后执行：
go run ./cmd/migrate-desktop --runtime-dir /path/to/runtime --apply --offline --allow-expansion
# 回滚时使用应用结果中的备份目录：
go run ./cmd/migrate-desktop --rollback /path/to/runtime/.desktop-migration-xxx --offline
```

应用前备份 Agent 文件，将旧 Desktop 技能目录移入备份，并校验预览与实际内容一致；回滚也检查后续修改，避免覆盖新编辑。`--configure` 可额外标记 Desktop 配置完成，默认不修改该状态。自定义状态根需要为命令注入与部署相同的 `AP_RUNTIME_STATE_DIR`。本次代码变更不自动迁移任何运行中的部署。

验证覆盖共享复用、版本保留、恢复快照、挂载和配置门禁、发布完整性及迁移回滚。Windows 使用交叉编译验证；Windows 实机、真实 Desktop 反向调用及 Container Hub 联调仍需目标环境验证。
