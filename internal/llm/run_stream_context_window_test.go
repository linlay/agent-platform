package llm

import (
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestRunContextWindowUsesAgentLimitWithinModelLimit(t *testing.T) {
	for _, tc := range []struct {
		model, agent, want int
	}{
		{model: 200000, want: 200000},
		{model: 200000, agent: 150000, want: 150000},
		{model: 100000, agent: 150000, want: 100000},
		{want: models.DefaultContextWindow},
	} {
		s := &llmRunStream{model: models.ModelDefinition{ContextWindow: tc.model}, session: contracts.QuerySession{MaxInputTokens: tc.agent}}
		if got := s.effectiveContextWindow(); got != tc.want {
			t.Fatalf("model=%d agent=%d window=%d want %d", tc.model, tc.agent, got, tc.want)
		}
	}
}
