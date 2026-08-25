package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

func TestExecutionHistoryServicePersistsCoalescedLifecycle(t *testing.T) {
	service := NewExecutionHistoryService(t.TempDir(), "executions.db", nil, nil)
	t.Cleanup(func() { _ = service.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	started := time.Now().UnixMilli()
	base := Execution{
		ID:             "exec-coalesced",
		AutomationID:   "daily",
		AutomationName: "Daily",
		ZoneID:         "UTC",
		QueryContent:   "hello",
		Status:         ExecutionStatusRunning,
		StartedAt:      started,
	}
	service.Submit(base)
	bound := base
	bound.ChatID = "chat-a"
	bound.RunID = "run-a"
	bound.RunStartedAt = executionInt64Ptr(started + 1)
	service.Submit(bound)
	completion := chat.RunCompletion{
		ChatID:          "chat-a",
		RunID:           "run-a",
		AssistantText:   "完整结果",
		InitialMessage:  "hello",
		FinishReason:    "complete",
		StartedAtMillis: started + 1,
		UpdatedAtMillis: started + 20,
	}
	service.Submit(completeExecution(base, &completion, "", nil))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		item, err := service.GetExecution(base.ID)
		if err == nil && item != nil && item.Status == ExecutionStatusSuccess {
			if item.ChatID != "chat-a" || item.RunID != "run-a" || item.ResultContent != "完整结果" {
				t.Fatalf("unexpected persisted execution %#v", item)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for persisted completion")
}

func TestExecutionHistoryServiceInitializationFailureDoesNotBlockSubmit(t *testing.T) {
	root := t.TempDir()
	notDirectory := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	service := NewExecutionHistoryService(notDirectory, "executions.db", nil, nil)
	t.Cleanup(func() { _ = service.Close() })

	started := time.Now()
	service.Submit(Execution{ID: "exec-unavailable", AutomationID: "daily", ZoneID: "UTC", Status: ExecutionStatusRunning, StartedAt: time.Now().UnixMilli()})
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("Submit blocked on unavailable database for %s", elapsed)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if service.Status().State == ExecutionHistoryUnavailable {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected unavailable state, got %#v", service.Status())
}

func TestExecutionHistoryServiceRetriesInitializationAndPersistsLatestCompletion(t *testing.T) {
	root := t.TempDir()
	historyDir := filepath.Join(root, "history")
	if err := os.WriteFile(historyDir, []byte("blocks mkdir"), 0o644); err != nil {
		t.Fatalf("write initialization blocker: %v", err)
	}
	service := NewExecutionHistoryService(historyDir, "executions.db", nil, nil)
	t.Cleanup(func() { _ = service.Close() })

	started := time.Now().UnixMilli()
	base := Execution{ID: "exec-retry", AutomationID: "daily", ZoneID: "UTC", QueryContent: "hello", Status: ExecutionStatusRunning, StartedAt: started}
	service.Submit(base)
	completion := chat.RunCompletion{ChatID: "chat-retry", RunID: "run-retry", AssistantText: "partial then complete", FinishReason: "complete", StartedAtMillis: started + 1, UpdatedAtMillis: started + 5}
	service.Submit(completeExecution(base, &completion, "", nil))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && service.Status().State != ExecutionHistoryUnavailable {
		time.Sleep(5 * time.Millisecond)
	}
	if service.Status().State != ExecutionHistoryUnavailable {
		t.Fatalf("expected initial unavailable state, got %#v", service.Status())
	}
	if err := os.Remove(historyDir); err != nil {
		t.Fatalf("remove initialization blocker: %v", err)
	}
	if err := os.Mkdir(historyDir, 0o755); err != nil {
		t.Fatalf("create history directory: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.WaitReady(ctx); err != nil {
		t.Fatalf("wait for retry recovery: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		item, err := service.GetExecution(base.ID)
		if err == nil && item != nil && item.Status == ExecutionStatusSuccess {
			if item.ResultContent != completion.AssistantText || item.RunID != completion.RunID {
				t.Fatalf("completion snapshot was not self-contained: %#v", item)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completion was not inserted after initialization recovered")
}

func TestExecutionHistoryServiceKeepsTerminalPendingSnapshotImmutable(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	service := NewExecutionHistoryService(blocked, "executions.db", nil, nil)
	t.Cleanup(func() { _ = service.Close() })
	started := time.Now().UnixMilli()
	base := Execution{ID: "exec-terminal", AutomationID: "daily", ZoneID: "UTC", Status: ExecutionStatusRunning, StartedAt: started}
	completion := chat.RunCompletion{ChatID: "chat-terminal", RunID: "run-terminal", AssistantText: "done", FinishReason: "complete", UpdatedAtMillis: started + 5}
	service.Submit(completeExecution(base, &completion, "", nil))
	lateBound := base
	lateBound.ChatID = "late-chat"
	lateBound.RunID = "late-run"
	service.Submit(lateBound)

	service.mu.RLock()
	pending := service.pending[base.ID].item
	service.mu.RUnlock()
	if pending.Status != ExecutionStatusSuccess || pending.RunID != "run-terminal" || pending.ResultContent != "done" {
		t.Fatalf("late running snapshot overwrote terminal completion: %#v", pending)
	}
}

func TestExecutionHistoryServiceEvictsOldestDistinctPendingExecution(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	service := NewExecutionHistoryService(blocked, "executions.db", nil, nil)
	service.capacity = 2
	t.Cleanup(func() { _ = service.Close() })
	started := time.Now().UnixMilli()
	for _, id := range []string{"exec-oldest", "exec-middle", "exec-newest"} {
		service.Submit(Execution{ID: id, AutomationID: "daily", ZoneID: "UTC", Status: ExecutionStatusRunning, StartedAt: started})
	}
	service.mu.RLock()
	_, oldestExists := service.pending["exec-oldest"]
	_, middleExists := service.pending["exec-middle"]
	_, newestExists := service.pending["exec-newest"]
	service.mu.RUnlock()
	if oldestExists || !middleExists || !newestExists {
		t.Fatalf("unexpected bounded pending set oldest=%v middle=%v newest=%v", oldestExists, middleExists, newestExists)
	}
}

func TestExecutionHistoryServiceReconcilesOnlyPreexistingRunningExecutions(t *testing.T) {
	root := t.TempDir()
	store, err := NewExecutionStore(root, "executions.db")
	if err != nil {
		t.Fatalf("create execution store: %v", err)
	}
	oldStarted := time.Now().Add(-time.Minute).UnixMilli()
	for _, item := range []Execution{
		{ID: "exec-recover", AutomationID: "daily", ZoneID: "UTC", ChatID: "chat-recover", RunID: "run-recover", Status: ExecutionStatusRunning, StartedAt: oldStarted},
		{ID: "exec-restart", AutomationID: "daily", ZoneID: "UTC", Status: ExecutionStatusRunning, StartedAt: oldStarted + 1},
	} {
		if err := store.Upsert(item); err != nil {
			t.Fatalf("seed running execution: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	service := NewExecutionHistoryService(root, "executions.db", nil, func(chatID, runID string) (*chat.RunSummary, error) {
		if chatID == "chat-recover" && runID == "run-recover" {
			return &chat.RunSummary{ChatID: chatID, RunID: runID, AssistantText: "recovered result", FinishReason: "complete", StartedAt: oldStarted, CompletedAt: oldStarted + 20}, nil
		}
		return nil, nil
	})
	t.Cleanup(func() { _ = service.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recovered, recoveredErr := service.GetExecution("exec-recover")
		restarted, restartedErr := service.GetExecution("exec-restart")
		if recoveredErr == nil && restartedErr == nil && recovered != nil && restarted != nil && recovered.Status != ExecutionStatusRunning && restarted.Status != ExecutionStatusRunning {
			if recovered.Status != ExecutionStatusSuccess || recovered.ResultContent != "recovered result" {
				t.Fatalf("unexpected recovered completion %#v", recovered)
			}
			if restarted.Status != ExecutionStatusFailed || !strings.Contains(restarted.Error, "platform restarted") {
				t.Fatalf("unexpected restarted completion %#v", restarted)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for running execution reconciliation")
}

type panicExecutionRecorder struct{}

func (panicExecutionRecorder) Submit(Execution) { panic("history failed") }

func TestDispatcherIsolatesExecutionRecorderPanic(t *testing.T) {
	dispatcher := NewDispatcher(func(_ context.Context, req api.QueryRequest, hooks QueryRunHooks) (QueryRunResult, error) {
		return successfulTestQuery(req, hooks), nil
	}, nil, panicExecutionRecorder{})
	if err := dispatcher.Dispatch(context.Background(), Definition{
		ID:       "daily",
		Name:     "Daily",
		Enabled:  true,
		AgentKey: "agent-a",
		Query:    Query{Message: "hello"},
	}, "UTC"); err != nil {
		t.Fatalf("history panic changed query result: %v", err)
	}
}
