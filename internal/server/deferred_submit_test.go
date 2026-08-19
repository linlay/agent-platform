package server

import (
	"bytes"
	"context"
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
	"agent-platform/internal/runenv"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type awaitingReconcileFailureStore struct {
	chat.Store
	stage string
}

func (s *awaitingReconcileFailureStore) LoadAwaitingStep(chatID string, awaitingID string) (*chat.PersistedAwaitingStep, error) {
	reader, ok := s.Store.(chat.AwaitingRecoveryReader)
	if !ok {
		return nil, errors.New("awaiting recovery reader unavailable")
	}
	return reader.LoadAwaitingStep(chatID, awaitingID)
}

func (s *awaitingReconcileFailureStore) LoadRunStartedAt(chatID string, runID string) (int64, error) {
	reader, ok := s.Store.(chat.RunStartReader)
	if !ok {
		return 0, errors.New("run start reader unavailable")
	}
	return reader.LoadRunStartedAt(chatID, runID)
}

func (s *awaitingReconcileFailureStore) AppendSubmitLine(chatID string, line chat.SubmitLine) error {
	if s.stage == "answer" {
		return errors.New("injected awaiting answer failure")
	}
	return s.Store.AppendSubmitLine(chatID, line)
}

func (s *awaitingReconcileFailureStore) AppendStepLine(chatID string, line chat.StepLine) error {
	if s.stage == "tool_result" && line.Type == chat.StepLineTypeReactTool {
		return errors.New("injected awaiting tool result failure")
	}
	return s.Store.AppendStepLine(chatID, line)
}

func (s *awaitingReconcileFailureStore) OnRunCompleted(completion chat.RunCompletion) error {
	if s.stage == "completion" {
		return errors.New("injected awaiting completion failure")
	}
	return s.Store.OnRunCompleted(completion)
}

func (s *awaitingReconcileFailureStore) ClearPendingAwaiting(chatID string, awaitingID string) error {
	if s.stage == "clear_pending" {
		return errors.New("injected awaiting clear failure")
	}
	return s.Store.ClearPendingAwaiting(chatID, awaitingID)
}

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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
	planningCursor := restarted.persistedRunLiveSeq(chatID, runID)
	planningObserver, err := restartedRuns.AttachObserver(runID, planningCursor)
	if err != nil {
		t.Fatalf("attach recovered planning run: %v", err)
	}
	defer restartedRuns.DetachObserver(runID, planningObserver.ID)

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
	var planningEvents []stream.EventData
	planningDeadline := time.After(2 * time.Second)
	for {
		select {
		case event, open := <-planningObserver.Events:
			if !open {
				goto planningComplete
			}
			planningEvents = append(planningEvents, event)
		case <-planningDeadline:
			t.Fatalf("timed out waiting for recovered planning run completion: %#v", planningEvents)
		}
	}

planningComplete:
	wantPlanningTypes := []string{"request.submit", "awaiting.answer", "tool.result", "run.complete"}
	if len(planningEvents) != len(wantPlanningTypes) {
		t.Fatalf("recovered planning events = %#v, want %v", planningEvents, wantPlanningTypes)
	}
	for index, wantType := range wantPlanningTypes {
		if planningEvents[index].Type != wantType || planningEvents[index].Seq != planningCursor+int64(index)+1 {
			t.Fatalf("planning event %d = %#v, want type=%s seq=%d", index, planningEvents[index], wantType, planningCursor+int64(index)+1)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for providerCallCount.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := providerCallCount.Load(); got != 1 {
		t.Fatalf("expected one provider call, got %d", got)
	}
	waitForRecordedNotificationType(t, notifications, "run.finished")
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
	status, ok := fixture.runs.RunStatus("run-http")
	if !ok || status.State != contracts.RunLoopStateWaitingSubmit || status.StartedAt != persistedStartedAt {
		t.Fatalf("expected recovered WAITING_SUBMIT active run, got %#v", status)
	}
	attachCursor := restarted.persistedRunLiveSeq("chat-http", "run-http")
	chatDetail, err := restarted.loadChatDetail(context.Background(), "chat-http", false)
	if err != nil {
		t.Fatalf("load recovered chat detail: %v", err)
	}
	if chatDetail.Awaiting == nil || chatDetail.Awaiting.AwaitingID != "await-http" || chatDetail.ActiveRun == nil || chatDetail.ActiveRun.RunID != "run-http" || chatDetail.ActiveRun.State != string(contracts.RunLoopStateWaitingSubmit) || chatDetail.ActiveRun.LastSeq != attachCursor {
		t.Fatalf("expected authoritative awaiting plus attachable activeRun, got awaiting=%#v activeRun=%#v", chatDetail.Awaiting, chatDetail.ActiveRun)
	}
	observer, err := fixture.runs.AttachObserver("run-http", attachCursor)
	if err != nil {
		t.Fatalf("attach recovered run at cursor %d: %v", attachCursor, err)
	}
	defer fixture.runs.DetachObserver("run-http", observer.ID)

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
	for index, wantType := range []string{"request.submit", "awaiting.answer"} {
		select {
		case event := <-observer.Events:
			if event.Type != wantType || event.Seq != attachCursor+int64(index)+1 {
				t.Fatalf("unexpected recovered attach event %#v, want type=%s seq=%d", event, wantType, attachCursor+int64(index)+1)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for recovered attach event %s", wantType)
		}
	}
	go func() {
		for range observer.Events {
		}
		observer.MarkDone()
	}()
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
	if eventTypes := notifications.EventTypes(); len(eventTypes) == 0 || eventTypes[0] != "awaiting.answered" {
		t.Fatalf("expected awaiting.answered notification, got %#v", eventTypes)
	} else {
		for _, eventType := range eventTypes[1:] {
			if eventType == "run.started" {
				t.Fatalf("recovered same-run continuation must not emit duplicate run.started: %#v", eventTypes)
			}
		}
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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

	submit := func(submitID string, wantHTTPStatus int) api.SubmitResponse {
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
		if rec.Code != wantHTTPStatus {
			t.Fatalf("submit expected %d, got %d: %s", wantHTTPStatus, rec.Code, rec.Body.String())
		}
		var response api.ApiResponse[api.SubmitResponse]
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode submit response: %v", err)
		}
		return response.Data
	}

	first := submit("submit-idem-1", http.StatusOK)
	if !first.Accepted || first.Status != "accepted" || first.SubmitID != "submit-idem-1" {
		t.Fatalf("unexpected first submit response %#v", first)
	}
	second := submit("submit-idem-1", http.StatusOK)
	if !second.Accepted || second.Status != "accepted" || second.SubmitID != "submit-idem-1" {
		t.Fatalf("unexpected retry submit response %#v", second)
	}
	third := submit("submit-idem-2", http.StatusConflict)
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
				{"id": "cmd-1", "decision": "reject"},
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
		if tc.mode == "planning" {
			planning := tc.ask["planning"].(map[string]any)
			planningFile := filepath.Join(fixture.chats.ChatDir(chatID), chat.ToolRootDirName, chat.ToolPlanningDirName, "run-planning_planning_1.md")
			if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
				t.Fatalf("mkdir planning dir: %v", err)
			}
			if err := os.WriteFile(planningFile, []byte("# Planning"), 0o644); err != nil {
				t.Fatalf("write planning file: %v", err)
			}
			planning["planningFile"] = planningFile
		}
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
				if rec.Code != http.StatusConflict {
					t.Fatalf("non-restorable submit expected 409, got %d: %s", rec.Code, rec.Body.String())
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
	if rec.Code != http.StatusConflict {
		t.Fatalf("submit expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "awaiting_expired") {
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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

func TestRecoveredAwaitingSupervisorTerminalizesTimeoutOnAttachedRun(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: notifications})

	createdAt := time.Now().UnixMilli() - 1200
	seedDeferredAwaiting(t, fixture.chats, "chat-runtime-timeout", "run-runtime-timeout", "await-runtime-timeout", "question", 2, createdAt)
	restarted, err := New(deferredRestartDependencies(fixture, fixture.chats, notifications))
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}
	cursor := restarted.persistedRunLiveSeq("chat-runtime-timeout", "run-runtime-timeout")
	observer, err := fixture.runs.AttachObserver("run-runtime-timeout", cursor)
	if err != nil {
		t.Fatalf("attach recovered timeout run: %v", err)
	}
	defer fixture.runs.DetachObserver("run-runtime-timeout", observer.ID)

	var events []stream.EventData
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event, ok := <-observer.Events:
			if !ok {
				goto completed
			}
			events = append(events, event)
		case <-deadline:
			t.Fatalf("timed out waiting for recovered timeout events: %#v", events)
		}
	}

completed:
	wantTypes := []string{"awaiting.answer", "tool.result", "run.cancel"}
	if len(events) != len(wantTypes) {
		t.Fatalf("recovered timeout events = %#v, want %v", events, wantTypes)
	}
	for index, wantType := range wantTypes {
		if events[index].Type != wantType || events[index].Seq != cursor+int64(index)+1 {
			t.Fatalf("event %d = %#v, want type=%s seq=%d", index, events[index], wantType, cursor+int64(index)+1)
		}
	}
	result := contracts.AnyMapNode(events[1].Payload["result"])
	if result["executed"] != false || contracts.AnyStringNode(result["error"]) != "timeout" || contracts.AnyStringNode(result["awaitingId"]) != "await-runtime-timeout" {
		t.Fatalf("unexpected recovered timeout tool result %#v", result)
	}
	if _, active, err := fixture.runs.ActiveRunForChat("chat-runtime-timeout"); err != nil || active {
		t.Fatalf("recovered timeout run remained active: active=%v err=%v", active, err)
	}
	assertRestartTerminalizedAwaiting(t, fixture.chats, "chat-runtime-timeout", "run-runtime-timeout", "await-runtime-timeout", "timeout")
	waitForRecordedNotificationType(t, notifications, "run.finished")
}

func TestRecoveredAwaitingSupervisorTerminalizesInterrupt(t *testing.T) {
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{notifications: notifications})

	seedDeferredAwaiting(t, fixture.chats, "chat-runtime-interrupt", "run-runtime-interrupt", "await-runtime-interrupt", "question", 0, time.Now().UnixMilli())
	restarted, err := New(deferredRestartDependencies(fixture, fixture.chats, notifications))
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}
	cursor := restarted.persistedRunLiveSeq("chat-runtime-interrupt", "run-runtime-interrupt")
	observer, err := fixture.runs.AttachObserver("run-runtime-interrupt", cursor)
	if err != nil {
		t.Fatalf("attach recovered interrupt run: %v", err)
	}
	defer fixture.runs.DetachObserver("run-runtime-interrupt", observer.ID)
	ack := fixture.runs.Interrupt(api.InterruptRequest{
		ChatID: "chat-runtime-interrupt", RunID: "run-runtime-interrupt", InterruptReason: "user_requested",
	})
	if !ack.Accepted {
		t.Fatalf("interrupt recovered run: %#v", ack)
	}

	var events []stream.EventData
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event, ok := <-observer.Events:
			if !ok {
				goto completed
			}
			events = append(events, event)
		case <-deadline:
			t.Fatalf("timed out waiting for recovered interrupt events: %#v", events)
		}
	}

