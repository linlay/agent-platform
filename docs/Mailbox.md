# Mailbox

Mailbox 通过 `builtin.task-control` 提供 `chat_send`、`chat_notify`、`chat_submit`；`chat_start`、`chat_steer` 继续保留。每条记录叫 mail，kind 为 letter、reply、notify、ask。完整设计及状态表见 [方案](../plans/mailbox.md)，工具参数见内置 task-control 的 [Mailbox 手册](../internal/resources/connectors/builtin.task-control/skills/task-control/references/mailbox.md)。

## 投递与回信

地址始终是 Chat：省略 chatId/newChat 寄给 Agent 主要对话，缺失才补建；newChat:true 开独立工作；chatId 补充已有工作。指定地址只能是本 Agent 开出的、用户最近一次直接输入引用的、目标 Agent 的主要对话，且属于同一用户；当前、归档、已删除会话拒绝。

mail.db 位于 AP_RUNTIME_CHATS_DIR，application_id=0x41504D4C、user_version=2，使用严格 sqlitecontract。先落 accepting 和附件摘要，再创建会话/复制文件；投递前以事务占 inflight、attempt、run/steer ID，消费 query/steer 后才记 delivered。回调按 attempt 隔离。插入后 Run 结束仍未消费的退回 queued；为它开出的 Run 未读取就结束的转为 failed，避免反复开 Run；结果未知的 letter/reply 挂起。投递复核只有确定的结论（目标 Agent、会话、归属、引用、模型、附件不再成立）才转为 failed 并寄失败回信；读盘、关停等临时错误保持 queued，等下一次触发或重启后挂起。

空闲会话开 Run，活动普通 Run 插入 steer 并唤醒 wait；提问、审批、表单继续等待原 submit。规划、压缩、收尾和无 Run 的未决等待项排队，在维护/续接仲裁后重新投递。Run 结束时，每个发信会话收到一份完整 AssistantText 和本 Run 发布附件；原信 done 与唯一 reply_key 在同一事务写入。信封头只在模型边界生成，不污染原 query/message。

## 权限与重启

发信时在调用 Run 权限临界区冻结档位并消费一次性审批；延迟投递重新检查目标、引用和模型，不检查发信 Run 是否仍活动。只有 letter 的 started 授予发信方查询/中断该 Run；inserted 不授予整个 Run 的控制，回信、即时消息和请示开出的 Run 也不授予对方任何控制。控制身份只在进程内；人工重投者接管该信和回程启动的 Run。

重启不自动投递：unfinished letter 和未消费 reply 挂起，保留 resume_phase、consumed_at 和 JSONL 消费证据；已消费 reply 永不再投。none/consumed/unknown 分别提示未执行、重投会重复执行、可能重复。accepting 从受理阶段继续。删除收件会话清理寄往它的正文与文件引用，保留单端仍存在的幂等收据；删除发件会话不清除已受理回信的正文和收件侧附件副本。未完成信失败（收件端删除时生成唯一失败回信），通知与请示撤回，寄往已删除会话的未消费回信丢弃。

## 请示与跳数

由 letter 启动的 Run 进入问题/表单/审批等待时产生 ask，仍等待原 submit。上级只能回答送达自己 Chat 的 ask；来源 awaiting_source 决定可否代批，mode 只决定提交形状。代批使用上级 live 档位在分身原环境重评，不继承上级路径、工具、凭据或规则，强制人工审批永不可代答。代答 origin 和 accessGrant 写入 submit。

mail 发出时 hop=当前+1，接收取 max；用户直接输入归零；chat_submit 代答取 max(当前,上级+1)；chat_start 开出的 Run 为父 Run 当前跳数+1。mail.max-hops 默认4；达到上限禁止 send/notify、停止 ask 上报，最终 reply 和 chat_submit 仍允许。由 letter 启动的 Run 使用普通 wait 等回信，不能用旧 chat_* 控制工具或 run.terminal 条件；回信、即时消息、请示唤醒的是收件会话自己的 Run，保留这些工具。请示正文按等待项渲染为可读文本（不含表单 HTML），快照仍存于 mail.db。

## HTTP 与展示

- GET /api/mails?agentKey=&chatId=&kind=&status=&limit=&offset=：只返回当前 subject 的 mail，按收件 Agent/Chat 等过滤。
- POST /api/mails/action {mailId,action:redeliver|discard}：只处理挂起的 letter/reply，校验当前 subject。
- mail.updated：mailId、kind、status、delivery、toAgentKey、toChatId、runId。
- request.query/request.steer 与 JSONL：mailId/mailKind/mailAttempt/mailHop，reply 另有 outcome；这些由平台生成，公共输入不得伪造。

相邻 WebClient 的收件箱和时间线消费上述协议。自动重投、用户消息经 mailbox、审批转到主视图均不在本期。


## Agent 默认权限

letter 在受理时、平台生成的 reply/notify/ask 在创建时，从收件 Agent 的 accessConfig.default 解析并保存权限，未配置为 ask_for_approval；letter 的显式档位不得超过发送 Run 受理时当前档位。权限来源、目标 revision 保存于 accessGrant，letter 另存父基线。平台生成邮件不要求原发送 Run 仍活动。接收 Agent 已不可用时，回信保留完整结果并挂起，不静默降为 ask_for_approval；人工重投时首次成功解析权限。已经冻结的权限不因收件 Agent 后续改默认、发送 Run 结束或重投而改变。inserted 仅给活动 Run 补消息，不改变其当前档位；letter/reply/notify/ask 的控制权边界保持原合同。


## 存储命名与保留期

`mail.db` 的 `MAILS` 表沿用 `chats.db` 风格：大写表名，字段为大写下划线加尾部 `_`，索引以 `IDX_` 开头。主键为 `MAIL_ID_`；父邮件、回信、结果 Run 分别使用 `PARENT_MAIL_ID_`、`ANSWERED_BY_MAIL_ID_`、`REPLY_OF_RUN_ID_`。完整 DDL 以 [store.go](../internal/mailbox/store.go) 为准，Go DTO 和 HTTP JSON 字段名保持原协议。

本次 schema V2 为上线前不兼容变更。旧开发库会被严格校验拒绝，程序不迁移、删除或自动重建；开发环境需停服后显式备份并重建旧 `mail.db`。`chats.db` 和 Chat 资源不因该 schema 变更而重建。

`mail.retention-days` 默认 30（正整数，最多 365000），启动后和此后每 24 小时运行清理：

- 终态时间 `TERMINAL_AT_` 与 `UPDATED_AT_` 独立。letter 的 delivered 仍在等回信，不是终态；suspended 也不清理。
- 到期清空 input、message（包括请示等待项快照）、references、files 元数据及可能包含正文的 error，记录 `BODY_PURGED_AT_`（JSON 仍为 purgedAt）。保留信封、幂等摘要、answeredBy、parentId 和 openedChat，清理不改变首次终态时间。
- 已投递附件副本属于收件 Chat 资源，按 Chat 生命周期保留，避免破坏已落盘历史的文件引用；定期清理不删除这些物理文件。
- `FROM_CHAT_DELETED_AT_` / `TO_CHAT_DELETED_AT_` 独立记录两端删除，两端均删除且邮件终态时才删除行。删除归档 Chat 同样触发清理；归档本身不算删除。
- 定时核对活动与归档存储，补偿漏掉的删除钩子；尚在 accepting 的新 Chat 预留地址不视为已删除。清理失败记录日志，不阻止启动，不自动重投。

notify 与 letter 一样先写 accepting 和附件摘要，再复制附件、转 queued；复制失败仍有可重放的幂等收据，避免重复受理。
