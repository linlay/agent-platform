package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/models"
)

func TestSummaryRequestLeavesOutputOpen(t *testing.T) {
	s := &llmRunStream{model: models.ModelDefinition{Protocol: "OPENAI", ContextWindow: 2000}, summaryTargetTokens: 200}
	prepared := preparedProviderRequest{RequestBody: map[string]any{"model": "test", "messages": []any{}, "tools": []any{"unexpected"}, "tool_choice": "required", "parallel_tool_calls": true}}
	if err := s.prepareSummaryRequest(&prepared); err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(prepared.RequestBodyJSON, &actual); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "tools", "tool_choice", "parallel_tool_calls"} {
		if actual[key] != nil {
			t.Fatalf("summary request carries %s: %#v", key, actual)
		}
	}
}

func TestSummaryRequestClampsProviderRequiredLimitToWindow(t *testing.T) {
	for _, protocol := range []string{"OPENAI", "ANTHROPIC"} {
		t.Run(protocol, func(t *testing.T) {
			s := &llmRunStream{model: models.ModelDefinition{Protocol: protocol, ContextWindow: 2000}, summaryTargetTokens: 200}
			prepared := preparedProviderRequest{RequestBody: map[string]any{"model": "test", "messages": []any{}, "max_tokens": 99999}}
			if err := s.prepareSummaryRequest(&prepared); err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := json.Unmarshal(prepared.RequestBodyJSON, &actual); err != nil {
				t.Fatal(err)
			}
			limit, _ := actual["max_tokens"].(float64)
			if limit < 200 || limit >= 2000 {
				t.Fatalf("provider-required limit not clamped to the window remainder: %#v", actual)
			}
			prepared.RequestBody["messages"] = []any{map[string]any{"role": "user", "content": strings.Repeat("large", 2000)}}
			if !errors.Is(s.prepareSummaryRequest(&prepared), chat.ErrCompactSummaryInputTooLarge) {
				t.Fatal("oversized actual request was admitted")
			}
		})
	}
}
