package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/voice"
	"github.com/gorilla/websocket"
)

type voiceTestCommand struct {
	Header struct {
		ID     string `json:"task_id"`
		Action string `json:"action"`
	} `json:"header"`
	Payload struct {
		Task       string `json:"task"`
		Parameters struct {
			SampleRate int `json:"sample_rate"`
		} `json:"parameters"`
		Input struct {
			Text string `json:"text"`
		} `json:"input"`
	} `json:"payload"`
}

type voiceTestProvider struct {
	texts      chan string
	asr        atomic.Int32
	tts        atomic.Int32
	pcmBytes   atomic.Int32
	transcript atomic.Value
	autoFinal  atomic.Bool
	holdStart  atomic.Bool
	failTTS    atomic.Bool
}

func voiceProvider(t *testing.T) (*httptest.Server, *voiceTestProvider) {
	t.Helper()
	p := &voiceTestProvider{texts: make(chan string, 32)}
	p.transcript.Store("第一句。第二句。")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-voice-key" {
			t.Error("lost provider credential")
			http.Error(w, "unauthorized", 401)
			return
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		for {
			var start voiceTestCommand
			if err := c.ReadJSON(&start); err != nil {
				return
			}
			if start.Header.Action != "run-task" {
				t.Error("expected run-task")
				return
			}
			id := start.Header.ID
			emit := func(kind string, sentence map[string]any) error {
				return c.WriteJSON(map[string]any{"header": map[string]any{"task_id": id, "event": kind}, "payload": map[string]any{"output": map[string]any{"sentence": sentence}}})
			}
			if p.holdStart.Load() {
				if _, _, err := c.ReadMessage(); err == nil {
					t.Error("PCM reached provider before task-started")
				}
				return
			}
			if emit("task-started", nil) != nil {
				return
			}
			if start.Payload.Task == "asr" {
				p.asr.Add(1)
				for {
					kind, data, err := c.ReadMessage()
					if err != nil {
						return
					}
					if kind == websocket.BinaryMessage {
						p.pcmBytes.Add(int32(len(data)))
						if p.pcmBytes.Load() >= 642 && p.autoFinal.CompareAndSwap(true, false) {
							if emit("result-generated", map[string]any{"begin_time": 0, "text": p.transcript.Load().(string), "sentence_end": true}) != nil {
								return
							}
						}
						continue
					}
					var finish voiceTestCommand
					if json.Unmarshal(data, &finish) != nil || finish.Header.Action != "finish-task" {
						t.Error("expected ASR finish")
						return
					}
					break
				}
				if emit("result-generated", map[string]any{"begin_time": 0, "text": p.transcript.Load().(string), "sentence_end": true}) != nil {
					return
				}
				if emit("task-finished", nil) != nil {
					return
				}
			} else if start.Payload.Task == "tts" {
				p.tts.Add(1)
				var text, finish voiceTestCommand
				if c.ReadJSON(&text) != nil || c.ReadJSON(&finish) != nil {
					return
				}
				if text.Header.Action != "continue-task" || finish.Header.Action != "finish-task" {
					t.Error("wrong TTS task sequence")
					return
				}
				p.texts <- text.Payload.Input.Text
				if p.failTTS.CompareAndSwap(true, false) {
					if emit("task-failed", nil) != nil {
						return
					}
					continue
				}
				size := start.Payload.Parameters.SampleRate * 2 / 50
				pcm := bytes.Repeat([]byte{byte(p.tts.Load())}, size*2+2)
				if c.WriteMessage(websocket.BinaryMessage, pcm[:size+1]) != nil || c.WriteMessage(websocket.BinaryMessage, pcm[size+1:]) != nil {
					return
				}
				if emit("task-finished", nil) != nil {
					return
				}
			} else {
				t.Error("unexpected speech task")
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, p
}

func enableVoiceFixture(t *testing.T, f testFixture, runtime voice.Runtime) (*httptest.Server, *voiceTestProvider) {
	t.Helper()
	return enableVoiceFixtureWithChats(t, f, runtime, nil)
}

func enableVoiceFixtureWithChats(t *testing.T, f testFixture, runtime voice.Runtime, chats voice.ChatReader) (*httptest.Server, *voiceTestProvider) {
	t.Helper()
	provider, p := voiceProvider(t)
	root := f.cfg.Paths.RegistriesDir
	files := map[string]string{
		"providers/voice.yml":  "key: voice\nbaseUrl: " + provider.URL + "\napiKey: test-voice-key\n",
		"models/voice-asr.yml": "key: voice-asr\nprovider: voice\ntype: asr\nprotocol: DASHSCOPE_ASR\nmodelId: configured-asr\n",
		"models/voice-tts.yml": "key: voice-tts\nprovider: voice\ntype: tts\nprotocol: DASHSCOPE_REALTIME_TTS\nmodelId: configured-tts\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.modelRegistry.ReloadProviders(); err != nil {
		t.Fatal(err)
	}
	if err := f.modelRegistry.ReloadModels(); err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		runtime = f.server.deps.Runtime
	}
	if chats == nil {
		reader, ok := f.chats.(voice.ChatReader)
		if !ok {
			t.Fatal("fixture has no primary reader")
		}
		chats = reader
	}
	cfg := config.VoiceConfig{Enabled: true, ASRModel: "voice-asr", TTSModel: "voice-tts", DefaultVoice: "configured", Voices: []string{"configured"}, MaxSentenceSilenceMS: 600, DownlinkLeadMS: 300}
	service := voice.NewService(context.Background(), voice.Dependencies{Config: cfg, Models: f.modelRegistry, Runtime: runtime, Chats: chats, Agents: f.registry, Runs: f.runs})
	f.server.deps.Voice = service
	srv := httptest.NewServer(f.server)
	t.Cleanup(func() { service.Close(); srv.Close() })
	return srv, p
}

func voiceHello(chatID string, rate int) map[string]any {
	hello := map[string]any{"type": "hello", "version": 1, "agentKey": "mock-agent", "audio": map[string]any{"input": map[string]any{"codec": "pcm_s16le", "sampleRate": 16000, "channels": 1, "frameMs": 20}, "output": map[string]any{"codec": "pcm_s16le", "sampleRate": rate}}}
	if chatID != "" {
		hello["chatId"] = chatID
	}
	return hello
}

func dialVoice(t *testing.T, srv *httptest.Server, chatID string, rate int) (*websocket.Conn, string) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/voice/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.WriteJSON(voiceHello(chatID, rate)); err != nil {
		t.Fatal(err)
	}
	frame, _ := readVoice(t, c)
	if frame["type"] != "hello" {
		t.Fatalf("hello failed: %#v", frame)
	}
	return c, frame["chatId"].(string)
}

