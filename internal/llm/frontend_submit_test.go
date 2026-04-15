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
