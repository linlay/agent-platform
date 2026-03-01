# LLM 网关（llm_gateway）

## 关键类
- `LlmService`
- `OpenAiCompatibleSseClient`
- `NewApiOpenAiCompatibleSseClient`
- `AnthropicSseClient`（部分占位）

## 路径选择
- OPENAI：优先 raw SSE（有 tools 或需要 reasoning/schema）否则可走 ChatClient。
- NEWAPI_OPENAI_COMPATIBLE：走 raw SSE + provider `new-api-path`。
- ANTHROPIC：`stream` 路径存在，`completeText` 未实现并抛 `UnsupportedOperationException`。

## 请求体关键字段
- `stream=true`
- `stream_options.include_usage=true`
- `tools[]`
- `tool_choice`
- `parallel_tool_calls`

## 日志约束
- 记录请求体、system prompt、history、user prompt、delta。
- 通过 `LlmCallLogger` 脱敏 token/apiKey/secret/password 等字段。
