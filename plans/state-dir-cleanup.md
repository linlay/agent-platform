# `.state` 收口：后续计划

`.state`（`AP_RUNTIME_STATE_DIR`，缺省 `<AP_RUNTIME_DIR>/.state`）只用来保存凭据和身份。它的规则是：不能删、需要备份、三档权限下普通文件工具和 Bash 都硬拒绝。混入会增长、可再生的运行数据之后，清理、备份和迁移规则就没法统一。

2026-10-12 的 5a96e8a2 已经搬走 Run 连接器快照、memory-worker 进度和文档预览登记，但 `.state/connectors/` 内部当时没有清理，之后又新增了几处。本计划记录 2026-11-01 的核对结果和整改方案。

整改遵循直接切换：不写迁移代码，旧目录不再读取。需要保留的数据由部署者按本文末尾的清单手工移动或删除。

## 目标边界

允许放在 `.state` 的只有三类：

1. **凭据**：连接器 Token/OAuth 凭据、OAuth 客户端注册、CLI 自管登录目录。
2. **身份**：缺省身份票据 `identity/sso-id-token`。
3. **含密钥的受管配置**：`memx/models.yml`、`kbx/index.yml`。

另外，凭据本身的附属元数据可以放在凭据旁边，但要同时满足三个条件：只服务于这份凭据；和凭据一起写、一起删；每个连接器只有固定几个文件，体积有界。

以下内容不放进 `.state`：收据、审计、操作记录、锁、日志、进度、期望状态、授权策略、临时文件。

## 现状（2026-11-01 核对代码与 `zenmind-env/.state`）

实际目录约 520K，其中 `connectors/` 占 488K。

### 符合目标

| 路径 | 内容 | 代码 |
|---|---|---|
| `connectors/<id>/credentials.json`、`pending-credentials.json` | Token 表单凭据和待验证的候选凭据 | `internal/connector/state.go`、`internal/connectorauth/token.go` |
| `connectors/<id>/oauth.json` | MCP OAuth token | `internal/connectorauth/store.go` |
| `connectors/<id>/client-*.json` | OAuth 客户端注册，可能含 clientSecret | `internal/connectorauth/client_info.go` |
| `connectors/<id>/config/` | CLI 自管登录目录，即 `CONNECTOR_CONFIG_DIR` | `internal/connector/cli_environment.go` |
| `identity/sso-id-token` | 单独运行时的缺省身份票据；Desktop 用 `--sso-id-token-file` 传入自己的文件 | `internal/config/config_defaults.go` |
| `memx/models.yml` | memx 模型配置，含 apiKey | `internal/app/app.go`（`ConfigDir`） |
| `kbx/index.yml` | KBX 默认 embedding 配置，含 api_key | `internal/kbx/library_engine_config.go` |

`config/` 有个已知副作用：CLI 会把自己的日志和缓存也写进去，例如 `tmeet/config/logs/` 和 `wecom/config/cache/`。Platform 不控制 CLI 的写法，这是把配置目录指向这里的代价，本计划不处理。

### 凭据附属元数据（符合上面三个条件，保留）

| 路径 | 内容 |
|---|---|
| `connectors/<id>/auth-state.json` | 凭据的 generation/revision；MCP 凭据监听和指纹使用 |
| `connectors/<id>/connection.json` | 是否已完成配置 |
| `connectors/<id>/verification.json` | 凭据验证结果缓存，按指纹失效 |
| `connectors/<id>/discovery-*.json` | OAuth 资源与授权服务器的发现结果 |

### 不符合目标

