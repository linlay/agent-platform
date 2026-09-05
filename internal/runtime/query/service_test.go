package query

import (
	"context"
	"errors"
	"testing"

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