func readVoice(t *testing.T, c *websocket.Conn) (map[string]any, []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	kind, data, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind == websocket.BinaryMessage {
		return nil, data
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatal(err)
	}
	return frame, nil
}

func speakVoice(t *testing.T, c *websocket.Conn) {
	t.Helper()
	for _, msg := range []map[string]any{{"type": "listen", "state": "start", "mode": "manual"}, {"type": "listen", "state": "start", "mode": "manual"}} {
		if c.WriteJSON(msg) != nil {
			t.Fatal("listen write")
		}
	}
	if c.WriteMessage(websocket.BinaryMessage, make([]byte, 640)) != nil || c.WriteMessage(websocket.BinaryMessage, []byte{1, 2}) != nil {
		t.Fatal("PCM write")
	}
	if c.WriteJSON(map[string]any{"type": "listen", "state": "stop", "mode": "manual"}) != nil {
		t.Fatal("stop write")
	}
	if c.WriteJSON(map[string]any{"type": "listen", "state": "stop", "mode": "manual"}) != nil {
		t.Fatal("duplicate stop write")
	}
}

func voiceUntil(t *testing.T, c *websocket.Conn, kind, state string) (map[string]any, []int) {
	t.Helper()
	var frames []int
	for {
		msg, pcm := readVoice(t, c)
		if pcm != nil {
			frames = append(frames, len(pcm))
			continue
		}
		if msg["type"] == "error" {
			t.Fatalf("voice error: %#v", msg)
		}
		if msg["type"] == kind && (state == "" || msg["state"] == state) {
			return msg, frames
		}
	}
}

