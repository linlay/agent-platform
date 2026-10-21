package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/modelusage"
)

const speechTestAPIKey = "sk-speech-secret"

var (
	speechTestMP3 = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), []byte("mp3-frames")...)
	speechTestWAV = append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), []byte("wav-samples")...)
)

type speechCapture struct {
	paths   []string
	auth    string
	body    map[string]any
	handler func(req *http.Request) (int, string)
}

func (c *speechCapture) client(t *testing.T) *http.Client {
	return &http.Client{Transport: imageGenerateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		c.paths = append(c.paths, req.URL.Path)
		if req.Method == http.MethodPost {
			c.auth = req.Header.Get("Authorization")
			data, _ := io.ReadAll(req.Body)
			c.body = map[string]any{}
			if err := json.Unmarshal(data, &c.body); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
		}
		status, body := c.handler(req)
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func writeSpeechRegistry(t *testing.T, protocol string, baseURL string) *models.ModelRegistry {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"providers", "models"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	provider := "key: speech\nbaseUrl: " + baseURL + "\napiKey: " + speechTestAPIKey + "\n"
	model := strings.Join([]string{
		"key: speech-model",
		"provider: speech",
		"type: tts",
		"protocol: " + protocol,
		"modelId: speech-model-id",
		"pricing:",
		"  currency: CNY",
		"  unit: per_10k_characters",
		"  characters: 3.5",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "providers", "speech.yml"), []byte(provider), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "speech-model.yml"), []byte(model), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return registry
}

func speechTestExecutor(t *testing.T, protocol string, profile config.SpeechGenerateProfileConfig, capture *speechCapture) (*RuntimeToolExecutor, string) {
	t.Helper()
	chatsRoot := t.TempDir()
	if profile.ModelKey == "" {
		profile.ModelKey = "speech-model"
	}
	if profile.MaxTextChars == 0 {
		profile.MaxTextChars = 3000
	}
	if profile.MaxAudioBytes == 0 {
		profile.MaxAudioBytes = 1 << 20
	}
	executor := &RuntimeToolExecutor{
		cfg: config.Config{
			SpeechGenerate: config.SpeechGenerateConfig{
				Enabled:        true,
				DefaultProfile: "general",
				Profiles:       map[string]config.SpeechGenerateProfileConfig{"general": profile},
			},
			Paths: config.PathsConfig{ChatsDir: chatsRoot},
		},
		models: writeSpeechRegistry(t, protocol, "https://speech.test"),
	}
	executor.httpClient = capture.client(t)
	return executor, chatsRoot
}

func speechExecCtx() *contracts.ExecutionContext {
	return &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-1", RunID: "run-1"}}
}

func assertNoSpeechSecret(t *testing.T, result contracts.ToolExecutionResult) {
	t.Helper()
	if strings.Contains(result.Output, speechTestAPIKey) {
		t.Fatalf("tool output leaked the API key: %s", result.Output)
	}
}

func TestSpeechGenerateMiniMaxRequestAndHexAudio(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		return http.StatusOK, `{"data":{"audio":"` + hex.EncodeToString(speechTestMP3) + `","status":2},` +
			`"extra_info":{"usage_characters":12,"audio_length":1800,"audio_format":"mp3"},` +
			`"trace_id":"trace-123","base_resp":{"status_code":0,"status_msg":"success"}}`
	}}
	executor, chatsRoot := speechTestExecutor(t, models.ProtocolMiniMaxTTS, config.SpeechGenerateProfileConfig{Voice: "male-qn-qingse", Format: "mp3", SampleRate: 32000}, capture)

	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{
		"text": "你好，世界", "speed": 1.2, "pitch": int64(-2), "volume": 2, "emotion": "calm", "language": "Chinese",
	}, speechExecCtx())
	if err != nil || result.Error != "" {
		t.Fatalf("speech_generate: err=%v result=%#v", err, result)
	}
	if got := strings.Join(capture.paths, ","); got != "/v1/t2a_v2" {
		t.Fatalf("requests = %s, want default MiniMax endpoint", got)
	}
	if capture.auth != "Bearer "+speechTestAPIKey {
		t.Fatalf("authorization = %q", capture.auth)
	}
	body := capture.body
	if body["model"] != "speech-model-id" || body["text"] != "你好，世界" || body["stream"] != false || body["output_format"] != "hex" || body["language_boost"] != "Chinese" {
		t.Fatalf("unexpected MiniMax body: %#v", body)
	}
	voice := contracts.AnyMapNode(body["voice_setting"])
	if voice["voice_id"] != "male-qn-qingse" || voice["speed"] != 1.2 || voice["pitch"] != float64(-2) || voice["vol"] != float64(2) || voice["emotion"] != "calm" {
		t.Fatalf("unexpected voice_setting: %#v", voice)
	}
	audioSetting := contracts.AnyMapNode(body["audio_setting"])
	if audioSetting["format"] != "mp3" || audioSetting["sample_rate"] != float64(32000) {
		t.Fatalf("unexpected audio_setting: %#v", audioSetting)
	}

	audio := contracts.AnyMapNode(result.Structured["audio"])
	url, _ := audio["url"].(string)
	if !strings.HasPrefix(url, "@chat/speech_generate_run-1_") || !strings.HasSuffix(url, ".mp3") {
		t.Fatalf("audio url = %q", url)
	}
	if audio["mimeType"] != "audio/mpeg" || audio["durationMs"] != 1800 || audio["type"] != "audio" {
		t.Fatalf("unexpected audio payload: %#v", audio)
	}
	saved, err := os.ReadFile(filepath.Join(chatsRoot, "chat-1", strings.TrimPrefix(url, "@chat/")))
	if err != nil || string(saved) != string(speechTestMP3) {
		t.Fatalf("saved audio mismatch: %v", err)
	}
	if len(result.ModelUsage) != 1 {
		t.Fatalf("model usage = %#v", result.ModelUsage)
	}
	usage := result.ModelUsage[0]
	if usage.ModelKey != "speech-model" || usage.Operation != modelusage.OperationSpeech || usage.Characters != 12 || usage.UpstreamRequestID != "trace-123" || usage.HasTokens() {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	assertNoSpeechSecret(t, result)
}

