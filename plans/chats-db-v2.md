# chats.db V2：启动期升级、MAILS 并库与 Run 字段

一次结构升级完成以下几件事：

1. sqlitecontract 支持在启动阶段把已知旧版本就地升级，替代现在"结构不符即拒绝启动"的做法；运行期仍只校验。
2. MAILS 从 mail.db 并入 chats.db，停用 mail.db。
3. 归档的 Chat 退出 mail。
4. CHATS、RUNS 补齐列：创建关系、等待过期时间、Run 档位、控制归属、来源；删除无用的 `TEAM_ID_`。
5. chats.db、archive.db 加入工具硬拒绝名单。

Mailbox 的后续阶段见 [mailbox-next.md](mailbox-next.md)，本方案是其中阶段 A 的前置。

## 已确认的决定

| 项 | 决定 |
|---|---|
| chats.db | V1 → V2 |
| archive.db | V1 → V2 |
| MAILS | 并入 chats.db；表始终存在，`mail.enabled` 只控制是否读写 |
| 归档 | 归档的 Chat 退出 mail，处理方式与删除相同 |
| 升级时机 | 只在启动阶段检查并升级；运行期仍只校验 |
| `CONTROL_`、`ORIGIN_` | 与 `USAGE_` 一样，作为带前缀的列组，Go 中各对应一个结构体；本次只建列，接入另做 |
| 重启后丢失等待态的归属 | 可以接受（重启本来就会取消所有未完成的 Run） |
| 旧 mail.db | 当作不存在：代码不打开、不导入、不删除，也不校验 |
| MAILS 发件字段 | 归成 `FROM_` 一组：`SUBJECT_` → `FROM_SUBJECT_`，`ORIGIN_TYPE_` → `FROM_TYPE_` |
| executions.db | 本次不动，保持 V2；其中的 `TEAM_ID_` 暂缓删除 |
| `AWAITING_EXPIRES_AT_` | 只加列，值恒为 0；过期行为另做（见"另做"） |
| 没有版本标记的旧库 | 标记为 0/0 且结构完全等于 V1 的，视为 V1 并升级 |
| 升级备份 | 不自动清理，每次升级留一份，由人手动删除 |
| AGENTS.md | 不另加例外，改写 §9 现有那句（见"文档"） |
| archive.db 同步列 | `ARCHIVED_CHATS` 加 `OPENED_BY_`，`ARCHIVED_RUNS` 加 `ACCESS_LEVEL_` 与 `ORIGIN_` 组 |
| `ORIGIN_TYPE_` | 加入 `ORIGIN_` 组，记录所有 Run 的来源（user/agent/automation） |

## 结构变化

### chats.db V2

CHATS：

| 变化 | 列 | 说明 |
|---|---|---|
| 新增 | `OPENED_BY_AGENT_KEY_ TEXT NOT NULL DEFAULT ''` | 开出该 Chat 的 Agent。只在 `chat_send {newChat:true}` 建会话时写入，之后不变 |
| 新增 | `OPENED_BY_CHAT_ID_ TEXT NOT NULL DEFAULT ''` | 发起的会话 |
| 新增 | `AWAITING_EXPIRES_AT_ INTEGER NOT NULL DEFAULT 0` | 本次只加列，值恒为 0 |
| 删除 | `TEAM_ID_` | 只出现在建表语句中，业务代码不读也不写 |
| 新增索引 | `IDX_CHATS_OPENED_BY_ (OPENED_BY_AGENT_KEY_, UPDATED_AT_ DESC, CHAT_ID_ DESC)` | 供工作会话列表使用 |

RUNS（新增列均为 `TEXT NOT NULL DEFAULT ''`，另有说明的除外）：

| 组 | 列 | 来源 |
|---|---|---|
| — | `ACCESS_LEVEL_` | Run 启动时写受理档位，结束时更新为最终档位 |
| `CONTROL_` | `CONTROL_TRANSPORT_`、`CONTROL_LANE_`、`CONTROL_SUBJECT_`、`CONTROL_BOUNDARY_` | 控制归属 `controlscope.Scope` |
| `CONTROL_` | `CONTROL_BOUND_ INTEGER NOT NULL DEFAULT 0` | 区分"进程内创建、归属为空"和"没有归属" |
| `ORIGIN_` | `ORIGIN_TYPE_` | 平台判定的来源 `queryinput.Origin.Type`（user/agent/automation），与 JSONL query 行顶层的 origin 一致 |
| `ORIGIN_` | `ORIGIN_AGENT_KEY_`、`ORIGIN_SUBJECT_`、`ORIGIN_CHAT_ID_`、`ORIGIN_RUN_ID_`、`ORIGIN_TOOL_ID_` | `contracts.RunOrigin` |
| 删除 | `TEAM_ID_` | 同上 |

MAILS：整表连同索引从 mail.db 移入。借这次移动顺带调整，HTTP/JSON 字段名不变：