func queryLines(t *testing.T, f testFixture, chatID string) []map[string]any {
	t.Helper()
	file, err := os.Open(filepath.Join(f.cfg.Paths.ChatsDir, chatID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []map[string]any
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var row map[string]any
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row["_type"] == "query" {
			lines = append(lines, row)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestVoiceFullAnswerPrimaryDefaultsAndPCMTail(t *testing.T) {
	body := strings.Repeat("这是最后模型回合的完整正文。", 40)
	var calls atomic.Int32
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"中间过程不能朗读","tool_calls":[{"index":0,"id":"clock","type":"function","function":{"name":"datetime","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, "[DONE]")
		} else {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": body}, "finish_reason": "stop"}}})
			writeProviderSSE(t, w, string(data), "[DONE]")
		}
	})
	srv, p := enableVoiceFixture(t, f, nil)
	c, chatID := dialVoice(t, srv, "", 24000)
	if summary, err := f.chats.Summary(chatID); err != nil || summary != nil {
		t.Fatal("hello created a Chat")
	}
	speakVoice(t, c)
	start, _ := voiceUntil(t, c, "tts", "start")
	if start["text"] != body {
		t.Fatalf("did not read only full final answer: %q", start["text"])
	}
	stop, frames := voiceUntil(t, c, "tts", "stop")
	if stop["reason"] != "done" || fmt.Sprint(frames) != "[960 960 2]" {
		t.Fatalf("PCM frames %v stop=%v", frames, stop)
	}
	if p.asr.Load() != 1 || p.tts.Load() != 1 || p.pcmBytes.Load() != 642 || calls.Load() != 2 {
		t.Fatalf("duplicate/lost work: asr=%d tts=%d PCM=%d model=%d", p.asr.Load(), p.tts.Load(), p.pcmBytes.Load(), calls.Load())
	}
	primary, _ := f.chats.(chat.PrimaryStore).PrimaryChatID("mock-agent")
	if primary != chatID {
		t.Fatal("first Query did not publish primary")
	}
	rows := queryLines(t, f, chatID)
	if len(rows) != 1 {
		t.Fatalf("query count: %d", len(rows))
	}
	query, ok := rows[0]["query"].(map[string]any)
	if !ok || query["message"] != p.transcript.Load().(string) || query["planningMode"] != false || query["accessLevel"] != nil || query["editingMode"] != nil || query["model"] != nil || query["references"] != nil {
		t.Fatalf("voice overrode ordinary Query: %#v", rows[0])
	}
	if rows[0]["planningMode"] == true || rows[0]["editingMode"] != true || rows[0]["accessLevel"] != "ask_for_approval" {
		t.Fatalf("Agent defaults lost: planning=%v editing=%v access=%v", rows[0]["planningMode"], rows[0]["editingMode"], rows[0]["accessLevel"])
	}
	// The next hello reuses the Chat and does not replay the previous answer.
	c.Close()
	c2, reused := dialVoice(t, srv, "", 16000)
	if reused != chatID {
		t.Fatal("did not reuse primary")
	}
	_ = c2.WriteJSON(map[string]any{"type": "abort"})
	voiceUntil(t, c2, "abort", "")
	if len(queryLines(t, f, chatID)) != 1 || p.tts.Load() != 1 {
		t.Fatal("reconnect replayed the old turn")
	}
}

type voiceDelayedRuntime struct {
	voice.Runtime
	gate, entered chan struct{}
	calls         atomic.Int32
	interrupts    atomic.Int32
}

func (r *voiceDelayedRuntime) ExecuteQueryWithHooks(ctx context.Context, cmd rt.QueryCommand, hooks rt.QueryHooks) (rt.QueryResult, error) {
	delay := r.calls.Add(1) == 1
	return r.Runtime.ExecuteQueryWithHooks(ctx, cmd, rt.QueryHooks{OnRunStarted: func(start chat.RunStart) {
		if delay {
			close(r.entered)
			<-r.gate
		}
		if hooks.OnRunStarted != nil {
			hooks.OnRunStarted(start)
		}
	}})
}
func (r *voiceDelayedRuntime) Interrupt(ctx context.Context, cmd rt.InterruptCommand) (rt.InterruptResult, error) {
	r.interrupts.Add(1)
	return r.Runtime.Interrupt(ctx, cmd)
}

type voiceCancelEngine struct {
	calls atomic.Int32
	first *gatedQueryEngine
}

func (e *voiceCancelEngine) Stream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	if e.calls.Add(1) == 1 {
		return e.first.Stream(ctx, req, session)
	}
	return &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "新问题的回答。"}}}, nil
}

func TestVoiceAbortLateIdentityAndPendingTranscript(t *testing.T) {
	f := newTestFixture(t)
	engine := &voiceCancelEngine{first: &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	runtime := &voiceDelayedRuntime{Runtime: f.server.deps.Runtime, gate: make(chan struct{}), entered: make(chan struct{})}
	srv, p := enableVoiceFixture(t, f, runtime)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(runtime.gate) }) })
	c, chatID := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "stt", "")
	select {
	case <-runtime.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not reach identity hook")
	}
	for i := 0; i < 2; i++ {
		_ = c.WriteJSON(map[string]any{"type": "abort"})
		voiceUntil(t, c, "abort", "")
	}
	speakVoice(t, c)
	for {
		msg, _ := readVoice(t, c)
		if msg["type"] == "stt" && msg["final"] == true && msg["turn"] == float64(2) {
			break
		}
		if msg["type"] == "error" {
			t.Fatal(msg)
		}
	}
	release.Do(func() { close(runtime.gate) })
	start, _ := voiceUntil(t, c, "run", "start")
	if start["turn"] != float64(2) {
		t.Fatalf("old Run crossed abort: %#v", start)
	}
	voiceUntil(t, c, "tts", "stop")
	if runtime.calls.Load() != 2 || runtime.interrupts.Load() != 1 || p.tts.Load() != 1 || len(queryLines(t, f, chatID)) != 2 {
		t.Fatalf("duplicate work: queries=%d interrupts=%d tts=%d", runtime.calls.Load(), runtime.interrupts.Load(), p.tts.Load())
	}
}

