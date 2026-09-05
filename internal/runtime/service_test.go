package runtime

import (
	"context"
	"errors"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

type fakeBackend struct {
	started runtimetypes.QueryCommand
	events  []stream.EventData
}

func (f *fakeBackend) StartQuery(_ context.Context, command runtimetypes.QueryCommand) (runtimetypes.RunHandle, error) {
	f.started = command
	return runtimetypes.RunHandle{RunID: "run-1", ChatID: "chat-1", Status: "running", Detached: true}, nil
}
func (f *fakeBackend) ExecuteQuery(_ context.Context, _ runtimetypes.QueryCommand, hooks runtimetypes.QueryHooks) (runtimetypes.QueryResult, error) {
	if hooks.OnRunStarted != nil {
		hooks.OnRunStarted(chat.RunStart{RunID: "run-1", ChatID: "chat-1"})
	}
	return runtimetypes.QueryResult{Content: "done"}, nil
}
func (f *fakeBackend) StartRun(context.Context, contracts.RunStartRequest) (contracts.RunSnapshot, error) {
	return contracts.RunSnapshot{}, nil
}
func (f *fakeBackend) RunStatus(string) (contracts.RunSnapshot, error) {
	return contracts.RunSnapshot{RunID: "run-1"}, nil
}
func (f *fakeBackend) AttachRun(context.Context, runtimetypes.RunRef, int64) (*runtimetypes.Subscription, error) {
	events := make(chan stream.EventData, len(f.events))
	for _, event := range f.events {
		events <- event
	}
	close(events)
	return runtimetypes.NewSubscription("observer-1", events, nil), nil
}

type collectingSink struct {
	starts []chat.RunStart
	events []stream.EventData
}

func (s *collectingSink) OnRunStarted(start chat.RunStart) { s.starts = append(s.starts, start) }
func (s *collectingSink) Emit(_ context.Context, event stream.EventData) error {
	s.events = append(s.events, event)
	return nil
}
func (f *fakeBackend) Submit(context.Context, runtimetypes.SubmitCommand) (runtimetypes.SubmitResult, error) {
	return runtimetypes.SubmitResult{Accepted: true}, nil
}
func (f *fakeBackend) Steer(context.Context, runtimetypes.SteerCommand) (runtimetypes.SteerResult, error) {
	return runtimetypes.SteerResult{Accepted: true}, nil
}
func (f *fakeBackend) Interrupt(context.Context, runtimetypes.InterruptCommand) (runtimetypes.InterruptResult, error) {
	return runtimetypes.InterruptResult{Accepted: true}, nil
}
func (f *fakeBackend) SetAccessLevel(context.Context, runtimetypes.AccessLevelCommand) (runtimetypes.AccessLevelResult, error) {
	return runtimetypes.AccessLevelResult{Accepted: true}, nil
}

func TestServiceRejectsCallsBeforeBinding(t *testing.T) {
	service := NewService()
	if _, err := service.StartQuery(context.Background(), runtimetypes.QueryCommand{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("StartQuery error = %v", err)
	}
}

func TestServiceDelegatesTransportNeutralCommand(t *testing.T) {
	service := NewService()
	backend := &fakeBackend{}
	service.Bind(backend)
	handle, err := service.StartQuery(context.Background(), runtimetypes.QueryCommand{AgentKey: "agent-1", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if handle.RunID != "run-1" || backend.started.AgentKey != "agent-1" || backend.started.Message != "hello" {
		t.Fatalf("handle=%#v command=%#v", handle, backend.started)
	}
}

func TestExecuteQueryForwardsRuntimeEventsToSink(t *testing.T) {
	service := NewService()
	backend := &fakeBackend{events: []stream.EventData{{Seq: 1, Type: "run.start"}, {Seq: 2, Type: "run.complete"}}}
	service.Bind(backend)
	sink := &collectingSink{}
	result, err := service.ExecuteQuery(context.Background(), runtimetypes.QueryCommand{AgentKey: "agent-1", Message: "hello"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || len(sink.starts) != 1 || len(sink.events) != 2 {
		t.Fatalf("result=%#v starts=%#v events=%#v", result, sink.starts, sink.events)
	}
	if sink.events[0].Seq != 1 || sink.events[1].Type != "run.complete" {
		t.Fatalf("event order changed: %#v", sink.events)
	}
}
