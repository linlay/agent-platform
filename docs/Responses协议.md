# Responses 协议

Platform 为原生 Chat 模型提供独立的 `OPENAI_RESPONSES` 协议，通过 `/v1/responses` 调用上游。`OPENAI` 使用 Chat Completions，`ANTHROPIC` 使用 Messages 与[自适应思考](配置化说明.md#anthropic-自适应思考)；公开 Query、SSE、WebSocket 与工具执行接口不变。

## 模型与请求

模型 YAML 使用独立 key，方便与旧协议并存：

```yaml
key: babelark-gpt-6-luna-responses
name: GPT-6 Luna (Responses)
provider: babelark
protocol: OPENAI_RESPONSES
modelId: gpt-6-luna
isReasoner: true
isFunction: true
isVision: false
```

Provider 复用现有 `baseUrl`、`apiKey`。默认端点为 `/v1/responses`；baseUrl 已以 `/v1` 结尾时追加 `/responses`。需要覆盖时使用 Provider 的 `protocols.OPENAI_RESPONSES.endpointPath`，该协议的 headers/compat 与模型配置沿用既有合并规则。

主模型调用固定 `stream:true`、`store:false`，将本地有效上下文转换为 `input`，请求 `include:["reasoning.encrypted_content"]`。不发送 `previous_response_id` 或 `conversation`，也不会通过 response ID 拉取历史。兼容配置不能覆盖本地上下文这一策略。

有 Chat 上下文的 Responses 请求自动发送 `prompt_cache_key`，值为 `apc_` 加 `SHA256(chatId)` 的前 48 位小写十六进制字符（共 52 字符）。直接对实际 Chat ID 的 UTF-8 字节计算，不要求 UUID，不截断原始 ID 后再哈希。生成只依赖 Chat ID，同一 Chat 的模型轮次、工具续接、重试、跨 Run 续聊、HITL 恢复与 L2 摘要保持一致；不同 Chat 使用各自的 key。该规则是稳定的会话亲和约定，不能随版本、Run ID、模型档位或进程启动时间变化。

`prompt_cache_key` 是请求体参数，不是 HTTP Header。它由已有 Chat ID 即时计算，不新增数据库、`.state` 或 JSONL 字段，也从 `system-init/react` 的 `requestOptions` 排除；旧 Chat 无需迁移。可选 `.llm-records` 原始请求 trace 仍记录实际发送的字段。无 Chat 上下文时省略；文本提取和视觉识别的独立一次性辅助调用也不生成。Provider/模型 compat 中的静态 `prompt_cache_key` 不覆盖此规则，避免所有会话共用一个值。

该字段不是 Responses 协议的必填项；平台默认发送是为了兼容依赖会话亲和的上游。固定 key 不保证已有失效密文恢复有效，也不用于推断服务商的具体路由机制。

开启思考时发送 `reasoning:{effort:"…",summary:"auto"}`；档位优先使用模型 `reasoningEffortMapping`，未配置映射时使用所选档位的小写值，未指定时使用 medium。模型实际支持的档位由上游决定；关闭平台思考开关时不自动请求摘要或设置 effort，上游自身的默认推理行为仍由模型/compat 决定。输出预算映射为 `max_output_tokens`，只在显式配置时传 temperature/top_p，不继承 Chat Completions 的默认 temperature、seed、penalty 参数。

`internal/modelresponses` 负责协议 DTO 和转换；`internal/llm/protocol_responses.go` 负责请求与 SSE；`internal/modelclient` 继续负责 HTTP；原有 tool loop、HITL、权限和并发控制继续执行 Platform 的函数工具。文本辅助调用和视觉识别工具也支持 Responses 非流式请求（视觉模型仍须声明 isVision）。L2 摘要使用相同协议并移除工具。

首版支持文本、输入图片、函数调用与函数结果、可读推理摘要、加密推理状态。未接入上游托管的 computer/code interpreter/web search 等工具；遇到这类输出会显式失败，不能绕过 Platform 工具执行器。

## JSONL 的最小扩展

仍只保存到 `<chatId>.jsonl`，没有独立 response 文件，也没有 `responseItems`。新增字段：

| 位置 | 字段 | 语义 |
| --- | --- | --- |
| `react` 行 | `responseId`（可选） | 该次已接受模型调用的上游 ID；每次调用各自保存 |
| assistant `reasoning_content[]` | `type:"encrypted_text"` | 不可读的原生推理状态 |
| 加密条目 | `encrypted_text` | 上游 `encrypted_content` 的原值，不能裁剪或改写 |
| 加密条目 | `id` | 上游 reasoning item ID，不是 response ID 或 tool call ID |
| 加密条目 | `summary`（可选） | 原始 summary 数组；上游无摘要时为空数组 |

旧的正文与可读思考继续使用 `{"type":"text","text":"…"}`。加密条目不用 `text` 字段，避免被现有文本消费路径误读。`summary` 是上游提供的多个摘要片段，例如 `[{"type":"summary_text","text":"…"}]`，不是完整内部思维；流式摘要进入既有可读思考事件。没有摘要或没有加密内容不证明模型没有推理，只有上游明确提供的 token/条目才能作为观测证据。

以下为省略既有 usage/systemRef 等可选字段的连续三行示例：

```jsonl
{"_type":"react","chatId":"chat-demo","runId":"run-001","updatedAt":1790208000000,"seq":1,"modelKey":"babelark-gpt-6-luna-responses","responseId":"resp_001","messages":[{"role":"assistant","ts":1790208000000,"reasoning_content":[{"type":"encrypted_text","encrypted_text":"<上游加密原值>","id":"rs_001","summary":[]}],"tool_calls":[{"id":"call_001","type":"function","function":{"name":"file_read","arguments":"{\"path\":\"README.md\"}"}}]}]}
{"_type":"react-tool","chatId":"chat-demo","runId":"run-001","updatedAt":1790208001000,"seq":1,"messages":[{"role":"tool","ts":1790208001000,"tool_call_id":"call_001","name":"file_read","content":[{"type":"text","text":"项目使用 Go。"}]}]}
{"_type":"react","chatId":"chat-demo","runId":"run-001","updatedAt":1790208002000,"seq":2,"modelKey":"babelark-gpt-6-luna-responses","responseId":"resp_002","messages":[{"role":"assistant","ts":1790208002000,"content":[{"type":"text","text":"项目使用 Go。"}],"reasoning_content":[{"type":"encrypted_text","encrypted_text":"<第二次加密原值>","id":"rs_002","summary":[]}]}]}
```

第二行是工具执行结果，所以使用 `react-tool`、继承对应调用的 seq、不重复 responseId。子任务使用其自己的 task buffer 按相同规则保存。responseId 和加密条目在模型提交门打开后随该轮消息落盘；重试、断流或丢弃的未提交轮次不冒充成功响应写入。输出上限/过滤终态沿用现有部分内容保留与明确错误规则，不执行本轮工具。

新版本可读取旧 JSONL，无需迁移；旧协议请求不会带加密条目。新格式回滚到旧二进制前，应先确认旧版 reader 对新增类型的支持，不能保证旧二进制可无损处理新条目。

## 无服务端状态的续聊

本地读取顺序、`_compact`、system-init 和完整工具组仍是上下文事实源。转换示意：

```json
{
  "model": "gpt-6-luna",
  "store": false,
  "stream": true,
  "include": ["reasoning.encrypted_content"],
  "reasoning": {"effort":"medium","summary":"auto"},
  "input": [
    {"role":"user","content":[{"type":"input_text","text":"读取 README.md"}]},
    {"type":"reasoning","id":"rs_001","summary":[],"encrypted_content":"<JSONL encrypted_text 原值>"},
    {"type":"function_call","call_id":"call_001","name":"file_read","arguments":"{\"path\":\"README.md\"}"},
    {"type":"function_call_output","call_id":"call_001","output":"项目使用 Go。"}
  ]
}
```

实际还会附带当前 system 指令及工具定义。正文转换为 input_text/output_text；工具定义转为 Responses 的扁平结构，并设置 `strict:false`，保持原工具可选参数语义。工具 ID 对应 `call_id`，不冒充上游 function item ID。普通 `reasoning_content` 文本不伪造成原生 reasoning item。

因此 responseId 长时间后失效不影响继续 chat。加密条目仍可能被上游拒绝：只有结构化错误码 `invalid_encrypted_content` 才触发一次恢复，且仅当全部待移除条目属于已完成的 assistant 最终回答之前的历史段。恢复保留正文、工具调用和结果，只在内存请求中移除加密状态，不改写历史文件。未完成的工具/审批链包含加密条目时不静默降级；返回错误，避免丢失必要状态或重复执行工具。通用 400、超时、鉴权错误不触发这种恢复。

切换模型 key 后不向新模型发送旧模型的加密条目。相同 key 下更换上游部署的兼容性由上游决定，不能保证任意密文永久有效。

L1 保护最近轮次及未完成交互；保留的工具组保留其加密推理，移除工具组时同步移除其必要推理。旧的独立推理可被清除。L2、聊天可读回放、搜索与文本 token 估算不把密文当正文；原始 JSONL/原始消息仍保存密文以供续聊。密文长度不能推算模型 token，窗口判断继续结合实际 usage 与现有估算机制。

## 终态与 usage

SSE 按 output_index 累积，完整终态快照补足尾部并核对已收到的文本/参数，避免重复拼接。兼容端点若在 `response.completed` 返回空/缺省 `output`，仅当已观察的全部输出项都有 `response.output_item.done`、索引从 0 连续且 item 状态未声明未完成时，按索引恢复完整输出，再统一校验身份、正文/摘要/参数和工具 ID；不从未完成项或仅 delta 猜测结果。非空但缺项的终态不补齐，`response.incomplete` 不走此恢复。恢复数量记录在 trace 的 `diagnostics.stream.responsesRecoveredItems`。可读推理摘要在不同 item 或 summary_index 之间保留段落边界。

只有 `response.completed` 或受支持的 `response.incomplete` 才能收口；单独 EOF、`[DONE]`、failed/error 或不一致快照均不能当成功。完整输出校验后，函数调用进入现有权限/HITL/执行链。多工具 ID 必须唯一，arguments 必须是 JSON 对象。

usage 映射：input_tokens → promptTokens，output_tokens → completionTokens，input_tokens_details.cached_tokens → promptCacheHitTokens，output_tokens_details.reasoning_tokens → reasoningTokens。缓存命中率可按 cached_tokens / input_tokens 计算（input_tokens > 0）；上游未提供细分时不能据此证明命中。`store:false`、response ID、Prompt Cache 是三件独立的事，发送全量有效上下文仍可能命中上游缓存。

## 流错误诊断

HTTP 200 的 SSE 也可能携带上游错误。标准 `error` / `response.failed`，以及网关错误 envelope 均提取结构化 code/type，复用 `internal/modelclient` 的分类：先完整匹配已知 code，再匹配 type，最后按失败 HTTP 状态兜底。限流返回 `provider_rate_limited`，额度、鉴权、权限、模型不存在、上下文超限、内容过滤和服务不可用使用对应平台错误码；不解析英文 message 的关键词，未知流内错误返回 `provider_stream_failed`。OpenAI Chat 与 Anthropic 流内错误使用相同分类。上游消息保留有界、凭据脱敏后的诊断原文。公开 `status` 是平台分类状态（限流为 429），`diagnostics.upstreamStatus` 始终保留实际 HTTP 状态（可能为 200）；不改变已经建立的 SSE 连接状态。缺少错误与事件类型的帧仍按 `provider_stream_invalid` 拒绝。

模型尝试失败在重试收尾前统一补充 protocol、attempt/maxAttempts、实际上游状态、请求/响应 ID 与流读取计数，保留已有错误诊断及状态/重试属性。直接 EOF 分别以 `diagnostics.reason: stream_ended_before_output`（尚无有效输出）和 `stream_ended_before_completion`（已有输出但未完成）说明，不能据此推断限流或网关内部原因。

格式/流失败的 `diagnostics` 增加事件类型、帧序号/字节数、尝试次数、已知顶层字段名及上游请求 ID（若提供）。格式失败还保留 `validationError`，避免界面翻译通用 message 后丢失具体校验原因。终态校验失败还包含 response ID/status、终态输出项数、已观察项数及 done 项数；缺项时包含 `outputIndex`、`observedItemType`、`observedItemDone`，并保留上游 `incompleteReason`。这些信息用于区分完整结束、输出受限和快照不一致。

结构信息同时进入常开日志及可选 trace 的 `diagnostics.stream.responsesFailure`，不保存原始 SSE、正文、推理、密文或工具参数；上游错误说明保留在公开错误与 trace 的 `error` 中，不加入常开诊断日志。重试提示按原因区分“超时”和一般“失败”。

开启 `AP_DEBUG_LLM_CHAT_RECORD=true` 可在 trace 的 `streamEvents` 查看有界的帧类型、结构大小、处理结果和读取结束方式；重试逐次留档，原路径保留最近一次尝试。用于判断缺少终态与未消费事件，不从字节数推断正文或 token 数，详见 [模型空响应排查](配置化说明.md#模型空响应排查)。

## 验证

自动化测试覆盖请求转换、默认端点、旧协议隔离、终态/断流/截断、usage、每轮 JSONL ID、磁盘重载、延迟快照、丢弃轮次、L1 状态保护、辅助文本/图片调用和加密状态恢复边界。

可选实网测试（会调用上游并产生费用）通过环境变量 `AP_TEST_RESPONSES_KEY`、`AP_TEST_RESPONSES_URL` 开启 `TestResponsesLiveStatelessToolRoundTrip`；默认跳过。测试只调用合成函数、不读写用户文件。实网加密条目回传尚未完成验证，相关转换与持久化由合成测试覆盖。