func TestVoiceDisconnectRetainsAcceptedRun(t *testing.T) {
	f := newTestFixture(t)
	engine := &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	srv, p := enableVoiceFixture(t, f, nil)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(engine.release) }) })
	c, chatID := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "run", "start")
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Run not executing")
	}
	c.Close()
	active, found, err := f.runs.ActiveRunForChat(chatID)
	if err != nil || !found {
		t.Fatal("disconnect cancelled accepted Run")
	}
	release.Do(func() { close(engine.release) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		summary, _ := f.chats.Summary(chatID)
		if summary != nil && summary.LastRunID == active.RunID && summary.LastRunContent == "before after" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached Run result not retained")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.tts.Load() != 0 {
		t.Fatal("disconnected Run synthesized audio")
	}
	c2, id := dialVoice(t, srv, "", 16000)
	if id != chatID {
		t.Fatal("reconnect lost Chat")
	}
	_ = c2.WriteJSON(map[string]any{"type": "abort"})
	voiceUntil(t, c2, "abort", "")
	if len(queryLines(t, f, chatID)) != 1 {
		t.Fatal("reconnect resubmitted Query")
	}
}

func TestVoiceAwaitingHintWaitsForOrdinaryUISubmit(t *testing.T) {
	var calls atomic.Int32
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			frame := providerToolCallsFrame(t, []providerToolCallSpec{{ID: "voice-question", Name: "ask_user_question", Args: map[string]any{"mode": "question", "questions": []any{map[string]any{"question": "如何继续？", "type": "text"}}}}})
			writeProviderSSE(t, w, frame, "[DONE]")
		} else {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"根据界面提交继续完成。"},"finish_reason":"stop"}]}`, "[DONE]")
		}
	})
	srv, p := enableVoiceFixture(t, f, nil)
	c, chatID := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	awaiting, _ := voiceUntil(t, c, "awaiting", "")
	if awaiting["mode"] != "question" {
		t.Fatal(awaiting)
	}
	voiceUntil(t, c, "tts", "stop")
	// Even after the prompt drained, neither a repeated listen nor a voice
	// "submit" can answer the existing question.
	_ = c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "manual"})
	_ = c.WriteJSON(map[string]any{"type": "submit"})
	for _, code := range []string{"turn_busy", "invalid_request"} {
		msg, _ := readVoice(t, c)
		if msg["type"] != "error" || msg["code"] != code {
			t.Fatal(msg)
		}
	}
	if p.asr.Load() != 1 || calls.Load() != 1 {
		t.Fatal("hint stop resumed listening or answered question")
	}
	body, _ := json.Marshal(map[string]any{"agentKey": "mock-agent", "runId": awaiting["runId"], "awaitingId": awaiting["awaitingId"], "params": []any{map[string]any{"id": "q1", "answer": "继续"}}})
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ordinary Submit rejected: %d %s", rec.Code, rec.Body.String())
	}
	start, _ := voiceUntil(t, c, "tts", "start")
	if start["text"] != "根据界面提交继续完成。" {
		t.Fatal(start)
	}
	voiceUntil(t, c, "tts", "stop")
	if p.tts.Load() != 2 || calls.Load() != 2 || len(queryLines(t, f, chatID)) != 1 {
		t.Fatal("HITL continuation duplicated Query or lost final answer")
	}
}

type voiceCompletionFailureStore struct{ chat.Store }

func (s voiceCompletionFailureStore) OnRunCompleted(chat.RunCompletion) error {
	return errors.New("injected completion failure")
}

func TestVoiceDoesNotReadUnpersistedCompletion(t *testing.T) {
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"没有保存成功的正文"},"finish_reason":"stop"}]}`, "[DONE]")
	})
	if _, _, err := f.chats.EnsureChat("voice-unsaved", "mock-agent", "existing"); err != nil {
		t.Fatal(err)
	}
	f.server.deps.Chats = voiceCompletionFailureStore{f.chats}
	bindTestRuntime(f.server)
	srv, p := enableVoiceFixture(t, f, nil)
	c, _ := dialVoice(t, srv, "voice-unsaved", 16000)
	speakVoice(t, c)
	for {
		msg, pcm := readVoice(t, c)
		if len(pcm) > 0 || msg["type"] == "tts" {
			t.Fatal("unpersisted completion was spoken")
		}
		if msg["type"] == "error" {
			if msg["code"] != "run_failed" {
				t.Fatal(msg)
			}
			break
		}
	}
	if p.tts.Load() != 0 {
		t.Fatal("unpersisted completion invoked supplier TTS")
	}
}

