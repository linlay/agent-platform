# 实时语音对话方案

日期：2026-10-30 初稿，2026-11-01 改为通用方案。状态：部分实施。2026-11-02 已实现 T01 模型/配置/客户端及 T02 Platform 会话/API，后端隔离矩阵通过，WebClient v1 切换、旧链删除与 Chrome 合成音频验证完成；真人、设备与目标环境验证尚未完成。

## 目标与结论

Platform 提供通用的实时语音对话能力，服务智能硬件、WebClient 与 Desktop：客户端上传麦克风音频，Platform 识别后作为普通 Query 交给 Agent，Run 结束后把最终正文合成语音推回客户端播放。

- 在 Platform 进程内新增 `internal/voice`，不设独立网关：直接经 Runtime 启动、订阅和中断 Run，连接身份即 Platform principal，供应商凭据与模型配置复用 model registry。
- 级联方案：实时识别 → Agent Run → 流式合成。不用端到端语音模型，它们自带推理与工具调用，会绕开 Agent 循环、工具权限、HITL、记忆和模型 registry。
- 只朗读 Run 的最终正文；中间模型回合、工具和思考过程不发声，客户端只显示思考中。
- 口语化由 Agent 自身 prompt 负责（为语音场景配置专用 Agent），Platform 不注入语音提示、不新增 mode。
- 首期识别与合成使用 DashScope（Paraformer 实时识别、CosyVoice 流式合成），PCM 半双工；Opus、全双工在后续阶段。
- WebClient 旧语音协议（客户端自行串联 `asr.*`/`tts.*`）废弃，统一使用本文协议。
- 协议形状参考小智 xiaozhi-esp32（hello 握手、listen、abort、stt、tts 起止、裸音频帧），字段遵循平台 camelCase，不承诺与小智固件线协议兼容。

## 链路

```text
客户端麦克风 ─PCM─▶ /api/voice/ws ─▶ voice 会话 ─▶ DashScope 实时识别
                                         │ 整句定稿
                                         ▼
                              Runtime.StartQuery（普通 Query，同一 Chat）
                                         │ Run 终态，取最终正文
                                         ▼
客户端喇叭 ◀─PCM─ /api/voice/ws ◀─ 文本清理 → DashScope CosyVoice 流式合成
```

首声延迟 = 端点静音 + 识别收尾 + Run 完成时间 + 合成首包 + 起播缓冲。只读最终正文意味着整个 Run 时间都计入首声，主要靠 Agent 配置压缩：短的口语回答、首 token 快的模型、低思考强度。

## 模块边界

- `internal/voice`：协议编解码、会话与轮次状态、识别/合成 Adapter 接口及 DashScope 实现、朗读前文本清理。经 Runtime 窄接口工作，不依赖 server、llm、tools。
- `internal/server`：`/api/voice/capabilities` 与 `/api/voice/ws` 的鉴权、升级和薄适配，连接交给 voice。语音 WS 独立于 `internal/ws` 控制面（后者只解码 JSON 帧）。
- `internal/models`：registry 新增 `asr` 类型及 DashScope 实时识别协议、CosyVoice 流式合成协议；`speech_generate` 仍只用完整文件协议。
- App 装配 voice，保持 `CGO_ENABLED=0`。DashScope 两个接口都是 WebSocket，纯 Go 实现；CosyVoice 可直接输出 16kHz PCM，首期无需重采样或编解码库。

## 会话与 Run

- 一个 WS 连接是一个语音会话，绑定一个 Agent 和一个 Chat：hello 给 chatId 则续用；省略则用该 Agent 的主要聊天（`chat-primary.json`），没有则以 `primary:true` 新建。对话就是普通 Chat，WebClient 可同时查看。
- 一次整句定稿对应一个 Query：message 为识别文本，原样作为调用方输入保存；planningMode 固定 false，accessLevel、editingMode 省略，取 Agent 默认。
- 最终正文取 Run 完成时的 `completion.AssistantText`，即最后一个已提交模型回合的正文，与 Run 结束时 `chat.updated` 广播的 `lastRunContent` 同源。voice 在进程内按自己的 runId 获取，不读 chats.db 的 `LAST_RUN_CONTENT_`（截断为 200 字），不新增事件。
- 只有 run.complete 朗读；cancel、error 发 error 消息，由客户端本地提示音表达。
- 同一 Chat 同时只有一个 Run：打断后的新问题可以立刻识别，但等旧 Run 终态后才提交 Query，避免 409。Chat 已有非本会话的活动 Run（例如用户正在 WebClient 输入）时返回 `chat_busy`。
- 断线不取消 Run（与现有 SSE 语义一致），结果留在 Chat，重连不补播。Platform 重启照常取消未完成 Run。
- 朗读前只做最小清理：去掉代码块、VIEW 块、表格、链接地址和 Markdown 符号，不做语义改写。

## 协议 v1

### HTTP

`GET /api/voice/capabilities`，返回 `{code,msg,data}`：

