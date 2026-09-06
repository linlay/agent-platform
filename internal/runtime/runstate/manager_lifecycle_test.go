package runstate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"agent-platform/internal/stream"
)

func TestManagerRegisterDetachesFromParentContext(t *testing.T) {
	manager := newTestManager(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	runCtx, control, _ := manager.Register(parent, contracts.QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
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

func TestManagerFinishDestroysRunEnvironment(t *testing.T) {
	scope := runenv.NewScope(runenv.Limits{})
	if _, err := scope.Mutate(runenv.MutationRequest{Operation: runenv.OperationSet, Name: "DOCUMENT_ID", Value: "doc"}); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t)
	manager.Register(context.Background(), contracts.QuerySession{
		RunID: "run_env_cleanup", ChatID: "chat_env_cleanup", AgentKey: "office",
		RunOwner: contracts.AgentRunOwner("office", ""), RunEnvironment: scope,
	})
	if _, ok := manager.RunEnvironment("run_env_cleanup"); !ok {
		t.Fatal("active run environment is unavailable")
	}
	manager.Finish("run_env_cleanup")
	if _, ok := manager.RunEnvironment("run_env_cleanup"); ok {
		t.Fatal("finished run environment remains available through manager")
	}
	if _, _, err := scope.Snapshot(); !errors.Is(err, runenv.ErrClosed) {
		t.Fatalf("finished run environment snapshot error = %v, want ErrClosed", err)
	}
}

func TestManagerWebClientTargetIsLastWriterWins(t *testing.T) {
	manager := newTestManager(t)
	initial := contracts.WebClientTarget{SessionID: "ws-initial"}
	manager.Register(context.Background(), contracts.QuerySession{
		RunID:           "run_target",
		ChatID:          "chat_target",
		AgentKey:        "agent_1",
		RunOwner:        contracts.AgentRunOwner("agent_1", ""),
		WebClientTarget: initial,
	})

	if got, ok := manager.ResolveWebClientTarget("run_target"); !ok || got != initial {
		t.Fatalf("initial target = %#v, %v, want %#v", got, ok, initial)
	}
	latest := contracts.WebClientTarget{
		BoundaryKey: "subject:user\x00device:device-2",
		Subject:     "user",
		SurfaceID:   "surface-2",
	}
	if !manager.BindWebClientTarget("run_target", latest) {
		t.Fatal("expected latest target to bind")
	}
	if got, ok := manager.ResolveWebClientTarget("run_target"); !ok || got != latest {
		t.Fatalf("latest target = %#v, %v, want %#v", got, ok, latest)
	}
	if manager.BindWebClientTarget("run_target", contracts.WebClientTarget{}) {
		t.Fatal("zero target must not clear the latest binding")
	}
	if got, ok := manager.ResolveWebClientTarget("run_target"); !ok || got != latest {
		t.Fatalf("zero bind changed target to %#v, %v", got, ok)
	}
	if manager.BindWebClientTarget("missing", initial) {
		t.Fatal("missing run must reject target binding")
	}
}

func TestManagerWebClientTargetConcurrentBindsRemainAtomic(t *testing.T) {
	manager := newTestManager(t)
	manager.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_concurrent_target",
		ChatID:   "chat_concurrent_target",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
	})

	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			manager.BindWebClientTarget("run_concurrent_target", contracts.WebClientTarget{
				SessionID: fmt.Sprintf("ws-%d", index),
			})
		}(index)
	}
	wg.Wait()

	latest := contracts.WebClientTarget{SessionID: "ws-latest-completed"}
	if !manager.BindWebClientTarget("run_concurrent_target", latest) {
		t.Fatal("bind latest completed target")
	}
	if got, ok := manager.ResolveWebClientTarget("run_concurrent_target"); !ok || got != latest {
		t.Fatalf("target after concurrent binds = %#v, %v, want %#v", got, ok, latest)
	}
}

func TestManagerActiveRunForChatReturnsSingleActiveRun(t *testing.T) {
	manager := newTestManager(t)
	_, _, _ = manager.Register(context.Background(), contracts.QuerySession{
		RunID:       "run_1",
		ChatID:      "chat_1",
		AgentKey:    "agent_1",
		RunOwner:    contracts.AgentRunOwner("agent_1", ""),
		EditingMode: true,
	})

	status, ok, err := manager.ActiveRunForChat("chat_1")
	if err != nil {
		t.Fatalf("active run for chat: %v", err)
	}
	if !ok {
		t.Fatalf("expected active run for chat")
	}
	if status.RunID != "run_1" || status.ChatID != "chat_1" || !status.EditingMode {
		t.Fatalf("unexpected active run status %#v", status)
	}
}