func TestVoiceDoesNotPreemptOtherChatRun(t *testing.T) {
	f := newTestFixture(t)
	engine := &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	srv, p := enableVoiceFixture(t, f, nil)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(engine.release) }) })
	handle, err := f.server.deps.Runtime.StartQuery(context.Background(), rt.QueryCommand{AgentKey: "mock-agent", ChatID: "voice-busy", Message: "other source"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("other Run did not enter")
	}
	c, _ := dialVoice(t, srv, "voice-busy", 16000)
	speakVoice(t, c)
	for {
		msg, pcm := readVoice(t, c)
		if len(pcm) > 0 || msg["type"] == "tts" || msg["type"] == "run" {
			t.Fatal("voice executed in occupied Chat")
		}
		if msg["type"] == "error" {
			if msg["code"] != "chat_busy" {
				t.Fatal(msg)
			}
			break
		}
	}
	active, found, err := f.runs.ActiveRunForChat("voice-busy")
	if err != nil || !found || active.RunID != handle.RunID || p.tts.Load() != 0 || len(queryLines(t, f, "voice-busy")) != 1 {
		t.Fatal("voice preempted or duplicated another source's Run")
	}
}

func TestVoiceAuthCapabilitiesOriginAndHelloValidation(t *testing.T) {
	var key *rsa.PrivateKey
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid hello invoked Agent") }, testFixtureOptions{setupRuntime: func(root string, cfg *config.Config) {
		var public string
		key, public = writeTestJWTKeyPair(t, root)
		cfg.Auth = config.AuthConfig{Enabled: true, LocalPublicKeyFile: public, Issuer: "voice-test", RequireExpiry: true}
	}})
	srv, p := enableVoiceFixture(t, f, nil)
	token := mustSignRS256JWT(t, key, map[string]any{"sub": "voice-user", "iss": "voice-test", "exp": time.Now().Add(time.Hour).Unix()})
	for _, path := range []string{"/api/voice/capabilities", "/api/voice/ws"} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthed voice route %s: %d", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/voice/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	var capability api.ApiResponse[map[string]any]
	if json.Unmarshal(rec.Body.Bytes(), &capability) != nil || capability.Code != 0 || capability.Data["enabled"] != true || p.asr.Load() != 0 || p.tts.Load() != 0 {
		t.Fatal("capabilities did not use local state only")
	}
	dialer := websocket.Dialer{Subprotocols: []string{"bearer." + token}}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/voice/ws"
	if c, res, err := dialer.Dial(wsURL, http.Header{"Origin": []string{"https://untrusted.example"}}); err == nil {
		c.Close()
		t.Fatal("cross-origin voice accepted")
	} else if res == nil || res.StatusCode != http.StatusForbidden {
		t.Fatal("wrong origin rejection")
	}
	if c, res, err := websocket.DefaultDialer.Dial(wsURL+"?token="+url.QueryEscape(token), nil); err == nil {
		c.Close()
		t.Fatal("URL credential accepted")
	} else if res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatal("wrong URL credential rejection")
	}
	for _, modify := range []func(map[string]any){
		func(msg map[string]any) { msg["version"] = 2 },
		func(msg map[string]any) { msg["agentKey"] = "missing" },
		func(msg map[string]any) { msg["chatId"] = "missing" },
		func(msg map[string]any) { msg["voice"] = "not-allowed" },
		func(msg map[string]any) { msg["accessLevel"] = "full_access" },
	} {
		c, _, err := dialer.Dial(wsURL, http.Header{"Origin": []string{srv.URL}})
		if err != nil {
			t.Fatal("valid WS auth rejected")
		}
		msg := voiceHello("", 16000)
		modify(msg)
		_ = c.WriteJSON(msg)
		frame, _ := readVoice(t, c)
		c.Close()
		if frame["type"] != "error" || frame["code"] != "invalid_request" {
			t.Fatal(frame)
		}
	}
	if p.asr.Load() != 0 || p.tts.Load() != 0 {
		t.Fatal("invalid hello contacted speech provider")
	}
	// Existing cookie authentication uses the same principal and Origin gate.
	c, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": []string{"access_token=" + token}, "Origin": []string{srv.URL}})
	if err != nil {
		t.Fatal("cookie WS auth rejected")
	}
	_ = c.WriteJSON(voiceHello("", 16000))
	frame, _ := readVoice(t, c)
	c.Close()
	if frame["type"] != "hello" {
		t.Fatal(frame)
	}
}

