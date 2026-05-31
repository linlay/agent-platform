package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/llm"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestDeferredSubmitHTTPRestoresPendingAwaitingAfterRestart(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-http", "run-http", "await-http", "question", 0, time.Now().UnixMilli())

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	reqBody := bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"run-http","awaitingId":"await-http","params":[{"id":"q1","answer":"Approve"}]}`)
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
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingIntervalMs = 30000
		},
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-ws", "run-ws", "await-ws", "question", 0, time.Now().UnixMilli())

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
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
			"agentKey":   "mock-agent",
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

func TestDeferredSubmitRestoresAllAwaitingModesAfterRestart(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	nowMs := time.Now().UnixMilli()
	cases := []struct {
		name       string
		mode       string
		awaitingID string
		ask        map[string]any
		params     api.SubmitParams
	}{
		{
			name:       "question",
			mode:       "question",
			awaitingID: "await-question",
			ask: map[string]any{
				"questions": []any{
					map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
				},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "q1", "answer": "Approve"},
			}),
		},
		{
			name:       "approval",
			mode:       "approval",
			awaitingID: "await-approval",
			ask: map[string]any{
				"approvals": []any{
					map[string]any{"id": "cmd-1", "command": "chmod 777 ~/a.sh"},
				},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "cmd-1", "decision": "approve"},
			}),
		},
		{
			name:       "form",
			mode:       "form",
			awaitingID: "await-form",
			ask: map[string]any{
				"forms": []any{
					map[string]any{"id": "form-1", "command": "mock create-leave", "form": map[string]any{"days": 1}},
				},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "form-1", "decision": "approve", "form": map[string]any{"days": 2}},
			}),
		},
		{
			name:       "plan",
			mode:       "plan",
			awaitingID: "await-plan",
			ask: map[string]any{
				"plan": map[string]any{"id": "confirm", "planningId": "run-plan_planning_1"},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "confirm", "decision": "approve"},
			}),
		},
	}

	for _, tc := range cases {
		chatID := "chat-" + tc.name
		runID := "run-" + tc.name
		seedDeferredAwaitingPayload(t, fixture.chats, chatID, runID, tc.awaitingID, tc.mode, 60000, nowMs, tc.ask)
	}

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chatID := "chat-" + tc.name
			runID := "run-" + tc.name
			summary, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatalf("load summary: %v", err)
			}
			apiSummary := mapChatSummaries([]chat.Summary{*summary})[0]
			if apiSummary.Awaiting == nil || apiSummary.Awaiting.Status != "awaiting" || apiSummary.Awaiting.Mode != tc.mode {
				t.Fatalf("expected awaiting status in summary, got %#v", apiSummary.Awaiting)
			}

			detail, err := fixture.chats.LoadChat(chatID)
			if err != nil {
				t.Fatalf("load chat detail: %v", err)
			}
			foundAsk := false
			for _, event := range detail.Events {
				if event.Type == "awaiting.ask" && event.String("awaitingId") == tc.awaitingID && event.String("mode") == tc.mode {
					foundAsk = true
				}
			}
			if !foundAsk {
				t.Fatalf("expected replayed awaiting.ask for %s, got %#v", tc.mode, detail.Events)
			}

			body, err := json.Marshal(api.SubmitRequest{
				AgentKey:   "mock-agent",
				RunID:      runID,
				AwaitingID: tc.awaitingID,
				Params:     tc.params,
			})
			if err != nil {
				t.Fatalf("marshal submit: %v", err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			restarted.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("submit expected 200, got %d: %s", rec.Code, rec.Body.String())
			}

			summary, err = fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatalf("reload summary: %v", err)
			}
			if summary.PendingAwaiting != nil {
				t.Fatalf("expected pending awaiting cleared after submit, got %#v", summary.PendingAwaiting)
			}
		})
	}
}

func TestDeferredSubmitRejectsExpiredAwaiting(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-expired", "run-expired", "await-expired", "question", 10, time.Now().UnixMilli())

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"run-expired","awaitingId":"await-expired","params":[{"id":"q1","answer":"Approve"}]}`))
	req.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("submit expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "awaiting has expired") {
		t.Fatalf("expected expired submit error, got %s", rec.Body.String())
	}

	summary, err := fixture.chats.Summary("chat-expired")
	if err != nil {
		t.Fatalf("load summary after expired submit: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting cleared after expired submit, got %#v", summary)
	}
}

func TestHydrationSkipsExpiredAwaitings(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	nowMs := time.Now().UnixMilli()
	seedDeferredAwaiting(t, fixture.chats, "chat-stale", "run-stale", "await-stale", "question", 1000, nowMs-5000)
	seedDeferredAwaiting(t, fixture.chats, "chat-fresh", "run-fresh", "await-fresh", "question", 60000, nowMs-1000)

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	staleSummary, err := fixture.chats.Summary("chat-stale")
	if err != nil {
		t.Fatalf("load stale summary: %v", err)
	}
	if staleSummary == nil || staleSummary.PendingAwaiting != nil {
		t.Fatalf("expected stale pending awaiting cleared during hydration, got %#v", staleSummary)
	}

	freshSummary, err := fixture.chats.Summary("chat-fresh")
	if err != nil {
		t.Fatalf("load fresh summary: %v", err)
	}
	if freshSummary == nil || freshSummary.PendingAwaiting == nil {
		t.Fatalf("expected fresh pending awaiting kept during hydration, got %#v", freshSummary)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"run-fresh","awaitingId":"await-fresh","params":[{"id":"q1","answer":"Approve"}]}`))
	req.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit fresh expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeferredSubmitAcceptsWithinTimeout(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-within", "run-within", "await-within", "question", 60000, time.Now().UnixMilli()-1000)

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"run-within","awaitingId":"await-within","params":[{"id":"q1","answer":"Approve"}]}`))
	req.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func seedDeferredAwaiting(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutMs int, createdAt int64) {
	t.Helper()
	seedDeferredAwaitingPayload(t, store, chatID, runID, awaitingID, mode, timeoutMs, createdAt, map[string]any{
		"questions": []any{
			map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
		},
	})
}

func seedDeferredAwaitingPayload(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutMs int, createdAt int64, askPayload map[string]any) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	ask := map[string]any{
		"type":       "awaiting.ask",
		"awaitingId": awaitingID,
		"mode":       mode,
		"timeout":    timeoutMs,
	}
	for key, value := range askPayload {
		ask[key] = value
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: createdAt,
		Type:      "react",
		Awaiting:  []map[string]any{ask},
	}); err != nil {
		t.Fatalf("append awaiting step line: %v", err)
	}
	if err := store.SetPendingAwaiting(chatID, chat.PendingAwaiting{
		AwaitingID: awaitingID,
		RunID:      runID,
		Mode:       mode,
		CreatedAt:  createdAt,
	}); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}
}
