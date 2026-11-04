package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"agent-platform/internal/models"
	"github.com/gorilla/websocket"
)

type testCommand struct {
	Header struct {
		Action string `json:"action"`
		ID     string `json:"task_id"`
	} `json:"header"`
	Payload struct {
		Model      string         `json:"model"`
		Parameters map[string]any `json:"parameters"`
		Input      struct {
			Text string `json:"text"`
		} `json:"input"`
	} `json:"payload"`
}

func readCommand(t *testing.T, c *websocket.Conn, action string) testCommand {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var cmd testCommand
	if err := c.ReadJSON(&cmd); err != nil {
		t.Error(err)
		return cmd
	}
	if cmd.Header.Action != action {
		t.Errorf("action %q, want %q", cmd.Header.Action, action)
	}
	return cmd
}

func event(t *testing.T, c *websocket.Conn, id, kind string, sentence map[string]any) {
	t.Helper()
	if err := c.WriteJSON(map[string]any{"header": map[string]any{"event": kind, "task_id": id}, "payload": map[string]any{"output": map[string]any{"sentence": sentence}}}); err != nil {
		t.Error(err)
	}
}

func fakeVoice(t *testing.T, handler func(*websocket.Conn)) models.ProviderDefinition {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/configured/voice" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("endpoint or authentication lost")
		}
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		handler(c)
	}))
	t.Cleanup(s.Close)
	return models.ProviderDefinition{BaseURL: s.URL, APIKey: "test-secret", Protocols: map[string]models.ProtocolDefinition{
		models.ProtocolDashScopeASR:         {EndpointPath: "/configured/voice"},
		models.ProtocolDashScopeRealtimeTTS: {EndpointPath: "/configured/voice"},
	}}
}

func testASR(t *testing.T, p models.ProviderDefinition) *ASRClient {
	t.Helper()
	c, err := NewASRClient(models.ModelDefinition{Type: models.ModelTypeASR, Protocol: models.ProtocolDashScopeASR, ModelID: "configured-asr"}, p, 600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func testTTS(t *testing.T, p models.ProviderDefinition) *TTSClient {
	t.Helper()
	c, err := NewTTSClient(models.ModelDefinition{Type: models.ModelTypeTTS, Protocol: models.ProtocolDashScopeRealtimeTTS, ModelID: "configured-tts"}, p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestVoiceConnectionFailureDiagnostic(t *testing.T) {
	var output bytes.Buffer
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags); log.SetPrefix(prefix) })
	for _, cancelled := range []bool{false, true} {
		output.Reset()
		ctx, cancel := context.WithCancel(context.Background())
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cancelled {
				cancel()
			}
			http.Error(w, "private response body test-secret", http.StatusUnauthorized)
		}))
		client := testASR(t, models.ProviderDefinition{BaseURL: server.URL, APIKey: "test-secret"})
		client.socket.model.Key = "asr-test"
		_, err := client.Recognize(ctx, make(chan []byte), "manual", nil)
		cancel()
		server.Close()
		if cancelled {
			if !errors.Is(err, context.Canceled) || output.Len() != 0 {
				t.Fatalf("cancellation logged as connection failure: %v %s", err, &output)
			}
			continue
		}
		if err == nil || err.Error() != "voice provider websocket connection failed" {
			t.Fatalf("public error changed: %v", err)
		}
		var entry map[string]any
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatalf("expected one structured diagnostic: %v", err)
		}
		if entry["stage"] != "ws_connect" || entry["status"] != float64(401) || entry["modelKey"] != "asr-test" || entry["reason"] != "websocket: bad handshake" {
			t.Fatalf("missing connection context: %v", entry)
		}
		for _, forbidden := range []string{"test-secret", "private response body", server.URL, "Authorization"} {
			if strings.Contains(output.String(), forbidden) {
				t.Fatal("connection diagnostic exposed private data")
			}
		}
	}
	output.Reset()
	socket := &dashScopeSocket{provider: models.ProviderDefinition{APIKey: "raw-config-key"}, endpoint: "wss://voice.test/private"}
	socket.logConnectFailure(errors.New(`raw-config-key bare-login-token Bearer bare-login-token token=other-secret wss://voice.test/private TLS ALPN h2 `+strings.Repeat("长", 1500)), 0, "Bearer bare-login-token")
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "TLS ALPN h2") || utf8.RuneCountInString(reason) > 1025 {
		t.Fatal("diagnostic lost the cause or exceeded its bound")
	}
	for _, forbidden := range []string{"raw-config-key", "bare-login-token", "other-secret", socket.endpoint} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatal("connection diagnostic exposed a credential or endpoint")
		}
	}
}

