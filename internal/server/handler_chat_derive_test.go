package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type deriveRecordingNotificationSink struct {
	mu     sync.Mutex
	events []string
	data   []map[string]any
}

func (s *deriveRecordingNotificationSink) Broadcast(eventType string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, eventType)
	cloned := make(map[string]any, len(data))
	for key, value := range data {
		cloned[key] = value
	}
	s.data = append(s.data, cloned)
}

func (s *deriveRecordingNotificationSink) snapshot() ([]string, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...), append([]map[string]any(nil), s.data...)
}

func TestHandleChatDeriveCreatesIndependentChatAndBroadcasts(t *testing.T) {
	notifications := &deriveRecordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: notifications})
	seedDeriveServerChat(t, fixture.chats, "chat-http-source", "run-http-1", "hello source", "hello derived")

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/derive", bytes.NewBufferString(`{
		"sourceChatId":"chat-http-source",
		"chatId":"chat-http-derived",
		"chatName":"Derived Chat"
	}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp api.ApiResponse[api.DeriveChatResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ChatID != "chat-http-derived" || resp.Data.ChatName != "Derived Chat" || resp.Data.SourceChatID != "chat-http-source" || resp.Data.SourceRunID != "run-http-1" || resp.Data.CopiedRuns != 1 {
		t.Fatalf("unexpected response: %#v", resp.Data)
	}
	if resp.Data.LastRunID == "" || resp.Data.LastRunID == "run-http-1" {
		t.Fatalf("expected mapped lastRunId, got %#v", resp.Data)
	}
	detail, err := fixture.chats.LoadChat("chat-http-derived")
	if err != nil {
		t.Fatalf("load derived chat: %v", err)
	}
	if len(detail.RawMessages) != 2 || detail.RawMessages[0]["content"] != "hello source" || detail.RawMessages[1]["content"] != "hello derived" {
		t.Fatalf("unexpected derived raw messages: %#v", detail.RawMessages)
	}
	events, data := notifications.snapshot()
	if !reflect.DeepEqual(events, []string{"chat.created", "chat.updated"}) {
		t.Fatalf("unexpected broadcasts: %v", events)
	}
	if data[0]["chatId"] != "chat-http-derived" || data[1]["lastRunId"] != resp.Data.LastRunID {
		t.Fatalf("unexpected broadcast payloads: %#v", data)
	}
}

