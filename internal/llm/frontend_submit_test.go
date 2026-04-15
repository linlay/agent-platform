package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
)

func TestNormalizeAskUserQuestionSubmit_ArrayParams(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"header":   "行程安排",
				"options": []any{
					map[string]any{"label": "Weekend"},
				},
			},
			map[string]any{
				"question": "How many people?",
				"type":     "number",
				"header":   "人数",
			},
		},
	}

	result, err := normalizeAskUserQuestionSubmit(args, []any{
		map[string]any{"question": "Pick a plan", "answer": "Weekend"},
		map[string]any{"question": "How many people?", "answer": 2},
	})
	if err != nil {
		t.Fatalf("normalizeAskUserQuestionSubmit returned error: %v", err)
	}
	if got := result["mode"]; got != "question" {
		t.Fatalf("expected mode=question, got %#v", got)
	}

	answers, ok := result["answers"].([]map[string]any)
	if !ok {
		t.Fatalf("expected answers slice, got %#v", result["answers"])
	}
	if len(answers) != 2 {
		t.Fatalf("expected 2 answers, got %#v", answers)
	}
	if answers[0]["header"] != "行程安排" {
		t.Fatalf("expected header to be preserved, got %#v", answers[0])
	}
	if answers[1]["answer"] != 2 {
		t.Fatalf("expected numeric answer to be preserved, got %#v", answers[1])
	}
}

func TestNormalizeAskUserQuestionSubmit_RejectsLegacyObjectParams(t *testing.T) {
	_, err := normalizeAskUserQuestionSubmit(map[string]any{}, map[string]any{
		"answers": []any{},
	})
	if err == nil {
		t.Fatal("expected error for legacy object params")
	}
	if !strings.Contains(err.Error(), "params must be an array") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeAskUserQuestionSubmit_EmptyArrayReturnsCancelled(t *testing.T) {
	result, err := normalizeAskUserQuestionSubmit(map[string]any{}, []any{})
	if err != nil {
		t.Fatalf("normalizeAskUserQuestionSubmit returned error: %v", err)
	}
	expected := map[string]any{
		"mode":      "question",
		"cancelled": true,
		"reason":    "user_dismissed",
	}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("expected cancelled result, got %#v", result)
	}
}

func TestNormalizeAskUserApprovalSubmit_EmptyObjectReturnsCancelled(t *testing.T) {
	result, err := normalizeAskUserApprovalSubmit(map[string]any{}, map[string]any{})
	if err != nil {
		t.Fatalf("normalizeAskUserApprovalSubmit returned error: %v", err)
	}
	expected := map[string]any{
		"mode":      "approval",
		"cancelled": true,
		"reason":    "user_dismissed",
	}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("expected cancelled result, got %#v", result)
	}
}

func TestNormalizeAskUserApprovalSubmit_RejectsValueAndFreeTextTogether(t *testing.T) {
	_, err := normalizeAskUserApprovalSubmit(map[string]any{
		"allowFreeText": true,
	}, map[string]any{
		"value":    "approve",
		"freeText": "override",
	})
	if err == nil {
		t.Fatal("expected error when both value and freeText are present")
	}
	if !strings.Contains(err.Error(), "exactly one of value or freeText") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeQuestionAnswer_SelectUnwrapsSingleItemArray(t *testing.T) {
	value, err := normalizeQuestionAnswer(map[string]any{
		"type": "select",
		"options": []any{
			map[string]any{"label": "Weekend"},
		},
	}, []any{"Weekend"})
	if err != nil {
		t.Fatalf("normalizeQuestionAnswer returned error: %v", err)
	}
	if value != "Weekend" {
		t.Fatalf("expected unwrapped string answer, got %#v", value)
	}
}

func TestFrontendSubmitCoordinatorAwait_AskUserQuestionPreservesRawParams(t *testing.T) {
	rawParams := []any{
		map[string]any{"question": "Pick a plan", "answer": "Weekend"},
		map[string]any{"question": "How many people?", "answer": 2},
	}
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit("tool_1")
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewFrontendSubmitCoordinator().Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
		},
	}, map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"header":   "行程安排",
				"options": []any{
					map[string]any{"label": "Weekend"},
				},
			},
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
	if result.SubmitInfo == nil || !reflect.DeepEqual(result.SubmitInfo.Params, rawParams) {
		t.Fatalf("expected SubmitInfo params to preserve original submit params, got %#v", result.SubmitInfo)
	}
	answers, ok := result.Structured["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers in Structured, got %#v", result.Structured)
	}
}

