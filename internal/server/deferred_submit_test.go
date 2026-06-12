package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	reqBody := bytes.NewBufferString(`{"chatId":"chat-http","submitId":"submit-http","agentKey":"mock-agent","runId":"run-http","awaitingId":"await-http","params":[{"id":"q1","answer":"Approve"}]}`)
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
	if response.Data.SubmitID != "submit-http" || !response.Data.Continued {
		t.Fatalf("expected submitId echo and continued response, got %#v", response.Data)
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
	if eventTypes := notifications.EventTypes(); len(eventTypes) < 2 || eventTypes[0] != "awaiting.answered" || eventTypes[1] != "run.started" {
		t.Fatalf("expected awaiting.answered then run.started notifications, got %#v", eventTypes)
	}
}

func TestPersistDeferredAwaitingToolAnswerWritesReactToolLine(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStore(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-react-tool"
	runID := "run-react-tool"
	awaitingID := "await-react-tool"
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	assistantTs := int64(1701)
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 1701,
		Type:      chat.StepLineTypeReact,
		Seq:       1,
		Messages: []chat.StoredMessage{{
			Role: "assistant",
			ToolCalls: []chat.StoredToolCall{{
				ID:   awaitingID,
				Type: "function",
				Function: chat.StoredFunction{
					Name:      "ask_user_question",
					Arguments: `{"questions":[]}`,
				},
			}},
			ToolID: awaitingID,
			MsgID:  "msg-1",
			Ts:     &assistantTs,
		}},
	}); err != nil {
		t.Fatalf("append assistant step: %v", err)
	}

	server := &Server{deps: Dependencies{Chats: store}}
	answer := map[string]any{
		"type":       "awaiting.answer",
		"awaitingId": awaitingID,
		"mode":       "question",
		"status":     "answered",
		"answers":    []any{map[string]any{"id": "q1", "answer": "ok"}},
	}
	if err := server.persistDeferredAwaitingToolAnswer(chatID, runID, awaitingID, answer, 1702); err != nil {
		t.Fatalf("persist deferred awaiting tool answer: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(root, chatID+".jsonl"))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected assistant and react-tool lines, got %q", raw)
	}
	var appended map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &appended); err != nil {
		t.Fatalf("decode appended line: %v", err)
	}
	if appended["_type"] != chat.StepLineTypeReactTool {
		t.Fatalf("expected react-tool line, got %#v", appended)
	}
	messages, _ := appended["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one tool message, got %#v", appended)
	}
	message, _ := messages[0].(map[string]any)
	if message["role"] != "tool" || message["name"] != "ask_user_question" || message["tool_call_id"] != awaitingID {
		t.Fatalf("unexpected tool message %#v", message)
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
			cfg.WebSocket.PingInterval = 30000
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
			"chatId":     "chat-ws",
			"submitId":   "submit-ws",
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
			if frame.Type == "awaiting.answered" {
				gotPush = true
			}
		}
	}
	if !gotResponse || !gotPush {
		t.Fatalf("expected websocket response and awaiting.answered push, got response=%v push=%v", gotResponse, gotPush)
	}

	summary, err := fixture.chats.Summary("chat-ws")
	if err != nil {
		t.Fatalf("load summary after ws submit: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting to be cleared after ws submit, got %#v", summary)
	}
}

func TestDeferredSubmitSubmitIDIsIdempotent(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-idempotent", "run-idempotent", "await-idempotent", "question", 0, time.Now().UnixMilli())

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

	submit := func(submitID string) api.SubmitResponse {
		t.Helper()
		body, err := json.Marshal(api.SubmitRequest{
			ChatID:     "chat-idempotent",
			SubmitID:   submitID,
			AgentKey:   "mock-agent",
			RunID:      "run-idempotent",
			AwaitingID: "await-idempotent",
			Params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "q1", "answer": "Approve"},
			}),
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
		var response api.ApiResponse[api.SubmitResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode submit response: %v", err)
		}
		return response.Data
	}

	first := submit("submit-idem-1")
	if !first.Accepted || first.Status != "accepted" || first.SubmitID != "submit-idem-1" {
		t.Fatalf("unexpected first submit response %#v", first)
	}
	second := submit("submit-idem-1")
	if !second.Accepted || second.Status != "accepted" || second.SubmitID != "submit-idem-1" {
		t.Fatalf("unexpected retry submit response %#v", second)
	}
	third := submit("submit-idem-2")
	if third.Accepted || third.Status != "already_resolved" || third.SubmitID != "submit-idem-2" {
		t.Fatalf("unexpected conflicting submit response %#v", third)
	}

	detail, err := fixture.chats.LoadChat("chat-idempotent")
	if err != nil {
		t.Fatalf("load chat detail: %v", err)
	}
	submitCount := 0
	answerCount := 0
	for _, event := range detail.Events {
		switch event.Type {
		case "request.submit":
			submitCount++
		case "awaiting.answer":
			answerCount++
		}
	}
	if submitCount != 1 || answerCount != 1 {
		t.Fatalf("expected one submit and one answer, got submit=%d answer=%d events=%#v", submitCount, answerCount, detail.Events)
	}
}

