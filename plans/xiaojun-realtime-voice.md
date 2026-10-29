# 小君实时语音输入与输出方案

日期：2026-10-30。状态：设计，尚未实施或完成目标环境验证。

## 目标与结论

ESP32-S3 桌面宠物“小君”已有 16kHz/16bit/mono 麦克风和喇叭、离线唤醒“小君你好”、女声回应及基于播放能量的嘴型。目标是通过 Wi-Fi 独立连接云端 zenmi，摆脱 Mac USB 桥接。

推荐新增独立 voice-gateway：设备通过 WSS 连接网关，网关负责 ASR、端点检测、文本分句、TTS、音频缓冲和取消，复用 Platform 的 Query、Chat、Run、Interrupt、Attach、Submit。先交付 PCM 半双工，再优化流式延迟，最后通过实机 AEC 验收开放全双工。

现有约 3.8 秒文字完成时间、线上 voice capabilities 404、runtime-config.js 的 VOICE_ENABLED=false 来自任务输入，本次未重新实测。分析未读取私密 token。

## 源码核实

以下行号对应分析时源码，可结合符号定位：

| 能力 | 事实来源与影响 |
|---|---|
| HTTP 控制入口 | internal/server/server_routes.go:336 注册 Query、Attach、Submit、Steer、Interrupt。 |
| SSE | internal/server/handler_query.go:103 经 Runtime 启动并订阅；internal/server/sse_writer.go:45 设置 X-Accel-Buffering:no；internal/stream/defaults.go:12 默认关闭 Render 缓冲。反向代理逐事件抵达仍需测量。 |
| 文本增量 | internal/stream/dispatcher_content.go:32、61；content.delta 包含 contentId/delta，不保证每条携带 runId/chatId，由流和 content.start 关联。 |
| 身份 | internal/stream/assembler.go:139 的 run.start 提供 runId/chatId/agentKey；internal/runtime/query/admission.go:364 在省略时生成 ID。Chat 保留上下文，一轮讲话对应一个新 Run。 |
| 断线 | internal/server/query_disconnect_parity_test.go:80 明确覆盖 SSE 断开后 Run 继续、Attach 恢复。关闭 SSE 不等于取消。 |
| 中断 | internal/server/handler_query_submit.go:55、internal/contracts/run_control.go:302；中断取消 context 并唤醒等待者。ACK 是受理，不是收尾完成。 |
| Chat 排他 | internal/runtime/query/query_gate.go:233；同 Chat 另一个活动 Run 导致 409 active_run_conflict。 |
| 续接 | internal/server/handler_attach.go:13；使用 lastSeq，窗口过期返回 SEQ_EXPIRED，Attach 不能修改 detail。 |
| WebSocket | internal/server/ws_routes.go:214 支持业务控制；internal/ws/conn.go:384 将消息解码为 JSON RequestFrame，没有实时 PCM/Opus 媒体协议。 |
| HTTP 鉴权 | internal/server/server_auth.go:63 支持 Bearer/cookie；internal/server/security.go:93 使用 RS256、公钥/JWKS，并按配置核验 issuer/audience/expiry。 |
| 控制身份 | internal/runtime/controlscope/scope.go:76 检查 transport/subject/boundary/lane；网关应统一通过 HTTP/SSE 创建、续接和控制 Run。 |
| HITL | docs/HITL协议.md:20；question/approval/form/planning 使用根 agentKey、runId、awaitingId，param/params 按模式区分。 |

speech_generate 是完整音频文件生成工具，不是实时播放链：internal/tools/tool_speech_generate.go:47、107 要求 provider 凭据并获取、校验、持久化音频；speech_protocol_minimax.go:59 明确 stream:false，DashScope 同样使用完整响应。

Sibling agent-webclient 的语音实现可以参考，但不能当成已交付服务：src/shared/config/featureFlags.ts:30 固定关闭；voiceRuntimeCore.ts:237 保留 tts.start/append/commit/stop；voiceAsrProtocol.ts:170 保留 ASR 控制协议。voiceChatAudio.ts:6 的 final 后等待为 1500ms；useVoiceChatSocket.ts:107 插话等待 final 且要求长度大于 2，不适合低延迟设备。浏览器 echoCancellation 不能替代板端 AEC。

## 架构选择与模块边界

独立网关的优势是媒体资源和故障隔离、供应商与编解码独立替换、无需侵入工具循环；代价是新增设备身份、会话映射、收据和运维。Platform 内置方案少一次内部转发，身份和用量更易统一，但必须扩展媒体生命周期、配置和运行时边界。两者最大延迟仍来自端点、LLM 首句与 TTS，不能只凭架构推断收益。

