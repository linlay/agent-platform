package frontendtools

import (
	"reflect"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func frontendTool(name string) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Name: name,
		Meta: map[string]any{
			"kind": "frontend",
		},
	}
}

func mustSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestAskUserQuestionHandlerBuildInitialAwaitAsk(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	awaitAsk := handler.BuildInitialAwaitAsk("tool_1", "run_1", frontendTool("_ask_user_question_"), map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, 0, 5000)
	if awaitAsk == nil {
		t.Fatal("expected initial await ask")
	}
	if awaitAsk.Mode != "question" || awaitAsk.AwaitingID != "tool_1" {
		t.Fatalf("unexpected await ask %#v", awaitAsk)
	}
	if awaitAsk.ViewportKey != "" || awaitAsk.ViewportType != "" {
		t.Fatalf("did not expect viewport metadata, got %#v", awaitAsk)
	}
	questions := awaitAsk.Questions
	if len(questions) != 2 {
		t.Fatalf("expected inline questions, got %#v", awaitAsk.Questions)
	}
	first := questions[0].(map[string]any)
	if first["id"] != "q1" || first["question"] != "Pick a plan" {
		t.Fatalf("unexpected first question %#v", first)
	}
}

func TestAskUserQuestionHandlerValidateArgs(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	if err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
		},
	}); err != nil {
		t.Fatalf("ValidateArgs returned error for valid args: %v", err)
	}

	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "options is required for select questions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerNormalizeSubmit(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Pick a plan",
				"type":     "select",
				"header":   "行程安排",
				"options":  []any{map[string]any{"label": "Weekend"}},
			},
			map[string]any{
				"question":    "Notification topics",
				"type":        "select",
				"header":      "通知内容",
				"multiple":    true,
				"options": []any{
					map[string]any{"label": "产品更新"},
					map[string]any{"label": "使用教程"},
				},
			},
		},
	}

	result, err := handler.NormalizeSubmit(args, mustSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
		{"answers": []string{"产品更新", "使用教程"}},
	}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if answers[0]["id"] != "q1" || answers[0]["header"] != "行程安排" {
		t.Fatalf("unexpected first answer %#v", answers[0])
	}
	if !reflect.DeepEqual(answers[1]["answer"], []string{"产品更新", "使用教程"}) {
		t.Fatalf("expected multi-select slice, got %#v", answers[1]["answer"])
	}
}

func TestAskUserQuestionHandlerValidateArgsRejectsLegacyMultiSelect(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	err := handler.ValidateArgs(map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question":    "Notification topics",
				"type":        "select",
				"multiSelect": true,
				"options":     []any{map[string]any{"label": "产品更新"}},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "multiSelect is no longer supported; use multiple") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerNormalizeSubmitIgnoresSubmittedIDs(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"id": "wrong-1", "answer": "Weekend"},
		{"id": "wrong-2", "answer": 2},
	}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 2 {
		t.Fatalf("expected normalized answers, got %#v", result["answers"])
	}
	if answers[0]["id"] != "q1" || answers[1]["id"] != "q2" {
		t.Fatalf("expected ids from question definitions, got %#v", answers)
	}
}

func TestAskUserQuestionHandlerNormalizeSubmitCancel(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{map[string]any{"question": "Pick a plan", "type": "select"}},
	}, mustSubmitParams(t, []map[string]any{}))
	if err != nil {
		t.Fatalf("NormalizeSubmit returned error: %v", err)
	}
	expected := map[string]any{
		"mode":      "question",
		"cancelled": true,
		"reason":    "user_dismissed",
	}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("unexpected cancel result %#v", result)
	}
}

func TestAskUserQuestionHandlerRejectsInvalidAnswerFields(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Notification topics", "type": "select", "multiple": true},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"id": "q1", "answer": "产品更新"},
	}))
	if err == nil || !strings.Contains(err.Error(), "answers is required for multiple questions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerRejectsAnswerCountMismatch(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	_, err := handler.NormalizeSubmit(map[string]any{
		"questions": []any{
			map[string]any{"question": "Pick a plan", "type": "select", "options": []any{map[string]any{"label": "Weekend"}}},
			map[string]any{"question": "How many people?", "type": "number"},
		},
	}, mustSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
	}))
	if err == nil || !strings.Contains(err.Error(), "expected 2 answers, got 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAskUserQuestionHandlerFormatSubmitResult(t *testing.T) {
	handler := NewAskUserQuestionHandler()
	result := contracts.ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"id": "q1", "question": "Pick a plan", "header": "行程安排", "answer": "Weekend"},
				map[string]any{"id": "q2", "question": "Subscription topics", "header": "订阅内容", "answer": []any{"产品更新", "使用教程"}},
			},
		},
	}
	if got, ok := handler.FormatSubmitResult("summary", result); !ok || got != "用户回答了以下问题:\n- 行程安排: Weekend\n- 订阅内容: 产品更新, 使用教程" {
		t.Fatalf("unexpected summary result: ok=%v got=%q", ok, got)
	}
}
