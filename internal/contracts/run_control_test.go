package contracts

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/stream"
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

func TestRunControlAwaitSubmitNoTimeoutRemainsInfiniteWithoutObserver(t *testing.T) {
	control := NewRunControl(context.Background(), "run_1")
	control.SetObserverCount(0)
	control.ExpectSubmit(testAwaitingContext("await_1"))

	resultCh := make(chan SubmitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := control.AwaitSubmitWithTimeout(context.Background(), "await_1", 0)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatalf("did not expect timeout=0 wait to expire: %v", err)
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

func TestInMemoryRunManagerRunScopeDoesNotBlockParentChat(t *testing.T) {
	manager := NewInMemoryRunManager()
	btw, err := manager.RegisterExclusiveForChat(context.Background(), QuerySession{
		RunID:      "run_btw",
		ChatID:     "chat_1",
		RunScopeID: "btw:chat_1:btw_1",
		AgentKey:   "agent_1",
	})
	if err != nil || !btw.Registered {
		t.Fatalf("register BTW run: %#v err=%v", btw, err)
	}
	main, err := manager.RegisterExclusiveForChat(context.Background(), QuerySession{
		RunID:    "run_main",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})
	if err != nil || !main.Registered {
		t.Fatalf("register parent run: %#v err=%v", main, err)
	}
	blocked, err := manager.RegisterExclusiveForChat(context.Background(), QuerySession{
		RunID:      "run_btw_2",
		ChatID:     "chat_1",
		RunScopeID: "btw:chat_1:btw_1",
		AgentKey:   "agent_1",
	})
	if err != nil {
		t.Fatalf("register duplicate BTW: %v", err)
	}
	if blocked.Registered || blocked.ActiveRun.RunID != "run_btw" || blocked.ActiveRun.ChatID != "chat_1" {
		t.Fatalf("expected same BTW scope to be blocked, got %#v", blocked)
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

func TestInMemoryRunManagerRegisterExclusiveForChatAllowsOnlyOneActiveRun(t *testing.T) {
	manager := NewInMemoryRunManager()
	const attempts = 20
	start := make(chan struct{})
	results := make(chan ExclusiveRunRegistration, attempts)
	errs := make(chan error, attempts)

	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := manager.RegisterExclusiveForChat(context.Background(), QuerySession{
				RunID:    "run_exclusive_" + string(rune('a'+index)),
				ChatID:   "chat_exclusive",
				AgentKey: "agent_1",
			})
			results <- result
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	registered := 0
	blocked := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("register exclusive returned unexpected error: %v", err)
		}
	}
	for result := range results {
		if result.Registered {
			registered++
			continue
		}
		if result.ActiveRun.RunID == "" {
			t.Fatalf("blocked registration should include active run status: %#v", result)
		}
		blocked++
	}
	if registered != 1 || blocked != attempts-1 {
		t.Fatalf("expected one registered and %d blocked, got registered=%d blocked=%d", attempts-1, registered, blocked)
	}
}

func TestInMemoryRunManagerUpdateAccessLevelPublishesEventAndStatus(t *testing.T) {
	manager := NewInMemoryRunManager()
	_, _, _ = manager.Register(context.Background(), QuerySession{
		RunID:       "run_access",
		ChatID:      "chat_1",
		AgentKey:    "agent_1",
		AccessLevel: AccessLevelDefault,
	})
	observer, err := manager.AttachObserver("run_access", 0)
	if err != nil {
		t.Fatalf("attach observer: %v", err)
	}
	defer manager.DetachObserver("run_access", observer.ID)

	ack := manager.UpdateAccessLevel(api.AccessLevelRequest{
		RunID:       "run_access",
		AgentKey:    "agent_1",
		AccessLevel: AccessLevelAutoApprove,
		Reason:      "test",
	})
	if !ack.Accepted || ack.Status != "updated" || ack.PreviousAccessLevel != AccessLevelDefault || ack.AccessLevel != AccessLevelAutoApprove || ack.Version != 2 {
		t.Fatalf("unexpected ack %#v", ack)
	}
	status, ok := manager.RunStatus("run_access")
	if !ok {
		t.Fatalf("expected run status")
	}
	if status.AccessLevel != AccessLevelAutoApprove || status.AccessLevelVersion != 2 {
		t.Fatalf("unexpected access level status %#v", status)
	}

	select {
	case event := <-observer.Events:
		if event.Type != "run.access_level.changed" {
			t.Fatalf("unexpected event %#v", event)
		}
		if event.String("previousAccessLevel") != AccessLevelDefault || event.String("accessLevel") != AccessLevelAutoApprove {
			t.Fatalf("unexpected event payload %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for access-level event")
	}

	unchanged := manager.UpdateAccessLevel(api.AccessLevelRequest{
		RunID:       "run_access",
		AgentKey:    "agent_1",
		AccessLevel: AccessLevelAutoApprove,
	})
	if !unchanged.Accepted || unchanged.Status != "unchanged" || unchanged.Version != 2 {
		t.Fatalf("unexpected unchanged ack %#v", unchanged)
	}
}

func TestInMemoryRunManagerReaperPublishesExpiredRunErrorBeforeInterrupt(t *testing.T) {
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

	observer, err := eventBus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe replay: %v", err)
	}
	defer eventBus.Unsubscribe(observer.ID)

	first := mustReadEvent(t, observer.Events)
	manager.reapExpiredRuns()

	if !control.Interrupted() {
		t.Fatalf("expected run to be interrupted by reaper")
	}
	info, ok := control.InterruptInfo()
	if !ok {
		t.Fatalf("expected reaper interrupt info")
	}
	if info.Source != InterruptSourceReaper || info.Reason != InterruptReasonRunExpired || info.ChatID != "chat_1" {
		t.Fatalf("unexpected reaper interrupt info: %#v", info)
	}

	second := mustReadEvent(t, observer.Events)
	if first.Type != "run.start" {
		t.Fatalf("expected first replay event run.start, got %#v", first)
	}
	if second.Type != "run.error" {
		t.Fatalf("expected second replay event run.error, got %#v", second)
	}
	if second.String("runId") != "run_expired" {
		t.Fatalf("expected run.error payload to include runId, got %#v", second)
	}
	errorPayload, _ := second.Value("error").(map[string]any)
	if errorPayload["code"] != "expired" {
		t.Fatalf("expected run.error code expired, got %#v", second)
	}
}

func TestInMemoryRunManagerReaperTreatsMaxBackgroundDurationAsGlobalLimit(t *testing.T) {
	manager := NewInMemoryRunManager()
	manager.maxBackgroundDuration = time.Millisecond

	_, control, _ := manager.Register(context.Background(), QuerySession{
		RunID:    "run_no_timeout",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	})
	control.ExpectSubmit(AwaitingSubmitContext{
		AwaitingID: "await_plan",
		Mode:       "plan",
		ItemCount:  1,
		NoTimeout:  true,
	})
	eventBus, ok := manager.EventBus("run_no_timeout")
	if !ok {
		t.Fatalf("expected event bus")
	}
	eventBus.Publish(stream.EventData{
		Seq:       1,
		Type:      "run.start",
		Timestamp: time.Now().UnixMilli(),
		Payload:   map[string]any{"runId": "run_no_timeout"},
	})

	manager.mu.Lock()
	manager.runs["run_no_timeout"].startedAt = time.Now().Add(-time.Second)
	manager.mu.Unlock()

	manager.reapExpiredRuns()

	if !control.Interrupted() {
		t.Fatalf("expected no-timeout awaiting run to be interrupted by global reaper limit")
	}

	observer, err := eventBus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe replay: %v", err)
	}
	defer eventBus.Unsubscribe(observer.ID)

	first := mustReadEvent(t, observer.Events)
	if first.Type != "run.start" {
		t.Fatalf("expected first replay event run.start, got %#v", first)
	}
	second := mustReadEvent(t, observer.Events)
	if second.Type != "run.error" {
		t.Fatalf("expected second replay event run.error, got %#v", second)
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

func assertNoReplayEvent(t *testing.T, events <-chan stream.EventData) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("did not expect replay event, got %#v", event)
	case <-time.After(80 * time.Millisecond):
	}
}
