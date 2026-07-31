package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"

	"agent-platform/internal/chat"
	"agent-platform/internal/ws"
)

func TestHTTPChatMapsIncompleteHistoryToConflict(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-http-incomplete-history"
	persistIncompleteChatHistory(t, fixture.chats, chatID)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"msg":"chat_history_incomplete"`) ||
		!strings.Contains(rec.Body.String(), `"code":"chat_history_incomplete"`) {
		t.Fatalf("expected chat_history_incomplete response, got %s", rec.Body.String())
	}
}

func TestWebSocketChatMapsIncompleteHistoryToConflict(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
	})
	const chatID = "chat-ws-incomplete-history"
	persistIncompleteChatHistory(t, fixture.chats, chatID)

	server := httptest.NewServer(fixture.server)
	defer server.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/chat",
		ID:    "req-incomplete-chat",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId": chatID,
		}),
	}); err != nil {
		t.Fatalf("write request: %v", err)
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read connected frame: %v", err)
	}
	var connected ws.PushFrame
	if err := json.Unmarshal(raw, &connected); err != nil || connected.Frame != ws.FramePush {
		t.Fatalf("unexpected connected frame: %s", string(raw))
	}

	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read error frame: %v", err)
	}
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode error frame: %v", err)
	}
	if frame.Frame != ws.FrameError ||
		frame.Type != "chat_history_incomplete" ||
		frame.Code != http.StatusConflict {
		t.Fatalf("unexpected websocket error frame: %s", string(raw))
	}
	data, err := json.Marshal(frame.Data)
	if err != nil || !strings.Contains(string(data), `"code":"chat_history_incomplete"`) {
		t.Fatalf("expected structured chat_history_incomplete data, got %s", string(data))
	}
}

func persistIncompleteChatHistory(t *testing.T, store chat.Store, chatID string) {
	t.Helper()
	runID := chatID + "-run"
	startedAt := time.Now().UnixMilli()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "run it"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if recorder, ok := store.(chat.RunStartRecorder); ok {
		if err := recorder.OnRunStarted(chat.RunStart{
			ChatID:          chatID,
			RunID:           runID,
			AgentKey:        "mock-agent",
			AgentMode:       "REACT",
			InitialMessage:  "run it",
			StartedAtMillis: startedAt,
		}); err != nil {
			t.Fatalf("start run: %v", err)
		}
	}
	messageAt := startedAt + 1
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		Seq:       1,
		UpdatedAt: messageAt,
		Messages: []chat.StoredMessage{{
			Role: "assistant",
			ToolCalls: []chat.StoredToolCall{{
				ID:       "call-unknown",
				Type:     "function",
				Function: chat.StoredFunction{Name: "bash", Arguments: `{"command":"touch marker"}`},
			}},
			Ts: &messageAt,
		}},
	}); err != nil {
		t.Fatalf("append incomplete react: %v", err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "mock-agent",
		AgentMode:       "REACT",
		InitialMessage:  "run it",
		FinishReason:    "error",
		StartedAtMillis: startedAt,
		UpdatedAtMillis: startedAt + 2,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
}