func TestManagerRunScopeDoesNotBlockParentChat(t *testing.T) {
	manager := newTestManager(t)
	btw, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
		RunID:      "run_btw",
		ChatID:     "chat_1",
		RunScopeID: "btw:chat_1:btw_1",
		AgentKey:   "agent_1",
		RunOwner:   contracts.AgentRunOwner("agent_1", ""),
	})
	if err != nil || !btw.Registered {
		t.Fatalf("register BTW run: %#v err=%v", btw, err)
	}
	main, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
		RunID:    "run_main",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
	})
	if err != nil || !main.Registered {
		t.Fatalf("register parent run: %#v err=%v", main, err)
	}
	blocked, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
		RunID:      "run_btw_2",
		ChatID:     "chat_1",
		RunScopeID: "btw:chat_1:btw_1",
		AgentKey:   "agent_1",
		RunOwner:   contracts.AgentRunOwner("agent_1", ""),
	})
	if err != nil {
		t.Fatalf("register duplicate BTW: %v", err)
	}
	if blocked.Registered || blocked.ActiveRun.RunID != "run_btw" || blocked.ActiveRun.ChatID != "chat_1" {
		t.Fatalf("expected same BTW scope to be blocked, got %#v", blocked)
	}
}

func TestManagerActiveRunForChatReturnsConflictForMultipleRuns(t *testing.T) {
	manager := newTestManager(t)
	_, _, _ = manager.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
	})
	_, _, _ = manager.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_2",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
	})

	_, ok, err := manager.ActiveRunForChat("chat_1")
	if ok {
		t.Fatalf("expected conflict to suppress active run result")
	}
	var conflictErr *contracts.ActiveRunConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected ActiveRunConflictError, got %v", err)
	}
	if len(conflictErr.RunIDs) != 2 {
		t.Fatalf("expected both run ids in conflict, got %#v", conflictErr.RunIDs)
	}
}

