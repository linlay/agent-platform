package contracts

import (
	"testing"

	"agent-platform-runner-go/internal/config"
)

func TestResolveBudgetSupportsHitlTimeoutOverride(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				RunTimeoutMs: 300000,
				Model:        config.RetryBudgetConfig{MaxCalls: 30, TimeoutMs: 120000},
				Tool:         config.RetryBudgetConfig{MaxCalls: 20, TimeoutMs: 60000},
				Hitl:         config.HitlBudgetConfig{TimeoutMs: 15000},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"hitl": map[string]any{
			"timeoutMs": 45000,
		},
	})
	if budget.Hitl.TimeoutMs != 45000 {
		t.Fatalf("expected HITL timeout override 45000, got %#v", budget)
	}
}

func TestNormalizeBudgetLeavesUnsetHitlTimeoutAtZero(t *testing.T) {
	budget := NormalizeBudget(Budget{})
	if budget.Hitl.TimeoutMs != 0 {
		t.Fatalf("expected unset HITL timeout to stay 0, got %#v", budget)
	}
}