func TestHandleChatDeriveThenQueryUsesDerivedHistoryOnly(t *testing.T) {
	var (
		mu           sync.Mutex
		providerBody string
	)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		providerBody = string(body)
		mu.Unlock()
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"followed"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{})
	seedDeriveServerChat(t, fixture.chats, "chat-query-source", "run-query-source", "old user", "old assistant")
	sourceBefore, err := fixture.chats.LoadJSONLContent("chat-query-source")
	if err != nil {
		t.Fatalf("load source jsonl: %v", err)
	}

	deriveRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(deriveRec, httptest.NewRequest(http.MethodPost, "/api/chat/derive", bytes.NewBufferString(`{"sourceChatId":"chat-query-source","chatId":"chat-query-derived"}`)))
	if deriveRec.Code != http.StatusOK {
		t.Fatalf("derive status=%d body=%s", deriveRec.Code, deriveRec.Body.String())
	}

	queryRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(queryRec, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"chat-query-derived",
		"agentKey":"mock-agent",
		"message":"follow up",
		"stream":false
	}`)))
	if queryRec.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", queryRec.Code, queryRec.Body.String())
	}
	mu.Lock()
	body := providerBody
	mu.Unlock()
	for _, expected := range []string{"old user", "old assistant", "follow up"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("provider request missing %q: %s", expected, body)
		}
	}
	sourceAfter, err := fixture.chats.LoadJSONLContent("chat-query-source")
	if err != nil {
		t.Fatalf("reload source jsonl: %v", err)
	}
	if sourceAfter != sourceBefore {
		t.Fatalf("source chat changed after querying derived chat")
	}
	derived, err := fixture.chats.LoadJSONLContent("chat-query-derived")
	if err != nil {
		t.Fatalf("load derived jsonl: %v", err)
	}
	if !strings.Contains(derived, "follow up") || strings.Contains(sourceAfter, "follow up") {
		t.Fatalf("query was not isolated to derived chat")
	}
}

func TestHandleChatDeriveErrors(t *testing.T) {
	fixture := newTestFixture(t)
	seedDeriveServerChat(t, fixture.chats, "chat-error-source", "run-error-source", "hello", "done")
	if _, _, err := fixture.chats.EnsureChat("chat-error-target", "mock-agent", "", "target"); err != nil {
		t.Fatalf("ensure target: %v", err)
	}
	if _, _, err := fixture.chats.EnsureChat("chat-error-pending", "mock-agent", "", "pending"); err != nil {
		t.Fatalf("ensure pending: %v", err)
	}
	if err := fixture.chats.SetPendingAwaiting("chat-error-pending", chat.PendingAwaiting{AwaitingID: "await-1", RunID: "run-pending", Mode: "question", CreatedAt: testEpochMillis + 1}); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	seedDeriveServerChat(t, fixture.chats, "chat-error-active", "run-error-active", "active", "done")
	fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: "run-active-live", ChatID: "chat-error-active", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent", "")})

	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "invalid source", body: `{"sourceChatId":"../bad"}`, code: http.StatusBadRequest},
		{name: "missing source", body: `{"sourceChatId":"chat-missing"}`, code: http.StatusNotFound},
		{name: "missing run", body: `{"sourceChatId":"chat-error-source","sourceRunId":"run-missing"}`, code: http.StatusNotFound},
		{name: "target exists", body: `{"sourceChatId":"chat-error-source","chatId":"chat-error-target"}`, code: http.StatusConflict},
		{name: "pending", body: `{"sourceChatId":"chat-error-pending"}`, code: http.StatusConflict},
		{name: "active", body: `{"sourceChatId":"chat-error-active"}`, code: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chat/derive", bytes.NewBufferString(tt.body)))
			if rec.Code != tt.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.code, rec.Body.String())
			}
		})
	}
}

func TestWebSocketChatDeriveRoute(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 8
			cfg.WebSocket.PingInterval = 30000
		},
	})
	seedDeriveServerChat(t, fixture.chats, "chat-ws-source", "run-ws-source", "ws user", "ws assistant")
	server := httptest.NewServer(fixture.server)
	defer server.Close()

	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/chat/derive",
		ID:    "derive_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"sourceChatId": "chat-ws-source",
			"chatId":       "chat-ws-derived",
		}),
	}); err != nil {
		t.Fatalf("write derive frame: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		return json.Unmarshal(data, &meta) == nil && meta.Frame == ws.FrameResponse && meta.ID == "derive_ws"
	})
	var frame ws.ResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode response frame: %v", err)
	}
	if frame.Code != 0 || frame.Type != "/api/chat/derive" {
		t.Fatalf("unexpected frame: %#v", frame)
	}
	var data api.DeriveChatResponse
	dataBytes, err := json.Marshal(frame.Data)
	if err != nil {
		t.Fatalf("marshal response data: %v", err)
	}
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		t.Fatalf("decode response data: %v", err)
	}
	if data.ChatID != "chat-ws-derived" || data.SourceRunID != "run-ws-source" || data.LastRunID == "" {
		t.Fatalf("unexpected derive response: %#v", data)
	}
}

func seedDeriveServerChat(t *testing.T, store chat.Store, chatID string, runID string, userText string, assistantText string) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", userText); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startedAt := testEpochMillis + 1_000
	assistantAt := startedAt + 1
	startServerFixtureRun(t, store, chatID, runID, startedAt)
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		Type:      "query",
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: startedAt,
		Query: map[string]any{
			"chatId":    chatID,
			"runId":     runID,
			"requestId": runID,
			"role":      "user",
			"message":   userText,
			"agentKey":  "mock-agent",
		},
		Messages: []map[string]any{{"role": "user", "content": userText, "ts": startedAt}},
	}); err != nil {
		t.Fatalf("append query: %v", err)
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		Type:      chat.StepLineTypeReact,
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: assistantAt,
		Messages: []chat.StoredMessage{{
			Role:    "assistant",
			Content: []chat.ContentPart{{Type: "text", Text: assistantText}},
			Ts:      &assistantAt,
		}},
	}); err != nil {
		t.Fatalf("append step: %v", err)
	}
	if err := store.OnRunCompleted(chat.RunCompletion{
		ChatID:          chatID,
		RunID:           runID,
		AgentKey:        "mock-agent",
		InitialMessage:  userText,
		AssistantText:   assistantText,
		FinishReason:    "complete",
		StartedAtMillis: startedAt,
		UpdatedAtMillis: assistantAt + 1,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
}
