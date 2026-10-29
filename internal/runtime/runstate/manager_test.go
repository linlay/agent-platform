package runstate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager()
	t.Cleanup(func() { close(m.reaperStop) })
	return m
}

func TestManagerRetainsTerminalRunsUntilReaped(t *testing.T) {
	for _, terminal := range []contracts.RunLoopState{contracts.RunLoopStateCompleted, contracts.RunLoopStateFailed, contracts.RunLoopStateCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			m := newTestManager(t)
			_, control, _ := m.Register(context.Background(), contracts.QuerySession{RunID: "run", ChatID: "chat"})
			switch terminal {
			case contracts.RunLoopStateFailed:
				control.ClaimFailure()
			case contracts.RunLoopStateCancelled:
				m.Interrupt(api.InterruptRequest{RunID: "run"})
			}
			bus, _ := m.EventBus("run")
			bus.Publish(stream.EventData{Seq: 1, Type: "run.finish"})
			bus.FreezeAndWait()
			m.Finish("run")
			m.Finish("run")
			m.reapExpiredRuns()
			status, ok := m.RunStatus("run")
			if !ok || status.State != terminal || status.CompletedAt == 0 {
				t.Fatalf("terminal status = %#v, exists=%v", status, ok)
			}
			observer, err := m.AttachObserver("run", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.MarkDone()
			if event := mustReadEvent(t, observer.Events); event.Seq != 1 {
				t.Fatalf("terminal replay = %#v", event)
			}
			if _, open := <-observer.Events; open {
				t.Fatal("terminal replay remains open")
			}
			registration, err := m.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "next", ChatID: "chat"})
			if err != nil || !registration.Registered {
				t.Fatalf("next registration = %#v, err=%v", registration, err)
			}
			m.mu.Lock()
			m.runs["run"].completedAt = time.Now().Add(-m.completedRetention - time.Second)
			m.mu.Unlock()
			m.reapExpiredRuns()
			if _, ok := m.RunStatus("run"); ok {
				t.Fatal("expired terminal run still retained")
			}
			if _, err := m.AttachObserver("run", 0); !errors.Is(err, contracts.ErrRunControlUnavailable) {
				t.Fatalf("attach reaped run = %v", err)
			}
		})
	}
}

func TestManagerDefaultDurationDoesNotExpireOldActiveRun(t *testing.T) {
	m := newTestManager(t)
	_, control, _ := m.Register(context.Background(), contracts.QuerySession{
		RunID: "old", StartedAtMillis: time.Now().Add(-365 * 24 * time.Hour).UnixMilli(),
	})
	m.reapExpiredRuns()
	if control.Interrupted() {
		t.Fatal("default duration expired an active run")
	}
}

func TestManagerFinishConcurrentWithControlAndStatus(t *testing.T) {
	m := newTestManager(t)
	for i := 0; i < 100; i++ {
		m.Register(context.Background(), contracts.QuerySession{RunID: "run", ChatID: "chat"})
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, action := range []func(){
			func() { m.Finish("run") },
			func() {
				m.UpdateAccessLevel(api.AccessLevelRequest{RunID: "run", AccessLevel: contracts.AccessLevelAutoApprove})
			},
			func() { m.RunStatus("run"); m.ActiveRunForChat("chat") },
			func() { m.reapExpiredRuns() },
		} {
			wg.Add(1)
			go func(action func()) { defer wg.Done(); <-start; action() }(action)
		}
		close(start)
		wg.Wait()
		if status, _ := m.RunStatus("run"); status.State != contracts.RunLoopStateCompleted {
			t.Fatalf("concurrent finish status = %#v", status)
		}
	}
}

func TestManagerObserverLimitsAndReplayWindow(t *testing.T) {
	m := newTestManager(t)
	m.eventBusMaxEvents, m.maxObserversPerRun = 2, 1
	m.Register(context.Background(), contracts.QuerySession{RunID: "run"})
	bus, _ := m.EventBus("run")
	for seq := int64(1); seq <= 3; seq++ {
		bus.Publish(stream.EventData{Seq: seq})
	}
	if _, err := m.AttachObserver("run", 0); err == nil {
		t.Fatal("attach accepted cursor outside retained window")
	} else {
		var window *stream.ReplayWindowExceededError
		if !errors.As(err, &window) {
			t.Fatalf("replay error = %v", err)
		}
	}
	observer, err := m.AttachObserver("run", 1)
	if err != nil {
		t.Fatal(err)
	}
	for seq := int64(2); seq <= 3; seq++ {
		if event := mustReadEvent(t, observer.Events); event.Seq != seq {
			t.Fatalf("replay seq = %d, want %d", event.Seq, seq)
		}
	}
	if status, _ := m.RunStatus("run"); status.ObserverCount != 1 || status.OldestSeq != 2 || status.LastSeq != 3 {
		t.Fatalf("attached status = %#v", status)
	}
	if _, err := m.AttachObserver("run", 3); err == nil {
		t.Fatal("observer limit not enforced")
	} else {
		var limit *stream.ObserverLimitExceededError
		if !errors.As(err, &limit) {
			t.Fatalf("observer error = %v", err)
		}
	}
	m.DetachObserver("run", observer.ID)
	m.DetachObserver("run", observer.ID)
	select {
	case <-observer.Done():
	default:
		t.Fatal("detach did not acknowledge observer")
	}
	if status, _ := m.RunStatus("run"); status.ObserverCount != 0 {
		t.Fatalf("detached status = %#v", status)
	}
	replacement, err := m.AttachObserver("run", 3)
	if err != nil {
		t.Fatal(err)
	}
	m.DetachObserver("run", replacement.ID)
}
