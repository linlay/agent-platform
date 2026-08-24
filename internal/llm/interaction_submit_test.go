package llm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	contracts "agent-platform/internal/contracts"
	"agent-platform/internal/toolinteraction"
)

func interactionAwaitingContext(awaitingID string) contracts.AwaitingSubmitContext {
	return contracts.AwaitingSubmitContext{
		AwaitingID: awaitingID,
		Mode:       "question",
		ItemCount:  2,
	}
}

func interactionSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestInteractionSubmitCoordinatorAwait_AskUserQuestionPreservesRawParams(t *testing.T) {
	rawParams := interactionSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
		{"answer": 2},
	})
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(interactionAwaitingContext("tool_1"))
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 1},
		},
	}, map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "header": "行程安排", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number", "header": "人数"},
		},
	})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success exit code, got %#v", result)
	}
	if !reflect.DeepEqual(result.RawParams, rawParams) {
		t.Fatalf("expected RawParams to preserve original submit params, got %#v", result.RawParams)
	}
	answers, ok := result.Structured["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers in Structured, got %#v", result.Structured)
	}
	if result.Structured["status"] != "answered" {
		t.Fatalf("expected answered status in Structured, got %#v", result.Structured)
	}
	if result.Output != "问题：Pick a plan\n回答：Weekend\n问题：How many people?\n回答：2" {
		t.Fatalf("expected fixed QA model output, got %q", result.Output)
	}
	if answers[0]["id"] != "q1" || answers[1]["id"] != "q2" {
		t.Fatalf("expected normalized ids from question definitions, got %#v", answers)
	}
}

func TestInteractionSubmitCoordinatorAwait_CompactWakesAndPreservesAwaiting(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run-compact-wait")
	control.EnableContextCompact()
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: "tool-question",
		Mode:       "question",
		ItemCount:  1,
		NoTimeout:  true,
	})
	execCtx := &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool-question",
		CurrentToolName: "ask_user_question",
	}
	resultCh := make(chan error, 1)
	go func() {
		_, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), execCtx, map[string]any{
			"questions": []any{map[string]any{"id": "answer", "question": "continue?"}},
		})
		resultCh <- err
	}()

	deadline := time.Now().Add(time.Second)
	for control.State() != contracts.RunLoopStateWaitingSubmit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if control.State() != contracts.RunLoopStateWaitingSubmit {
		t.Fatalf("run state = %s, want %s", control.State(), contracts.RunLoopStateWaitingSubmit)
	}
	if _, status := control.EnqueueCompact(contracts.CompactControlRequest{
		RequestID: "compact-request",
		CompactID: "compact-id",
		ChatID:    "chat-1",
		Trigger:   "manual",
		Level:     "summary",
	}); status != "queued" {
		t.Fatalf("enqueue compact status = %q, want queued", status)
	}

	select {
	case err := <-resultCh:
		if !errors.Is(err, contracts.ErrContextCompactPending) {
			t.Fatalf("await error = %v, want %v", err, contracts.ErrContextCompactPending)
		}
	case <-time.After(time.Second):
		t.Fatal("interaction wait did not wake for compact")
	}
	if _, ok := control.LookupAwaiting("tool-question"); !ok {
		t.Fatal("awaiting registration was cleared while compact was pending")
	}
}

func TestInteractionSubmitCoordinatorAwait_AskUserQuestionIgnoresSubmittedIDs(t *testing.T) {
	rawParams := interactionSubmitParams(t, []map[string]any{
		{"id": "wrong-1", "answer": "Weekend"},
		{"id": "wrong-2", "answer": 2},
	})
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(interactionAwaitingContext("tool_1"))
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 1},
		},
	}, map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "header": "行程安排", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number", "header": "人数"},
		},
	})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	answers, ok := result.Structured["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers in Structured, got %#v", result.Structured)
	}
	if result.Structured["status"] != "answered" {
		t.Fatalf("expected answered status in Structured, got %#v", result.Structured)
	}
	if answers[0]["id"] != "q1" || answers[1]["id"] != "q2" {
		t.Fatalf("expected ids from question definitions, got %#v", answers)
	}
}