completed:
	wantTypes := []string{"awaiting.answer", "tool.result", "run.cancel"}
	if len(events) != len(wantTypes) {
		t.Fatalf("recovered interrupt events = %#v, want %v", events, wantTypes)
	}
	for index, wantType := range wantTypes {
		if events[index].Type != wantType || events[index].Seq != cursor+int64(index)+1 {
			t.Fatalf("event %d = %#v, want type=%s seq=%d", index, events[index], wantType, cursor+int64(index)+1)
		}
	}
	assertRestartTerminalizedAwaiting(t, fixture.chats, "chat-runtime-interrupt", "run-runtime-interrupt", "await-runtime-interrupt", "run_interrupted")
}

func TestHydrationReconcilesRestartAwaitingModesAndStructuredConflicts(t *testing.T) {
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	})
	nowMs := time.Now().UnixMilli()
	seedDeferredAwaiting(t, fixture.chats, "chat-expired-question", "run-expired-question", "await-expired-question", "question", 1, nowMs-5_000)
	seedDeferredAwaitingPayload(t, fixture.chats, "chat-restarted-approval", "run-restarted-approval", "await-restarted-approval", "approval", 600, nowMs-1_000, map[string]any{
		"approvals": []any{map[string]any{"id": "tool-approval", "command": "touch marker"}},
	})
	seedDeferredAwaitingPayload(t, fixture.chats, "chat-expired-form", "run-expired-form", "await-expired-form", "form", 1, nowMs-5_000, map[string]any{
		"forms": []any{map[string]any{"id": "tool-form", "command": "create record", "form": map[string]any{"name": "demo"}}},
	})
	seedDeferredAwaitingPayload(t, fixture.chats, "chat-old-planning", "run-old-planning", "await-old-planning", "planning", 1, nowMs-7*24*60*60*1000, map[string]any{
		"planning": map[string]any{"id": "confirm", "planningId": "run-old-planning_planning_1"},
	})
	seedDeferredAwaiting(t, fixture.chats, "chat-old-question-no-timeout", "run-old-question-no-timeout", "await-old-question-no-timeout", "question", 0, nowMs-7*24*60*60*1000)

	restarted, err := New(deferredRestartDependencies(fixture, fixture.chats, nil))
	if err != nil {
		t.Fatalf("new restarted server: %v", err)
	}

	assertRestartTerminalizedAwaiting(t, fixture.chats, "chat-expired-question", "run-expired-question", "await-expired-question", "timeout")
	assertRestartTerminalizedAwaiting(t, fixture.chats, "chat-restarted-approval", "run-restarted-approval", "await-restarted-approval", "runtime_restarted")
	assertRestartTerminalizedAwaiting(t, fixture.chats, "chat-expired-form", "run-expired-form", "await-expired-form", "timeout")
	for _, chatID := range []string{"chat-old-planning", "chat-old-question-no-timeout"} {
		summary, summaryErr := fixture.chats.Summary(chatID)
		if summaryErr != nil {
			t.Fatalf("load %s summary: %v", chatID, summaryErr)
		}
		if summary == nil || summary.PendingAwaiting == nil {
			t.Fatalf("expected %s awaiting to remain recoverable, got %#v", chatID, summary)
		}
	}

	assertAwaitingSubmitConflict(t, restarted, "chat-expired-question", "run-expired-question", "await-expired-question", http.StatusConflict, "awaiting_expired", "expired")
	assertAwaitingSubmitConflict(t, restarted, "chat-restarted-approval", "run-restarted-approval", "await-restarted-approval", http.StatusConflict, "awaiting_interrupted", "interrupted")
	assertAwaitingSubmitConflict(t, restarted, "chat-expired-form", "run-expired-form", "await-expired-form", http.StatusConflict, "awaiting_expired", "expired")
	assertAwaitingSubmitConflict(t, restarted, "chat-wrong-identity", "run-expired-question", "await-expired-question", http.StatusBadRequest, "unknown_awaiting", "unknown")
	assertAwaitingSubmitConflict(t, restarted, "chat-unknown", "run-unknown", "await-unknown", http.StatusBadRequest, "unknown_awaiting", "unknown")
}