func TestDeferredSubmitRestoresQuestionAndPlanAfterRestart(t *testing.T) {
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
		restorable bool
	}{
		{
			name:       "question",
			mode:       "question",
			awaitingID: "await-question",
			restorable: true,
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
			restorable: false,
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
			restorable: false,
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
			restorable: true,
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
		seedDeferredAwaitingPayload(t, fixture.chats, chatID, runID, tc.awaitingID, tc.mode, 600, nowMs, tc.ask)
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
			if !tc.restorable {
				if apiSummary.Awaiting != nil {
					t.Fatalf("expected non-restorable awaiting to be cleared, got %#v", apiSummary.Awaiting)
				}
				body, err := json.Marshal(api.SubmitRequest{
					ChatID:     chatID,
					SubmitID:   "submit-" + tc.name,
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
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("non-restorable submit expected 400, got %d: %s", rec.Code, rec.Body.String())
				}
				return
			}
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
				ChatID:     chatID,
				SubmitID:   "submit-" + tc.name,
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

	seedDeferredAwaiting(t, fixture.chats, "chat-expired", "run-expired", "await-expired", "question", 1, time.Now().UnixMilli()-2000)

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
	if !strings.Contains(rec.Body.String(), "awaiting has expired") && !strings.Contains(rec.Body.String(), "unknown awaitingId") {
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
	seedDeferredAwaiting(t, fixture.chats, "chat-stale", "run-stale", "await-stale", "question", 1, nowMs-5000)
	seedDeferredAwaiting(t, fixture.chats, "chat-fresh", "run-fresh", "await-fresh", "question", 60, nowMs-1000)

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
	waitForRecordedNotificationType(t, notifications, "run.finished")
}

func TestHydrationClearsDanglingAndAnsweredAwaitings(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	nowMs := time.Now().UnixMilli()
	if _, _, err := fixture.chats.EnsureChat("chat-dangling", "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure dangling chat: %v", err)
	}
	if err := fixture.chats.SetPendingAwaiting("chat-dangling", chat.PendingAwaiting{
		AwaitingID: "await-dangling",
		RunID:      "run-dangling",
		Mode:       "question",
		CreatedAt:  nowMs,
	}); err != nil {
		t.Fatalf("set dangling pending awaiting: %v", err)
	}
	seedDeferredAwaiting(t, fixture.chats, "chat-answered", "run-answered", "await-answered", "question", 60, nowMs)
	if err := fixture.chats.AppendSubmitLine("chat-answered", chat.SubmitLine{
		ChatID:    "chat-answered",
		RunID:     "run-answered",
		UpdatedAt: nowMs + 1,
		Type:      "submit",
		Answer: map[string]any{
			"type":       "awaiting.answer",
			"awaitingId": "await-answered",
			"mode":       "question",
			"status":     "answered",
		},
	}); err != nil {
		t.Fatalf("append answered line: %v", err)
	}

	_, err := New(Dependencies{
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

	for _, chatID := range []string{"chat-dangling", "chat-answered"} {
		summary, err := fixture.chats.Summary(chatID)
		if err != nil {
			t.Fatalf("load %s summary: %v", chatID, err)
		}
		if summary == nil || summary.PendingAwaiting != nil {
			t.Fatalf("expected %s pending awaiting cleared during hydration, got %#v", chatID, summary)
		}
	}
}

func TestDeferredSubmitAcceptsWithinTimeout(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-within", "run-within", "await-within", "question", 60, time.Now().UnixMilli()-1000)

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

func seedDeferredAwaiting(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutSec int, createdAt int64) {
	t.Helper()
	seedDeferredAwaitingPayload(t, store, chatID, runID, awaitingID, mode, timeoutSec, createdAt, map[string]any{
		"questions": []any{
			map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
		},
	})
}

func seedDeferredAwaitingPayload(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutSec int, createdAt int64, askPayload map[string]any) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	ask := map[string]any{
		"type":       "awaiting.ask",
		"awaitingId": awaitingID,
		"mode":       mode,
		"timeout":    timeoutSec,
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

func waitForRecordedNotificationType(t *testing.T, sink *recordingNotificationSink, eventType string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, candidate := range sink.EventTypes() {
			if candidate == eventType {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for notification %s; got %#v", eventType, sink.EventTypes())
}
