package llm

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
)

func TestFormatSubmitResultForLLM_DelegatesToFrontendHandler(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "ask_user_question",
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
		Name: "ask_user_question",
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

func TestFormatSubmitResultForLLM_JSONCompactOmitsBase64Content(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "file_read",
		Meta: map[string]any{
			"submitResultFormat": "json-compact",
		},
	}
	result := ToolExecutionResult{
		Output: `{"kind":"image"}`,
		Structured: map[string]any{
			"filePath":      "/tmp/page1.png",
			"kind":          "image",
			"mimeType":      "image/png",
			"sizeBytes":     int64(573750),
			"contentBase64": "iVBORw0KGgoAAAANSUhEUgAA",
		},
	}

	got := formatSubmitResultForLLM(tool, frontendtools.NewDefaultRegistry(), result)
	if strings.Contains(got, "iVBORw0KGgoAAAANSUhEUgAA") {
		t.Fatalf("expected compact LLM result to omit raw base64, got %q", got)
	}
	if !strings.Contains(got, `"contentBase64Omitted":true`) {
		t.Fatalf("expected omitted marker, got %q", got)
	}
	if result.Structured["contentBase64"] != "iVBORw0KGgoAAAANSUhEUgAA" {
		t.Fatalf("expected original structured payload to remain unchanged, got %#v", result.Structured)
	}
}

func TestFormatSubmitResultForLLM_JSONCompactCompactsLargeTextContent(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name: "file_read",
		Meta: map[string]any{
			"submitResultFormat": "json-compact",
		},
	}
	embeddedBase64 := strings.Repeat("A", 50000)
	content := `{"messages":[{"content":"prefix","contentBase64":"` + embeddedBase64 + `","filePath":"/tmp/page1.png"}]}`
	result := ToolExecutionResult{
		Output: `{"kind":"text"}`,
		Structured: map[string]any{
			"filePath": "/tmp/chat.jsonl",
			"kind":     "text",
			"content":  content,
		},
	}

	got := formatSubmitResultForLLM(tool, frontendtools.NewDefaultRegistry(), result)
	if strings.Contains(got, embeddedBase64[:200]) {
		t.Fatalf("expected embedded base64 to be omitted, got %q", got)
	}
	if !strings.Contains(got, `"embeddedBase64Omitted":true`) {
		t.Fatalf("expected embedded base64 marker, got %q", got)
	}
	if !strings.Contains(got, `\"contentBase64\":\"\u003comitted\u003e\"`) {
		t.Fatalf("expected content base64 redaction inside text content, got %q", got)
	}
	if result.Structured["content"] != content {
		t.Fatalf("expected original content to remain unchanged")
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
