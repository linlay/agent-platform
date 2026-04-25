package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestDeferredSubmitHTTPRestoresPendingAwaitingAfterRestart(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-http", "run-http", "await-http", "question")

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	reqBody := bytes.NewBufferString(`{"runId":"run-http","awaitingId":"await-http","params":[{"id":"q1","answer":"Approve"}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/submit", reqBody)
	req.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !response.Data.Accepted || response.Data.Status != "accepted" {
		t.Fatalf("unexpected submit response %#v", response.Data)
	}

	summary, err := fixture.chats.Summary("chat-http")
	if err != nil {
		t.Fatalf("load summary after submit: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting to be cleared, got %#v", summary)
	}

	detail, err := fixture.chats.LoadChat("chat-http")
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	foundSubmit := false
	foundAnswer := false
	for _, event := range detail.Events {
		switch event.Type {
		case "request.submit":
			foundSubmit = true
		case "awaiting.answer":
			foundAnswer = true
			if event.String("awaitingId") != "await-http" || event.String("status") != "answered" {
				t.Fatalf("unexpected awaiting.answer %#v", event)
			}
		}
	}
	if !foundSubmit || !foundAnswer {
		t.Fatalf("expected submit replay in chat detail, got %#v", detail.Events)
	}
	if eventTypes := notifications.EventTypes(); len(eventTypes) == 0 || eventTypes[len(eventTypes)-1] != "awaiting.answer" {
		t.Fatalf("expected awaiting.answer notification, got %#v", eventTypes)
	}
}

func TestDeferredSubmitWSRestoresPendingAwaitingAfterRestart(t *testing.T) {
	hub := ws.NewHub()
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: hub,
		configure: func(cfg *config.Config) {
			cfg.WebSocket.Enabled = true
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-ws", "run-ws", "await-ws", "question")

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   hub,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	server := httptest.NewServer(restarted)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/submit",
		ID:    "req_submit_deferred",
		Payload: ws.MarshalPayload(map[string]any{
			"runId":      "run-ws",
			"awaitingId": "await-ws",
			"params": []map[string]any{
				{"id": "q1", "answer": "Approve"},
			},
		}),
	}); err != nil {
		t.Fatalf("write websocket submit request: %v", err)
	}

	var gotResponse bool
	var gotPush bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (!gotResponse || !gotPush) {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket frame: %v", err)
		}

		var meta struct {
			Frame string `json:"frame"`
			Type  string `json:"type"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode websocket frame metadata: %v", err)
		}

		switch meta.Frame {
		case ws.FrameResponse:
			var frame ws.ResponseFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode response frame: %v", err)
			}
			if frame.ID != "req_submit_deferred" {
				continue
			}
			gotResponse = true
			if frame.Code != 0 || frame.Msg != "success" {
				t.Fatalf("unexpected response frame %#v", frame)
			}
		case ws.FramePush:
			var frame ws.PushFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode push frame: %v", err)
			}
			if frame.Type == "awaiting.answer" {
				gotPush = true
			}
		}
	}
	if !gotResponse || !gotPush {
		t.Fatalf("expected websocket response and awaiting.answer push, got response=%v push=%v", gotResponse, gotPush)
	}

	summary, err := fixture.chats.Summary("chat-ws")
	if err != nil {
		t.Fatalf("load summary after ws submit: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting to be cleared after ws submit, got %#v", summary)
	}
}

func seedDeferredAwaiting(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-runner", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 100,
		Type:      "react",
		Awaiting: []map[string]any{
			{
				"type":       "awaiting.ask",
				"awaitingId": awaitingID,
				"mode":       mode,
				"questions": []any{
					map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("append awaiting step line: %v", err)
	}
	if err := store.SetPendingAwaiting(chatID, chat.PendingAwaiting{
		AwaitingID: awaitingID,
		RunID:      runID,
		Mode:       mode,
		CreatedAt:  100,
	}); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}
}
