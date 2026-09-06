package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type failedQueryStepStore struct{ chat.StepLineStore }

func (failedQueryStepStore) AppendQueryLine(string, chat.QueryLine) error {
	return errors.New("query persistence unavailable")
}

func TestSharedExecutorFailureAndContinuationLifecycle(t *testing.T) {
	for _, terminal := range []string{"complete", "error", "cancel", "persistence-error"} {
		t.Run(terminal, func(t *testing.T) {
			fixture := newTestFixture(t)
			req := api.QueryRequest{AgentKey: "mock-agent", ChatID: "lifecycle-chat", RunID: "lifecycle-run", Message: "hello"}
			prepared, err := fixture.server.prepareBlockingQuery(context.Background(), req, "zh-CN", "")
			if err != nil {
				t.Fatal(err)
			}
			registered, statusErr := fixture.server.registerQueryRun(context.Background(), prepared)
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			deltas := []contracts.AgentDelta{
				contracts.DeltaAwaitAsk{AwaitingID: "question-1", RunID: req.RunID, Mode: "question", Questions: []any{map[string]any{"question": "Choose", "type": "text"}}},
				contracts.DeltaAwaitingAnswer{AwaitingID: "question-1", Answer: map[string]any{"mode": "question", "status": "answered"}},
				contracts.DeltaContent{Text: "answer"},
				contracts.DeltaRunContinuation{SourceRunID: req.RunID, RunID: "next-run", ChatID: req.ChatID, Answer: map[string]any{"value": "yes"}},
			}
			switch terminal {
			case "error":
				deltas = append(deltas, contracts.DeltaError{Error: map[string]any{"message": "execution failed", "code": "stream_failed"}})
			case "cancel":
				deltas = append(deltas, contracts.DeltaRunCancel{RunID: req.RunID})
			}
			engine := &orchestratorAgentEngine{streams: []contracts.AgentStream{&stubOrchestratableStream{deltas: deltas}}}
			fixture.server.deps.Agent = engine
			bus, _ := fixture.server.deps.Runs.EventBus(req.RunID)
			params := fixture.server.localRunExecutorParams(prepared, registered, bus, nil)
			if terminal == "persistence-error" {
				params.StepWriter = chat.NewStepWriter(failedQueryStepStore{fixture.chats}, req.ChatID, req.RunID, "REACT")
			}
			var order []string
			var completed chat.RunCompletion
			complete := params.OnComplete
			params.OnComplete = func(value chat.RunCompletion) { completed = value; complete(value); order = append(order, "complete") }
			params.OnContinuation = func(value contracts.DeltaRunContinuation) (string, error) {
				if value.RunID != "next-run" {
					t.Fatalf("continuation=%#v", value)
				}
				order = append(order, "continuation")
				return value.RunID, nil
			}
			observedAwaiting := false
			params.ObserveEvent = func(event stream.EventData) {
				if event.Type == "awaiting.ask" {
					observedAwaiting = true
				}
			}
			result := runExecutor(params)
			expected := terminal
			if terminal == "persistence-error" {
				expected = "error"
				if result.Err == nil {
					t.Fatal("lost processing error")
				}
				if engine.index != 0 {
					t.Fatal("producer started after bootstrap persistence failure")
				}
			}
			if !result.Persisted || result.Completion.FinishReason != expected || !reflect.DeepEqual(completed, result.Completion) {
				t.Fatalf("result=%#v complete=%#v", result, completed)
			}
			wantOrder := []string{"complete"}
			if terminal == "complete" {
				wantOrder = append(wantOrder, "continuation")
			}
			if !reflect.DeepEqual(order, wantOrder) {
				t.Fatalf("order=%v want %v", order, wantOrder)
			}
			if terminal != "persistence-error" && !observedAwaiting {
				t.Fatal("lost normalized awaiting event")
			}
			summary, err := fixture.chats.Summary(req.ChatID)
			if err != nil {
				t.Fatal(err)
			}
			if summary.PendingAwaiting != nil {
				t.Fatalf("answer left pending awaiting: %#v", summary.PendingAwaiting)
			}
		})
	}
}