func TestManagerRegisterExclusiveForChatAllowsOnlyOneActiveRun(t *testing.T) {
	manager := newTestManager(t)
	const attempts = 20
	start := make(chan struct{})
	results := make(chan contracts.ExclusiveRunRegistration, attempts)
	errs := make(chan error, attempts)

	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := manager.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{
				RunID:    "run_exclusive_" + string(rune('a'+index)),
				ChatID:   "chat_exclusive",
				AgentKey: "agent_1",
				RunOwner: contracts.AgentRunOwner("agent_1", ""),
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

func TestManagerUpdateAccessLevelPublishesEventAndStatus(t *testing.T) {
	manager := newTestManager(t)
	_, _, _ = manager.Register(context.Background(), contracts.QuerySession{
		RunID:       "run_access",
		ChatID:      "chat_1",
		AgentKey:    "agent_1",
		RunOwner:    contracts.AgentRunOwner("agent_1", ""),
		AccessLevel: contracts.AccessLevelDefault,
	})
	observer, err := manager.AttachObserver("run_access", 0)
	if err != nil {
		t.Fatalf("attach observer: %v", err)
	}
	defer manager.DetachObserver("run_access", observer.ID)

	ack := manager.UpdateAccessLevel(api.AccessLevelRequest{
		RunID:       "run_access",
		AgentKey:    "agent_1",
		AccessLevel: contracts.AccessLevelAutoApprove,
		Reason:      "test",
	})
	if !ack.Accepted || ack.Status != "updated" || ack.PreviousAccessLevel != contracts.AccessLevelDefault || ack.AccessLevel != contracts.AccessLevelAutoApprove || ack.Version != 2 {
		t.Fatalf("unexpected ack %#v", ack)
	}
	status, ok := manager.RunStatus("run_access")
	if !ok {
		t.Fatalf("expected run status")
	}
	if status.AccessLevel != contracts.AccessLevelAutoApprove || status.AccessLevelVersion != 2 {
		t.Fatalf("unexpected access level status %#v", status)
	}

	select {
	case event := <-observer.Events:
		if event.Type != "run.access_level.changed" {
			t.Fatalf("unexpected event %#v", event)
		}
		if event.String("previousAccessLevel") != contracts.AccessLevelDefault || event.String("accessLevel") != contracts.AccessLevelAutoApprove {
			t.Fatalf("unexpected event payload %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for access-level event")
	}

	unchanged := manager.UpdateAccessLevel(api.AccessLevelRequest{
		RunID:       "run_access",
		AgentKey:    "agent_1",
		AccessLevel: contracts.AccessLevelAutoApprove,
	})
	if !unchanged.Accepted || unchanged.Status != "unchanged" || unchanged.Version != 2 {
		t.Fatalf("unexpected unchanged ack %#v", unchanged)
	}
}

func TestManagerReaperPublishesExpiredRunErrorBeforeInterrupt(t *testing.T) {
	manager := newTestManager(t)
	manager.maxBackgroundDuration = time.Millisecond

	_, control, _ := manager.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_expired",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
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
	if info.Source != contracts.InterruptSourceReaper || info.Reason != contracts.InterruptReasonRunExpired || info.ChatID != "chat_1" {
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

func TestManagerReaperTreatsMaxBackgroundDurationAsGlobalLimit(t *testing.T) {
	manager := newTestManager(t)
	manager.maxBackgroundDuration = time.Millisecond

	_, control, _ := manager.Register(context.Background(), contracts.QuerySession{
		RunID:    "run_no_timeout",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""),
	})
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
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

func TestManagerRecoveredAwaitingIsAttachableAndClaimedOnce(t *testing.T) {
	manager := newTestManager(t)
	startedAt := time.Now().Add(-48 * time.Hour).UnixMilli()
	recovered, err := manager.RegisterRecoveredAwaiting(context.Background(), contracts.QuerySession{
		RunID: "run_recovered", ChatID: "chat_recovered", AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""), StartedAtMillis: startedAt,
	}, "await_1", 29)
	if err != nil {
		t.Fatalf("register recovered awaiting: %v", err)
	}
	status, ok := manager.RunStatus("run_recovered")
	if !ok || status.State != contracts.RunLoopStateWaitingSubmit || status.StartedAt != startedAt || status.LastSeq != 29 {
		t.Fatalf("unexpected recovered status %#v", status)
	}
	observer, err := manager.AttachObserver("run_recovered", 29)
	if err != nil {
		t.Fatalf("attach recovered run: %v", err)
	}
	defer manager.DetachObserver("run_recovered", observer.ID)
	if _, ok := manager.ResolveWebClientTarget("run_recovered"); ok {
		t.Fatal("recovered awaiting run must initially allow a missing target")
	}
	recoveredTarget := contracts.WebClientTarget{SessionID: "ws-recovered"}
	if !manager.BindWebClientTarget("run_recovered", recoveredTarget) {
		t.Fatal("bind target after recovered awaiting attach")
	}
	if got, ok := manager.ResolveWebClientTarget("run_recovered"); !ok || got != recoveredTarget {
		t.Fatalf("recovered awaiting target = %#v, %v, want %#v", got, ok, recoveredTarget)
	}
	claim, ok := manager.ClaimRecoveredAwaiting("run_recovered", "await_1")
	if !ok || claim.Control != recovered.Control || claim.EventBus != recovered.EventBus || claim.InitialSeq != 29 {
		t.Fatalf("unexpected recovered claim %#v ok=%v", claim, ok)
	}
	if _, duplicate := manager.ClaimRecoveredAwaiting("run_recovered", "await_1"); duplicate {
		t.Fatal("recovered awaiting must be claimed once")
	}
	if !manager.ActivateRecoveredAwaiting("run_recovered", "await_1") || manager.IsRecoveredAwaiting("run_recovered", "await_1") {
		t.Fatal("expected recovered awaiting to become a normal active run")
	}
}

func TestManagerRecoveredAwaitingReaperStartsAtHydration(t *testing.T) {
	manager := newTestManager(t)
	manager.maxBackgroundDuration = time.Hour
	_, err := manager.RegisterRecoveredAwaiting(context.Background(), contracts.QuerySession{
		RunID: "run_old_recovered", ChatID: "chat_old_recovered", AgentKey: "agent_1",
		RunOwner: contracts.AgentRunOwner("agent_1", ""), StartedAtMillis: time.Now().Add(-7 * 24 * time.Hour).UnixMilli(),
	}, "await_old", 7)
	if err != nil {
		t.Fatalf("register recovered awaiting: %v", err)
	}
	manager.reapExpiredRuns()
	status, ok := manager.RunStatus("run_old_recovered")
	if !ok || status.State != contracts.RunLoopStateWaitingSubmit {
		t.Fatalf("recovered run was reaped from persisted startedAt: %#v", status)
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