func TestASRManualTailRevisionsAndConnectionReuse(t *testing.T) {
	var connections atomic.Int32
	p := fakeVoice(t, func(c *websocket.Conn) {
		connections.Add(1)
		for i := 0; i < 2; i++ {
			start := readCommand(t, c, "run-task")
			if start.Payload.Model != "configured-asr" || start.Payload.Parameters["sample_rate"] != float64(16000) || start.Payload.Parameters["max_sentence_silence"] != float64(600) {
				t.Error("wrong ASR parameters")
			}
			id := start.Header.ID
			event(t, c, "previous-task", "task-failed", nil)
			event(t, c, id, "task-started", nil)
			var pcm []byte
			for {
				kind, data, err := c.ReadMessage()
				if err != nil {
					t.Error(err)
					return
				}
				if kind == websocket.BinaryMessage {
					pcm = append(pcm, data...)
					continue
				}
				var finish testCommand
				_ = json.Unmarshal(data, &finish)
				if finish.Header.Action != "finish-task" {
					t.Error("expected finish after PCM")
				}
				break
			}
			if len(pcm) != 642 || pcm[len(pcm)-1] != 9 {
				t.Error("lost accepted tail PCM")
			}
			for _, sentence := range []map[string]any{
				{"begin_time": 0, "text": "第一", "sentence_end": false},
				{"begin_time": 0, "text": "第一句。", "sentence_end": true},
				{"begin_time": 0, "text": "重复旧句", "sentence_end": true},
				{"begin_time": 500, "text": "第二句。", "sentence_end": true},
			} {
				event(t, c, id, "result-generated", sentence)
			}
			event(t, c, id, "task-finished", nil)
		}
	})
	c := testASR(t, p)
	for i := 0; i < 2; i++ {
		input := make(chan []byte, 2)
		input <- make([]byte, 640)
		input <- []byte{8, 9}
		close(input)
		var partials []string
		text, err := c.Recognize(context.Background(), input, "manual", func(text string, _ bool) { partials = append(partials, text) })
		if err != nil || text != "第一句。第二句。" || len(partials) != 3 {
			t.Fatalf("%q %v %v", text, err, partials)
		}
	}
	if connections.Load() != 1 {
		t.Fatal("did not reuse completed ASR connection")
	}
}

func TestASRAutoFinishesAndCollectsInflightTail(t *testing.T) {
	input := make(chan []byte, 2)
	p := fakeVoice(t, func(c *websocket.Conn) {
		start := readCommand(t, c, "run-task")
		id := start.Header.ID
		event(t, c, id, "task-started", nil)
		input <- []byte{1, 2}
		if kind, _, err := c.ReadMessage(); err != nil || kind != websocket.BinaryMessage {
			t.Error("missing PCM")
		}
		event(t, c, id, "result-generated", map[string]any{"begin_time": 0, "text": "一句。", "sentence_end": true})
		readCommand(t, c, "finish-task")
		event(t, c, id, "result-generated", map[string]any{"begin_time": 200, "text": "尾句。", "sentence_end": true})
		event(t, c, id, "task-finished", nil)
	})
	text, err := testASR(t, p).Recognize(context.Background(), input, "auto", nil)
	if err != nil || text != "一句。尾句。" {
		t.Fatalf("%q %v", text, err)
	}
}

func TestASREmptyAndMalformedPCM(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "odd frame"}[invalid], func(t *testing.T) {
			p := fakeVoice(t, func(c *websocket.Conn) {
				start := readCommand(t, c, "run-task")
				event(t, c, start.Header.ID, "task-started", nil)
				if invalid {
					_, _, _ = c.ReadMessage()
					return
				}
				readCommand(t, c, "finish-task")
				event(t, c, start.Header.ID, "task-finished", nil)
			})
			input := make(chan []byte, 1)
			if invalid {
				input <- []byte{1}
			}
			close(input)
			text, err := testASR(t, p).Recognize(context.Background(), input, "manual", nil)
			if text != "" || (err != nil) != invalid {
				t.Fatalf("%q %v", text, err)
			}
		})
	}
}

