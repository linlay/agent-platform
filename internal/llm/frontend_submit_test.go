package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
)

func frontendAwaitingContext(awaitingID string) contracts.AwaitingSubmitContext {
	return contracts.AwaitingSubmitContext{
		AwaitingID: awaitingID,
		Mode:       "question",
		ItemCount:  2,
	}
}

func frontendSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestFrontendSubmitCoordinatorAwait_AskUserQuestionPreservesRawParams(t *testing.T) {
	rawParams := frontendSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
		{"answer": 2},
	})
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(frontendAwaitingContext("tool_1"))
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewFrontendSubmitCoordinator(frontendtools.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
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
	if answers[0]["id"] != "q1" || answers[1]["id"] != "q2" {
		t.Fatalf("expected normalized ids from question definitions, got %#v", answers)
	}
}

func TestFrontendSubmitCoordinatorAwait_AskUserQuestionIgnoresSubmittedIDs(t *testing.T) {
	rawParams := frontendSubmitParams(t, []map[string]any{
		{"id": "wrong-1", "answer": "Weekend"},
		{"id": "wrong-2", "answer": 2},
	})
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(frontendAwaitingContext("tool_1"))
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewFrontendSubmitCoordinator(frontendtools.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
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

func TestFrontendSubmitCoordinatorAwait_AskUserQuestionCancelClearsRawParams(t *testing.T) {
	rawParams := frontendSubmitParams(t, []map[string]any{})
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

	result, err := NewFrontendSubmitCoordinator(frontendtools.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
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

func TestFrontendSubmitCoordinatorAwait_MissingHandlerReturnsConfigError(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: "tool_1", Mode: "question"})

	result, err := NewFrontendSubmitCoordinator(frontendtools.NewRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_missing_frontend_tool_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
		},
	}, map[string]any{"mode": "question"})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.Error != "frontend_tool_handler_not_registered" {
		t.Fatalf("expected missing handler error, got %#v", result)
	}
	if !strings.Contains(result.Output, "frontend tool handler not registered") {
		t.Fatalf("expected config error output, got %q", result.Output)
	}
}

func TestFrontendSubmitCoordinatorAwait_TimeoutReturnsCompactStructuredError(t *testing.T) {
	result, err := NewFrontendSubmitCoordinator(frontendtools.NewDefaultRegistry()).Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      contracts.NewRunControl(context.Background(), "run_1"),
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 1},
		},
	}, map[string]any{"mode": "question"})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.Error != "frontend_submit_timeout" {
		t.Fatalf("expected timeout error code, got %#v", result)
	}
	if !strings.Contains(result.Output, "Frontend tool submit timeout:") {
		t.Fatalf("expected readable timeout output, got %q", result.Output)
	}
	expected := map[string]any{
		"mode":   "question",
		"status": "error",
		"error": map[string]any{
			"code":    "timeout",
			"message": "等待项已超时",
		},
	}
	if !reflect.DeepEqual(result.Structured, expected) {
		t.Fatalf("expected timeout awaiting.error payload, got %#v", result.Structured)
	}
}