func TestFrontendSubmitCoordinatorAwait_AskUserQuestionCancelClearsRawParams(t *testing.T) {
	rawParams := []any{}
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit("tool_1")
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewFrontendSubmitCoordinator().Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_question_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
		},
	}, map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select"},
		},
	})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success exit code, got %#v", result)
	}
	if result.RawParams != nil {
		t.Fatalf("expected RawParams to be cleared for cancelled submit, got %#v", result.RawParams)
	}
	expected := map[string]any{
		"mode":      "question",
		"cancelled": true,
		"reason":    "user_dismissed",
	}
	if !reflect.DeepEqual(result.Structured, expected) {
		t.Fatalf("expected cancelled Structured payload, got %#v", result.Structured)
	}
	if result.SubmitInfo == nil || !reflect.DeepEqual(result.SubmitInfo.Params, rawParams) {
		t.Fatalf("expected SubmitInfo params to preserve original submit params, got %#v", result.SubmitInfo)
	}
}

func TestFrontendSubmitCoordinatorAwait_AskUserApprovalCancelClearsRawParams(t *testing.T) {
	rawParams := map[string]any{}
	control := contracts.NewRunControl(context.Background(), "run_1")
	control.ExpectSubmit("tool_1")
	ack := control.ResolveSubmit(api.SubmitRequest{
		RunID:      "run_1",
		AwaitingID: "tool_1",
		Params:     rawParams,
	})
	if !ack.Accepted {
		t.Fatalf("expected submit to be accepted, got %#v", ack)
	}

	result, err := NewFrontendSubmitCoordinator().Await(context.Background(), &contracts.ExecutionContext{
		RunControl:      control,
		CurrentToolID:   "tool_1",
		CurrentToolName: "_ask_user_approval_",
		Budget: contracts.Budget{
			Tool: contracts.RetryPolicy{TimeoutMs: 50},
		},
	}, map[string]any{
		"options": []any{
			map[string]any{"label": "Approve", "value": "approve"},
		},
	})
	if err != nil {
		t.Fatalf("Await returned error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success exit code, got %#v", result)
	}
	if result.RawParams != nil {
		t.Fatalf("expected RawParams to be cleared for cancelled submit, got %#v", result.RawParams)
	}
	expected := map[string]any{
		"mode":      "approval",
		"cancelled": true,
		"reason":    "user_dismissed",
	}
	if !reflect.DeepEqual(result.Structured, expected) {
		t.Fatalf("expected cancelled Structured payload, got %#v", result.Structured)
	}
	if result.SubmitInfo == nil || !reflect.DeepEqual(result.SubmitInfo.Params, rawParams) {
		t.Fatalf("expected SubmitInfo params to preserve original submit params, got %#v", result.SubmitInfo)
	}
}

func TestFrontendSubmitCoordinatorAwait_TimeoutReturnsCompactStructuredError(t *testing.T) {
	result, err := NewFrontendSubmitCoordinator().Await(context.Background(), &contracts.ExecutionContext{
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
	if result.ExitCode != -1 {
		t.Fatalf("expected timeout exit code -1, got %#v", result)
	}
	if !strings.Contains(result.Output, "Frontend tool submit timeout") {
		t.Fatalf("expected detailed timeout output, got %q", result.Output)
	}
	if len(result.Structured) != 2 {
		t.Fatalf("expected compact Structured timeout payload, got %#v", result.Structured)
	}
	if result.Structured["code"] != "frontend_submit_timeout" || result.Structured["message"] != "用户未在规定时间内回答" {
		t.Fatalf("unexpected timeout Structured payload: %#v", result.Structured)
	}
}
