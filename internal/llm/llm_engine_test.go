package llm

import (
	"context"
	"strings"
	"testing"

	"agent-platform/internal/api"
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

func TestNewRunStreamRequiresExplicitModelKey(t *testing.T) {
	engine := &LLMAgentEngine{}

	_, err := engine.newRunStreamWithOptions(context.Background(), api.QueryRequest{}, contracts.QuerySession{}, true, runStreamOptions{Stage: "react"})
	if err == nil || !strings.Contains(err.Error(), "modelConfig.modelKey is required") {
		t.Fatalf("expected explicit model key error, got %v", err)
	}
}