func TestVoiceServiceShutdownReleasesItsWorkers(t *testing.T) {
	f := newTestFixture(t)
	engine := &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	srv, p := enableVoiceFixture(t, f, nil)
	c, chatID := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "run", "start")
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Run not started")
	}
	done := make(chan struct{})
	go func() { f.server.deps.Voice.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("voice shutdown left workers running")
	}
	if _, found, err := f.runs.ActiveRunForChat(chatID); err != nil || found || p.tts.Load() != 0 {
		t.Fatal("shutdown left an owned Run or synthesized a cancelled answer")
	}
}

func TestVoiceAfterRestartDoesNotResumeInterruptedRunOrReplayMedia(t *testing.T) {
	const chatID, oldRunID, awaitingID = "voice-restart-chat", "voice-restart-old", "voice-restart-await"
	const answer = "重启后的新问题已完成。"
	var calls atomic.Int32
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		writeProviderSSE(t, w, string(data), "[DONE]")
	})
	// Reuse the ordinary startup fixture: persisted, unfinished Run state, with
	// no live process/observer to restore. Do not introduce voice recovery state.
	seedDeferredAwaitingPayloadAndContent(t, f.chats, chatID, oldRunID, awaitingID, "question", 600,
		time.Now().UnixMilli()-2000, nil, []chat.ContentPart{{Type: "text", Text: "重启前已保存的部分正文。"}})
	f.runs = runstate.NewManager()
	var err error
	f.server, err = newRuntimeServer(deferredRestartDependencies(f, f.runs, f.chats, nil))
	if err != nil {
		t.Fatal(err)
	}
	assertRestartTerminalizedAwaiting(t, f.chats, chatID, oldRunID, awaitingID, "runtime_restarted")
	assertAwaitingSubmitConflict(t, f.server, chatID, oldRunID, awaitingID, http.StatusConflict, "awaiting_interrupted", "interrupted")
	old, err := f.chats.ListRuns(chatID)
	if err != nil || len(old) != 1 || old[0].FinishReason != "cancel" || old[0].AssistantText != "重启前已保存的部分正文。" {
		t.Fatalf("ordinary restart lost terminal state or saved text: %+v %v", old, err)
	}
	before, err := f.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatal(err)
	}
	srv, p := enableVoiceFixture(t, f, nil)
	c, reused := dialVoice(t, srv, chatID, 16000)
	if reused != chatID {
		t.Fatal("new hello changed the persisted Chat")
	}
	_ = c.WriteJSON(map[string]any{"type": "abort"})
	_, pcm := voiceUntil(t, c, "abort", "")
	after, err := f.chats.LoadJSONLContent(chatID)
	if err != nil || before != after || len(pcm) != 0 || calls.Load() != 0 || p.asr.Load() != 0 || p.tts.Load() != 0 {
		t.Fatal("new hello/abort resumed historical execution or media")
	}
	if _, found := f.runs.RunStatus(oldRunID); found {
		t.Fatal("new process registered the interrupted historical Run")
	}
	speakVoice(t, c)
	start, _ := voiceUntil(t, c, "run", "start")
	stop, _ := voiceUntil(t, c, "tts", "stop")
	if start["runId"] == oldRunID || stop["reason"] != "done" || calls.Load() != 1 || p.asr.Load() != 1 || p.tts.Load() != 1 {
		t.Fatalf("fresh Query did not complete exactly once: start=%v stop=%v calls=%d", start, stop, calls.Load())
	}
	select {
	case text := <-p.texts:
		if text != answer {
			t.Fatalf("read historical text instead of the new final answer: %q", text)
		}
	default:
		t.Fatal("new final answer was not synthesized")
	}
	if rows := queryLines(t, f, chatID); len(rows) != 1 || rows[0]["runId"] != start["runId"] {
		t.Fatalf("startup or hello duplicated a Query: %v", rows)
	}
}

func TestVoicePrimaryChangeBeforeFirstQueryDoesNotCreateChat(t *testing.T) {
	f := newTestFixture(t)
	srv, _ := enableVoiceFixture(t, f, nil)
	c, reserved := dialVoice(t, srv, "", 16000)
	if response := postPrimaryQuery(t, f.server, map[string]any{"agentKey": "mock-agent", "chatId": "chosen-after-hello", "message": "chosen", "primary": true}); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	speakVoice(t, c)
	for {
		msg, _ := readVoice(t, c)
		if msg["type"] == "error" {
			if msg["code"] != "chat_changed" {
				t.Fatal(msg)
			}
			break
		}
	}
	if summary, err := f.chats.Summary(reserved); err != nil || summary != nil {
		t.Fatal("stale reservation created a Chat")
	}
	if primary := agentPrimaryChatID(t, f.server, "mock-agent"); primary != "chosen-after-hello" {
		t.Fatal("voice replaced selected primary")
	}
}