func TestASRWaitsThroughSilenceUntilCallerStops(t *testing.T) {
	p := fakeVoice(t, func(c *websocket.Conn) {
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		var finish testCommand
		if err := c.ReadJSON(&finish); err != nil {
			t.Error(err)
			return
		}
		if finish.Header.Action != "finish-task" {
			t.Error("missing normal finish")
		}
		event(t, c, start.Header.ID, "result-generated", map[string]any{"begin_time": 0, "text": "稍后才说话。", "sentence_end": true})
		event(t, c, start.Header.ID, "task-finished", nil)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input := make(chan []byte)
	timer := time.AfterFunc(11*time.Second, func() { close(input) })
	defer timer.Stop()
	text, err := testASR(t, p).Recognize(ctx, input, "manual", nil)
	if err != nil || text != "稍后才说话。" {
		t.Fatalf("silence ended a valid task: %q %v", text, err)
	}
}

func TestASRStillBoundsFinalizationWithoutProviderEvents(t *testing.T) {
	p := fakeVoice(t, func(c *websocket.Conn) {
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		readCommand(t, c, "finish-task")
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, _, _ = c.ReadMessage() // Client must close the stalled provider task.
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input := make(chan []byte)
	close(input)
	_, err := testASR(t, p).Recognize(ctx, input, "manual", nil)
	if err == nil || err.Error() != "voice recognition finish timeout" {
		t.Fatalf("unbounded finalization: %v", err)
	}
}

func TestTTSStreamingFormatsAndReuse(t *testing.T) {
	var connections atomic.Int32
	p := fakeVoice(t, func(c *websocket.Conn) {
		connections.Add(1)
		for _, rate := range []int{16000, 24000} {
			start := readCommand(t, c, "run-task")
			id := start.Header.ID
			if start.Payload.Model != "configured-tts" || start.Payload.Parameters["voice"] != "configured-voice" || start.Payload.Parameters["format"] != "pcm" || start.Payload.Parameters["sample_rate"] != float64(rate) {
				t.Error("wrong TTS parameters")
			}
			event(t, c, "old-task", "task-finished", nil)
			event(t, c, id, "task-started", nil)
			text := readCommand(t, c, "continue-task")
			if text.Payload.Input.Text != "完整正文" || text.Header.ID != id {
				t.Error("wrong full text")
			}
			readCommand(t, c, "finish-task")
			for _, chunk := range [][]byte{{1, 2, 3}, {4, 5, 6}} {
				if err := c.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					t.Error(err)
				}
			}
			event(t, c, id, "task-finished", nil)
		}
	})
	c := testTTS(t, p)
	for _, rate := range []int{16000, 24000} {
		var got bytes.Buffer
		calls := 0
		err := c.Synthesize(context.Background(), "完整正文", "configured-voice", rate, func(p []byte) error { calls++; _, err := got.Write(p); return err })
		if err != nil || got.Len() != 6 || calls != 2 {
			t.Fatalf("%d %d %v", got.Len(), calls, err)
		}
	}
	if connections.Load() != 1 {
		t.Fatal("did not reuse completed TTS socket")
	}
}

func TestTTSFailureCancellationAndBackpressure(t *testing.T) {
	for _, mode := range []string{"failed", "cancel", "callback", "unaligned", "start timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p := fakeVoice(t, func(c *websocket.Conn) {
				start := readCommand(t, c, "run-task")
				id := start.Header.ID
				if mode == "start timeout" {
					<-ctx.Done()
					_, _, _ = c.ReadMessage()
					return
				}
				event(t, c, id, "task-started", nil)
				readCommand(t, c, "continue-task")
				readCommand(t, c, "finish-task")
				switch mode {
				case "failed":
					_ = c.WriteJSON(map[string]any{"header": map[string]any{"event": "task-failed", "task_id": id, "error_message": "test-secret Bearer test-secret"}})
				case "cancel":
					cancel()
					_, _, _ = c.ReadMessage()
				case "callback":
					_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
					_, _, _ = c.ReadMessage()
				case "unaligned":
					_ = c.WriteMessage(websocket.BinaryMessage, []byte{1})
					event(t, c, id, "task-finished", nil)
				}
			})
			c := testTTS(t, p)
			err := c.Synthesize(ctx, "正文", "voice", 16000, func([]byte) error {
				if mode == "callback" {
					return errors.New("consumer stopped")
				}
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("unsafe or missing error %v", err)
			}
			c.socket.mu.Lock()
			active := c.socket.conn != nil
			c.socket.mu.Unlock()
			if active {
				t.Fatal("failed task socket retained")
			}
		})
	}
}

func TestASRStartupOverflowAndCancellation(t *testing.T) {
	for _, overflow := range []bool{true, false} {
		t.Run(map[bool]string{true: "overflow", false: "cancel"}[overflow], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := fakeVoice(t, func(c *websocket.Conn) {
				readCommand(t, c, "run-task")
				if !overflow {
					cancel()
				}
				_, _, _ = c.ReadMessage()
			})
			input := make(chan []byte, 101)
			if overflow {
				for i := 0; i < 101; i++ {
					input <- make([]byte, 640)
				}
			}
			_, err := testASR(t, p).Recognize(ctx, input, "manual", nil)
			if err == nil {
				t.Fatal("missing failure")
			}
		})
	}
}

