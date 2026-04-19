package contracts

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/stream"
)

func testAwaitingContext(awaitingID string) AwaitingSubmitContext {
	return AwaitingSubmitContext{
		AwaitingID: awaitingID,
		Mode:       "question",
		ItemCount:  1,
	}
}

func testSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestInMemoryRunManagerRegisterDetachesFromParentContext(t *testing.T) {
	manager := NewInMemoryRunManager()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	runCtx, control, _ := manager.Register(parent, QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})

	cancel()

	select {
	case <-runCtx.Done():
		t.Fatalf("expected run context to remain active after parent cancellation")
	default:
	}
	if control.Interrupted() || control.Finished() {
		t.Fatalf("did not expect run to be interrupted or finished")
	}
}

func TestRunControlAwaitSubmitPausesTimeoutWithoutObserver(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetMaxDisconnectedWait(500 * time.Millisecond)
	control.SetObserverCount(1)
	control.ExpectSubmit(testAwaitingContext("await_1"))

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmitWithTimeout(context.Background(), "await_1", 120*time.Millisecond)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	time.Sleep(40 * time.Millisecond)
	control.SetObserverCount(0)
	time.Sleep(150 * time.Millisecond)
	control.SetObserverCount(1)

	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "ok"}}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	select {
	case err := <-errCh:
		t.Fatalf("expected paused timeout to survive disconnect, got %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "await_1" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for submit result")
	}
}

func TestRunControlAwaitSubmitHonorsMaxDisconnectedWait(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetMaxDisconnectedWait(80 * time.Millisecond)
	control.SetObserverCount(0)
	control.ExpectSubmit(testAwaitingContext("await_1"))

	startedAt := time.Now()
	_, err := control.AwaitSubmitWithTimeout(context.Background(), "await_1", time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed < 60*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("expected disconnect timeout to fire near configured window, elapsed=%s", elapsed)
	}
}

func TestRunControlResolveSubmitMarksAlreadyResolved(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(testAwaitingContext("await_1"))

	first := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "ok"}}),
	})
	if !first.Accepted || first.Status != "accepted" {
		t.Fatalf("expected first submit accepted, got %#v", first)
	}

	second := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "still-ok"}}),
	})
	if second.Accepted || second.Status != "already_resolved" {
		t.Fatalf("expected second submit already resolved, got %#v", second)
	}
}

func TestInMemoryRunManagerActiveRunForChatReturnsSingleActiveRun(t *testing.T) {
	manager := NewInMemoryRunManager()
	_, _, _ = manager.Register(context.Background(), QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})

	status, ok, err := manager.ActiveRunForChat("chat_1")
	if err != nil {
		t.Fatalf("active run for chat: %v", err)
	}
	if !ok {
		t.Fatalf("expected active run for chat")
	}
	if status.RunID != "run_1" || status.ChatID != "chat_1" {
		t.Fatalf("unexpected active run status %#v", status)
	}
}

func TestInMemoryRunManagerActiveRunForChatReturnsConflictForMultipleRuns(t *testing.T) {
	manager := NewInMemoryRunManager()
	_, _, _ = manager.Register(context.Background(), QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})
	_, _, _ = manager.Register(context.Background(), QuerySession{
		RunID:    "run_2",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})

	_, ok, err := manager.ActiveRunForChat("chat_1")
	if ok {
		t.Fatalf("expected conflict to suppress active run result")
	}
	var conflictErr *ActiveRunConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected ActiveRunConflictError, got %v", err)
	}
	if len(conflictErr.RunIDs) != 2 {
		t.Fatalf("expected both run ids in conflict, got %#v", conflictErr.RunIDs)
	}
}

func TestInMemoryRunManagerReaperPublishesRunExpiredBeforeInterrupt(t *testing.T) {
	manager := NewInMemoryRunManager()
	manager.maxBackgroundDuration = time.Millisecond

	_, control, _ := manager.Register(context.Background(), QuerySession{
		RunID:    "run_expired",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})
	eventBus, ok := manager.EventBus("run_expired")
	if !ok {
		t.Fatalf("expected event bus")
	}
	eventBus.Publish(stream.EventData{
		Seq:       1,
		Type:      "run.start",
		Timestamp: time.Now().UnixMilli(),
		Payload:   map[string]any{"runId": "run_expired"},
	})

	manager.mu.Lock()
	manager.runs["run_expired"].startedAt = time.Now().Add(-time.Second)
	manager.mu.Unlock()

	manager.reapExpiredRuns()

	if !control.Interrupted() {
		t.Fatalf("expected run to be interrupted by reaper")
	}

	observer, err := eventBus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe replay: %v", err)
	}
	defer eventBus.Unsubscribe(observer.ID)

	first := mustReadEvent(t, observer.Events)
	second := mustReadEvent(t, observer.Events)
	if first.Type != "run.start" {
		t.Fatalf("expected first replay event run.start, got %#v", first)
	}
	if second.Type != "run.expired" {
		t.Fatalf("expected second replay event run.expired, got %#v", second)
	}
	if second.String("runId") != "run_expired" {
		t.Fatalf("expected run.expired payload to include runId, got %#v", second)
	}
}

func mustReadEvent(t *testing.T, events <-chan stream.EventData) stream.EventData {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for replay event")
		return stream.EventData{}
	}
}
