package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
