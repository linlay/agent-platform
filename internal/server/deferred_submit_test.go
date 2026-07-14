package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

func TestDeferredPlanningApproveContinuationUsesCoderExecuteSystem(t *testing.T) {
	var providerCallCount atomic.Int32
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if call := providerCallCount.Add(1); call != 1 {
			t.Fatalf("unexpected provider call %d payload=%#v", call, payload)
		}
		toolNames := providerRequestToolNames(payload["tools"])
		assertStringSliceContains(t, toolNames, "bash", "file_read", "file_write", "file_edit", "file_glob", "file_grep", "datetime", "regex", "plan_add_tasks", "plan_get_tasks", "plan_update_task")
		assertStringSliceExcludes(t, toolNames, contracts.FinalizePlanningToolName, "ask_user_question")
		assertProviderMessagesContainToolResult(t, payload, "tool_plan", contracts.FinalizePlanningToolName, "approve")
		if !providerMessagesContainText(payload, "Execute the confirmed CODER planning.\n\nOriginal request:\nplease plan first") ||
			!providerMessagesContainText(payload, "Confirmed planning:\n# Deferred Coder Plan") {
			t.Fatalf("expected execute prompt in provider messages, got %#v", payload["messages"])
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"deferred execution completed"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{
		notifications: notifications,
		setupRuntime: func(root string, cfg *config.Config) {
			workspace := filepath.Join(root, "workspace")
			agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-app")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("mkdir coder agent: %v", err)
			}
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(strings.Join([]string{
				"key: coder-app",
				"name: Coder App",
				"mode: CODER",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(workspace),
			}, "\n")), 0o644); err != nil {
				t.Fatalf("write coder agent: %v", err)
			}
		},
	})

	chatID := "chat-deferred-coder-planning"
	runID := "run-deferred-coder-planning"
	awaitingID := "tool_plan"
	seedCoderPlanningAwaitingForDeferredSubmit(t, fixture.chats, chatID, runID, awaitingID, fixture.cfg.Paths.ChatsDir)

	restartedRuns := contracts.NewInMemoryRunManager()
	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            restartedRuns,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	params, err := api.EncodeSubmitParams([]map[string]any{{"id": "confirm", "decision": "approve"}})
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	body, err := json.Marshal(api.SubmitRequest{
		ChatID:     chatID,
		RunID:      runID,
		AgentKey:   "coder-app",
		AwaitingID: awaitingID,
		SubmitID:   "submit-deferred-planning",
		Params:     params,
	})
	if err != nil {
		t.Fatalf("marshal submit request: %v", err)
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
	if !response.Data.Accepted || !response.Data.Continued {
		t.Fatalf("expected continued submit response, got %#v", response.Data)
	}

	waitForRecordedNotificationType(t, notifications, "run.finished")
	if got := providerCallCount.Load(); got != 1 {
		t.Fatalf("expected one provider call, got %d", got)
	}
	assertDeferredPlanningApproveJSONL(t, fixture.chats, chatID, runID, awaitingID, "submit-deferred-planning")
}

func TestDeferredSubmitHTTPRestoresPendingAwaitingAfterRestart(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	persistedStartedAt := time.Now().UnixMilli()
	seedDeferredAwaiting(t, fixture.chats, "chat-http", "run-http", "await-http", "question", 0, persistedStartedAt)
	startReader, ok := fixture.chats.(chat.RunStartReader)
	if !ok {
		t.Fatal("fixture chat store must expose persisted run lifecycle starts")
	}
	if got, err := startReader.LoadRunStartedAt("chat-http", "run-http"); err != nil || got != persistedStartedAt {
		t.Fatalf("persisted run start = %d, %v; want %d", got, err, persistedStartedAt)
	}

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
	waitForRecordedNotificationType(t, notifications, "run.finished")
	if status, ok := fixture.runs.RunStatus("run-http"); !ok || status.StartedAt != persistedStartedAt {
		t.Fatalf("restarted run lifecycle start = %#v; want %d", status, persistedStartedAt)
	}
	if runs, err := fixture.chats.ListRuns("chat-http"); err != nil || len(runs) != 1 || runs[0].StartedAt != persistedStartedAt {
		t.Fatalf("persisted completion changed authoritative start: %#v err=%v want=%d", runs, err, persistedStartedAt)
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
			durationValue := event.Value("durationMs")
			if durationValue == nil || contracts.AnyIntNode(durationValue) < 0 {
				t.Fatalf("expected non-negative durationMs on deferred awaiting.answer, got %#v", event)
			}
		}
	}
	if !foundSubmit || !foundAnswer {
		t.Fatalf("expected submit replay in chat detail, got %#v", detail.Events)
	}
	if eventTypes := notifications.EventTypes(); len(eventTypes) < 2 || eventTypes[0] != "awaiting.answered" || eventTypes[1] != "run.started" {
		t.Fatalf("expected awaiting.answered then run.started notifications, got %#v", eventTypes)
	}
	if payloads := notifications.Payloads(); len(payloads) == 0 || payloads[0]["durationMs"] == nil || payloads[0]["answeredAt"] == nil || payloads[0]["resolvedAt"] != nil {
		t.Fatalf("expected deferred awaiting.answered notification durationMs and answeredAt, got %#v", payloads)
	}
}

