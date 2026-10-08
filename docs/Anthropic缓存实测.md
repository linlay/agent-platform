# Anthropic 缓存实测

2026-10-09（Asia/Shanghai），通过 BabelArk 的 `/v1/messages` 对 Haiku 5.5、Sonnet 5.5、Opus 5.5 发起 47 次独立 HTTP/SSE 请求。全部返回 HTTP 200 并正常收到 `message_stop`。这次只增加试验脚本和记录，没有修改 Platform 的请求构造或部署模型 YAML。

脚本使用合成参考文本和无副作用的模拟 echo 工具，不读取真实 Chat 内容；从已有 registry 读取 endpoint 和凭据，报告不保存凭据、正文、思考文本或签名。默认输出上限为每次 512 tokens，前缀约 13,000 tokens，缓存请求均在数秒至数十秒内连续执行。

## 已验证的请求方式

三个模型都通过 system 内容块上的显式 `cache_control` 产生了缓存写入和命中。Haiku 同时验证了增长的消息历史、工具结果和 HIGH 自适应思考。当前 BabelArk 渠道应优先使用显式断点，而不能把顶层自动缓存参数被 HTTP 200 接受当成缓存已经开启。

推荐在稳定 system 的最后一个内容块和最后一条 user 消息的最后一个可缓存内容块各设置一个断点：

```json
{
  "thinking": {"type": "adaptive", "display": "summarized"},
  "output_config": {"effort": "high"},
  "system": [
    {
      "type": "text",
      "text": "完整、稳定且足够长的 system 提示",
      "cache_control": {"type": "ephemeral"}
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": [
        {
          "type": "text",
          "text": "当前用户请求",
          "cache_control": {"type": "ephemeral"}
        }
      ]
    }
  ]
}
```

这只是缓存结构示意，实际请求还需要 `model`、`max_tokens`、`stream` 等字段；示例短文本本身没有达到缓存门槛。缓存前缀按 `tools → system → messages` 排列，因此 system 断点同时覆盖前面的工具定义。工具返回时，第二个断点可以放在最后一个 `tool_result` 块上。第二个断点随消息增长前移，不把旧断点永久留在所有历史消息中；本脚本始终只使用两个显式断点。

在增长的历史中，脚本完整重放上游 assistant 内容块，包含实际返回的 thinking/signature 和 tool_use。思考块本身不直接标记断点。上述断点位置、5 分钟默认 TTL、前缀查找规则和最低长度限制见 [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)。

## 实测结果

下表的读取和写入数均取上游顶层 usage；不是通过耗时、可见文字或 token 总数推算。完整的逐轮结果、SSE usage 事件和可用的请求摘要保存在 [原始用量报告](experiments/anthropic-cache-2026-10-09.json)。

| 模型与场景 | 实测结果 |
| --- | --- |
| Haiku，无缓存声明，3 次完全相同请求 | 每次写入 0、读取 0 |
| Haiku，顶层自动缓存，首次独立试验 | 首次写入 13,033；第二次读取 13,033 |
| Haiku，顶层自动缓存，增长对话和工具循环各 3 次 | 六次都写入 0、读取 0 |
| Sonnet，顶层自动缓存，3 次完全相同请求 | 前两次写入 0；第三次写入 13,056；未出现读取 |
| Opus，顶层自动缓存，3 次完全相同请求 | 三次都写入 0、读取 0 |
| Haiku，system 显式断点 | 已出现读取 13,031；也有同一前缀重新写入的轮次 |
| Sonnet，system 显式断点 | 首次写入 13,032；第二、三次都读取 13,032 |
| Opus，system 显式断点 | 前两次写入 13,030；第三次读取 13,030 |
| Haiku，system + 最后 user 块，增长对话 | 一次读取 13,057、只写入 16；另一次读取 13,073、写入 32 |
| Haiku，system + 最后 user/tool_result 块，工具循环 LOW | 已出现读取 13,445、只写入 107 |
| Haiku，同样的工具循环 HIGH | 第三次读取 13,523、只写入 16；输出 29，其中思考 25 |

47 次请求中，10 次顶层 `cache_read_input_tokens` 大于 0。这个混合场景的比例不作为模型或渠道的命中率指标：其中包含无缓存对照、每组首次冷写入和不同断点策略。

顶层自动缓存确实出现过命中，但本次不能据此认定它在 BabelArk 所有请求中都生效。完全相同的请求也出现重新写入，显式断点也不能保证每轮命中。现有响应没有提供足够信息，无法确定差异来自哪个上游渠道、账户、路由或转换环节。

## 上游用量的不一致

HIGH 工具循环的第四次响应中，`message_start` 和最终 `message_delta` 的顶层均报告：

```json
{
  "cache_creation_input_tokens": 13582,
  "cache_read_input_tokens": 0
}
```

但同一个最终 `message_delta.usage.iterations[0]` 报告：

```json
{
  "type": "message",
  "cache_creation_input_tokens": 43,
  "cache_read_input_tokens": 13539
}
```

对应 BabelArk request ID 为 `202610090745554298105aM5PsMHE`。两种统计的输入总量相同，但缓存写入/读取分配不一致。这是独立脚本直接收到的字段，不是 Platform 的归一化结果。官方说明顶层 usage 应汇总 executor 的 message iterations，见 [Usage and billing](https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool#usage-and-billing)。

脚本保留两层数据并标记 `usageWarnings`，不悄悄替换顶层数值，也不将两层相加。后续 Platform 应保留这些原始明细供排查；当前计费和命中显示不能通过猜测填数。仅看顶层命中为 0，也不能排除内部 iteration 报告过命中。

## 复现

使用现有模型 registry，执行只读加载和合成请求：

```bash
go run ./scripts/anthropic-cache-probe \
  --registries-dir "$AP_RUNTIME_REGISTRIES_DIR" \
  --model-keys babelark-claude-haiku-5_5 \
  --cases control,automatic,system,conversation-prefix,tool-loop-prefix \
  --rounds 3 \
  --effort high \
  --output /tmp/anthropic-cache-report.json
```

`automatic` 重复完全相同的请求；`system` 固定 system 并改变 user 后缀；`conversation` 和 `tool-loop` 使用顶层自动缓存并增长历史；`*-system` 只标记固定 system；`*-prefix` 同时标记固定 system 和最后一个 user 内容块。每组使用独立的合成前缀，避免其他组的暖缓存污染对照。

脚本不会自动重试失败的请求。HTTP、流格式或输出截断失败会记录原因，并在完成其他组后返回非零退出码；缓存命中 0 和统计不一致会保留实测值，不当作传输失败。报告中的 `usageEvents` 是各 SSE 事件的原始 usage；`usage` 是按事件顺序合并的累计值，不把 `message_start` 和 `message_delta` 重复相加。

本地检查：`CGO_ENABLED=0 go build ./scripts/anthropic-cache-probe`、`go test ./scripts ./scripts/anthropic-cache-probe`、`go vet ./scripts/anthropic-cache-probe`。这些检查和真实请求覆盖脚本，不代表 Platform 已接入该缓存策略。