| # | 路径 | 内容 | 问题 |
|---|---|---|---|
| S1 | `connectors/authorization-operations/<sha256>.json` | 每次授权尝试的终态记录；`internal/connectorauth/authorization_operation.go`，10-02 引入 | 不是凭据。没有任何清理，本机已有 36 个文件。用途是跨重启查询旧尝试，与"重启取消未完成 Run"冲突。目录名还占用了连接器 ID 命名空间：`authorization-operations` 符合 `idPattern`，同名连接器的状态目录会和它重叠 |
| S2 | `connectors/<id>/invocations/<key>.json(.done)` | connectorops 写操作的幂等 claim、审计和结果；`internal/connectorops/receipts.go`，09-19 引入。调用方是 VIEW invoke（`internal/viewaction`）和 `/api/connectors/execution` | 不是凭据。没有保留期，会一直增长。审计里有 chatId、runId、params，结果里有业务输出，所以搬出后仍然需要保护 |
| S3 | `connectors/.lock/<id>/state.lock`、`control.json`、`delete.json` | 凭据写入与连接器删除之间的锁和删除栅栏；`internal/connector/state_control.go`，10-18 引入 | `state.lock` 是锁。AGENTS.md 写着"连接器锁统一放入 runtime `.lock/`"，其他连接器锁（layout、assembly、install、operations、leases）都已迁过去，只有这一个留下。`control.json`、`delete.json` 属于凭据栅栏，见方案 3 |
| S4 | `kbx/libraries/<sha256(db)>/` | 每次 KBX 调用的临时配置（含密钥）；`internal/kbx/library_config.go` | 临时文件用完会删，但每个库的空目录一直留着，库删除后也不清 |
| S5 | `webapps/`（已移出） | 原由 `WebAppsStateDir()` 派生，计划放 WebApp 安装记录、期望状态、授权、user-config 和日志；代码只做过路径校验，从未写入 | 已在 9f915bf2 处理，见方案 4 |

### 不处理

- `memx/.memx-instance.lock`：memx 在自己的配置目录里放实例锁。它是 memx 自己的行为，体积为零，暂不处理。
- `kbx/index.yml` 虽然是由模型 registry 派生的副本，但含密钥，符合第三类，保留。

### 残留文件（现在的代码已不再写）

- `identity/access-token`：23782f59 已改名为 `sso-id-token`。
- `connectors/{jira-cli,tencent-docs,wecom}/oauth.lock`：a7179d3d 后锁改到 `.lock/<id>/state.lock`。
- `connectors/wecom-cli-connector/`：连接器已不在 connectors-center，状态目录和 `connector-installs/wecom-cli-connector` 都残留，应该是在 Platform 之外手工删包造成的。
- 各层的 `.DS_Store`。

### 文档与代码不一致

| 位置 | 现在的说法 | 实际情况 |
|---|---|---|
| `docs/配置化说明.md:105` | `.state` 只保存四类内容 | 还有 S1–S4 |
| `docs/连接器执行协议.md:57` | 收据继续位于 `.state/connectors/<id>/invocations` | 和上一行矛盾 |
| `internal/config/state_paths.go:30-32` 注释 | Run state, receipts and progress records do not belong here | 收据就在这里 |
| `docs/连接器.md:323` | 凭据写入与删除共享 `<有效连接器状态根>/.lock/<id>/state.lock` | 和 AGENTS.md"连接器锁统一放入 runtime `.lock/`"矛盾 |

## 方案

### 1. 授权尝试记录改为只保存在进程内存（S1）

- `connectorauth.Manager` 除了每个连接器当前的 `sessions[id]`，再增加一个已结束尝试的表，键为 `connectorId/attemptId`，只存不含 URL、凭据和诊断正文的 `Session`。条目在结束后保留 15 分钟（与尝试本身的时限相同），每次读写时顺带清理过期条目。
- `SessionStatus` 的回退路径从读文件改为读这个表。表里没有的尝试保持现有错误语义：HTTP 返回 `auth_session_unavailable` 409，wait 返回错误。
- 删除 `persistAuthorization`、`persistedAuthorization` 和 `authorization-operations` 目录的读写。`internal/operationstate/store.go` 的 `Write`/`Read`/`Path` 没有其他调用方，一起删除，只保留 `Receipts`。
- 重启后旧尝试一律查不到，这与"重启取消所有未完成 Run，wait 随之结束"一致。
- 目录移走后，S1 的连接器 ID 冲突自然消失，不另外加保留名。

测试：被新尝试替换后，旧尝试仍能查到终态；过期后查不到；不再创建 `authorization-operations` 目录。