func TestInteractionSubmitCoordinatorAwait_AskUserQuestionCancelClearsRawParams(t *testing.T) {
	rawParams := interactionSubmitParams(t, []map[string]any{})
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: "tool_1",
		Mode:       "question",
		ItemCount:  1,
	})
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 1},
		},
	}, map[string]any{
		"questions": []any{map[string]any{"question": "Pick a plan", "type": "select"}},
	})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.RawParams != nil && !reflect.DeepEqual(result.RawParams, api.SubmitParams(nil)) {
		t.Fatalf("expected RawParams to be cleared for cancelled submit, got %#v", result.RawParams)
	}
	expected := map[string]any{
		"mode":   "question",
		"status": "error",
		"error": map[string]any{
			"code":    "user_dismissed",
			"message": "用户关闭等待项",
		},
	}
	if !reflect.DeepEqual(result.Structured, expected) {
		t.Fatalf("expected cancelled Structured payload, got %#v", result.Structured)
	}
}

func TestInteractionSubmitCoordinatorAwait_MissingHandlerReturnsConfigError(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "tool_1", Mode: "question"})

	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_missing_interaction_tool_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 1},
		},
	}, map[string]any{"mode": "question"})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.Error != "tool_interaction_handler_not_registered" {
		t.Fatalf("expected missing handler error, got %#v", result)
	}
	if !strings.Contains(result.Output, "tool interaction handler not registered") {
		t.Fatalf("expected config error output, got %q", result.Output)
	}
}

func TestInteractionSubmitCoordinatorAwait_TimeoutReturnsCompactStructuredError(t *testing.T) {
	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      contracts.NewRunControl(context.Background(), "run_1"),
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Hitl: contracts.HitlPolicy{Timeout: 1},
		},
	}, map[string]any{"mode": "question"})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.Error != "tool_interaction_timeout" {
		t.Fatalf("expected timeout error code, got %#v", result)
	}
	if !strings.Contains(result.Output, "Tool interaction submit timeout:") {
		t.Fatalf("expected readable timeout output, got %q", result.Output)
	}
	expected := map[string]any{
		"mode":   "question",
		"status": "error",
		"error": map[string]any{
			"code":           "timeout",
			"message":        "等待项已超时（1秒）。原因：等待问题回复，超过配置的 1 秒未收到有效提交。",
			"timeoutSeconds": int64(1),
			"elapsedSeconds": int64(1),
			"reason":         "submit_not_received_before_timeout",
		},
	}
	if !reflect.DeepEqual(result.Structured, expected) {
		t.Fatalf("expected timeout awaiting.error payload, got %#v", result.Structured)
	}
}

func TestInteractionSubmitCoordinatorAwait_UsesAwaitingAskTimeoutOverToolBudget(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.SetObserverCount(0)
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: "tool_1",
		Mode:       "question",
		ItemCount:  1,
		Timeout:    1,
	})

	result, err := NewInteractionSubmitCoordinator(toolinteraction.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 1200},
		},
	}, map[string]any{"mode": "question"})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.Error != "tool_interaction_timeout" {
		t.Fatalf("expected timeout error code, got %#v", result)
	}
	if !strings.Contains(result.Output, "timeout=1") {
		t.Fatalf("expected awaiting.ask timeout to drive submit wait, got %q", result.Output)
	}
}

func TestInteractionSubmitTimeoutUsesDisplayedAwaitingAskTimeout(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: "tool_1",
		Mode:       "question",
		ItemCount:  1,
		Timeout:    600,
	})

	timeout := interactionSubmitTimeout(&contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "ask_user_question",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{Timeout: 120},
		},
	})
	if timeout.Seconds() != 600 {
		t.Fatalf("expected awaiting.ask.timeout 600 to drive backend wait, got %d", int64(timeout.Seconds()))
	}
}