func TestVoiceEndpointAndAuthRejection(t *testing.T) {
	p := models.ProviderDefinition{BaseURL: "https://speech.example", APIKey: "secret"}
	m := models.ModelDefinition{Type: models.ModelTypeASR, Protocol: models.ProtocolDashScopeASR, ModelID: "from-registry"}
	s, err := newDashScopeSocket(m, p, models.ModelTypeASR, models.ProtocolDashScopeASR)
	if err != nil || s.endpoint != "wss://speech.example/api-ws/v1/inference" {
		t.Fatalf("%v %v", s, err)
	}
	for _, base := range []string{"https://secret@host", "file:///tmp/voice", "https://host?key=secret"} {
		p.BaseURL = base
		if _, err := newDashScopeSocket(m, p, models.ModelTypeASR, models.ProtocolDashScopeASR); err == nil {
			t.Error("unsafe URL accepted")
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret Bearer secret", http.StatusUnauthorized)
	}))
	defer srv.Close()
	p.BaseURL = srv.URL
	c := testASR(t, p)
	input := make(chan []byte)
	close(input)
	_, err = c.Recognize(context.Background(), input, "manual", nil)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("credential leak %v", err)
	}
}

func TestReusedSocketClosedWhileIdleReconnectsOnce(t *testing.T) {
	var asrConns, ttsConns atomic.Int32
	asr := fakeVoice(t, func(c *websocket.Conn) {
		asrConns.Add(1)
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		for {
			kind, _, err := c.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			if kind == websocket.TextMessage {
				break // finish-task
			}
		}
		event(t, c, start.Header.ID, "result-generated", map[string]any{"begin_time": 0, "text": "一句。", "sentence_end": true})
		event(t, c, start.Header.ID, "task-finished", nil)
		// Returning closes the socket after the task, like a provider idle timeout.
	})
	tts := fakeVoice(t, func(c *websocket.Conn) {
		ttsConns.Add(1)
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		readCommand(t, c, "continue-task")
		readCommand(t, c, "finish-task")
		_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
		event(t, c, start.Header.ID, "task-finished", nil)
	})
	a, s := testASR(t, asr), testTTS(t, tts)
	for i := 0; i < 2; i++ {
		input := make(chan []byte, 1)
		input <- make([]byte, 640)
		close(input)
		if text, err := a.Recognize(context.Background(), input, "manual", nil); err != nil || text != "一句。" {
			t.Fatalf("ASR turn %d: %q %v", i, text, err)
		}
		if err := s.Synthesize(context.Background(), "正文", "voice", 16000, func([]byte) error { return nil }); err != nil {
			t.Fatalf("TTS turn %d: %v", i, err)
		}
	}
	if asrConns.Load() != 2 || ttsConns.Load() != 2 {
		t.Fatalf("connections asr=%d tts=%d", asrConns.Load(), ttsConns.Load())
	}
}

func TestReusedSocketProviderFailureIsNotRetried(t *testing.T) {
	var connections atomic.Int32
	p := fakeVoice(t, func(c *websocket.Conn) {
		connections.Add(1)
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		readCommand(t, c, "continue-task")
		readCommand(t, c, "finish-task")
		_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
		event(t, c, start.Header.ID, "task-finished", nil)
		again := readCommand(t, c, "run-task")
		event(t, c, again.Header.ID, "task-failed", nil)
		_, _, _ = c.ReadMessage()
	})
	c := testTTS(t, p)
	if err := c.Synthesize(context.Background(), "正文", "voice", 16000, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.Synthesize(context.Background(), "正文", "voice", 16000, func([]byte) error { return nil }); err == nil {
		t.Fatal("provider task failure was hidden")
	}
	if connections.Load() != 1 {
		t.Fatal("live provider failure was retried on a new connection")
	}
}

func TestReusedSilentSocketRetriesWithinStartupBuffer(t *testing.T) {
	var connections atomic.Int32
	p := fakeVoice(t, func(c *websocket.Conn) {
		n := connections.Add(1)
		start := readCommand(t, c, "run-task")
		event(t, c, start.Header.ID, "task-started", nil)
		readCommand(t, c, "continue-task")
		readCommand(t, c, "finish-task")
		_ = c.WriteMessage(websocket.BinaryMessage, []byte{1, 2})
		event(t, c, start.Header.ID, "task-finished", nil)
		if n == 1 {
			// Keep the socket open but never answer, like a dropped NAT mapping.
			readCommand(t, c, "run-task")
			_, _, _ = c.ReadMessage()
		}
	})
	c := testTTS(t, p)
	for i := 0; i < 2; i++ {
		begin := time.Now()
		if err := c.Synthesize(context.Background(), "正文", "voice", 16000, func([]byte) error { return nil }); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		// Waiting out the fresh-socket timeout would overflow the startup buffer.
		if elapsed := time.Since(begin); elapsed >= startupTimeout {
			t.Fatalf("turn %d reused socket did not fail fast: %v", i, elapsed)
		}
	}
	if connections.Load() != 2 {
		t.Fatalf("connections=%d", connections.Load())
	}
}
