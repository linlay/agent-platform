package contracts

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform/internal/api"
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

func TestRunControlInterruptInfoPreservesFirstCause(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	first := InterruptInfo{
		Source:    InterruptSourceHTTPAPI,
		Reason:    InterruptReasonUserCancelled,
		Detail:    "first cancel",
		RequestID: "request_1",
		ChatID:    "chat_1",
	}
	if !control.Interrupt(first) {
		t.Fatalf("expected first interrupt to be accepted")
	}
	if control.Interrupt(InterruptInfo{
		Source: InterruptSourceReaper,
		Reason: InterruptReasonRunExpired,
		Detail: "second cancel",
	}) {
		t.Fatalf("did not expect second interrupt to be accepted")
	}
	info, ok := control.InterruptInfo()
	if !ok {
		t.Fatalf("expected interrupt info")
	}
	if info.Source != InterruptSourceHTTPAPI || info.Reason != InterruptReasonUserCancelled || info.Detail != "first cancel" {
		t.Fatalf("unexpected interrupt info: %#v", info)
	}
	if info.RequestID != "request_1" || info.ChatID != "chat_1" || info.InterruptedAt.IsZero() {
		t.Fatalf("unexpected interrupt metadata: %#v", info)
	}
}

func TestRunControlFailureClaimRejectsLaterInterrupt(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")

	if !control.ClaimFailure() {
		t.Fatal("expected the first terminal claim to win")
	}
	if control.State() != RunLoopStateFailed {
		t.Fatalf("state = %s, want %s", control.State(), RunLoopStateFailed)
	}
	if control.Interrupt(InterruptInfo{
		Source: InterruptSourceHTTPAPI,
		Reason: InterruptReasonUserCancelled,
		Detail: "late cancel",
	}) {
		t.Fatal("expected interrupt after failure to be rejected")
	}
	control.TransitionState(RunLoopStateCancelled)
	control.Finish()
	if control.State() != RunLoopStateFailed {
		t.Fatalf("late terminal updates overwrote failure: %s", control.State())
	}
	if control.Interrupted() {
		t.Fatal("rejected interrupt must not mark the run interrupted")
	}
}

func TestRunControlDrainSteersBeforeFinishClosesEmptyQueue(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")

	if steers := control.DrainSteersBeforeFinish(); len(steers) != 0 {
		t.Fatalf("expected no steers, got %#v", steers)
	}
	if control.EnqueueSteer(api.SteerRequest{RunID: "run_1", Message: "too late"}) {
		t.Fatalf("expected steer to be rejected after finish gate closed")
	}
}

func TestRunControlDrainSteersBeforeFinishKeepsGateOpenWhenQueued(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	if !control.EnqueueSteer(api.SteerRequest{RunID: "run_1", Message: "first"}) {
		t.Fatalf("expected first steer to be accepted")
	}

	steers := control.DrainSteersBeforeFinish()
	if len(steers) != 1 || steers[0].Message != "first" {
		t.Fatalf("expected queued steer to drain, got %#v", steers)
	}
	if !control.EnqueueSteer(api.SteerRequest{RunID: "run_1", Message: "second"}) {
		t.Fatalf("expected steer gate to remain open after draining queued steer")
	}
	steers = control.DrainSteers()
	if len(steers) != 1 || steers[0].Message != "second" {
		t.Fatalf("expected second steer to drain normally, got %#v", steers)
	}
}

func TestRunControlDrainSteersBeforeFinishPreservesFIFO(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	for _, message := range []string{"first", "second", "third"} {
		if !control.EnqueueSteer(api.SteerRequest{RunID: "run_1", Message: message}) {
			t.Fatalf("expected steer %q to be accepted", message)
		}
	}

	steers := control.DrainSteersBeforeFinish()
	if len(steers) != 3 {
		t.Fatalf("expected three steers, got %#v", steers)
	}
	for index, want := range []string{"first", "second", "third"} {
		if steers[index].Message != want {
			t.Fatalf("steer[%d] = %q, want %q; all=%#v", index, steers[index].Message, want, steers)
		}
	}
}

func TestRunControlAwaitSubmitTimeoutUsesWallClockWithoutObserver(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetObserverCount(1)
	control.ExpectSubmit(testAwaitingContext("await_1"))

	errCh := make(chan error, 1)
	startedAt := time.Now()
	go func() {
		_, err := control.AwaitSubmitWithTimeout(context.Background(), "await_1", 120*time.Millisecond)
		errCh <- err
	}()

	time.Sleep(40 * time.Millisecond)
	control.SetObserverCount(0)

	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for submit timeout")
	}
	if elapsed := time.Since(startedAt); elapsed < 100*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("expected wall-clock timeout near configured window, elapsed=%s", elapsed)
	}

	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "ok"}}),
	})
	if ack.Accepted || ack.Status != "unmatched" {
		t.Fatalf("expected late submit after timeout to be unmatched, got %#v", ack)
	}
}

