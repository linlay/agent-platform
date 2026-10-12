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
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/llm"
	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/runtime/runstate"
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

func TestPersistDeferredAwaitingToolAnswerWritesReactToolLine(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStoreAtStartup(root)
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	chatID := "chat-react-tool"
	runID := "run-react-tool"
	awaitingID := "await-react-tool"
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "hello"); err != nil {
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

func TestRestartCancelsExpiredAwaiting(t *testing.T) {
	restartedRuns := runstate.NewManager()
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	seedDeferredAwaiting(t, fixture.chats, "chat-expired", "run-expired", "await-expired", "question", 1, time.Now().UnixMilli()-2000)

	restarted, err := newRuntimeServer(Dependencies{
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
	if !strings.Contains(rec.Body.String(), "awaiting_interrupted") {
		t.Fatalf("expected restart interruption, got %s", rec.Body.String())
	}

	summary, err := fixture.chats.Summary("chat-expired")
	if err != nil {
		t.Fatalf("load summary after expired submit: %v", err)
	}
	if summary == nil || summary.PendingAwaiting != nil {
		t.Fatalf("expected pending awaiting cleared after expired submit, got %#v", summary)
	}
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
			if _, err := newRuntimeServer(deferredRestartDependencies(fixture, runstate.NewManager(), failing, nil)); err == nil || !strings.Contains(err.Error(), "reconcile persisted awaitings") {
				t.Fatalf("expected startup reconciliation failure at %s, got %v", stage, err)
			}
			summary, err := fixture.chats.Summary(chatID)
			if err != nil {
				t.Fatalf("load pending summary after %s failure: %v", stage, err)
			}
			if summary == nil || summary.PendingAwaiting == nil {
				t.Fatalf("pending awaiting was cleared before %s completed: %#v", stage, summary)
			}

			if _, err := newRuntimeServer(deferredRestartDependencies(fixture, runstate.NewManager(), fixture.chats, nil)); err != nil {
				t.Fatalf("resume reconciliation after %s failure: %v", stage, err)
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "runtime_restarted")

			if err := fixture.chats.SetPendingAwaiting(chatID, chat.PendingAwaiting{
				AwaitingID: awaitingID,
				RunID:      runID,
				Mode:       "question",
				CreatedAt:  time.Now().UnixMilli() - 5_000,
			}); err != nil {
				t.Fatalf("restore stale pending marker: %v", err)
			}
			if _, err := newRuntimeServer(deferredRestartDependencies(fixture, runstate.NewManager(), fixture.chats, nil)); err != nil {
				t.Fatalf("repeat reconciliation after %s: %v", stage, err)
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "runtime_restarted")
		})
	}
}

func TestHydrationClearsDanglingAndAnsweredAwaitings(t *testing.T) {
	restartedRuns := runstate.NewManager()
	notifications := &recordingNotificationSink{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: notifications,
	})

	nowMs := time.Now().UnixMilli()
	if _, _, err := fixture.chats.EnsureChat("chat-dangling", "mock-agent", "hello"); err != nil {
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

	_, err := newRuntimeServer(Dependencies{
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

func seedDeferredAwaiting(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutSec int, createdAt int64, scopes ...controlscope.Scope) {
	t.Helper()
	seedDeferredAwaitingPayload(t, store, chatID, runID, awaitingID, mode, timeoutSec, createdAt, map[string]any{
		"questions": []any{
			map[string]any{"id": "q1", "question": "Need confirmation", "type": "text"},
		},
	}, scopes...)
}

func seedDeferredAwaitingPayload(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutSec int, createdAt int64, askPayload map[string]any, scopes ...controlscope.Scope) {
	seedDeferredAwaitingPayloadAndContent(t, store, chatID, runID, awaitingID, mode, timeoutSec, createdAt, askPayload, nil, scopes...)
}

func seedDeferredAwaitingPayloadAndContent(t *testing.T, store chat.Store, chatID string, runID string, awaitingID string, mode string, timeoutSec int, createdAt int64, askPayload map[string]any, content []chat.ContentPart, scopes ...controlscope.Scope) {
	t.Helper()
	if _, _, err := store.EnsureChat(chatID, "mock-agent", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	startServerFixtureRun(t, store, chatID, runID, createdAt, scopes...)
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
	if mode == "wait" {
		toolName = "wait"
	}
	if strings.EqualFold(mode, "approval") {
		toolName = "bash"
		if approvals, _ := ask["approvals"].([]any); len(approvals) > 0 {
			if candidate := strings.TrimSpace(contracts.AnyStringNode(contracts.AnyMapNode(approvals[0])["id"])); candidate != "" {
				toolID = candidate
			}
		}
	} else if strings.EqualFold(mode, "form") {
		toolName = "bash"
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
			Role:    "assistant",
			Content: content,
			ToolCalls: []chat.StoredToolCall{{
				ID:       toolID,
				Type:     "function",
				Function: chat.StoredFunction{Name: toolName, Arguments: "{}"},
			}},
			Ts: &messageTs,
		}},
		Awaiting: []map[string]any{ask},
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

func deferredRestartDependencies(fixture testFixture, runs *runstate.Manager, store chat.Store, notifications contracts.NotificationSink) Dependencies {
	return Dependencies{
		Config:          fixture.cfg,
		Chats:           store,
		Memory:          fixture.memories,
		Registry:        fixture.registry,
		Models:          fixture.modelRegistry,
		Runs:            runs,
		Agent:           fixture.agent,
		Tools:           fixture.tools,
		DeltaMappers:    llm.DeltaMapperFactory{Interactions: fixture.interactions},
		SystemInits:     llm.SystemInitProfileBuilder{Models: fixture.modelRegistry},
		Sandbox:         fixture.sandbox,
		MCP:             fixture.mcp,
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
			if contracts.AnyStringNode(answer["awaitingId"]) == awaitingID && contracts.AnyStringNode(contracts.AnyMapNode(answer["error"])["code"]) == wantErrorCode {
				answerCount++
			}
			submit := contracts.AnyMapNode(line["submit"])
			if contracts.AnyStringNode(submit["awaitingId"]) == awaitingID && contracts.AnyIntNode(submit["timestamp"]) == contracts.AnyIntNode(latest.Answer["timestamp"]) {
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
					_, executionClaim := payload["executed"]
					if contracts.AnyStringNode(payload["error"]) != wantErrorCode || (wantErrorCode == "runtime_restarted" && executionClaim) || (wantErrorCode != "runtime_restarted" && payload["executed"] != false) {
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
	if _, _, err := store.EnsureChat(chatID, "coder-app", "please plan first"); err != nil {
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
		"type":       "awaiting.ask",
		"awaitingId": awaitingID,
		"runId":      runID,
		"timestamp":  assistantTs,
		"mode":       "planning",
		"view":       map[string]any{"key": "planning"},
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
		if stringValue(query["message"]) == "执行计划" && stringValue(line["runId"]) != sourceRunID {
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
		!strings.Contains(textFromJSONLMessageContentForServerTest(message["content"]), "Execute the confirmed plan.") ||
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
