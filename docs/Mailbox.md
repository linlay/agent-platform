# Mailbox

Mailbox 通过 `builtin.task-control` 提供 `chat_send`、`chat_notify`、`chat_submit`；`chat_start`、`chat_steer` 继续保留。每条记录叫 mail，kind 为 letter、reply、notify、ask。完整设计及状态表见 [方案](../plans/mailbox.md)，工具参数见内置 task-control 的 [Mailbox 手册](../internal/resources/connectors/builtin.task-control/skills/task-control/references/mailbox.md)。

## 投递与回信

地址始终是 Chat：省略 chatId/newChat 寄给 Agent 主要对话，缺失才补建；newChat:true 开独立工作；chatId 补充已有工作。指定地址只能是本 Agent 开出的、用户最近一次直接输入引用的、目标 Agent 的主要对话，且属于同一用户；当前、归档、已删除会话拒绝。

mail.db 位于 AP_RUNTIME_CHATS_DIR，application_id=0x41504D4C、user_version=1，使用严格 sqlitecontract。先落 accepting 和附件摘要，再创建会话/复制文件；投递前以事务占 inflight、attempt、run/steer ID，消费 query/steer 后才记 delivered。回调按 attempt 隔离。插入后 Run 结束仍未消费的退回 queued；为它开出的 Run 未读取就结束的转为 failed，避免反复开 Run；结果未知的 letter/reply 挂起。投递复核只有确定的结论（目标 Agent、会话、归属、引用、模型、附件不再成立）才转为 failed 并寄失败回信；读盘、关停等临时错误保持 queued，等下一次触发或重启后挂起。

空闲会话开 Run，活动普通 Run 插入 steer 并唤醒 wait；提问、审批、表单继续等待原 submit。规划、压缩、收尾和无 Run 的未决等待项排队，在维护/续接仲裁后重新投递。Run 结束时，每个发信会话收到一份完整 AssistantText 和本 Run 发布附件；原信 done 与唯一 reply_key 在同一事务写入。信封头只在模型边界生成，不污染原 query/message。

## 权限与重启

发信时在调用 Run 权限临界区冻结档位并消费一次性审批；延迟投递重新检查目标、引用和模型，不检查发信 Run 是否仍活动。只有 letter 的 started 授予发信方查询/中断该 Run；inserted 不授予整个 Run 的控制，回信、即时消息和请示开出的 Run 也不授予对方任何控制。控制身份只在进程内；人工重投者接管该信和回程启动的 Run。

重启不自动投递：unfinished letter 和未消费 reply 挂起，保留 resume_phase、consumed_at 和 JSONL 消费证据；已消费 reply 永不再投。none/consumed/unknown 分别提示未执行、重投会重复执行、可能重复。accepting 从受理阶段继续。删除任一会话清正文与文件引用，保留幂等收据。

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

新开或闲时投递所需权限在邮件受理时从收件 Agent 的 accessConfig.default 解析并保存，未配置为 default；显式档位不得超过发送 Run 受理时当前档位。权限来源、目标 revision、父基线保存于 accessGrant。已经受理的邮件不因收件 Agent 后续改默认或发送 Run 结束而改变权限，重投也不重算。inserted 仅给活动 Run 补消息，不改变其当前档位；letter/reply/notify/ask 的控制权边界保持原合同。