文档：`docs/原生等待工具.md` 补一句"授权尝试终态只保存在进程内存，重启后不可查"。

### 2. connectorops 收据移到 `ru-receipts`（S2）

- 新目录 `<AP_RUNTIME_DIR>/ru-receipts/connectors/<connectorId>/`，从 runtime 根派生，与 ru-kbases、ru-previews 一样跨重启保留，不随启动清空，也不新增环境变量。
- `config.PathsConfig` 参照 `RUPreviewsDir` 增加 `RUReceiptsDir` 与 `EffectiveRUReceiptsDir()`；`connectorops.Service` 不再用 `Sources.PersistentRoot()`，改为注入这个根。
- 文件格式、key 推导（包括 `execution-v2` 盐）和"claim 先落盘再派发、未完成的 claim 永不重放"的语义都不变。
- 保护：加入 `accesspolicy.PlatformProtectedPaths`，三档权限下文件与 Bash 都硬拒绝；加入 `validateConnectorPaths` 的重叠校验；加入 `runtimerecovery` 的 `data` 列表；`.gitignore` 增加 `/ru-receipts/`；确认 release 和资源同步不打包它。
- 保留期：按 claim 文件的修改时间，超过 30 天的 claim 和对应的 `.done` 一起删除。启动时清理一次，之后每天一次。
- 删除连接器时不顺带删收据，到期自然清理。同 ID 重新导入后，旧收据只会阻止相同 key 重放，不会造成错误。
- 旧的 `.state/connectors/<id>/invocations` 不再读取。升级时可以手工移入新目录继续生效，格式相同。

测试：收据写在新根下；超过保留期后被清理；新根受访问策略保护；与其他根重叠时拒绝启动。

文档：`docs/连接器执行协议.md:57`、`docs/配置化说明.md`（目录表与保留期）、`docs/工具目录权限.md`（受保护路径）、AGENTS.md 第 5 节数据结构。

### 3. 连接器状态锁移到 runtime `.lock/`（S3）

- `state.lock` 改到 `<runtime>/.lock/connectors/state/<id>.lock`，与 `operations/`、`install/`、`leases/` 并列。`AcquireStateLock` 改为接收 `Sources`，由 runtime 根定位锁，不再使用状态根。
- `control.json`、`delete.json` 只决定凭据能否写入、删除事务如何恢复，满足附属元数据的三个条件，留在 `.state`。目录从 `.state/connectors/.lock/<id>/` 改名为 `.state/connectors/.control/<id>/`，避免名字里有 lock 却不放锁。
- 前提是不支持多个 runtime 共享一个 `AP_RUNTIME_STATE_DIR`；锁按 runtime 根生效，这个约束写进 `docs/配置化说明.md`。
- 旧的 `.lock/<id>/` 不再读取。升级前停掉旧进程，再把 `control.json` 和 `delete.json` 手工移到 `.control/<id>/`；没有它们的连接器什么都不用做。

测试：凭据写入与删除仍然互斥；删除、回滚和恢复流程不变；锁文件出现在 runtime `.lock/` 下，`.state` 中不再出现 `.lock`。

文档：`docs/连接器.md:323`、AGENTS.md 连接器锁一节。

### 4. WebApp 管理目录不放 `.state`（S5，已完成）

9f915bf2 已落地：

- 删除 `WebAppsStateDir()` 及其路径校验。
- `plans/webapp-node-hosting.md` §6 改为两个目录：`<RuntimeDir>/webapps/<webappId>/` 放 `app/`（当前代码，更新时原地替换，不分版本）以及 `install.json`、`desired.json`、`policy.json`、`user-config.json` 和 `logs/`；`<RuntimeDir>/webapp-data/<webappId>/` 放应用数据。更新经 `webapps/.staging/` 原子改名替换。
- `webapps/` 不论宿主是否开启都在 `PlatformProtectedPaths` 中，Agent 普通工具不能读写策略和期望状态；应用只能读 `app/` 和指定的 user-config 文件，日志只由父进程写。
- `docs/WebApp托管.md` 已同步。

