package voice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	rt "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type recoveryRuntime struct {
	Runtime
	execute   func(context.Context, rt.QueryCommand, rt.QueryHooks) (rt.QueryResult, error)
	attach    func() *rt.Subscription
	interrupt func() (rt.InterruptResult, error)
}

func (r recoveryRuntime) ExecuteQueryWithHooks(ctx context.Context, cmd rt.QueryCommand, hooks rt.QueryHooks) (rt.QueryResult, error) {
	return r.execute(ctx, cmd, hooks)
}
func (r recoveryRuntime) AttachRun(context.Context, rt.RunRef, int64) (*rt.Subscription, error) {
	return r.attach(), nil
}
func (r recoveryRuntime) Interrupt(context.Context, rt.InterruptCommand) (rt.InterruptResult, error) {
	return r.interrupt()
}

type recoveryRuns struct{ active string }

func (r recoveryRuns) ActiveRunForChat(string) (contracts.RunStatusInfo, bool, error) {
	return contracts.RunStatusInfo{RunID: r.active}, r.active != "", nil
}

func TestVoiceInterruptRetriesFailureButCoalescesPendingAndAccepted(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "rejected"}[rejected], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int32
				release := make(chan struct{})
				runtime := recoveryRuntime{interrupt: func() (rt.InterruptResult, error) {
					if calls.Add(1) == 1 {
						<-release
						if rejected {
							return rt.InterruptResult{}, nil
						}
						return rt.InterruptResult{}, errors.New("proxy unavailable")
					}
					return rt.InterruptResult{Accepted: true}, nil
				}}
				service := NewService(context.Background(), Dependencies{Runtime: runtime, Runs: recoveryRuns{active: "owned"}})
				defer service.Close()
				s := &session{service: service, ctx: service.ctx, events: make(chan sessionEvent, 16)}
				run := &ownedRun{}
				run.cancelled.Store(true)
				s.interrupt(run) // A late RunID must remain cancellable.
				if calls.Load() != 0 {
					t.Fatal("interrupted without identity")
				}
				run.ref.Store(&rt.RunRef{RunID: "owned", ChatID: "chat"})
				s.interrupt(run)
				synctest.Wait()
				s.interrupt(run)
				if calls.Load() != 1 {
					t.Fatal("duplicated in-flight interrupt")
				}
				close(release)
				synctest.Wait()
				if e := <-s.events; e.kind != "interruptFailed" || e.run != run {
					t.Fatal(e)
				}
				s.interrupt(run)
				synctest.Wait()
				s.interrupt(run)
				synctest.Wait()
				if calls.Load() != 2 || run.interruptState.Load() != interruptAccepted {
					t.Fatalf("calls=%d state=%d", calls.Load(), run.interruptState.Load())
				}
			})
		})
	}
}

func TestVoiceInterruptAlreadyFinishedIsNotRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		service := NewService(context.Background(), Dependencies{Runtime: recoveryRuntime{interrupt: func() (rt.InterruptResult, error) { calls.Add(1); return rt.InterruptResult{Accepted: false}, nil }}, Runs: recoveryRuns{}})
		defer service.Close()
		s := &session{service: service, ctx: service.ctx, events: make(chan sessionEvent, 16)}
		run := &ownedRun{}
		run.cancelled.Store(true)
		run.ref.Store(&rt.RunRef{RunID: "finished", ChatID: "chat"})
		s.interrupt(run)
		synctest.Wait()
		s.interrupt(run)
		synctest.Wait()
		if calls.Load() != 1 || len(s.events) != 0 {
			t.Fatal("finished Run treated as a failed interrupt")
		}
	})
}

func TestVoiceBoundsObservationAfterQueryReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := false
		runtime := recoveryRuntime{
			execute: func(_ context.Context, _ rt.QueryCommand, hooks rt.QueryHooks) (rt.QueryResult, error) {
				hooks.OnRunStarted(chat.RunStart{RunID: "run", ChatID: "chat"})
				return rt.QueryResult{}, errors.New("registration failed")
			},
			attach: func() *rt.Subscription {
				return rt.NewSubscription("observer", make(chan stream.EventData), func() { closed = true })
			},
		}
		service := NewService(context.Background(), Dependencies{Runtime: runtime, Runs: recoveryRuns{}})
		defer service.Close()
		ctx, cancel := context.WithCancel(service.ctx)
		defer cancel()
		s := &session{service: service, ctx: ctx, chatID: "chat", events: make(chan sessionEvent, 16)}
		turnCtx, turnCancel := context.WithCancel(ctx)
		defer turnCancel()
		turn := &turn{ctx: turnCtx, cancel: turnCancel, number: 1, text: "question"}
		s.current = turn
		if err := s.submit(turn); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if e := <-s.events; e.kind != "runStarted" {
			t.Fatal(e)
		}
		time.Sleep(finishTimeout)
		synctest.Wait()
		select {
		case e := <-s.events:
			if e.kind != "releaseFailed" || !closed {
				t.Fatalf("event=%s closed=%v", e.kind, closed)
			}
		default:
			t.Fatal("observer wait exceeded the finish deadline")
		}
	})
}
