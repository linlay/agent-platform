package llm

import (
	"testing"

	. "agent-platform-runner-go/internal/contracts"
)

func TestFormatSubmitResultForLLM_QuestionKVUsesHeaderThenQuestion(t *testing.T) {
	result := ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"question": "Pick a plan", "header": "行程安排", "answer": "Weekend"},
				map[string]any{"question": "How many people?", "answer": 2},
			},
		},
	}

	got := formatSubmitResultForLLM("_ask_user_question_", map[string]any{
		"submitResultFormat": "kv",
	}, result)
	want := "行程安排=Weekend; How many people?=2"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFormatSubmitResultForLLM_QuestionSummary(t *testing.T) {
	result := ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"question": "Pick a plan", "header": "行程安排", "answer": "Weekend"},
				map[string]any{"question": "How many people?", "header": "人数", "answer": 2},
			},
		},
	}

	got := formatSubmitResultForLLM("_ask_user_question_", map[string]any{
		"submitResultFormat": "summary",
	}, result)
	want := "用户回答了以下问题:\n- 行程安排: Weekend\n- 人数: 2"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFormatSubmitResultForLLM_ApprovalFormats(t *testing.T) {
	valueResult := ToolExecutionResult{
		Output:     `{"mode":"approval","value":"approve"}`,
		Structured: map[string]any{"value": "approve"},
	}
	if got := formatSubmitResultForLLM("_ask_user_approval_", map[string]any{
		"submitResultFormat": "summary",
	}, valueResult); got != "用户选择了: approve" {
		t.Fatalf("unexpected approval summary: %q", got)
	}
	if got := formatSubmitResultForLLM("_ask_user_approval_", map[string]any{
		"submitResultFormat": "kv",
	}, valueResult); got != "选择=approve" {
		t.Fatalf("unexpected approval kv: %q", got)
	}

	freeTextResult := ToolExecutionResult{
		Output:     `{"mode":"approval","freeText":"later"}`,
		Structured: map[string]any{"freeText": "later"},
	}
	if got := formatSubmitResultForLLM("_ask_user_approval_", map[string]any{
		"submitResultFormat": "summary",
	}, freeTextResult); got != "用户自由输入: later" {
		t.Fatalf("unexpected approval freeText summary: %q", got)
	}
}

func TestFormatSubmitResultForLLM_JSONCompact(t *testing.T) {
	result := ToolExecutionResult{
		Output: `{
  "mode": "question"
}`,
		Structured: map[string]any{
			"mode": "question",
			"answers": []any{
				map[string]any{"question": "Pick a plan", "answer": "Weekend"},
			},
		},
	}

	got := formatSubmitResultForLLM("_ask_user_question_", map[string]any{
		"submitResultFormat": "json-compact",
	}, result)
	want := `{"answers":[{"answer":"Weekend","question":"Pick a plan"}],"mode":"question"}`
	if got != want {
		t.Fatalf("expected compact json %q, got %q", want, got)
	}
}

func TestFormatSubmitResultForLLM_FallsBackToRaw(t *testing.T) {
	result := ToolExecutionResult{
		Output:     `{"raw":true}`,
		Structured: map[string]any{"unexpected": true},
	}

	for _, meta := range []map[string]any{
		nil,
		{"submitResultFormat": "unknown"},
		{"submitResultFormat": "kv"},
	} {
		if got := formatSubmitResultForLLM("_ask_user_question_", meta, result); got != result.Output {
			t.Fatalf("expected raw output fallback for meta %#v, got %q", meta, got)
		}
	}
}