- `FROM_CHAT_DELETED_AT_`、`TO_CHAT_DELETED_AT_` 改为 `FROM_CHAT_CLOSED_AT_`、`TO_CHAT_CLOSED_AT_`，"关闭"包括归档和删除。
- 发件字段归成 `FROM_` 一组：`SUBJECT_` → `FROM_SUBJECT_`，`ORIGIN_TYPE_` → `FROM_TYPE_`。

### archive.db V2

- `ARCHIVED_CHATS`、`ARCHIVED_RUNS` 删除 `TEAM_ID_`。
- `ARCHIVED_CHATS` 增加 `OPENED_BY_` 两列，`ARCHIVED_RUNS` 增加 `ACCESS_LEVEL_` 与 `ORIGIN_` 组（含 `ORIGIN_TYPE_`）。归档与恢复的复制语句同步带上这些列。它们是 Chat 和 Run 本身的事实，归档后再恢复不应丢失；现有归档表本来也完整保留了 `USAGE_` 与 `FEEDBACK_`。
- 不归档 `CONTROL_`：控制归属只在进程内有意义。
- 不加 `AWAITING_EXPIRES_AT_`（归档库没有进行中的等待项），也不加 MAILS。

### mail.db

当作不存在：删除打开它的代码和 runtime-recovery 中对它的校验，已有文件不导入、不删除，文档也不再提及。

## 归档后的 mail

归档钩子与删除钩子合并为同一个"关闭"处理：

- **寄往该 Chat 的**：未完成的信转为 `failed` 并寄失败回信；未消费的回信丢弃；即时消息、请示撤回；清空这些行的正文副本。
- **该 Chat 发出的**：已受理的回信照常投递；未完成的信转为 `failed`，不寄失败回信；即时消息、请示撤回。
- 记录 `FROM_CHAT_CLOSED_AT_`/`TO_CHAT_CLOSED_AT_`；两端都已关闭且邮件为终态时删除该行。
- 恢复归档不恢复任何 mail，也不重投。恢复后它就是普通 Chat，可以重新收信。
- 对账只需一条 SQL：MAILS 引用的 chatId 不在 CHATS 中，即视为已关闭（排除 `accepting` 的预留地址）。不再查询 archive.db。
- 代价：恢复归档时，如果相关 mail 行已经因两端关闭被删除，模型输入中那几条历史 mail 不再带信封头，正文仍在 JSONL 中。

## 连接与并发

- chats.db 改为 WAL；每个连接都设置 `busy_timeout=5000`、`synchronous=FULL`，通过 DSN 参数配置，确保连接池中的每个连接都生效。现在 chats.db 使用回滚日志模式，没有设置 busy_timeout，写入靠 FileStore 的进程内锁串行；不改的话，mailbox 写入时 chat 的读写会立刻得到 `SQLITE_BUSY`。
- 启动时只打开一次、校验或升级一次，再把同一个 `*sql.DB` 交给 chat 存储和 mailbox 存储。两边各自的锁照旧；任何一方都不在自己的事务里调用另一方。
- 依赖方向：建表语句仍归各自的包（CHATS/RUNS 归 `internal/chat`，MAILS 归 `internal/mailbox`）。mailbox 已经依赖 chat，chat 不能反过来依赖 mailbox，因此 chats.db 的完整契约（V2 定义、V1→V2 升级）放在一个同时依赖两者的小包中（暂名 `internal/chatdb`），由 app 和 runtime-recovery 共同使用。archive.db 的契约留在 chat 包。

## 升级流程

各库独立执行。任一步失败都回滚并拒绝启动，原库保持不变。

1. 读取标记，确认是本库已知的旧版本。标记为 0/0 且结构完全等于 V1 的库，视为 V1。
2. 用保留的 V1 建表定义完整校验旧结构。
3. 用 `VACUUM INTO` 做一致性备份并做完整性检查。备份放在原库旁边，文件名带旧版本号和时间，不自动清理。
4. 在一个事务内执行 DDL、校验目标结构、写入新版本号，然后提交。
5. 提交后切换为 WAL（journal_mode 不能在事务内修改），再按当前契约完整校验一次。

实现要点：

- `internal/sqlitecontract` 的 Spec 增加迁移声明（旧版契约加 DDL 语句列表），只在 `InitializeOrVerifyAtStartup` 中执行；运行期的 `InitializeOrVerify` 遇到旧版本仍然拒绝。
- 加列用 `ALTER TABLE ADD COLUMN`，删 `TEAM_ID_` 用 `ALTER TABLE DROP COLUMN`（该列不在任何索引或约束中）。结构校验忽略列的物理顺序，加出来的列与新建库一致。
- 现有的备份做法可参考 `internal/automation/execution_store.go`，但它是重建为空库，不能照搬。

## 必须同时做的保护

并入 MAILS 后，chats.db 中有 subject 和权限收据，之后还会放控制归属。现在 chats.db 和 mail.db 都不在硬拒绝名单中（`internal/accesspolicy/protected.go` 只有 StateDir、身份文件和预览记录），full_access 的写入根是 `@root`，Agent 可以直接修改它们。V2 上线时，把 `chats.db`、`chats.db-wal`、`chats.db-shm`、`archive.db` 及其 `-journal` 加入硬拒绝名单。