```json
{"enabled":true,"version":1,"websocketPath":"/api/voice/ws",
 "input":{"codecs":["pcm_s16le"],"sampleRate":16000,"channels":1},
 "output":{"codecs":["pcm_s16le"],"sampleRates":[16000,24000]},
 "voices":[{"id":"longanyang","label":"..."}],"defaultVoice":"longanyang"}
```

未启用或识别/合成模型未配置时 `enabled:false` 并给出原因；voices 来自配置。原 `/api/voice/tts/voices` 并入此接口后删除。

`GET /api/voice/ws`：鉴权与其他 HTTP 接口相同（Bearer/cookie）。文本帧是 JSON 控制消息，二进制帧是音频。

### 客户端 → 服务端

| type | 字段 | 说明 |
|---|---|---|
| hello | version, agentKey, chatId?, audio.input{codec,sampleRate,channels,frameMs}, audio.output{codec,sampleRate}, voice? | 连接后 10 秒内发送 |
| listen | state: start/stop, mode: auto/manual | start 后上传音频；auto 由服务端判断说完，manual（按键说话）以 stop 结束 |
| abort | reason? | 打断：停止播放，取消当前轮 |

### 服务端 → 客户端

| type | 字段 | 说明 |
|---|---|---|
| hello | version, sessionId, agentKey, chatId, audio.output{codec,sampleRate,channels,frameMs} | 握手结果 |
| stt | turn, text, final | 识别中间结果与整句定稿；定稿后本轮识别结束，进入 Agent |
| run | turn, state: start/end, runId, chatId, finishReason? | start 与 end 之间显示思考中 |
| tts | turn, state: start/stop, text?, reason? | start 携带朗读全文，随后的二进制帧属于该轮；stop 表示音频发完，不代表客户端播完 |
| awaiting | turn, runId, awaitingId, mode | Run 等待人工，见 HITL |
| abort | turn | 打断确认 |
| error | code, msg | chat_busy、asr_failed、tts_failed、run_failed 等 |

`turn` 由服务端为每段讲话递增分配，用于关联与日志。

### 音频

- 首期上下行均为 `pcm_s16le`、16kHz、单声道；每个二进制帧是一个 20ms 帧（640 字节），不加帧头。浏览器可请求 24kHz 输出。
- 上行按实时节奏发送。下行由服务端按实时节奏发送，只领先约 300ms；客户端起播缓冲 120–200ms，不做 ACK 流控。
- 半双工：一次 listen 只产生一个定稿，定稿后客户端停止上传；收到 tts stop 且本地播完后再发 listen start。

### 打断

单条 WebSocket 保证顺序，用确认消息划界，音频帧不带代数：

1. 客户端立即本地静音、清空播放队列并发送 abort，此后丢弃所有二进制帧和 tts 消息，直到收到服务端 abort 确认。
2. 服务端停止合成与下发，经 Runtime Interrupt 取消当前 Run，丢弃该轮所有晚到回调，回发 `{"type":"abort","turn":N}`。
3. 确认之后的消息都属于新轮。

关闭连接不取消 Run，取消必须发 abort。

### 时序示例

```text
→ {"type":"hello","version":1,"agentKey":"zenmi","audio":{"input":{"codec":"pcm_s16le","sampleRate":16000,"channels":1,"frameMs":20},"output":{"codec":"pcm_s16le","sampleRate":16000}}}
← {"type":"hello","version":1,"sessionId":"vs1","agentKey":"zenmi","chatId":"c1","audio":{"output":{"codec":"pcm_s16le","sampleRate":16000,"channels":1,"frameMs":20}}}
→ {"type":"listen","state":"start","mode":"auto"}
→ [PCM 20ms] ...
← {"type":"stt","turn":1,"text":"今天北京","final":false}
← {"type":"stt","turn":1,"text":"今天北京天气怎么样？","final":true}
← {"type":"run","turn":1,"state":"start","runId":"r1","chatId":"c1"}
← {"type":"run","turn":1,"state":"end","runId":"r1","finishReason":"complete"}
← {"type":"tts","turn":1,"state":"start","text":"北京今天晴，最高十八度。"}
← [PCM 20ms] ...
← {"type":"tts","turn":1,"state":"stop","reason":"done"}
→ {"type":"listen","state":"start","mode":"auto"}
```

## DashScope 接入

