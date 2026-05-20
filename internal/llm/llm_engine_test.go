package llm

import (
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestResolveMaxStepsPrefersSessionReactMaxSteps(t *testing.T) {
	engine := &LLMAgentEngine{
		cfg: config.Config{
			Defaults: config.DefaultsConfig{
				React: config.ReactDefaultsConfig{MaxSteps: 6},
			},
		},
	}

	if got := engine.resolveMaxSteps(contracts.QuerySession{ReactMaxSteps: 160}); got != 160 {
		t.Fatalf("resolveMaxSteps() = %d, want session override 160", got)
	}
	if got := engine.resolveMaxSteps(contracts.QuerySession{}); got != 6 {
		t.Fatalf("resolveMaxSteps() = %d, want config default 6", got)
	}
}