func TestSpeechGenerateMiniMaxBusinessErrorAtHTTP200(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		return http.StatusOK, `{"trace_id":"trace-err","base_resp":{"status_code":1004,"status_msg":"authentication failed"}}`
	}}
	executor, chatsRoot := speechTestExecutor(t, models.ProtocolMiniMaxTTS, config.SpeechGenerateProfileConfig{Voice: "v"}, capture)
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hi"}, speechExecCtx())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "speech_generate_model_request_failed" || !strings.Contains(result.Output, "1004") || !strings.Contains(result.Output, "authentication failed") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Structured["upstreamRequestId"] != "trace-err" {
		t.Fatalf("trace id missing from diagnostics: %#v", result.Structured)
	}
	if len(result.ModelUsage) != 0 {
		t.Fatalf("failed call must not report usage: %#v", result.ModelUsage)
	}
	if entries, _ := os.ReadDir(filepath.Join(chatsRoot, "chat-1")); len(entries) != 0 {
		t.Fatalf("failed call wrote files: %v", entries)
	}
	assertNoSpeechSecret(t, result)
}

func TestSpeechGenerateDashScopeDownloadsURL(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		if req.Method == http.MethodGet {
			return http.StatusOK, string(speechTestWAV)
		}
		return http.StatusOK, `{"status_code":200,"request_id":"req-1","code":"","message":"",` +
			`"output":{"finish_reason":"stop","audio":{"data":"","url":"http://oss.test/audio.wav?Signature=x","id":"a","expires_at":1}},` +
			`"usage":{"input_tokens":0,"output_tokens":0,"characters":5}}`
	}}
	executor, _ := speechTestExecutor(t, models.ProtocolDashScopeTTS, config.SpeechGenerateProfileConfig{Voice: "Cherry"}, capture)
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hello", "language": "English"}, speechExecCtx())
	if err != nil || result.Error != "" {
		t.Fatalf("speech_generate: err=%v result=%#v", err, result)
	}
	if got := strings.Join(capture.paths, ","); got != "/api/v1/services/aigc/multimodal-generation/generation,/audio.wav" {
		t.Fatalf("requests = %s", got)
	}
	input := contracts.AnyMapNode(capture.body["input"])
	if capture.body["model"] != "speech-model-id" || input["text"] != "hello" || input["voice"] != "Cherry" || input["language_type"] != "English" {
		t.Fatalf("unexpected DashScope body: %#v", capture.body)
	}
	audio := contracts.AnyMapNode(result.Structured["audio"])
	if audio["format"] != "wav" || !strings.HasSuffix(audio["url"].(string), ".wav") {
		t.Fatalf("unexpected audio: %#v", audio)
	}
	if len(result.ModelUsage) != 1 || result.ModelUsage[0].Characters != 5 || result.ModelUsage[0].UpstreamRequestID != "req-1" {
		t.Fatalf("unexpected usage: %#v", result.ModelUsage)
	}
}