网关与 Platform 同机或同地域部署，/api/voice/* 代理到网关。不要与已有 internal/gateway 反向网关混淆。若未来内置，新增 internal/voice，经 App 装配和 Runtime 窄接口工作，Server 只做薄适配；不新增 voice mode，不让 Server 直接依赖 llm/tools。保持 Platform CGO_ENABLED=0，模型和编解码依赖放独立服务。

链路：设备 AFE/VAD → WSS → ASR → 轮次控制 → Platform Query/SSE → 正文提取/分句 → TTS/重采样 → WSS → 设备播放队列/I2S。嘴型由实际播放 PCM 驱动。

## 输入与端点

20ms 音频帧为 320 个采样、640 字节。设备保留 240–320ms 预录，起声确认约 80–120ms；结束候选静音 300–450ms，犹豫或不完整尾句可延至 600–800ms。唤醒后约 5 秒无讲话退出；单次讲话限制 20–30 秒。以上均为待调优初值。

边录边传、边识别。设备和供应商 VAD 都只提供证据，网关统一提交。Adapter 区分 partial、segmentFinal、utteranceFinal；只有整轮 final 提交一次。首期不按 partial 提前执行工具，不用字符长度排除“停”“好”等输入。

SenseVoice 可作为整句基线，分片上传不等于原生流式识别。真正流式选择 FunASR Paraformer online/2pass 或商业实时 ASR，2pass 句末纠错必须在提交边界内处理。

## 文本流与 TTS

增量解析 Markdown，只朗读可见正文，忽略 thinking、工具原文、代码、HTML、VIEW、裸 URL；按 contentId、actor、task 归属避免重复朗读成员输出。

优先在句号、问号、叹号、分号处分句；首段约 8–20 汉字即可送出。等待超过约 250–400ms 或 30–50 字时安全切分，保护数字、英文和未闭合标记。content.end 刷新尾段；以 run.complete/cancel/error 判断终态，不能把 [DONE] 当成功。

初始一个活动合成流、最多一段预取。双向流式供应商持续追加文本；单段接口按短句调用并流式接收音频。Adapter 声明 streamInput/streamOutput/cancelSupported，不假设通用 flush/cancel。不要让 zenmi 逐句调用 speech_generate，也不等待 Run 全部完成。

## 接口与媒体协议（新增建议）

HTTP JSON 沿用 {code,msg,data}：

- GET /api/voice/capabilities：版本、格式、ASR/TTS readiness、双工能力，缺配置明确不可用。
- POST /api/voice/device/token：设备凭据换短期票据。
- GET /api/voice/v1/ws：JSON 控制与二进制音频。
- 会话状态读取：重连校准 Chat、Run、awaiting、播放进度。

控制事件示例：

```json
{"type":"session.start","requestId":"q1","agentKey":"zenmi","input":{"codec":"pcm_s16le","sampleRate":16000,"channels":1,"frameMs":20}}
{"type":"session.ready","sessionId":"vs1","connectionEpoch":3,"chatId":"c1","generation":7,"duplex":"half"}
{"type":"input.start","utteranceId":"u42"}
{"type":"asr.partial","utteranceId":"u42","revision":2,"text":"今天北京"}
{"type":"asr.final","utteranceId":"u42","text":"今天北京天气怎么样？"}
{"type":"agent.started","turnId":"t42","runId":"r42","chatId":"c1"}
{"type":"speech.segment","turnId":"t42","generation":7,"segmentId":1,"text":"我帮你看看。","sourceContentId":"ct1"}
{"type":"playback.ack","turnId":"t42","generation":7,"streamId":12,"playedSeq":25,"playedSamples":8320,"bufferedMs":140}
{"type":"turn.cancel","turnId":"t42","cancelGeneration":7,"cancelId":"cancel42","reason":"barge_in"}
```

每个媒体帧固定头至少含 version/kind/codec/flags、connectionEpoch、turnNo、generation、streamId、seq、sampleOffset、payloadBytes、samplesPerChannel。头整数使用网络字节序，PCM payload 为 little-endian。媒体 seq 与 Platform 事件 seq 独立，采样偏移与 epoch milliseconds 独立。

首版 PCM S16LE/16k/mono，每方向 32KB/s（256kbps）。后续 Opus mono/20ms 可从 16–24kbps 试起，先验证设备 CPU/RAM。自定义 WSS 可传 raw Opus packet；供应商若要求 Ogg，网关负责转换，不能直接透传。完整 MP3/WAV 保留为产物下载，不作为实时主链路。24kHz TTS 必须有状态重采样，不能只修改格式标签。

## 有界队列与取消

| 队列 | 初值 | 超限策略 |
|---|---|---|
| 设备上行待发 | 0.5–1 秒 | 明确断档并结束输入，避免缺字指令执行 |
| 网关待识别 | 1–2 秒 | 背压，持续超限终止输入 |
| 待合成文本 | 2–3 段、约 150–250 字 | 暂停预取，持续溢出结束或缩短回答 |
| 设备播放 | 起播 120–200ms，硬上限 500–800ms | credit/ACK 控制发送 |
| 网关待发音频 | 约 1 秒 | 暂停读取或取消合成 |

控制消息优先，小帧发送，限制在途音频。WSS 仍有 TCP 队头阻塞，Opus 不消除该问题。ACK 区分 received/queued/played；tts.done 不代表设备播完。

插话取消顺序：

1. 设备立即静音，清播放队列和 I2S DMA，拒收当前 generation，嘴型归零，不等待网络。
2. 保存新讲话预录并发送 turn.cancel。
3. 网关原子递增 generation，停止旧分句、TTS、待发音频；旧 ASR 取消，新讲话独立 utterance。
4. 独立 HTTP 请求显式调用 Platform /api/interrupt，不能只关闭 SSE。
5. 所有晚到回调按 session/epoch/turn/generation 校验后丢弃。
6. 旧 Run 终态或确认不再活动后，提交新 final，避免同 Chat 409。
7. Run ID 尚未返回时保留取消标记，注册或身份确认后补发 Interrupt，避免孤儿 Run。

取消不能撤回已完成的外部副作用，应按真实结果表达。

状态机：休眠 → 唤醒/监听 → 收音 → 端点确认 → Agent运行 → 播放 → Run结束且播完 → 监听；继续讲话从端点确认返回收音。Agent/播放遇插话进入取消中，旧 Run 收尾后处理新输入。awaiting.ask 进入等待人工，有效提交后恢复；规划批准需跟踪新的执行 Run。全双工将输入监听与播放拆为并行子状态。

## 半双工到全双工

第一步保留唤醒和本地回应，播放期间不向普通 ASR 提交麦克风，播完恢复连续对话；按钮停止可用。此阶段提示回应播完再说，不承诺唤醒后连说问题完整识别。

第二步加入播放期间唤醒/停止词打断，验证回声误触发。第三步实机验证 AEC/NS/VAD 和双讲后开放普通讲话插话。AEC 参考必须来自实际送入 I2S 的 PCM，包含本地回应、提示音、云端 TTS；采样率、时钟、延迟对齐。功放削波和结构振动须实测。

嘴型使用解码、重采样后实际播放 PCM 的短窗能量；静音、欠载、取消归零。ESP-SR 支持 AFE/AEC 不代表当前板子已达全双工效果。

## 弱网与会话

网关保存设备 owner、session→agentKey/chatId、当前 Run、提交收据、最后 Platform seq、播放 ACK；Platform 保留聊天历史。原始音频默认仅有界内存缓存。

重连采用抖动退避 0.5/1/2/4 秒，上限约 15 秒；短断线宽限约 5–10 秒。重新鉴权并更换 connectionEpoch，丢弃旧回调。输入超缓存窗口结束并提示重说，禁止补送过时录音。

运行中用 Attach+lastSeq 恢复；SEQ_EXPIRED 读取 Chat/activeRun 校准。回放只重建状态，默认不重复朗读。用户主动“继续”时按句段与播放账本续播。生成文本与已听文本分别记录，不伪造或回写历史。

受理结果不明时按预分配 runId、Chat 状态和网关收据对账；不能把 requestId 当已有 exactly-once 保证，禁止盲目重发可能执行工具的请求。Platform 重启取消未完成 Run，只保留 Chat；网关重启同样结束旧音频代数，不宣称恢复工具执行。

## 身份与 HITL

设备只保存唯一设备凭据/私钥，换短期可撤销语音票据。Platform 长期用户 token、供应商 key、签发私钥不能放设备。服务端绑定 owner、zenmi、Chat、限额，不信自报 deviceId/owner。

网关使用 Platform 可验证的用户委托身份。现有 JWT 验证可复用，但没有完整设备换票生命周期；身份服务若不能签发委托票据，需新增受控换票或窄代理，这是生产前置项。不能用广权共享账号代理所有设备。

语音 Query 初始显式 accessLevel:default、关闭 planning，按范围选择 editingMode:false；不通过 full_access 绕过确认。创建、Attach、Interrupt 保持相同 HTTP 控制身份；Submit 允许跨设备的既有流程仍须由网关验证设备与 owner/Chat 关系。

question 朗读问题和少量选项，回答绑定当前 awaiting；approval 读出具体操作，首版通过明确设备确认动作或手机页面提交；form 转手机；planning 简述计划后转确认页面，批准后追踪新执行 Run ID。普通“好”不能批准任意操作，语音不生成 approve_rule_run。强制人工表单保持原协议。

普通工具用表情/指示灯表达忙碌，长操作最多一句自然提示，不读日志。full detail 可供网关消费工具事件并自行筛选；summary 包含正文和 awaiting，不含普通工具事件。detail 在 Run 创建时冻结。

## 可替换适配与最小选型

ASR 接口：Open(ctx,config) → WriteAudio/EndInput/Events/Cancel，归一化 partial/segmentFinal/utteranceFinal/error。
TTS 接口：Open(ctx,voice,format) → Append/Finish/Audio/Cancel，声明真实流式与取消能力。
Agent 接口：StartQuery/Attach/Interrupt/Submit/ReadChatState。

无 key 的独立联网验证可将 SenseVoice 整句或 FunASR online 部署云端 Linux，TTS 使用 sherpa-onnx 中文 VITS 并分句合成；音色、模型许可证、CPU 和延迟需验证，不保证等同 Xiaoxiao。低延迟产品候选是商业实时 ASR + 流式 TTS，例如 Paraformer 与 CosyVoice WebSocket，需申请 key 和选择区域。要保留 Xiaoxiao 可评估 Azure Speech 官方 zh-CN-XiaoxiaoNeural，需要凭据及区域验证；临时桥接客户端不作为正式服务接口合同。

## 延迟预算、实施与验收

首声定义：用户最后音素结束到设备开始播放首段有意义回答；本地问候/提示音单独计量，不计回答首声。

| 环节 | 设计预算，非实测 |
|---|---|
| 端点 | 250–450ms |
| ASR 收尾 | 100–250ms |
| Agent 首个可朗读短句 | 600–1400ms |
| TTS 首音频 | 100–350ms |
| 网络及起播缓冲 | 80–200ms |
| 合计 | 约 1.1–2.7 秒 |

记录 speechEnd/asrFinal/queryAccepted/firstDelta/firstClause/firstAudio/devicePlayStart。ASR 与讲话重叠，后续 TTS 与 Agent 重叠。工具请求单独统计；3.8 秒完成时间不能推导首声。

| 阶段 | 交付 | 人日估算 |
|---|---|---|
| 0 | USB 基线、音质样本、zenmi 首 delta/首句测量 | 1–2 |
| 1 | Wi-Fi PCM、设备绑定、Adapter、Chat、有界队列、半双工、取消及代数隔离 | 7–10 |
| 2 | 真流式、分句、弱网收据、HITL、指标压测 | 8–12 |
| 3 | AEC 双讲、误触发、必要时 Opus | 10–18 |

后端和固件各一人，稳定半双工约 16–24 人日，全双工另计；身份服务、算力和板端资源尚未核实。新增网关会话/媒体/授权/Adapter/分句/账本和固件 WSS/队列/AEC；复用 Platform 的 Run、Chat、控制、HITL 和权限，WebClient 仅参考。

验收：

- 关闭 Mac、拔 USB 后完成唤醒、连续对话、工具等待及播放。
- 固定 Wi-Fi/供应商、无工具短问答至少 100 次：首版首声 P50≤2.5s、P95≤4s；优化目标 P50≤1.5s、P95≤2.5s。
- 本地停止约 100ms 内静音；全双工起声到静音目标 P95≤250ms。
- 1000 次插话/晚包/重连竞态中旧代音频误播为零，重复提交轮次为零。
- 100 次短断网不重复 Query，超过缓存窗口明确提示重说。
- 30 分钟连续使用无队列或内存持续增长、播放速度漂移。
- 全双工覆盖距离、音量、风扇噪声和双讲，未达标保持半双工。

## 外部一手资料

- ESP32-S3 AFE：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/audio_front_end/README.html
- AEC：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/acoustic_echo_cancellation/README.html
- SenseVoice 原论文：https://fun-audio-llm.github.io/pdf/FunAudioLLM.pdf
- FunASR 在线服务：https://github.com/modelscope/FunASR/blob/main/runtime/docs/SDK_tutorial_online.md
- Paraformer 客户端事件（Opus 要求 Ogg）：https://help.aliyun.com/zh/model-studio/paraformer-client-events
- CosyVoice WebSocket：https://help.aliyun.com/zh/model-studio/cosyvoice-websocket-api/
- sherpa-onnx VITS：https://k2-fsa.github.io/sherpa/onnx/tts/pretrained_models/vits.html
- Azure 音色：https://learn.microsoft.com/en-us/azure/ai-services/speech-service/language-support?tabs=tts
- Azure 输出格式：https://learn.microsoft.com/en-us/azure/ai-services/speech-service/rest-text-to-speech
- Opus：https://datatracker.ietf.org/doc/rfc7587/
