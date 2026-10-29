# Mailbox 与主要聊天：后续方案（Platform + WebClient）

接续 [mailbox.md](mailbox.md)（已实现）与 [primary-chat-dispatch.md](primary-chat-dispatch.md)（探讨稿）。本方案只覆盖 Platform 与 WebClient；Desktop 在 WebClient 定稿后对齐。

## 现状

- Platform：mailbox 方案的实施步骤 1、2 已完成，审核修复已提交（1f7c307a）。
- Platform：`accessConfig.default` 已落地，信在受理时按收件 Agent 默认档位冻结。
- WebClient：已有收件箱面板（侧栏设置菜单进入），重投、丢弃前的确认，时间线来源徽标，以及 wait 的 `mail` 唤醒原因。
- WebClient：主聊天里平台开出的 Run 会自动挂接并实时显示。
- 主要聊天：指针、`PUT /api/chats/primary`、query `primary:true`、mailbox 补建均已实现；WebClient 已有主要聊天导航。
- 用户输入仍分 `/api/query` 和 `/api/steer`：客户端要自己判断用哪个，会话忙时 query 返回 `active_run_conflict` 或 `awaiting_pending`。

## 阶段 1：mailbox 收尾（Platform）

| # | 内容 | 做法 |
|---|---|---|
| 1.1 | 平台生成的 mail 用收件 Agent 默认档位 | 回信、即时消息、请示目前固定写 `default`。改为创建时按收件 Agent 的 `accessConfig.default` 解析，与 letter 一样写入 `accessGrant`（来源、revision）。 |
| 1.2 | `/api/mails` 列表只给摘要 | 列表去掉 `input`、`accessGrant` 和正文全文，只留正文前 200 字；响应增加 `total`。新增 `GET /api/mails/detail?mailId=` 返回全文、附件和等待项快照。WebClient 收件箱同批改为点开再取详情。 |
| 1.3 | 保留期 | 终态超过 `mail.retention-days`（默认 30）的行清空 `input`、`message`、`refs`，保留行本身，信封头、幂等和回放不受影响。两端会话都已删除的行直接删除。启动时和每天各跑一次。 |
| 1.4 | 临时错误自动重试 | 投递遇到临时错误时按会话退避重试：5 秒起，翻倍，最长 5 分钟。同一会话只有一个重试计时；日志按会话限频。 |
| 1.5 | 归档 | 归档时，寄往该会话的未完成信转为 failed 并寄失败回信；回信转为 suspended（证据 none），恢复会话后可重投；即时消息和请示转为 withdrawn。不清正文；恢复会话不自动重投。 |
| 1.6 | 分身会话被删时的回信 | 分身发出、尚未送达的回信照常投递；只清空寄往被删会话的正文。被删会话发出的即时消息和请示照旧撤回。 |
| 1.7 | `chat_notify` 附件顺序 | 与 letter 相同：先写库（accepting），再复制附件，然后转 queued；不再在写库前复制。 |
| 1.8 | 分身审批时限 | 保持 600 秒，超时按拒绝；分身在回信里说明被拒。阶段 4 的主视图审批上线后再评估。 |

## 阶段 2：WebClient 展示补齐

依赖阶段 1.2 的摘要和详情接口；2.4 另需 Platform 新增一个接口。

| # | 内容 | 做法 |
|---|---|---|
| 2.1 | 挂起提醒 | 收件箱从设置菜单移到侧栏一级入口，带挂起数量角标（`/api/mails?status=suspended` 的 `total`，`mail.updated` 时刷新）。启动后若有挂起 mail，提示一次。 |
| 2.2 | 请示卡片 | 主聊天时间线里的请示显示为卡片：分身名称、问题、审批命令或表单字段，以及“去分身会话处理”跳转。请示被答复、超时或撤回后，卡片按 `mail.updated` 变灰。 |
| 2.3 | 代答展示 | 分身会话中由上级 `chat_submit` 作答的记录，显示“由 X 代答”；审批另外显示依据的档位（submit 行的 `origin`、`accessGrant`）。 |
| 2.4 | 我开出的会话 | Platform 新增 `GET /api/chats/opened?agentKey=`，与 `chat_query opened` 同源，状态区分运行中、等待人处理、空闲。WebClient 在主聊天导航下列出这些会话，“等待人处理”高亮。这也覆盖跳数达上限、请示不再上报的情况。 |
| 2.5 | 回放核对 | 刷新和历史回放后，信封来源、回信 outcome、请示卡片状态与实时一致。 |

## 阶段 3：用户消息走 mailbox

目标：用户随时可以发消息，由平台决定开 Run、插入或排队；客户端不再区分 query 和 steer，也不再因会话忙收到 409。

### Platform