如果以后 user-config 需要保存密钥字段，再单独把这些字段放进 `.state/webapps/<webappId>/`，届时另行讨论。

### 5. KBX 临时配置不留空目录（S4）

- `libraryConfigRunner.Run` 不再按库建 `libraries/<sha256>/` 目录，改为直接在 `kbx/` 下 `os.CreateTemp(".library-*.yml")`，调用结束后删除。
- 启动时删除 `kbx/` 下遗留的 `.library-*.yml`，覆盖崩溃后留下含密钥临时文件的情况。
- 旧的 `kbx/libraries/` 空目录手工删除。

### 6. 规则写进文档和注释

- `docs/配置化说明.md:105`：按"目标边界"重写，列出三类内容和附属元数据的三个条件，删掉"只保存四类内容"。
- `internal/config/state_paths.go`：注释与目标边界一致。
- AGENTS.md：在第 5 节或第 9 节加一句"`.state` 只放凭据、身份和含密钥的受管配置；收据、锁、日志、策略与进度放到对应的运行目录"，并把锁和收据位置改成新事实。按 AGENTS.md 编辑约定，原句不实的地方原地改写，不追加例外。

## 待确认

1. 收据目录名用 `ru-receipts/connectors/<id>/`，还是 `connector-receipts/<id>/`？建议用前者，与 ru-previews 同一类：平台生成、跨重启保留、受保护。
2. 收据保留期是否定为 30 天（与 mail 正文保留期相同）？未完成的 claim 是否也按 30 天删？建议都删，30 天后客户端不会再用同一个 key 重试。
3. `control.json`、`delete.json` 是否同意留在 `.state/connectors/.control/<id>/`？另一个选择是放到 runtime 下另建的持久目录，但那样凭据栅栏就和凭据分开了。

## 批次

| 批次 | 内容 | 说明 |
|---|---|---|
| 一 | 方案 4（WebApp 改计划，已完成）、方案 1（授权记录进内存） | 先拦住新增；方案 1 改动小，并顺带消除 ID 冲突 |
| 二 | 方案 2（收据迁移与保留期） | 需要新路径配置、访问保护、恢复列表和文档一起改 |
| 三 | 方案 3（状态锁）、方案 5（KBX 临时目录）、方案 6 收尾 | 方案 3 动到删除事务，单独回归 |

每批都要满足：`go test ./...` 通过；修改范围外的行为不变；同步检查 README、AGENTS.md、docs/ 和 `.gitignore`。

## 部署手工清理清单

升级到对应批次前，先停掉同一 runtime 的旧进程。

```text
# 批次一
rm -r  <state>/connectors/authorization-operations

# 批次二（可选：保留旧收据继续防重放）
mkdir -p <runtime>/ru-receipts/connectors/<id>
mv <state>/connectors/<id>/invocations/* <runtime>/ru-receipts/connectors/<id>/
rmdir <state>/connectors/<id>/invocations

# 批次三
mkdir -p <state>/connectors/.control/<id>
mv <state>/connectors/.lock/<id>/control.json <state>/connectors/.lock/<id>/delete.json <state>/connectors/.control/<id>/   # 存在才移
rm -r  <state>/connectors/.lock
rm -r  <state>/kbx/libraries

# 残留（随时可删）
rm     <state>/identity/access-token
rm     <state>/connectors/*/oauth.lock
rm -r  <state>/connectors/wecom-cli-connector <runtime>/connector-installs/wecom-cli-connector   # 确认已不再使用
find   <state> -name .DS_Store -delete
```

## 验收

在 `zenmind-env` 上完成三个批次和手工清理后，`.state` 下只剩：

```text
.state/
├── connectors/
│   ├── .control/<id>/{control.json,delete.json}   # 仅删除过或重新导入过的连接器
│   └── <id>/{credentials,pending-credentials,oauth,client-*,auth-state,connection,verification,discovery-*}.json + config/
├── identity/sso-id-token
├── kbx/index.yml
└── memx/{models.yml,.memx-instance.lock}
```