func TestDeferredQuestionSubmitRejectsInvalidAnswerAndAllowsRetry(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	chatID := "chat-deferred-multi-select"
	runID := "run-deferred-multi-select"
	awaitingID := "await-deferred-multi-select"
	seedDeferredAwaitingPayload(t, fixture.chats, chatID, runID, awaitingID, "question", 600, time.Now().UnixMilli(), map[string]any{
		"questions": []any{map[string]any{
			"id":       "q1",
			"question": "生活习惯",
			"type":     "multi-select",
			"options":  []any{map[string]any{"label": "A"}},
		}},
	})

	restarted, err := New(Dependencies{
		Config:          fixture.cfg,
		Chats:           fixture.chats,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
	})
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	invalidParams := mustEncodeSubmitParams(t, []map[string]any{{"id": "q1", "answer": "A"}})
	invalidBody, err := json.Marshal(api.SubmitRequest{
		ChatID:     chatID,
		AgentKey:   "mock-agent",
		RunID:      runID,
		AwaitingID: awaitingID,
		Params:     invalidParams,
	})
	if err != nil {
		t.Fatalf("marshal invalid submit request: %v", err)
	}
	invalidRec := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(invalidBody))
	invalidReq.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusOK {
		t.Fatalf("invalid submit expected 200, got %d: %s", invalidRec.Code, invalidRec.Body.String())
	}
	var invalidResponse api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(invalidRec.Body.Bytes(), &invalidResponse); err != nil {
		t.Fatalf("decode invalid submit response: %v", err)
	}
	if invalidResponse.Code != 0 || invalidResponse.Data.Accepted || invalidResponse.Data.Status != "invalid" || !strings.Contains(invalidResponse.Data.Detail, "answers is required") {
		t.Fatalf("expected rejected deferred question submit, got %#v", invalidResponse)
	}
	if _, ok := restarted.deferredAwaitings.Lookup(awaitingID); !ok {
		t.Fatal("invalid deferred submit must leave the awaiting item active")
	}
	summary, err := fixture.chats.Summary(chatID)
	if err != nil {
		t.Fatalf("load chat summary: %v", err)
	}
	if summary.PendingAwaiting == nil || summary.PendingAwaiting.AwaitingID != awaitingID {
		t.Fatalf("invalid deferred submit must preserve pending awaiting, got %#v", summary)
	}

	validParams := mustEncodeSubmitParams(t, []map[string]any{{"id": "q1", "answers": []string{"A"}}})
	validBody, err := json.Marshal(api.SubmitRequest{
		ChatID:     chatID,
		AgentKey:   "mock-agent",
		RunID:      runID,
		AwaitingID: awaitingID,
		Params:     validParams,
	})
	if err != nil {
		t.Fatalf("marshal valid submit request: %v", err)
	}
	validRec := httptest.NewRecorder()
	validReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(validBody))
	validReq.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("valid submit expected 200, got %d: %s", validRec.Code, validRec.Body.String())
	}
	var validResponse api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(validRec.Body.Bytes(), &validResponse); err != nil {
		t.Fatalf("decode valid submit response: %v", err)
	}
	if !validResponse.Data.Accepted || validResponse.Data.Status != "accepted" {
		t.Fatalf("expected accepted deferred question submit, got %#v", validResponse.Data)
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
	assistantTs := int64(1700000001701)
	startServerFixtureRun(t, store, chatID, runID, assistantTs)
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: 1700000001701,
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
	if err := server.persistDeferredAwaitingToolAnswer(chatID, runID, awaitingID, answer, 1700000001702); err != nil {
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
			restorable: true,
			ask: map[string]any{
				"approvals": []any{
					map[string]any{"id": "cmd-1", "command": "chmod 777 ~/a.sh"},
				},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "cmd-1", "decision": "reject"},
			}),
		},
		{
			name:       "form",
			mode:       "form",
			awaitingID: "await-form",
			restorable: true,
			ask: map[string]any{
				"forms": []any{
					map[string]any{"id": "form-1", "command": "mock create-leave", "form": map[string]any{"days": 1}},
				},
			},
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "form-1", "decision": "reject"},
			}),
		},
		{
			name:       "planning",
			mode:       "planning",
			awaitingID: "await-planning",
			restorable: true,
			ask: map[string]any{
				"planning": map[string]any{"id": "confirm", "planningId": "run-planning_planning_1"},
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
			"timestamp":  nowMs + 1,
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Frontend: fixture.frontend},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
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
	startServerFixtureRun(t, store, chatID, runID, createdAt)
	ask := map[string]any{
		"type":       "awaiting.ask",
		"awaitingId": awaitingID,
		"timestamp":  createdAt,
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

func seedCoderPlanningAwaitingForDeferredSubmit(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, chatsDir string) {
	t.Helper()
	queryTs := time.Now().UnixMilli()
	if _, _, err := store.EnsureChat(chatID, "coder-app", "", "please plan first"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startServerFixtureRun(t, store, chatID, runID, queryTs)
	if err := store.AppendQueryLine(chatID, chat.QueryLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: queryTs,
		LiveSeq:   1,
		Query: map[string]any{
			"requestId":    runID,
			"runId":        runID,
			"chatId":       chatID,
			"agentKey":     "coder-app",
			"role":         "user",
			"message":      "please plan first",
			"planningMode": true,
			"accessLevel":  contracts.AccessLevelDefault,
		},
		Type: "query",
	}); err != nil {
		t.Fatalf("append query line: %v", err)
	}

	planningID := runID + "_planning_1"
	planningFile := filepath.Join(chatsDir, chatID, chat.ToolRootDirName, chat.ToolPlanningDirName, planningID+".md")
	if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	markdown := "# Deferred Coder Plan\n\n## Summary\nExecute after restart."
	if err := os.WriteFile(planningFile, []byte(markdown), 0o644); err != nil {
		t.Fatalf("write planning file: %v", err)
	}
	assistantTs := queryTs + 1
	awaiting := map[string]any{
		"type":         "awaiting.ask",
		"awaitingId":   awaitingID,
		"runId":        runID,
		"timestamp":    assistantTs,
		"mode":         "planning",
		"viewportType": "builtin",
		"viewportKey":  "planning",
		"planning": map[string]any{
			"id":           "confirm",
			"planningId":   planningID,
			"planningFile": planningFile,
			"options": []any{
				map[string]any{"decision": "approve"},
				map[string]any{"decision": "reject"},
			},
		},
	}
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: assistantTs,
		LiveSeq:   7,
		Type:      chat.StepLineTypeReact,
		Seq:       1,
		Messages: []chat.StoredMessage{{
			Role: "assistant",
			ToolCalls: []chat.StoredToolCall{{
				ID:   awaitingID,
				Type: "function",
				Function: chat.StoredFunction{
					Name:      contracts.FinalizePlanningToolName,
					Arguments: `{"markdown":"` + strings.ReplaceAll(markdown, "\n", "\\n") + `"}`,
				},
			}},
			ToolID: awaitingID,
			MsgID:  "msg-plan",
			Ts:     &assistantTs,
		}},
		Awaiting: []map[string]any{awaiting},
	}); err != nil {
		t.Fatalf("append awaiting step line: %v", err)
	}
	if err := store.SetPendingAwaiting(chatID, chat.PendingAwaiting{
		AwaitingID: awaitingID,
		RunID:      runID,
		Mode:       "planning",
		CreatedAt:  assistantTs,
	}); err != nil {
		t.Fatalf("set pending awaiting: %v", err)
	}
}