- **新入口：** `POST /api/send`（HTTP 和主 WS 同一套逻辑）。参数为 `{chatId? | agentKey, message, references?, files?}` 加运行选项（`planningMode`、`editingMode`、`model`、`accessLevel`）；返回 `{mailId, chatId, delivery: started|inserted|queued, runId?}`。
- **记录方式：** 用户消息记为 kind `letter`、`origin_type = user`，hop 为 0。只有 Agent 发出的 letter 才会生成回信；用户看到的回复就是会话里的 Run 输出。
- **投递规则与 mail 相同：** 空闲开 Run，运行中插入，停在等待项上时在 Run 内排队，规划、压缩和续接仲裁期间排队。
- **运行选项：** 只在这条消息开新 Run 时生效。会话忙、且消息带的运行选项与当前 Run 不同时，消息排队，等当前 Run 结束后用这些选项开新 Run，不插入。
- **取消排队：** `POST /api/mails/action` 增加 `cancel`，只对仍是 queued 的 mail 有效，含用户消息与 letter。
- **重启：** 未投递的用户消息挂起，由用户在会话里重发或丢弃。
- **旧入口：** `/api/query`、`/api/steer` 保留给 Desktop、gateway、Automation、execute 和渠道，见待确认 D8。

### WebClient

- 输入框一律调用 send。消息显示为待发气泡，状态为排队中、已插入或已开始；排队中的可以取消。
- 去掉“运行中改走 steer”的分支，以及 `active_run_conflict` 的提示路径。
- 订阅：先沿用现有 `run.started` 推送加自动挂接。若仍有遗漏，再加 WS 会话订阅 `chat.subscribe {chatId}`，推送该会话当前及之后所有 Run 的事件和排队变化。

## 阶段 4：主视图处理与主要聊天形态

| # | 内容 | 做法 |
|---|---|---|
| 4.1 | 主视图直接审批 | 请示卡片内直接批准、拒绝或作答，用人的身份对分身的 `runId/awaitingId` 调 `/api/submit`，不受代批上限约束。超出上级档位的审批不必再进分身会话。 |
| 4.2 | Automation 与渠道改为发信 | 走阶段 3 的入口（origin 为 automation 或 channel）。目标会话忙时排队，不再拒绝。 |
| 4.3 | 删除 `chat_start`、`chat_steer` | 阶段 3 稳定后硬切删除，连同 runops 的一层限制、工具 YAML、task-control 手册和文档。 |
| 4.4 | 更新 `primary-chat-dispatch.md` | 写入下方的结论，状态从探讨稿改为已定。 |

主要聊天待拍板项的建议答案（编号对应 primary-chat-dispatch.md）：

- **Q1：** 主聊天既做短活，也负责派活；长任务或另一件事用 `chat_send {newChat:true}` 派给分身。规则写进 task-control 手册。
- **Q2：** 派出的会话挂在主聊天下（阶段 2.4），不进普通历史列表的顶层。
- **Q3、Q5：** 已由 mailbox 回答：结果自动开回调 Run；可以多层派活，受跳数限制。
- **Q4：** 派给自己的分身不设为默认，由模型按 Q1 的规则选择。
- **Q6：** 保留 `primary_chat_busy`，只用于切换指针。阶段 3 之后，用户消息不会因忙被拒。
- **Q7：** 审批两处都能处理：主视图卡片（4.1）或分身会话。
- **Q8：** 任务托盘就是“我开出的会话”列表；Kanban 和云端 Job 本期不合并。
- **Q9：** 见 4.2。
- **Q10：** Desktop 在本方案完成后对齐。

## 待确认

| # | 问题 | 建议 |
|---|---|---|
| D1 | 保留期 | 30 天；只清正文、保留行；两端会话都删除的行删除 |
| D2 | 临时错误 | 自动退避重试，最长间隔 5 分钟 |
| D3 | 分身会话删除后的回信 | 照常送达 |
| D4 | 归档 | 信失败并寄失败回信；回信挂起；即时消息、请示撤回 |
| D5 | 分身审批时限 | 保持 600 秒 |
| D6 | 用户消息在 mail.db 中的形态 | letter + `origin_type = user`，不生成回信 |
| D7 | 运行选项不同的用户消息 | 排队，等当前 Run 结束开新 Run |
| D8 | `/api/query`、`/api/steer` | 保留给非 WebClient 调用方；Desktop、gateway 迁移后再定删除 |
| D9 | 主要聊天 Q1、Q2、Q7 | 按上文建议 |

## 顺序与依赖

1. 阶段 1，Platform。1.2 与阶段 2 的收件箱改动同批发布。
2. 阶段 2，WebClient，外加 Platform 的 2.4 接口。
3. 阶段 3，Platform 与 WebClient 同批。
4. 阶段 4：4.1 可以紧跟阶段 2 的请示卡片做；4.2、4.3 在阶段 3 之后。

需要同步修改：`docs/Mailbox.md`、`docs/API与协议.md`、`docs/HITL协议.md`、`AGENTS.md`、`configs/runtime.example.yml`（`mail.retention-days`）、task-control 手册；WebClient 的接口类型、文案和测试。


## 第一批实施范围（2026-10-29）

本批为上线前不兼容存储调整，不提供旧 mail.db 迁移，不自动清库。包括 MAILS/字段/索引命名统一、关系字段明确、独立终态与正文清理时间、生成邮件默认权限、notify 附件先受理后复制、删除分身后保留已受理回信、默认 30 天的终态正文保留期与删除对账。验收以本批针对性测试及项目检查结果为准，尚未部署验证。

摘要/详情 API 与 WebClient 联动、归档投递状态细化、自动退避重试及 /api/send 均不属于本批；上表保留为后续规划。保留期不清理 suspended，物理附件按收件 Chat 生命周期保留，正文清理不删除创建会话与回信关系。
