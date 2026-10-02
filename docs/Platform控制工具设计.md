# Platform 控制工具设计与实现

## 目标与边界

`platform_control` 是显式挂载的 system tool。所有挂载它的 Agent 使用同一固定 Schema；Skill 只能说明调用方法，不能替 Agent 挂载 Tool。

动态环境由独立工具 `run_env` 管理，见 [Run 环境工具](Run环境工具.md)。`platform-control.enabled` 不再控制环境能力。

## 固定操作

当前注册操作为：

- `capabilities.list`
- `catalog.defaults.get`
- `catalog.validate`：resourceType 为 agent/team/skill/connector；connector 只校验 connector.json，旧 mcp-server 类型已退役。
- `chat.set_pinned`：持久化当前或指定 Chat 的实例级置顶状态。
- `runtime.status`
- `security.explain`

旧 `run.env.set/unset/bind/get/list/bulk` 未注册，调用统一返回 `platform_control_invalid_operation`。

### `chat.set_pinned`

```json
{"operation":"chat.set_pinned","params":{"pinned":true}}
```

`pinned` 必须显式给出 boolean，`false` 取消置顶。可选 `chatId` 必须是有效非空 ID；省略时从可信 `ExecutionContext.Session.ChatID` 取当前 Chat，不从模型参数、请求 body 或界面选中项推断。可以显式操作同一实例的其他 Chat，不按标题匹配，不新增 Chat 列表工具。置顶是实例级共享展示偏好，不按调用者或 Agent 分开。

仅允许显式挂载 `platform_control` 的普通 native Agent root Run；子任务、Team、ACP、Proxy、Channel 不开放。操作为 mutation barrier，planning/read-only 阶段拒绝；`capabilities.list` 与 `security.explain` 使用同一调用者判定。工具挂载和服务端控制面启用要求保持不变。

成功结果：

```json
{"operation":"chat.set_pinned","status":"ok","scope":"instance","data":{"chatId":"chat-1","pinned":true,"changed":true}}
```

该操作成功和错误 envelope 均使用 `scope: instance`，不包含 Run env `revision`；其他 operation 同样不包含环境 revision。首次置顶插入首位，重复设置相同状态返回 `changed:false`，不重排、不写盘、不广播；取消不存在 Chat 的置顶为幂等清理。不存在或仅上传形成的未命名占位 Chat 不能置顶。

HTTP、WS 和工具通过 `internal/conversation.Service.SetChatPinned` 共用存储与通知，持久化成功且有变化后发送 `chats.order.changed`（`updatedAt` 为置顶状态毫秒时间戳）。广播在持久化后立即发送，不依赖调用方随后读取列表成功。Desktop 现有导航订阅触发刷新，不打开 Chat、不切换当前对话。历史 JSONL 不修改，工具不直接写 `chat-pinned.json`。

错误码：`platform_control_invalid_params`（缺少 boolean、非法/空 chatId 或未知参数）、`platform_control_stage_forbidden`、`platform_control_disabled`、`chat_pin_forbidden`（调用者不符合 root/native/挂载边界）、`chat_context_unavailable`（省略 ID 且无可信当前 Chat）、`chat_not_found`、`chat_pin_invalid_target`（占位 Chat）、`chat_pin_unavailable`（服务未装配或存储不支持）、`chat_pin_failed`（持久化失败）。失败不得声称已置顶。

## Catalog 校验回执与历史

候选校验与配置写入保持分离。校验结果中的 `candidate` 回执基于实际收到的完整 UTF-8 内容计算 SHA-256 和字节数，用于核对后续写入的是同一份候选；回执不是写入授权，也不代表资源已加载或知识库检索可用。候选变更后必须重新校验。

已支持的 Catalog 候选内容在 SSE、模型历史、trace 和持久化历史中保持完整内容，便于核对和复用；已有历史的脱敏占位符不恢复。未知或缺失资源类型仍整体隐藏候选内容。

参数脱敏必须幂等，并保持请求字段结构，不向 `params` 注入字节数等展示元数据。SSE、模型历史和持久化历史中的占位符只表示内容已隐藏，不能据此否定成功回执或推断原始请求。旧历史中的 `contentBytes` 不能复制到新请求；显式提交占位符时返回可恢复的错误，要求重新读取候选内容。

## 控制面与动态环境硬切

`capabilities.list` 只返回本工具操作，不再返回环境限额或 revision；`runtime.status` 不返回 runEnv；`security.explain` 不接受环境 key，环境规则改用 `run_env explain`。结果 envelope 不再附带环境 revision。`chat.set_pinned` 保持实例级行为。

`platform-control` 配置仅保留 `enabled`；旧 deny-keys 与 max-dynamic-keys/max-value-bytes/max-total-bytes 出现即启动失败，必须移到 `run-env`。不兼容旧 run.env 操作，也不重写历史。

在线文档流程改为 create/upload 获取 documentId → `run_env` 的 `set` → HTTPX session/edit/commit/download，无需先调用 capabilities.list。具体输入、状态、重启和迁移见 [Run 环境工具](Run环境工具.md)。

操作调度属性统一由 `internal/toolpolicy` 提供，业务校验和执行保留在 `internal/platformcontrol`；本工具已有 Catalog 参数脱敏行为保留。`run_env` 不参与脱敏、流式参数缓冲和原始模型帧屏蔽。
