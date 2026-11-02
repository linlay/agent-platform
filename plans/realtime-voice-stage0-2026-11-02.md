# 实时语音阶段 0：ASR / TTS 可用性验证

测试时间：2026-11-02 09:24:51（Asia/Shanghai）。状态：单次真实服务调用成功；尚未验证完整 Agent 语音链路、WebClient 或设备环境。

## 结论

现有 `dashscope` provider 的同一个 API Key 可调用 `cosyvoice-v3-flash` 和 `paraformer-realtime-v2`。经典地址 `wss://dashscope.aliyuncs.com/api-ws/v1/inference` 在本次测试中可用，无需为此次接入新建 provider 或更换域名。连接从本机直达 DashScope，未经过 Transit Hub 或环境变量指定的代理。

凭据由验证程序直接读取既有 provider YAML，不写入源码、终端输出或测试产物；未修改实际 provider / model 配置。

## 方法与结果

CosyVoice 将“你好，今天天气怎么样”合成为 16kHz、单声道、16-bit little-endian PCM，音色为 `longanyang`。收到 `task-started` 后发送一次 `continue-task`，随即发送 `finish-task`，持续读取音频至 `task-finished`。

Paraformer 使用相同 PCM，以 100ms 音频块按真实时间节奏发送，追加 1200ms 静音。关闭语义断句，设置 `max_sentence_silence=600`；持续读取结果至 `task-finished`。音频不是一次性快速上传。

| 指标 | 单次观测值 |
|---|---:|
| TTS WebSocket 建连 | 160.589ms |
| TTS run-task → task-started | 111.537ms |
| TTS 提交文本 → 首个非空音频包 | 290.692ms |
| TTS 冷连接开始 → 首个音频包 | 562.820ms |
| TTS run-task → task-finished | 1024.142ms |
| TTS 输出音频时长 | 2480ms |
| TTS 输出 PCM 大小 | 79,360 bytes |
| TTS 二进制消息数 | 12 |
| ASR WebSocket 建连 | 190.538ms |
| ASR run-task → task-started | 56.408ms |
| ASR 开始发音频 → 首个非空文本结果 | 657.960ms |
| ASR 源音频发送结束 → 首个句子定稿 | 392.373ms |
| ASR 是否在发送 finish-task 前定稿 | 是 |

ASR 返回：`你好，今天天气怎么样？`。忽略标点与空白后与输入文本完全一致。供应商用量回报：TTS `characters=19`；ASR `duration=2` 秒。用量按供应商返回保存，本次未核算实际扣费或接入 Chat 统计。

PCM 长度、采样参数、非静音峰值及 WAV 包装后的内容一致性已检查。12 个 TTS 消息的平均音频时长约 207ms，并非平台协议拟定的固定 20ms 帧；正式 Adapter 需要重新分帧。

## 复现

从仓库根目录执行（会产生真实调用费用）：

```sh
go run ./scripts/voice-probe \
  -provider /Users/linlay/Project/zenmind/zenmind-env/registries/providers/dashscope.yml \
  -out /private/tmp/agent-platform-voice-stage0-20261102
```

验证程序：`scripts/voice-probe/main.go`。本次原始结构化结果及音频：

- `/private/tmp/agent-platform-voice-stage0-20261102/report.json`
- `/private/tmp/agent-platform-voice-stage0-20261102/tts.pcm`
- `/private/tmp/agent-platform-voice-stage0-20261102/tts.wav`

## 测量边界

- 只有一次冷连接样本，不能推导 P50、P95、并发容量或长期稳定性。
- ASR 延迟基准是整个源 PCM 的发送结束时刻（含其中可能存在的尾部静音），不是人工标注的真实 speechEnd。
- 合成语音回识别仅用于连通性验证，不能代表真人、噪声、远场及句中停顿下的识别质量。
- 未测 24kHz、连接复用、manual 多句、任务取消、浏览器播放、设备或 Agent Run；未验证整体首声 3s/5s 目标。

协议参考：[CosyVoice 客户端事件](https://help.aliyun.com/zh/model-studio/cosyvoice-client-events)、[Paraformer 客户端事件](https://help.aliyun.com/zh/model-studio/paraformer-client-events)。