Bash 中的不透明命令仍然无法拦截，与 `.state` 的现状相同，不是新增的缺口。升级备份不含 MAILS 和控制归属，不加入名单。

## 另做

以下内容本次只建列，行为另开一轮。

**Run 字段接入**（`ACCESS_LEVEL_`、`CONTROL_`、`ORIGIN_`）只需改三处：

- `internal/runtime/runstate/control_scope.go`：两张内存表改为先查内存、再查列，内存条目从此可以淘汰。
- `internal/chat/run_record.go` 的 `LoadRunRecord`：多读这些列；用 `ACCESS_LEVEL_` 取代现在从 JSONL query 行读取档位的做法。
- `internal/chat/lifecycle.go` 的 `OnRunStarted`：写入这些列。

**等待过期时间**（`AWAITING_EXPIRES_AT_`）：等待项产生时一次确定 createdAt 和 expiresAt，写入内存、SQLite 和 JSONL；expiresAt 为 0 表示无限等待，API 省略该字段；计时器、启动恢复和提交判断统一使用 expiresAt，去掉按历史 timeout 推导的逻辑；旧等待项不回填，不改写历史 JSONL。超时现在散落在 `internal/llm` 的 HITL 流程中（`hitl_timeout.go`、`run_stream_hitl_flow.go`、`run_stream_hitl_batch.go`、`interaction_submit.go`），这会改变人工审批的超时语义，因此单独做。

`OPENED_BY_` 不在"另做"之列，它随 mailbox 并库一起接入：mailbox 建会话（`EnsureMailChat`）时由 Runtime 写入。

## 与贴入稿的差异

- `.state/run-controls` 已在 47a0aa9f 停止读写，控制归属目前只在内存中。本方案不再涉及该目录，"旧文件不导入"一项作废。
- 不建 `RUN_CONTROLS` 表，按贴入稿后半部分改为 RUNS 的 `CONTROL_` 列组。
- 新增内容：MAILS 并库、归档的 Chat 退出 mail、`OPENED_BY_`、WAL 与共享连接、archive.db 同步列、runtime-recovery 调整。

## 测试

- V1 升级后的结构等于新建的 V2，数据行保留；无标记的 V1 同样能升级。
- 生成备份文件，并通过完整性检查。
- DDL 中途失败时，原库字节不变。
- 未知版本和未来版本仍然拒绝；运行期入口不触发升级。
- 升级后为 WAL，连接池中的每个连接都设置了 busy_timeout。
- chat 与 mailbox 并发写入时不出现 `SQLITE_BUSY`。
- `mail.enabled` 关闭时不读写 MAILS。
- 归档：寄往与发自该 Chat 的 mail 按规则关闭；恢复后不重投；两端关闭后删行；对账不读 archive.db。
- `OPENED_BY_` 只在 newChat 时写入，补建主要对话时不写；归档再恢复后仍保留。
- 工具读写 `chats.db`、`-wal`、`-shm`、`archive.db` 时被硬拒绝。
- runtime-recovery 接受 V1 与 V2，不再校验 mail.db。

## 文档

- AGENTS.md：§5 数据结构、mailbox 模块说明，以及 §9 的 SQLite 契约。§9 现在写的是"其他标记组合或结构差异拒绝并阻止启动"，升级上线后这句就不再成立，所以要改写，而不是在后面另加一条例外。改写后要让以后改结构的人知道该怎么做（之前 mail.db 就因为只升了版本号、没写迁移，导致启动失败）：

  > SQLite runtime store 使用 `application_id`（库类型）和 `user_version`（schema 版本）作为身份契约。改结构必须升 `user_version`，并在契约中声明从上一个已知版本的迁移；迁移只在 `app.New` 启动装配期执行，先备份、单事务执行并校验，失败则拒绝启动。运行期仅验证，绝不认领、迁移、删除或修复。标记为 `0/0` 且结构等于某个已知版本的库按该版本处理；未知版本、未来版本和结构差异拒绝并阻止启动。列物理顺序不影响比较。
- `docs/Mailbox.md`："chats.db 与 mail.db"一节改为同一个库里的两组表；同时更新"启用与关闭"和"归档"。
- `docs/会话存储与回放.md`（`TEAM_ID_` 的说明）、`docs/AccessPolicy与HITL改造.md`、`docs/工具目录权限.md`、`docs/手工测试用例.md`。
- `configs/runtime.example.yml` 中 mail 的注释，以及 runtime-recovery 的契约说明。

## 实施顺序

1. sqlitecontract 的启动期升级能力及测试。
2. `internal/chatdb`：chats.db V2 契约与升级、WAL 与共享连接；archive.db V2。完成第 1、2 步后，可以先单独验证升级。
3. 加入硬拒绝名单。
4. mailbox 改用 chats.db（MAILS 字段调整、关闭语义、归档钩子、对账 SQL），删除 mail.db 的打开代码和 runtime-recovery 中的校验。
5. 写入 `OPENED_BY_`，提供 `/api/chats?openedBy=`（即 mailbox-next 的 B1）。
6. 更新文档。

Run 字段的接入在此之后另做。
