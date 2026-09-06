package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

func TestAttachRunUsesRunStateObserver(t *testing.T) {
	runs := runstate.NewManager()
	_, _, _ = runs.Register(context.Background(), contracts.QuerySession{
		RunID: "run-1", ChatID: "chat-1", AgentKey: "agent-1",
		RunOwner: contracts.AgentRunOwner("agent-1", ""),
	})
	bus, ok := runs.EventBus("run-1")
	if !ok {
		t.Fatal("event bus missing")
	}
	bus.Publish(stream.EventData{Seq: 1, Type: "run.start"})

	service := NewService(Dependencies{Runs: runs})
	subscription, err := service.AttachRun(context.Background(), runtimetypes.RunRef{RunID: "run-1"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	event := <-subscription.Events
	if event.Seq != 1 || event.Type != "run.start" {
		t.Fatalf("event = %#v", event)
	}
}

func TestControlCommandsAreValidatedBeforeAdapters(t *testing.T) {
	service := NewService(Dependencies{})
	_, err := service.Submit(context.Background(), runtimetypes.SubmitCommand{})
	assertApplicationCode(t, err, apperrors.CodeInvalidRequest)
	_, err = service.Steer(context.Background(), runtimetypes.SteerCommand{})
	assertApplicationCode(t, err, apperrors.CodeInvalidRequest)
	_, err = service.Interrupt(context.Background(), runtimetypes.InterruptCommand{})
	assertApplicationCode(t, err, apperrors.CodeInvalidRequest)
	_, err = service.SetAccessLevel(context.Background(), runtimetypes.AccessLevelCommand{})
	assertApplicationCode(t, err, apperrors.CodeInvalidRequest)
}

func assertApplicationCode(t *testing.T, err error, want apperrors.Code) {
	t.Helper()
	var appErr *apperrors.Error
	if !errors.As(err, &appErr) || appErr.Code() != want {
		t.Fatalf("error = %T %v, want code %s", err, err, want)
	}
}

func TestSubscriptionCloseAllowsFreezeAndNextChatRun(t *testing.T) {
	for _, closeBeforeFreeze := range []bool{true, false} {
		name := "after_freeze"
		if closeBeforeFreeze {
			name = "before_freeze"
		}
		t.Run(name, func(t *testing.T) {
			runs := runstate.NewManager()
			registration, err := runs.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "run", ChatID: "chat"})
			if err != nil || !registration.Registered {
				t.Fatalf("register = %#v, err=%v", registration, err)
			}
			service := NewService(Dependencies{Runs: runs})
			subscription, err := service.AttachRun(context.Background(), runtimetypes.RunRef{RunID: "run"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer subscription.Close()
			bus, _ := runs.EventBus("run")
			bus.Publish(stream.EventData{Seq: 1, Type: "run.finish"})
			if closeBeforeFreeze {
				subscription.Close()
				if registration.Control.Interrupted() || registration.Context.Err() != nil {
					t.Fatal("closing subscription canceled run")
				}
			}
			finished := make(chan struct{})
			go func() {
				bus.FreezeAndWait()
				runs.Finish("run")
				close(finished)
			}()
			// The closed event channel proves freeze has removed the live observer.
			select {
			case event := <-subscription.Events:
				if event.Seq != 1 {
					t.Fatalf("terminal event = %#v", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminal event not delivered")
			}
			select {
			case _, open := <-subscription.Events:
				if open {
					t.Fatal("event channel remains open")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("freeze did not close observer")
			}
			if !closeBeforeFreeze {
				select {
				case <-finished:
					t.Fatal("finished before delivery acknowledgement")
				default:
				}
				blocked, err := runs.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "too-early", ChatID: "chat"})
				if err != nil || blocked.Registered || blocked.ActiveRun.RunID != "run" {
					t.Fatalf("registration before acknowledgement = %#v, err=%v", blocked, err)
				}
			}
			subscription.Close()
			subscription.Close()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("close did not unblock FreezeAndWait")
			}
			status, ok := runs.RunStatus("run")
			if !ok || status.ObserverCount != 0 || status.CompletedAt == 0 || status.State != contracts.RunLoopStateCompleted {
				t.Fatalf("finished status = %#v", status)
			}
			next, err := runs.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "next", ChatID: "chat"})
			if err != nil || !next.Registered {
				t.Fatalf("next registration = %#v, err=%v", next, err)
			}
			runs.Finish("next")
		})
	}
}