func providerMessagesContainText(payload map[string]any, want string) bool {
	messages, _ := payload["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if strings.Contains(textFromJSONLMessageContentForServerTest(message["content"]), want) {
			return true
		}
	}
	return false
}

func assertDeferredPlanningApproveJSONL(t *testing.T, store chat.Store, chatID string, sourceRunID string, awaitingID string, submitID string) {
	t.Helper()
	content, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load jsonl: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	lines := []map[string]any{}
	for {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode jsonl: %v\n%s", err, content)
		}
		lines = append(lines, line)
	}
	foundSourcePlanningResult := false
	executeQueryIndex := -1
	for index, line := range lines {
		if stringValue(line["_type"]) == chat.StepLineTypeReactTool && stringValue(line["runId"]) == sourceRunID && lineHasFinalizePlanningToolResultForServerTest(line) {
			foundSourcePlanningResult = true
		}
		if stringValue(line["_type"]) != "query" {
			continue
		}
		query, _ := line["query"].(map[string]any)
		if stringValue(query["message"]) == "Execute planning" && stringValue(line["runId"]) != sourceRunID {
			executeQueryIndex = index
			break
		}
	}
	if executeQueryIndex < 0 {
		t.Fatalf("expected coder execute query in:\n%s", content)
	}
	if !foundSourcePlanningResult {
		t.Fatalf("expected source run %s to keep finalize_planning submit tool result in:\n%s", sourceRunID, content)
	}
	executeQueryLine := lines[executeQueryIndex]
	executeRunID := stringValue(executeQueryLine["runId"])
	if executeRunID == "" || executeRunID == sourceRunID {
		t.Fatalf("expected execute query to use a new execution run id, got %#v in:\n%s", executeQueryLine, content)
	}
	if liveSeq := testInt64Value(executeQueryLine["liveSeq"]); liveSeq <= 0 {
		t.Fatalf("expected execute query liveSeq for new run, got %#v in:\n%s", executeQueryLine["liveSeq"], content)
	}
	query, _ := executeQueryLine["query"].(map[string]any)
	if stringValue(query["requestId"]) != submitID {
		t.Fatalf("expected execute query requestId %q, got %#v in:\n%s", submitID, query, content)
	}
	for _, field := range []string{"synthetic", "stage", "source"} {
		if _, ok := query[field]; ok {
			t.Fatalf("did not expect %s in execute query payload: %#v", field, query)
		}
	}
	if _, ok := query["system"]; ok {
		t.Fatalf("did not expect nested system in execute query payload: %#v", query)
	}
	system, _ := executeQueryLine["system"].(map[string]any)
	if len(system) == 0 {
		t.Fatalf("expected one execute query system, got %#v in:\n%s", executeQueryLine, content)
	}
	if system["cacheKey"] != "coder:execute" || stringValue(system["agentKey"]) == "" {
		t.Fatalf("expected coder:execute system, got %#v in:\n%s", system, content)
	}
	rawMessages, _ := executeQueryLine["messages"].([]any)
	if len(rawMessages) != 1 {
		t.Fatalf("expected one execute query message, got %#v in:\n%s", executeQueryLine, content)
	}
	message, _ := rawMessages[0].(map[string]any)
	if stringValue(message["role"]) != "user" ||
		!strings.Contains(textFromJSONLMessageContentForServerTest(message["content"]), "Execute the confirmed CODER planning.") ||
		!strings.Contains(textFromJSONLMessageContentForServerTest(message["content"]), "Confirmed planning:\n# Deferred Coder Plan") {
		t.Fatalf("unexpected execute query message %#v in:\n%s", message, content)
	}

	for _, line := range lines[executeQueryIndex+1:] {
		if stringValue(line["_type"]) != chat.StepLineTypeReact {
			continue
		}
		if stringValue(line["runId"]) != executeRunID {
			continue
		}
		systemRef, _ := line["systemRef"].(map[string]any)
		if len(systemRef) == 0 {
			continue
		}
		if systemRef["cacheKey"] != "coder:execute" {
			t.Fatalf("expected execute react systemRef coder:execute, got %#v in:\n%s", systemRef, content)
		}
		if stringValue(systemRef["agentKey"]) == "" {
			t.Fatalf("expected execute systemRef agentKey, got %#v in:\n%s", systemRef, content)
		}
		if systemRef["cacheKey"] == "coder:main" {
			t.Fatalf("did not expect coder:main systemRef in:\n%s", content)
		}
		if liveSeq := testInt64Value(line["liveSeq"]); liveSeq <= testInt64Value(executeQueryLine["liveSeq"]) {
			t.Fatalf("expected execute step liveSeq after execute query, got line=%#v query=%#v", line["liveSeq"], executeQueryLine["liveSeq"])
		}
		return
	}
	t.Fatalf("expected execute react after bootstrap query for awaiting %s in:\n%s", awaitingID, content)
}

func testInt64Value(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	default:
		return 0
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
