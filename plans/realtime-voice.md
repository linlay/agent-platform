# 实时语音对话方案

日期：2026-10-30 初稿，2026-11-01 改为通用方案，2026-11-02 实现 v1 后精简为决策与剩余工作。

## 现状

- 已实现：Platform 进程内 `internal/voice`（DashScope Paraformer 实时识别 + CosyVoice 流式合成，PCM 半双工）、`/api/voice/capabilities` 与 `/api/voice/ws`、模型 registry 的 `asr` 类型与两个实时协议，后端隔离矩阵已通过。WebClient v1（agent-webclient #49）与 Desktop 宿主代理（zenmind-desktop #239）已合并切换。
- 协议与会话语义见 [API 与协议](../docs/API与协议.md#实时语音-v1)，模型与 runtime 配置见 [配置化说明](../docs/配置化说明.md#实时语音模型与部署配置)，模块边界见 [Runtime 模块边界](../docs/Runtime模块边界.md)。
- 阶段 0（2026-11-02）：现有 dashscope Key 经经典 WS 地址调通两个模型，单次冷连接 TTS 提交文本到首包约 291ms，ASR 发送结束到定稿约 392ms。只有一个样本，未覆盖完整 Agent 链路和延迟分位数。复测用 `scripts/voice-probe`（真实计费）。

## 已定决策

- 不设独立网关：voice 在 Platform 进程内经 Runtime 启动、观察和中断 Run，身份即 Platform principal，凭据与模型复用 model registry。
- 级联方案（识别 → Agent Run → 合成），不用端到端语音模型，以免绕开 Agent 循环、工具权限、HITL、记忆和 registry。
- 只朗读 Run 的最终正文，中间回合、工具和思考不发声。首声 = 端点静音 + 识别收尾 + Run 时间 + 合成首包 + 起播缓冲，主要靠 Agent 配置压缩：短回答、首 token 快的模型、低思考强度。
- 口语风格写在语音专用 Agent 的 prompt 里；Platform 不注入语音提示、不新增 mode，Agent 不新增语音配置。
- 语音不能批准任何操作：awaiting 只朗读提示，用户在原界面 Submit。
- 断线不取消 Run、不续播、不补录。
- 协议形状参考小智 xiaozhi-esp32，字段用平台 camelCase，不承诺与小智固件线协议兼容。客户端自行串联 `asr.*`/`tts.*` 的旧语音协议废弃；需要音频文件时用 `speech_generate`。

## 剩余工作

1. zenmind-gateway：nginx 仍把 `/api/voice/` 转发到旧 voice-server（11953），需改为转发 Platform 并支持 WS 升级，再决定 voice-server 何时下线。切换前不得宣称可经网关使用。
2. 阶段 1 验收：
   - 硬件不经 Mac，完成唤醒、连续对话、工具等待及播放（依赖设备固件接入）。
   - 无工具短问答 100 次，记录 speechEnd、asrFinal、runStart、runEnd、ttsFirstAudio、playStart；首声目标 P50 ≤ 3s、P95 ≤ 5s，实测后校准。
   - 本地打断 100ms 内静音；1000 次打断、晚到回调和重连竞态中旧轮音频误播为零、重复提交为零。
   - 30 分钟连续使用无内存或队列持续增长。
   - 真实浏览器（含 Firefox 采样率回退）、Desktop 宿主和目标部署环境下的端到端验证。
3. 设备身份：设备不保存长期用户 token；设备凭据换短期票据需由 identity-center 签发（携带 deviceId），硬件上线前单独确认。
4. 阶段 2：
   - Opus：识别侧要求 Ogg 封装，合成侧封装形式待实测；纯 Go 封装/解封装，不引入编解码库。
   - 语音回答 question。
   - 用量入账：CosyVoice 沿用 `per_10k_characters`；Paraformer 按时长计费，registry 需新增计价单位。
   - 可选：最终回合预合成（模型回合以无工具调用结束时直接下发已合成音频）。
5. 阶段 3：板端 AEC 实机验证后开放全双工插话。

## 参考

- 小智 WebSocket 协议：https://github.com/78/xiaozhi-esp32/blob/main/docs/websocket.md
- Paraformer 实时识别客户端事件：https://help.aliyun.com/zh/model-studio/paraformer-client-events
- CosyVoice WebSocket：https://help.aliyun.com/zh/model-studio/cosyvoice-websocket-api/
- CosyVoice 客户端事件（format、sample_rate）：https://www.alibabacloud.com/help/zh/model-studio/cosyvoice-client-events
- ESP32-S3 AFE：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/audio_front_end/README.html
- ESP32-S3 AEC：https://docs.espressif.com/projects/esp-sr/en/latest/esp32s3/acoustic_echo_cancellation/README.html
