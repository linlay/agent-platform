package llm

import (
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestResolveMaxStepsUsesBudgetAndDefaults(t *testing.T) {
	engine := &LLMAgentEngine{
		cfg: config.Config{
			Defaults: config.DefaultsConfig{
				React: config.ReactDefaultsConfig{MaxSteps: 6},
			},
		},
	}

	if got := engine.resolveMaxSteps(contracts.QuerySession{
		Budget: map[string]any{"maxSteps": 24},
		ResolvedBudget: contracts.Budget{
			MaxSteps: 24,
		},
	}, "react"); got != 24 {
		t.Fatalf("resolveMaxSteps() = %d, want budget max steps 24", got)
	}
	if got := engine.resolveMaxSteps(contracts.QuerySession{}, "react"); got != 100 {
		t.Fatalf("resolveMaxSteps() = %d, want budget default 100", got)
	}
}