func TestSpeechGenerateDashScopeRejectsUnsupportedControlsBeforeRequest(t *testing.T) {
	for _, args := range []map[string]any{
		{"speed": 1.5}, {"pitch": 2}, {"volume": 1}, {"emotion": "happy"}, {"sampleRate": 16000}, {"format": "mp3"},
	} {
		capture := &speechCapture{handler: func(req *http.Request) (int, string) { return http.StatusOK, "{}" }}
		executor, _ := speechTestExecutor(t, models.ProtocolDashScopeTTS, config.SpeechGenerateProfileConfig{Voice: "Cherry"}, capture)
		args["text"] = "hello"
		result, err := executor.invokeSpeechGenerate(context.Background(), args, speechExecCtx())
		if err != nil {
			t.Fatal(err)
		}
		if result.Error != "speech_generate_param_unsupported" || len(capture.paths) != 0 {
			t.Fatalf("args %v: result=%#v requests=%v", args, result, capture.paths)
		}
	}
}

func TestSpeechGenerateDashScopeHTTPError(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		return http.StatusUnauthorized, `{"request_id":"req-401","code":"InvalidApiKey","message":"Invalid API-key provided."}`
	}}
	executor, _ := speechTestExecutor(t, models.ProtocolDashScopeTTS, config.SpeechGenerateProfileConfig{Voice: "Cherry"}, capture)
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hello"}, speechExecCtx())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "speech_generate_model_request_failed" || result.Structured["statusCode"] != 401 || !strings.Contains(result.Output, "InvalidApiKey") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Structured["upstreamRequestId"] != "req-401" || len(result.ModelUsage) != 0 {
		t.Fatalf("unexpected diagnostics or usage: %#v %#v", result.Structured, result.ModelUsage)
	}
	assertNoSpeechSecret(t, result)
}

func TestSpeechGenerateBillsInvalidAudio(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		if req.Method == http.MethodGet {
			return http.StatusOK, "<html>expired</html>"
		}
		return http.StatusOK, `{"request_id":"req-2","output":{"audio":{"url":"https://oss.test/a.wav"}},"usage":{"characters":7}}`
	}}
	executor, chatsRoot := speechTestExecutor(t, models.ProtocolDashScopeTTS, config.SpeechGenerateProfileConfig{Voice: "Cherry"}, capture)
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hello"}, speechExecCtx())
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "speech_generate_model_response_invalid" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(result.ModelUsage) != 1 || result.ModelUsage[0].Characters != 7 {
		t.Fatalf("billed characters must still be reported: %#v", result.ModelUsage)
	}
	if entries, _ := os.ReadDir(filepath.Join(chatsRoot, "chat-1")); len(entries) != 0 {
		t.Fatalf("invalid audio was saved: %v", entries)
	}
}

func TestSpeechGenerateNoUsageWithoutProviderCharacters(t *testing.T) {
	capture := &speechCapture{handler: func(req *http.Request) (int, string) {
		return http.StatusOK, `{"data":{"audio":"` + hex.EncodeToString(speechTestMP3) + `"},"base_resp":{"status_code":0}}`
	}}
	executor, _ := speechTestExecutor(t, models.ProtocolMiniMaxTTS, config.SpeechGenerateProfileConfig{Voice: "v"}, capture)
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hello world"}, speechExecCtx())
	if err != nil || result.Error != "" {
		t.Fatalf("speech_generate: err=%v result=%#v", err, result)
	}
	if len(result.ModelUsage) != 0 {
		t.Fatalf("usage must not be estimated from text length: %#v", result.ModelUsage)
	}
}

func TestSpeechGenerateInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile config.SpeechGenerateProfileConfig
		args    map[string]any
		want    string
	}{
		{name: "voice required", args: map[string]any{"text": "hi"}, want: "speech_generate_voice_required"},
		{name: "voice allowlist", profile: config.SpeechGenerateProfileConfig{Voice: "a", Voices: []string{"a", "b"}}, args: map[string]any{"text": "hi", "voice": "c"}, want: "speech_generate_voice_not_allowed"},
		{name: "text too long", profile: config.SpeechGenerateProfileConfig{Voice: "a", MaxTextChars: 3}, args: map[string]any{"text": "四个汉字"}, want: "speech_generate_text_too_long"},
		{name: "text required", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "  "}, want: "speech_generate_text_required"},
		{name: "pitch integer", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "hi", "pitch": 1.5}, want: "speech_generate_pitch_invalid"},
		{name: "speed number", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "hi", "speed": "fast"}, want: "speech_generate_speed_invalid"},
		{name: "emotion enum", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "hi", "emotion": "bored"}, want: "speech_generate_emotion_invalid"},
		{name: "minimax speed range", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "hi", "speed": 3}, want: "speech_generate_param_unsupported"},
		{name: "minimax format", profile: config.SpeechGenerateProfileConfig{Voice: "a", Format: "pcm"}, args: map[string]any{"text": "hi"}, want: "speech_generate_param_unsupported"},
		{name: "unknown profile", profile: config.SpeechGenerateProfileConfig{Voice: "a"}, args: map[string]any{"text": "hi", "profile": "missing"}, want: "speech_generate_profile_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &speechCapture{handler: func(req *http.Request) (int, string) { return http.StatusOK, "{}" }}
			executor, _ := speechTestExecutor(t, models.ProtocolMiniMaxTTS, tc.profile, capture)
			result, err := executor.invokeSpeechGenerate(context.Background(), tc.args, speechExecCtx())
			if err != nil {
				t.Fatal(err)
			}
			if result.Error != tc.want || len(capture.paths) != 0 {
				t.Fatalf("result=%#v requests=%v, want %s without request", result, capture.paths, tc.want)
			}
		})
	}
}

func TestSpeechGenerateDisabled(t *testing.T) {
	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	result, err := executor.invokeSpeechGenerate(context.Background(), map[string]any{"text": "hi"}, speechExecCtx())
	if err != nil || result.Error != "speech_generate_disabled" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSpeechProtocolsCoverRegistry(t *testing.T) {
	for _, protocol := range models.TTSProtocols {
		if _, ok := speechProtocols[protocol]; !ok {
			t.Errorf("registry protocol %s has no implementation", protocol)
		}
	}
	if len(speechProtocols) != len(models.TTSProtocols) {
		t.Errorf("implementations %d != registry protocols %d", len(speechProtocols), len(models.TTSProtocols))
	}
}

func TestSpeechGenerateProfileSchemaListsVoices(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	configureSpeechGenerateProfileSchema(defs, config.SpeechGenerateConfig{
		DefaultProfile: "hd",
		Profiles:       map[string]config.SpeechGenerateProfileConfig{"hd": {Voice: "a", Voices: []string{"a", "b"}}},
	})
	for _, def := range defs {
		if def.Name != "speech_generate" {
			continue
		}
		properties := contracts.AnyMapNode(def.Parameters["properties"])
		profile := contracts.AnyMapNode(properties["profile"])
		voice := contracts.AnyMapNode(properties["voice"])
		if enum, _ := profile["enum"].([]any); len(enum) != 1 || enum[0] != "hd" {
			t.Fatalf("profile enum = %#v", profile["enum"])
		}
		if desc, _ := voice["description"].(string); !strings.Contains(desc, "hd: default a; allowed a, b") {
			t.Fatalf("voice description = %q", desc)
		}
		return
	}
	t.Fatal("speech_generate definition not found")
}
