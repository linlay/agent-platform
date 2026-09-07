package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestSummaryRequestBudgetAppliesAfterProviderOverrides(t *testing.T) {
	for _, protocol := range []string{"OPENAI", "ANTHROPIC"} {
		t.Run(protocol, func(t *testing.T) {
			s := &llmRunStream{model: models.ModelDefinition{Protocol: protocol, ContextWindow: 2000}, stageSettings: contracts.StageSettings{MaxOutputTokens: 200}}
			prepared := preparedProviderRequest{RequestBody: map[string]any{"model": "test", "messages": []any{}, "max_tokens": 99999, "max_completion_tokens": 99999, "tools": []any{"unexpected"}, "tool_choice": "required", "parallel_tool_calls": true}}
			if err := s.prepareSummaryRequest(&prepared); err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := json.Unmarshal(prepared.RequestBodyJSON, &actual); err != nil {
				t.Fatal(err)
			}
			if actual["max_tokens"] != float64(200) || actual["max_completion_tokens"] != nil || actual["tools"] != nil || actual["tool_choice"] != nil || actual["parallel_tool_calls"] != nil {
				t.Fatalf("summary contract overridden: %#v", actual)
			}
			prepared.RequestBody["messages"] = []any{map[string]any{"role": "user", "content": strings.Repeat("large", 2000)}}
			if !errors.Is(s.prepareSummaryRequest(&prepared), chat.ErrCompactSummaryInputTooLarge) {
				t.Fatal("oversized actual request was admitted")
			}
		})
	}
}
