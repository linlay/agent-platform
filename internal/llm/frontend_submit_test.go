package llm

import (
	"strings"
	"testing"
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
