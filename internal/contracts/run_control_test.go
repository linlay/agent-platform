package contracts

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
)

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
	control.ExpectSubmit("await_1")

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

	ack := control.ResolveSubmit(api.SubmitRequest{RunID: "run_1", AwaitingID: "await_1", Params: map[string]any{"ok": true}})
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
	control.ExpectSubmit("await_1")

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
	control.ExpectSubmit("await_1")

	first := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     map[string]any{"ok": true},
	})
	if !first.Accepted || first.Status != "accepted" {
		t.Fatalf("expected first submit accepted, got %#v", first)
	}

	second := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     map[string]any{"ok": false},
	})
	if second.Accepted || second.Status != "already_resolved" {
		t.Fatalf("expected second submit already resolved, got %#v", second)
	}
}