// Block only the first server binary write, after HTTP/hello have completed.
// This exercises the actual writer ordering without a production socket shim.
type voiceWriteGate struct {
	entered, release chan struct{}
	once             sync.Once
}
type voiceGatedConn struct {
	net.Conn
	gate *voiceWriteGate
}

func (c voiceGatedConn) Write(p []byte) (int, error) {
	if len(p) > 0 && p[0]&0x0f == websocket.BinaryMessage {
		c.gate.once.Do(func() { close(c.gate.entered); <-c.gate.release })
	}
	return c.Conn.Write(p)
}

type voiceGatedResponse struct {
	http.ResponseWriter
	gate *voiceWriteGate
}

func (w voiceGatedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return voiceGatedConn{c, w.gate}, rw, nil
}

func TestVoiceAbortDuringWriteHasNoOldPCMAfterConfirmation(t *testing.T) {
	f := newTestFixture(t)
	_, p := enableVoiceFixture(t, f, nil)
	gate := &voiceWriteGate{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.server.ServeHTTP(voiceGatedResponse{w, gate}, r) }))
	defer srv.Close()
	c, _ := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "tts", "start")
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("binary write did not enter gate")
	}
	_ = c.WriteJSON(map[string]any{"type": "abort"})
	release.Do(func() { close(gate.release) })
	voiceUntil(t, c, "abort", "")
	speakVoice(t, c)
	var frames int
	for {
		msg, pcm := readVoice(t, c)
		if len(pcm) > 0 {
			frames++
			for _, b := range pcm {
				if b != 2 {
					t.Fatal("old PCM crossed abort confirmation")
				}
			}
			continue
		}
		if msg["type"] == "error" {
			t.Fatal(msg)
		}
		if msg["type"] == "tts" && msg["state"] == "stop" {
			break
		}
	}
	if p.tts.Load() != 2 || frames != 3 {
		t.Fatal("lost new audio after abort")
	}
}

func TestVoicePendingTranscriptExpiresWithoutSubmitting(t *testing.T) {
	f := newTestFixture(t)
	engine := &voiceCancelEngine{first: &gatedQueryEngine{entered: make(chan struct{}), release: make(chan struct{})}}
	f.server.deps.Agent = engine
	bindTestRuntime(f.server)
	runtime := &voiceDelayedRuntime{Runtime: f.server.deps.Runtime, gate: make(chan struct{}), entered: make(chan struct{})}
	srv, p := enableVoiceFixture(t, f, runtime)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(runtime.gate) }) })
	c, _ := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "stt", "")
	select {
	case <-runtime.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not enter identity gate")
	}
	_ = c.WriteJSON(map[string]any{"type": "abort"})
	voiceUntil(t, c, "abort", "")
	speakVoice(t, c)
	for {
		msg, _ := readVoice(t, c)
		if msg["type"] == "stt" && msg["final"] == true && msg["turn"] == float64(2) {
			break
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(12 * time.Second))
	var frame map[string]any
	if err := c.ReadJSON(&frame); err != nil {
		t.Fatal(err)
	}
	if frame["type"] != "error" || frame["code"] != "run_busy_timeout" || runtime.calls.Load() != 1 || p.tts.Load() != 0 {
		t.Fatalf("pending transcript escaped its bound: %v", frame)
	}
	release.Do(func() { close(runtime.gate) })
}

func TestVoiceInputFailuresNeverCreateQuery(t *testing.T) {
	for _, scenario := range []string{"empty", "odd PCM", "large PCM", "text limit", "startup overflow"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); t.Error("invalid input invoked Agent") })
			srv, p := enableVoiceFixture(t, f, nil)
			if scenario == "empty" {
				p.transcript.Store("")
			}
			if scenario == "text limit" {
				p.transcript.Store(strings.Repeat("长", 4001))
			}
			if scenario == "startup overflow" {
				p.holdStart.Store(true)
			}
			c, chatID := dialVoice(t, srv, "", 16000)
			if scenario == "empty" || scenario == "text limit" {
				speakVoice(t, c)
			} else if scenario == "startup overflow" {
				_ = c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "manual"})
				for i := 0; i < 101; i++ {
					_ = c.WriteMessage(websocket.BinaryMessage, make([]byte, 640))
				}
			} else {
				_ = c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "manual"})
				n := 1
				if scenario == "large PCM" {
					n = 642
				}
				_ = c.WriteMessage(websocket.BinaryMessage, make([]byte, n))
			}
			for {
				msg, _ := readVoice(t, c)
				if msg["type"] == "error" {
					want := "invalid_audio"
					if scenario == "empty" {
						want = "no_speech"
					}
					if scenario == "text limit" {
						want = "transcript_limit"
					}
					if scenario == "startup overflow" {
						want = "audio_overflow"
					}
					if msg["code"] != want {
						t.Fatal(msg)
					}
					break
				}
			}
			if summary, err := f.chats.Summary(chatID); err != nil || summary != nil || calls.Load() != 0 || p.tts.Load() != 0 {
				t.Fatal("invalid input created Chat/Query/TTS")
			}
		})
	}
}

