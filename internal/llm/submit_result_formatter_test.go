package llm

import (
	"testing"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
)

func TestFormatSubmitResultForLLM_DelegatesToFrontendHandler(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_ask_user_question_",
		Meta: map[string]any{
			"kind":               "frontend",
			"submitResultFormat": "qa",
		},
	}
	result := ToolExecutionResult{
		Output: `{"mode":"question"}`,
		Structured: map[string]any{
			"answers": []any{
				map[string]any{"question": "Pick a plan", "answer": "Weekend"},
				map[string]any{"question": "Preferred scenes", "answer": []any{"自然风光", "古镇"}},
			},
		},
	}

	got := formatSubmitResultForLLM(tool, frontendtools.NewDefaultRegistry(), result)
	want := "问题：Pick a plan\n回答：Weekend\n问题：Preferred scenes\n回答：自然风光, 古镇"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFormatSubmitResultForLLM_JSONCompact(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_ask_user_question_",
		Meta: map[string]any{
			"kind":               "frontend",
			"submitResultFormat": "json-compact",
		},
	}
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

	got := formatSubmitResultForLLM(tool, frontendtools.NewDefaultRegistry(), result)
	want := `{"answers":[{"answer":"Weekend","question":"Pick a plan"}],"mode":"question"}`
	if got != want {
		t.Fatalf("expected compact json %q, got %q", want, got)
	}
}

func TestFormatSubmitResultForLLM_FallsBackToRawWhenHandlerMissing(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "_missing_frontend_tool_",
		Meta: map[string]any{
			"kind":               "frontend",
			"submitResultFormat": "kv",
		},
	}
	result := ToolExecutionResult{
		Output:     `{"raw":true}`,
		Structured: map[string]any{"unexpected": true},
	}

	if got := formatSubmitResultForLLM(tool, frontendtools.NewRegistry(), result); got != result.Output {
		t.Fatalf("expected raw output fallback, got %q", got)
	}
}