func TestRunControlAwaitSubmitIndefinitelyRemainsInfiniteWithoutObserver(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetObserverCount(0)
	control.ExpectSubmit(testAwaitingContext("await_1"))

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmitIndefinitely(context.Background(), "await_1")
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatalf("did not expect indefinite wait to expire: %v", err)
	case result := <-resultCh:
		t.Fatalf("did not expect awaiting to resolve before submit: %#v", result)
	case <-time.After(80 * time.Millisecond):
	}

	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "ok"}}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted for no-timeout wait, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "await_1" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for submit result")
	}
}

func TestRunControlAwaitSubmitNoTimeoutFlagIgnoresConfiguredTimeout(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetObserverCount(0)
	control.ExpectSubmit(AwaitingSubmitContext{
		AwaitingID: "await_1",
		Mode:       "approval",
		ItemCount:  1,
		NoTimeout:  true,
	})

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmitWithTimeout(context.Background(), "await_1", 40*time.Millisecond)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatalf("did not expect no-timeout awaiting to expire: %v", err)
	case result := <-resultCh:
		t.Fatalf("did not expect awaiting to resolve before submit: %#v", result)
	case <-time.After(80 * time.Millisecond):
	}

	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "await_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "confirm", "decision": "approve"}}),
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted for no-timeout awaiting, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "await_1" {
			t.Fatalf("unexpected result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for submit result")
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

func TestRunControlResolveSubmitAliasDeliversRawAwaitingID(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(AwaitingSubmitContext{
		AwaitingID:       "raw_await",
		PublicAwaitingID: "task_1:raw_await",
		TaskID:           "task_1",
		Mode:             "question",
		ItemCount:        1,
	})
	if ctx, ok := control.LookupAwaiting("task_1:raw_await"); !ok || ctx.AwaitingID != "raw_await" || ctx.PublicAwaitingID != "task_1:raw_await" {
		t.Fatalf("expected public awaiting lookup to return raw context, got %#v ok=%v", ctx, ok)
	}

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmitWithTimeout(context.Background(), "raw_await", time.Second)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "task_1:raw_await",
		SubmitID:   "submit_alias_1",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "ok"}}),
	})
	if !ack.Accepted || ack.Status != "accepted" {
		t.Fatalf("expected aliased submit accepted, got %#v", ack)
	}
	select {
	case err := <-errCh:
		t.Fatalf("expected raw submit result, got err %v", err)
	case result := <-resultCh:
		if result.Request.AwaitingID != "raw_await" {
			t.Fatalf("expected delivered request to use raw awaiting id, got %#v", result.Request)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for raw awaiting result")
	}

	duplicate := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "task_1:raw_await",
		SubmitID:   "submit_alias_2",
		Params:     testSubmitParams(t, []map[string]any{{"id": "q1", "answer": "again"}}),
	})
	if duplicate.Accepted || duplicate.Status != "already_resolved" || duplicate.SubmitID != "submit_alias_1" {
		t.Fatalf("expected duplicate aliased submit to be already resolved, got %#v", duplicate)
	}
}

func TestRunControlPreservesMergedAwaitingRoutesOnLifecycleRefresh(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(AwaitingSubmitContext{
		AwaitingID: "run_1_team_await_1",
		Mode:       "form",
		ItemCount:  1,
		Routes: []AwaitingSubmitRoute{{
			FieldID:    "run_1_team_t_1:raw_await",
			TaskID:     "run_1_team_t_1",
			AwaitingID: "raw_await",
			Mode:       "question",
			ItemCount:  1,
			Questions:  []any{map[string]any{"id": "q1"}},
		}},
	})
	// The generic run lifecycle observes the public awaiting event later and
	// re-registers it without internal routing metadata.
	control.ExpectSubmit(AwaitingSubmitContext{
		AwaitingID: "run_1_team_await_1",
		Mode:       "form",
		ItemCount:  1,
	})
	got, ok := control.LookupAwaiting("run_1_team_await_1")
	if !ok || len(got.Routes) != 1 {
		t.Fatalf("merged routes were lost: %#v ok=%v", got, ok)
	}
	if got.Routes[0].FieldID != "run_1_team_t_1:raw_await" || got.Routes[0].AwaitingID != "raw_await" {
		t.Fatalf("unexpected merged route %#v", got.Routes[0])
	}
	got.Routes[0].Questions[0].(map[string]any)["id"] = "mutated"
	again, _ := control.LookupAwaiting("run_1_team_await_1")
	if again.Routes[0].Questions[0].(map[string]any)["id"] != "q1" {
		t.Fatalf("LookupAwaiting leaked mutable route data: %#v", again.Routes)
	}
}