- 识别：Paraformer 实时识别（paraformer-realtime-v2），run-task → 二进制音频 → finish-task。auto 模式以服务端返回的整句结束作为说完，`max_sentence_silence` 默认 1300ms（范围 200–6000），首期在 500–800ms 调优；manual 模式由 listen stop 触发 finish-task。
- 合成：CosyVoice WebSocket（run-task → continue-task → finish-task，音频走二进制），format 取 pcm，sample_rate 直接取客户端要求的 16000 或 24000；最终正文一次 continue-task 后 finish-task，服务端自动分句并流式返回音频。
- Opus：识别侧要求 Ogg 封装；合成侧支持 opus 输出，封装形式待实测。纯 Go 做 Ogg 封装/解封装，不引入编解码库。
- 复用现有 provider `dashscope`（zenmind-env `registries/providers/dashscope.yml`，北京地域 `https://dashscope.aliyuncs.com`，与 qwen3-tts-flash 共用同一 API Key），不新建 provider。WS 地址由 baseUrl 换 wss 加新协议默认路径 `/api-ws/v1/inference` 得到，可用 provider 的协议级 endpointPath 覆盖。Transit Hub（th-main）只代理 HTTP 非流式接口，语音直连 dashscope。
- 新增两个模型文件：`dashscope-paraformer-realtime-v2.yml`（type asr）与 `dashscope-cosyvoice-v3-flash.yml`（type tts，流式协议）；协议名在实现时定。现有 `dashscope-qwen3-tts-flash`（DASHSCOPE_TTS，HTTP 非流式，单次 ≤600 字，24kHz WAV）继续只供 speech_generate。CosyVoice 按字符计价沿用 `per_10k_characters`；Paraformer 按时长计费，registry 尚无该计价单位，随用量入账放到阶段 2。
- 代码不写死模型名、音色和地址。阶段 0 用该 Key 实测两个模型已开通、经典地址可用；若只能走工作空间专属域名，另建 provider。

阶段 0 可用性验证（2026-11-02）：现有同一 Key 通过经典 WS 地址成功调用 `cosyvoice-v3-flash`（16kHz PCM）与 `paraformer-realtime-v2`，合成后回识别的文本一致。单次冷连接测试中，TTS 提交文本到首个音频包约 291ms，ASR 源音频发送结束到句子定稿约 392ms；尚未验证完整 Agent 链路及延迟分位数。方法、原始指标和测量边界见 [阶段 0 验证记录](realtime-voice-stage0-2026-11-02.md)。

## 配置

`configs/runtime.yml` 新增 `voice` 节：`enabled`（默认 false）、`asr-model`、`tts-model`、`default-voice`、`voices`、`max-sentence-silence-ms`、`downlink-lead-ms`。Agent 不新增语音配置，口语风格写在 Agent prompt。

## HITL 与身份

- Run 进入 awaiting 时，服务端发送 awaiting 消息并朗读固定提示（如"需要你在手机上确认"）；用户在 WebClient/Desktop 照常提交，Run 继续，voice 继续等待最终正文。语音不能批准任何操作。
- planningMode 固定关闭，不会出现计划确认。
- 首期客户端使用 Platform 可验证的 JWT，Run 的 subject 即该用户。设备不保存长期用户 token；"设备凭据换短期票据"需由 identity-center 签发（携带 deviceId），作为硬件上线前置项单独确认。

## v1 不做

语音回答 question/approval；全双工与回声消除；Opus；断线续播；朗读中间过程；语音用量计入 Chat 统计（首期只记日志与指标）；独立网关。

## 阶段与验收

| 阶段 | 内容 |
|---|---|
| 0 | DashScope 识别/合成连通与延迟样本；测量目标 Agent 短问答的 Run 完成时间 |
| 1 | `internal/voice`、`/api/voice/*`、registry 协议、PCM 半双工、abort、awaiting 提示；WebClient 改用新协议；设备固件接入 |
| 2 | Opus、语音回答 question、设备换票、用量入账；可选：最终回合预合成（模型回合以无工具调用结束时直接下发已合成音频） |
| 3 | 板端 AEC 实机验证后开放全双工插话 |

WebClient 改造：语音模式采集 16kHz PCM 走新协议，时间线照常经 Chat/Run 事件显示，用 run 消息的 runId 关联；旧 `asr.*`/`tts.*` 帧、ASR/TTS 调试面板和 ` ```tts-voice ` 块随旧协议删除，需要音频文件时用 `speech_generate`。

验收：

- 硬件不经 Mac，完成唤醒、连续对话、工具等待及播放。
- 无工具短问答 100 次，记录 speechEnd、asrFinal、runStart、runEnd、ttsFirstAudio、playStart；首声目标 P50 ≤ 3s、P95 ≤ 5s，阶段 0 实测后校准。
- 本地打断 100ms 内静音；1000 次打断、晚到回调和重连竞态中旧轮音频误播为零、重复提交为零。
- 30 分钟连续使用无内存或队列持续增长。

## 参考

- 小智 WebSocket 协议：https://github.com/78/xiaozhi-esp32/blob/main/docs/websocket.md
- Paraformer 实时识别客户端事件：https://help.aliyun.com/zh/model-studio/paraformer-client-events
- CosyVoice WebSocket：https://help.aliyun.com/zh/model-studio/cosyvoice-websocket-api/
- CosyVoice 客户端事件（format、sample_rate）：https://www.alibabacloud.com/help/zh/model-studio/cosyvoice-client-events
- ESP32-S3 AFE：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/audio_front_end/README.html
- ESP32-S3 AEC：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/acoustic_echo_cancellation/README.html