func TestHydrationReconciliationIsIdempotentAcrossEveryWriteStage(t *testing.T) {
	for _, stage := range []string{"answer", "tool_result", "completion", "clear_pending"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `[DONE]`)
			})
			chatID := "chat-reconcile-" + stage
			runID := "run-reconcile-" + stage
			awaitingID := "await-reconcile-" + stage
			seedDeferredAwaiting(t, fixture.chats, chatID, runID, awaitingID, "question", 1, time.Now().UnixMilli()-5_000)

			failing := &awaitingReconcileFailureStore{Store: fixture.chats, stage: stage}
			if _, err := New(deferredRestartDependencies(fixture, failing, nil)); err == nil || !strings.Contains(err.Error(), "reconcile persisted awaitings") {
				t.Fatalf("expected startup reconciliation failure at %s, got %v", stage, err)
			}
			summary, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatalf("load pending summary after %s failure: %v", stage, err)
			}
			if summary == nil || summary.PendingAwaiting == nil {
				t.Fatalf("pending awaiting was cleared before %s completed: %#v", stage, summary)
			}

			if _, err := New(deferredRestartDependencies(fixture, fixture.chats, nil)); err != nil {
				t.Fatalf("resume reconciliation after %s failure: %v", stage, err)
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "timeout")

			if err := fixture.chats.SetPendingAwaiting(chatID, chat.PendingAwaiting{
				AwaitingID: awaitingID,
				RunID:      runID,
				Mode:       "question",
				CreatedAt:  time.Now().UnixMilli() - 5_000,
			}); err != nil {
				t.Fatalf("restore stale pending marker: %v", err)
			}
			if _, err := New(deferredRestartDependencies(fixture, fixture.chats, nil)); err != nil {
				t.Fatalf("repeat reconciliation after %s: %v", stage, err)
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "timeout")
		})
	}
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
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

