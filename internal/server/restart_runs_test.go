package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
)

func TestRestartCancelsEveryAwaitingAndAllowsFreshQuery(t *testing.T) {
	for _, mode := range []string{"question", "planning", "approval", "form", "wait"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if err := validateAwaitingToolPairs(request["messages"]); err != nil {
					t.Error(err)
				}
				calls.Add(1)
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"fresh request"},"finish_reason":"stop"}]}`, "[DONE]")
			})
			const chatID, runID, awaitingID = "restart-chat", "restart-run", "restart-await"
			createdAt := time.Now().UnixMilli() - 2000
			seedDeferredAwaitingPayloadAndContent(t, fixture.chats, chatID, runID, awaitingID, mode, 600, createdAt, map[string]any{"planning": map[string]any{"planningId": "restart-plan"}}, []chat.ContentPart{{Type: "text", Text: "saved partial response"}})
			if mode == "planning" {
				// Even an answer accepted before the crash must not start an
				// execution when this process starts or receives a retry.
				if err := fixture.chats.AppendSubmitLine(chatID, chat.SubmitLine{Type: "submit", ChatID: chatID, RunID: runID, UpdatedAt: createdAt + 1, Submit: map[string]any{"submitId": "old-submit", "timestamp": createdAt + 1}, Answer: map[string]any{"type": "awaiting.answer", "timestamp": createdAt + 1, "awaitingId": awaitingID, "runId": runID, "mode": mode, "planning": map[string]any{"decision": "approve"}}}); err != nil {
					t.Fatal(err)
				}
			}
			runs := runstate.NewManager()
			restarted, err := newRuntimeServer(deferredRestartDependencies(fixture, runs, fixture.chats, nil))
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("restart executed a saved request")
			}
			if _, found := runs.RunStatus(runID); found {
				t.Fatal("restart registered a historical Run")
			}
			assertRestartTerminalizedAwaiting(t, fixture.chats, chatID, runID, awaitingID, "runtime_restarted")
			completed, err := fixture.chats.ListRuns(chatID)
			if err != nil || len(completed) != 1 || completed[0].AssistantText != "saved partial response" {
				t.Fatalf("restart lost saved partial response: %+v %v", completed, err)
			}
			detail, err := restarted.loadChatDetail(context.Background(), chatID, false)
			if err != nil || detail.ActiveRun != nil || detail.Awaiting != nil {
				t.Fatalf("chat did not return to idle: active=%+v awaiting=%+v err=%v", detail.ActiveRun, detail.Awaiting, err)
			}
			assertAwaitingSubmitConflict(t, restarted, chatID, runID, awaitingID, http.StatusConflict, "awaiting_interrupted", "interrupted")
			before, _ := fixture.chats.LoadJSONLContent(chatID)
			if err := testQueryService(restarted).Reconcile(); err != nil {
				t.Fatal(err)
			}
			after, _ := fixture.chats.LoadJSONLContent(chatID)
			if before != after {
				t.Fatal("restart reconciliation rewrote terminal history")
			}
			rec := httptest.NewRecorder()
			restarted.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query?detail=full", strings.NewReader(`{"agentKey":"mock-agent","chatId":"restart-chat","runId":"fresh-run","message":"new request"}`)))
			if rec.Code != http.StatusOK || calls.Load() != 1 || !strings.Contains(rec.Body.String(), "fresh request") {
				t.Fatalf("fresh query failed: calls=%d status=%d body=%s", calls.Load(), rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRestartClosesInterruptedOrdinaryRunAndToolBatch(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID = "interrupted-chat", "interrupted-run"
	now := time.Now().UnixMilli() - 1000
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "hello"); err != nil {
		t.Fatal(err)
	}
	startServerFixtureRun(t, fixture.chats, chatID, runID, now)
	if err := fixture.chats.AppendQueryLine(chatID, chat.QueryLine{Type: "query", ChatID: chatID, RunID: runID, UpdatedAt: now, Query: map[string]any{"message": "interrupted request", "agentKey": "mock-agent"}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.chats.AppendStepLine(chatID, chat.StepLine{Type: chat.StepLineTypeReact, ChatID: chatID, RunID: runID, UpdatedAt: now, Seq: 1, Messages: []chat.StoredMessage{{Role: "assistant", Ts: &now, Content: []chat.ContentPart{{Type: "text", Text: "partial response"}}, ToolCalls: []chat.StoredToolCall{{ID: "finished", Type: "function", Function: chat.StoredFunction{Name: "datetime", Arguments: "{}"}}, {ID: "unknown", Type: "function", Function: chat.StoredFunction{Name: "bash", Arguments: "{}"}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.chats.AppendStepLine(chatID, chat.StepLine{Type: chat.StepLineTypeReactTool, ChatID: chatID, RunID: runID, UpdatedAt: now, Seq: 1, Messages: []chat.StoredMessage{{Role: "tool", Name: "datetime", ToolCallID: "finished", Ts: &now, Content: []chat.ContentPart{{Type: "text", Text: "recorded result"}}}}}); err != nil {
		t.Fatal(err)
	}
	runs := runstate.NewManager()
	restarted, err := newRuntimeServer(deferredRestartDependencies(fixture, runs, fixture.chats, nil))
	if err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.chats.ListRuns(chatID)
	if err != nil || len(completed) != 1 || completed[0].FinishReason != "cancel" || completed[0].StartedAt != now || completed[0].AssistantText != "partial response" {
		t.Fatalf("interrupted lifecycle/content lost: %+v %v", completed, err)
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAwaitingToolPairs(detail.RawMessages); err != nil {
		t.Fatal(err)
	}
	before, _ := fixture.chats.LoadJSONLContent(chatID)
	if err := testQueryService(restarted).Reconcile(); err != nil {
		t.Fatal(err)
	}
	after, _ := fixture.chats.LoadJSONLContent(chatID)
	if before != after || !strings.Contains(after, "runtime_restarted") || !strings.Contains(after, "recorded result") {
		t.Fatal("reconciliation was not idempotent or removed recorded content")
	}
}

func TestReconcilePreservesCurrentProcessRun(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID, runID = "live-reconcile-chat", "live-reconcile-run"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "hello"); err != nil {
		t.Fatal(err)
	}
	_, _, _ = fixture.runs.Register(context.Background(), contracts.QuerySession{RunID: runID, ChatID: chatID, AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent")})
	defer fixture.runs.Finish(runID)
	status, _ := fixture.runs.RunStatus(runID)
	startServerFixtureRun(t, fixture.chats, chatID, runID, status.StartedAt)
	if err := testQueryService(fixture.server).Reconcile(); err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.chats.ListRuns(chatID)
	if err != nil || len(completed) != 0 {
		t.Fatal("reconciliation canceled a live process Run")
	}
	if active, found := fixture.runs.RunStatus(runID); !found || active.CompletedAt != 0 {
		t.Fatal("live Run was lost")
	}
}