func TestVoiceAutoStopsOnSentenceFinalAndKeepsAcceptedTail(t *testing.T) {
	f := newTestFixture(t)
	srv, p := enableVoiceFixture(t, f, nil)
	p.autoFinal.Store(true)
	c, chatID := dialVoice(t, srv, "", 16000)
	_ = c.WriteJSON(map[string]any{"type": "listen", "state": "start", "mode": "auto"})
	_ = c.WriteMessage(websocket.BinaryMessage, make([]byte, 640))
	_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
	voiceUntil(t, c, "tts", "stop")
	if p.pcmBytes.Load() != 642 || len(queryLines(t, f, chatID)) != 1 || p.asr.Load() != 1 || p.tts.Load() != 1 {
		t.Fatal("auto final lost accepted audio or duplicated Query")
	}
}

// voiceLeftoverAwaitingChats reports a pending item that outlives the Run.
type voiceLeftoverAwaitingChats struct{ voice.ChatReader }

func (c voiceLeftoverAwaitingChats) Summary(chatID string) (*chat.Summary, error) {
	summary, err := c.ChatReader.Summary(chatID)
	if summary == nil || err != nil {
		return summary, err
	}
	copied := *summary
	copied.PendingAwaiting = &chat.PendingAwaiting{AwaitingID: "left-over", RunID: summary.LastRunID, Mode: "question"}
	return &copied, nil
}

func TestVoiceCompletedRunWithLeftoverAwaitingEndsTurn(t *testing.T) {
	f := newTestFixture(t)
	reader, ok := f.chats.(voice.ChatReader)
	if !ok {
		t.Fatal("fixture has no primary reader")
	}
	srv, p := enableVoiceFixtureWithChats(t, f, nil, voiceLeftoverAwaitingChats{reader})
	c, _ := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	voiceUntil(t, c, "run", "end")
	msg, _ := readVoice(t, c)
	if msg["type"] != "error" || msg["code"] != "awaiting_pending" {
		t.Fatal(msg)
	}
	// The turn is closed, so the next utterance opens turn 2 instead of being dropped.
	speakVoice(t, c)
	for {
		msg, _ := readVoice(t, c)
		if msg["type"] == "error" {
			t.Fatal(msg)
		}
		if msg["type"] == "stt" && msg["final"] == true {
			if msg["turn"] != float64(2) {
				t.Fatal(msg)
			}
			break
		}
	}
	if p.tts.Load() != 0 {
		t.Fatal("left-over awaiting answer was spoken")
	}
}

func TestVoiceAwaitingHintFailureKeepsTheTurnWaiting(t *testing.T) {
	var calls atomic.Int32
	f := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			frame := providerToolCallsFrame(t, []providerToolCallSpec{{ID: "voice-question", Name: "ask_user_question", Args: map[string]any{"mode": "question", "questions": []any{map[string]any{"question": "如何继续？", "type": "text"}}}}})
			writeProviderSSE(t, w, frame, "[DONE]")
		} else {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"根据界面提交继续完成。"},"finish_reason":"stop"}]}`, "[DONE]")
		}
	})
	srv, p := enableVoiceFixture(t, f, nil)
	p.failTTS.Store(true)
	c, _ := dialVoice(t, srv, "", 16000)
	speakVoice(t, c)
	awaiting, _ := voiceUntil(t, c, "awaiting", "")
	// voiceUntil fails on any error frame: the hint failure must not end the turn.
	if stop, _ := voiceUntil(t, c, "tts", "stop"); stop["reason"] != "error" {
		t.Fatal(stop)
	}
	body, _ := json.Marshal(map[string]any{"agentKey": "mock-agent", "runId": awaiting["runId"], "awaitingId": awaiting["awaitingId"], "params": []any{map[string]any{"id": "q1", "answer": "继续"}}})
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("ordinary Submit rejected: %d %s", rec.Code, rec.Body.String())
	}
	start, _ := voiceUntil(t, c, "tts", "start")
	if start["text"] != "根据界面提交继续完成。" || start["turn"] != awaiting["turn"] {
		t.Fatal(start)
	}
	voiceUntil(t, c, "tts", "stop")
}