func TestHydrationTerminalizesMissingRunEnvironmentCheckpoint(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			agentPath := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(agentPath)
			if err != nil {
				t.Fatal(err)
			}
			updated := strings.Replace(string(data), "    - ask_user_question\n", "    - ask_user_question\n    - platform_control\n", 1)
			if updated == string(data) {
				t.Fatal("failed to mount platform_control")
			}
			if err := os.WriteFile(agentPath, []byte(updated), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})

	const chatID = "chat-run-env-restore-failure"
	const runID = "run-run-env-restore-failure"
	const awaitingID = "await-run-env-restore-failure"
	seedDeferredAwaitingPayload(t, fixture.chats, chatID, runID, awaitingID, "question", 60, time.Now().UnixMilli()-1000, map[string]any{
		"_runEnvRevision": 1,
		"questions":       []any{map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"}},
	})

	deps := deferredRestartDependencies(fixture, fixture.chats, contracts.NewNoopNotificationSink())
	root := t.TempDir()
	deps.RunEnvironments = runenv.NewStore(filepath.Join(root, "state"), filepath.Join(root, "identity", "missing.key"), runenv.Limits{})
	if _, err := New(deps); err != nil {
		t.Fatalf("run environment restore failure must terminalize only the affected run: %v", err)
	}
	assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "run_env_restore_failed")
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
	var runEnvRevision *uint64
	if rawRevision, exists := askPayload["_runEnvRevision"]; exists {
		revision := uint64(contracts.AnyIntNode(rawRevision))
		runEnvRevision = &revision
		delete(askPayload, "_runEnvRevision")
	}
	for key, value := range askPayload {
		ask[key] = value
	}
	if strings.EqualFold(mode, "planning") {
		planning := contracts.AnyMapNode(ask["planning"])
		if strings.TrimSpace(contracts.AnyStringNode(planning["planningFile"])) == "" {
			planningID := strings.TrimSpace(contracts.AnyStringNode(planning["planningId"]))
			planningFile := filepath.Join(store.ChatDir(chatID), chat.ToolRootDirName, chat.ToolPlanningDirName, planningID+".md")
			if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
				t.Fatalf("mkdir planning dir: %v", err)
			}
			if err := os.WriteFile(planningFile, []byte("# Planning"), 0o644); err != nil {
				t.Fatalf("write planning file: %v", err)
			}
			planning["planningFile"] = planningFile
			ask["planning"] = planning
		}
	}
	toolID := awaitingID
	toolName := "ask_user_question"
	if strings.EqualFold(mode, "approval") {
		toolName = "bash"
		if approvals, _ := ask["approvals"].([]any); len(approvals) > 0 {
			if candidate := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(approvals[0])["id"])); candidate != "" {
				toolID = candidate
			}
		}
	} else if strings.EqualFold(mode, "form") {
		toolName = "bash"
		if forms, _ := ask["forms"].([]any); len(forms) > 0 {
			if candidate := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(forms[0])["id"])); candidate != "" {
				toolID = candidate
			}
		}
	} else if strings.EqualFold(mode, "planning") {
		toolName = "finalize_planning"
	}
	messageTs := createdAt
	if err := store.AppendStepLine(chatID, chat.StepLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: createdAt,
		Type:      "react",
		Seq:       1,
		Messages: []chat.StoredMessage{{
			Role: "assistant",
			ToolCalls: []chat.StoredToolCall{{
				ID:       toolID,
				Type:     "function",
				Function: chat.StoredFunction{Name: toolName, Arguments: "{}"},
			}},
			Ts: &messageTs,
		}},
		Awaiting:       []map[string]any{ask},
		RunEnvRevision: runEnvRevision,
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

func deferredRestartDependencies(fixture testFixture, store chat.Store, notifications contracts.NotificationSink) Dependencies {
	return Dependencies{
		Config:          fixture.cfg,
		Chats:           store,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            fixture.runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
		Viewport:        fixture.viewport,
		CatalogReloader: fixture.catalogReloader,
		Notifications:   notifications,
	}
}

func assertAwaitingSubmitConflict(
	t *testing.T,
	server http.Handler,
	chatID string,
	runID string,
	awaitingID string,
	wantHTTPStatus int,
	wantErrorCode string,
	wantStatus string,
) {
	t.Helper()
	body, err := json.Marshal(api.SubmitRequest{
		ChatID:     chatID,
		AgentKey:   "mock-agent",
		RunID:      runID,
		AwaitingID: awaitingID,
		SubmitID:   "submit-after-terminal",
		Params: mustEncodeSubmitParams(t, []map[string]any{
			{"id": "q1", "answer": "Approve"},
		}),
	})
	if err != nil {
		t.Fatalf("marshal terminal submit: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(rec, req)
	if rec.Code != wantHTTPStatus {
		t.Fatalf("submit %s expected HTTP %d, got %d: %s", awaitingID, wantHTTPStatus, rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode terminal submit response: %v", err)
	}
	if response.Msg != wantErrorCode || contracts.AnyStringNode(response.Data["errorCode"]) != wantErrorCode || contracts.AnyStringNode(response.Data["status"]) != wantStatus {
		t.Fatalf("unexpected terminal response %#v", response)
	}
	if nested := contracts.AnyMapNode(response.Data["error"]); contracts.AnyStringNode(nested["code"]) != wantErrorCode {
		t.Fatalf("expected nested error code %s, got %#v", wantErrorCode, response.Data)
	}
	if contracts.AnyStringNode(response.Data["chatId"]) != chatID || contracts.AnyStringNode(response.Data["runId"]) != runID || contracts.AnyStringNode(response.Data["awaitingId"]) != awaitingID {
		t.Fatalf("expected terminal response identity, got %#v", response.Data)
	}
}

func assertRestartTerminalizedAwaiting(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, wantErrorCode string) {
	t.Helper()
	summary, err := store.Summary(chatID)
	if err != nil {
		t.Fatalf("load terminal summary: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected terminal pending awaiting cleared, got %#v", summary)
	}
	latest, err := store.LoadLatestAwaitingSubmit(chatID, awaitingID)
	if err != nil || latest == nil {
		t.Fatalf("load terminal answer: %#v, %v", latest, err)
	}
	if contracts.AnyStringNode(latest.Answer["status"]) != "error" || contracts.AnyStringNode(contracts.AnyMapNode(latest.Answer["error"])["code"]) != wantErrorCode {
		t.Fatalf("unexpected terminal answer %#v", latest.Answer)
	}
	if len(latest.Submit) != 0 {
		t.Fatalf("automatic terminal answer must not fabricate request.submit: %#v", latest.Submit)
	}

	lines := decodeDeferredChatJSONL(t, store, chatID)
	answerCount := 0
	toolResultCount := 0
	requestSubmitCount := 0
	for _, line := range lines {
		if strings.TrimSpace(contracts.AnyStringNode(line["_type"])) == "submit" {
			answer := contracts.AnyMapNode(line["answer"])
			if contracts.AnyStringNode(answer["awaitingId"]) == awaitingID {
				answerCount++
			}
			submit := contracts.AnyMapNode(line["submit"])
			if contracts.AnyStringNode(submit["awaitingId"]) == awaitingID {
				requestSubmitCount++
			}
		}
		if strings.TrimSpace(contracts.AnyStringNode(line["_type"])) != chat.StepLineTypeReactTool || contracts.AnyStringNode(line["runId"]) != runID {
			continue
		}
		messages, _ := line["messages"].([]any)
		for _, rawMessage := range messages {
			message := contracts.AnyMapNode(rawMessage)
			if contracts.AnyStringNode(message["role"]) != "tool" {
				continue
			}
			content, _ := message["content"].([]any)
			for _, rawPart := range content {
				part := contracts.AnyMapNode(rawPart)
				var payload map[string]any
				if err := json.Unmarshal([]byte(contracts.AnyStringNode(part["text"])), &payload); err != nil {
					continue
				}
				if contracts.AnyStringNode(payload["awaitingId"]) == awaitingID {
					toolResultCount++
					if payload["executed"] != false || contracts.AnyStringNode(payload["error"]) != wantErrorCode {
						t.Fatalf("unexpected terminal tool result %#v", payload)
					}
				}
			}
		}
	}
	if answerCount != 1 || toolResultCount != 1 || requestSubmitCount != 0 {
		t.Fatalf("terminal records answer=%d toolResult=%d requestSubmit=%d lines=%#v", answerCount, toolResultCount, requestSubmitCount, lines)
	}
	runs, err := store.ListRuns(chatID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load terminal run: %#v, %v", runs, err)
	}
	if runs[0].RunID != runID || runs[0].FinishReason != "cancel" || runs[0].CompletedAt <= 0 {
		t.Fatalf("unexpected terminal run %#v", runs[0])
	}
}

func decodeDeferredChatJSONL(t *testing.T, store chat.Store, chatID string) []map[string]any {
	t.Helper()
	content, err := store.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatalf("load chat jsonl: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	var lines []map[string]any
	for {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode chat jsonl: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
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
