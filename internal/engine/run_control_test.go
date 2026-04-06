package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
)

func TestRunControlAwaitSubmitReceivesResolvedPayload(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmit(context.Background(), "tool_1")
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	time.Sleep(10 * time.Millisecond)
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:  "run_1",
		ToolID: "tool_1",
		Params: map[string]any{"approved": true},
	})
	if !ack.Accepted || ack.Status != "accepted" {
		t.Fatalf("expected accepted submit ack, got %#v", ack)
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected await submit error: %v", err)
	case result := <-resultCh:
		params, _ := result.Request.Params.(map[string]any)
		if approved, _ := params["approved"].(bool); !approved {
			t.Fatalf("expected resolved submit params, got %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submit result")
	}
}

func TestRunControlFinishCancelsPendingSubmitWaiters(t *testing.T) {
	control := NewRunControl(context.Background(), "run_2")

	errCh := make(chan error, 1)
	go func() {
		_, err := control.AwaitSubmit(context.Background(), "tool_2")
		errCh <- err
	}()

	time.Sleep(10 * time.Millisecond)
	control.Finish()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrRunFinished) {
			t.Fatalf("expected ErrRunFinished, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submit waiter cancellation")
	}
}

func TestRunControlResolveSubmitOnlyAcceptsFirstDelivery(t *testing.T) {
	control := NewRunControl(context.Background(), "run_2b")

	done := make(chan struct{}, 1)
	go func() {
		_, _ = control.AwaitSubmit(context.Background(), "tool_2b")
		done <- struct{}{}
	}()

	time.Sleep(10 * time.Millisecond)
	first := control.ResolveSubmit(api.SubmitRequest{
		RunID:  "run_2b",
		ToolID: "tool_2b",
		Params: map[string]any{"attempt": 1},
	})
	second := control.ResolveSubmit(api.SubmitRequest{
		RunID:  "run_2b",
		ToolID: "tool_2b",
		Params: map[string]any{"attempt": 2},
	})

	if !first.Accepted || first.Status != "accepted" {
		t.Fatalf("expected first submit to be accepted, got %#v", first)
	}
	if second.Accepted || second.Status != "unmatched" {
		t.Fatalf("expected second submit to be unmatched, got %#v", second)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first submit delivery")
	}
}

func TestRunControlResolveSubmitAfterFinishReturnsUnmatched(t *testing.T) {
	control := NewRunControl(context.Background(), "run_2c")

	errCh := make(chan error, 1)
	go func() {
		_, err := control.AwaitSubmit(context.Background(), "tool_2c")
		errCh <- err
	}()

	time.Sleep(10 * time.Millisecond)
	control.Finish()
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:  "run_2c",
		ToolID: "tool_2c",
		Params: map[string]any{"attempt": 1},
	})
	if ack.Accepted || ack.Status != "unmatched" {
		t.Fatalf("expected submit after finish to be unmatched, got %#v", ack)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrRunFinished) {
			t.Fatalf("expected ErrRunFinished, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for submit waiter cancellation")
	}
}

func TestRunControlSteerQueuePreservesOrder(t *testing.T) {
	control := NewRunControl(context.Background(), "run_3")

	if !control.EnqueueSteer(api.SteerRequest{RunID: "run_3", SteerID: "s1", Message: "first"}) {
		t.Fatal("expected first steer to be accepted")
	}
	if !control.EnqueueSteer(api.SteerRequest{RunID: "run_3", SteerID: "s2", Message: "second"}) {
		t.Fatal("expected second steer to be accepted")
	}

	items := control.DrainSteers()
	if len(items) != 2 {
		t.Fatalf("expected two steer requests, got %#v", items)
	}
	if items[0].SteerID != "s1" || items[1].SteerID != "s2" {
		t.Fatalf("expected FIFO steer order, got %#v", items)
	}
	if drained := control.DrainSteers(); len(drained) != 0 {
		t.Fatalf("expected steer queue to be empty after drain, got %#v", drained)
	}
}

func TestInMemoryRunManagerInterruptRemovesActiveRun(t *testing.T) {
	manager := NewInMemoryRunManager()
	_, control, _ := manager.Register(context.Background(), QuerySession{
		RunID:    "run_4",
		ChatID:   "chat_4",
		AgentKey: "agent_4",
	})

	ack := manager.Interrupt(api.InterruptRequest{RunID: "run_4"})
	if !ack.Accepted || ack.Status != "accepted" {
		t.Fatalf("expected accepted interrupt ack, got %#v", ack)
	}
	if !control.Interrupted() {
		t.Fatal("expected run control to be interrupted")
	}
	if ack := manager.Submit(api.SubmitRequest{RunID: "run_4", ToolID: "tool_4"}); ack.Accepted {
		t.Fatalf("expected submit to be unmatched after interrupt, got %#v", ack)
	}
}
