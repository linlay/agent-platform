package contracts

import (
	"testing"

	"agent-platform/internal/config"
)

func TestResolveBudgetSupportsHitlTimeoutOverride(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				Timeout: 300,
				Model:   config.RetryBudgetConfig{MaxCalls: 30, Timeout: 120},
				Tool:    config.RetryBudgetConfig{MaxCalls: 20, Timeout: 60},
				Hitl: config.HitlBudgetConfig{
					Timeout:  15,
					Question: config.HitlModeBudgetConfig{Timeout: 16},
				},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"hitl": map[string]any{
			"timeout":  45,
			"question": map[string]any{"timeout": 55},
			"approval": map[string]any{"timeout": 65},
		},
	})
	if budget.Hitl.Timeout != 45 || budget.Hitl.Question.Timeout != 55 || budget.Hitl.Approval.Timeout != 65 {
		t.Fatalf("expected HITL timeout override 45, got %#v", budget)
	}
	if budget.Hitl.Form.Timeout != 0 || budget.Hitl.Plan.Timeout != 0 {
		t.Fatalf("did not expect unset HITL mode timeouts, got %#v", budget.Hitl)
	}
}

func TestNormalizeBudgetLeavesUnsetHitlTimeoutAtZero(t *testing.T) {
	budget := NormalizeBudget(Budget{})
	if budget.Hitl.Timeout != 0 {
		t.Fatalf("expected unset HITL timeout to stay 0, got %#v", budget)
	}
	if budget.Hitl.Question.Timeout != 0 || budget.Hitl.Approval.Timeout != 0 ||
		budget.Hitl.Form.Timeout != 0 || budget.Hitl.Plan.Timeout != 0 {
		t.Fatalf("expected unset HITL mode timeouts to stay 0, got %#v", budget.Hitl)
	}
	if budget.MaxSteps != 100 {
		t.Fatalf("expected default max steps 100, got %#v", budget)
	}
	if budget.Timeout != 3600 {
		t.Fatalf("expected default timeout 3600, got %#v", budget)
	}
	if budget.Model.MaxCalls != 100 {
		t.Fatalf("expected default model max calls 100, got %#v", budget)
	}
	if budget.Model.Timeout != 30 {
		t.Fatalf("expected default model timeout 30, got %#v", budget)
	}
	if budget.Tool.MaxCalls != 100 {
		t.Fatalf("expected default tool max calls 100, got %#v", budget)
	}
	if budget.Tool.Timeout != 600 {
		t.Fatalf("expected default tool timeout 600, got %#v", budget)
	}

	derived := NormalizeBudget(Budget{MaxSteps: 7})
	if derived.Tool.MaxCalls != 14 {
		t.Fatalf("expected explicit max steps to derive tool max calls 14, got %#v", derived)
	}
}

func TestResolveHITLTimeoutUsesModeItemAndFallbackPriority(t *testing.T) {
	budget := Budget{Hitl: HitlPolicy{
		Timeout:  100,
		Question: HitlModePolicy{Timeout: 110},
		Approval: HitlModePolicy{Timeout: 120},
		Form:     HitlModePolicy{Timeout: 130},
		Plan:     HitlModePolicy{Timeout: 140},
	}}

	// question: item-specific timeout is NOT supported, should fall through to mode budget
	if got := ResolveHITLTimeout("question", 900, budget); got != 110 {
		t.Fatalf("question timeout = %d, want mode override 110", got)
	}
	// question: no item-specific → mode budget
	if got := ResolveHITLTimeout("question", 0, budget); got != 110 {
		t.Fatalf("question timeout = %d, want mode override 110", got)
	}
	// plan: item-specific timeout is NOT supported, should fall through to mode budget
	if got := ResolveHITLTimeout("plan", 900, budget); got != 140 {
		t.Fatalf("plan timeout = %d, want mode override 140", got)
	}
	// approval: item-specific timeout > mode budget
	if got := ResolveHITLTimeout("approval", 900, budget); got != 900 {
		t.Fatalf("approval timeout = %d, want item override 900", got)
	}
	// form: item-specific timeout > mode budget
	if got := ResolveHITLTimeout("form", 800, budget); got != 800 {
		t.Fatalf("form timeout = %d, want item override 800", got)
	}
	// approval: no item-specific → mode budget
	if got := ResolveHITLTimeout("approval", 0, budget); got != 120 {
		t.Fatalf("approval timeout = %d, want mode override 120", got)
	}
	// question: no mode or item → global hitl
	if got := ResolveHITLTimeout("question", 0, Budget{Hitl: HitlPolicy{Timeout: 100}}); got != 100 {
		t.Fatalf("question timeout = %d, want global override 100", got)
	}
	// question: nothing set → default
	if got := ResolveHITLTimeout("question", 0, Budget{}); got != DefaultHITLTimeout {
		t.Fatalf("question timeout = %d, want fallback %d", got, DefaultHITLTimeout)
	}
}

func TestResolveBudgetMaxSteps(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				Timeout:  300,
				MaxSteps: 100,
				Model:    config.RetryBudgetConfig{MaxCalls: 100, Timeout: 120},
				Tool:     config.RetryBudgetConfig{MaxCalls: 60, Timeout: 60},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"maxSteps": 25,
	})
	if budget.MaxSteps != 25 || budget.Model.MaxCalls != 25 || budget.Tool.MaxCalls != 50 {
		t.Fatalf("resolved budget with maxSteps = %#v, want max/model 25 and derived tool 50", budget)
	}

	preferred := ResolveBudget(cfg, map[string]any{
		"maxSteps": 30,
		"model":    map[string]any{"maxCalls": 12},
	})
	if preferred.MaxSteps != 30 || preferred.Model.MaxCalls != 30 || preferred.Tool.MaxCalls != 60 {
		t.Fatalf("resolved preferred budget = %#v, want max/model 30 and derived tool 60", preferred)
	}
}

func TestResolveBudgetStageToolDerivesFromStageMaxSteps(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				Timeout:  300,
				MaxSteps: 100,
				Model:    config.RetryBudgetConfig{MaxCalls: 100, Timeout: 120},
				Tool:     config.RetryBudgetConfig{MaxCalls: 60, Timeout: 60},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"stages": map[string]any{
			"execute": map[string]any{"maxSteps": 8},
			"summary": map[string]any{
				"maxSteps": 1,
				"tool":     map[string]any{"maxCalls": 1},
			},
		},
	})
	if budget.Stages["execute"].MaxSteps != 8 || budget.Stages["execute"].Tool.MaxCalls != 16 {
		t.Fatalf("execute stage budget = %#v, want maxSteps 8 and derived tool 16", budget.Stages["execute"])
	}
	if budget.Stages["summary"].MaxSteps != 1 || budget.Stages["summary"].Tool.MaxCalls != 1 {
		t.Fatalf("summary stage budget = %#v, want explicit tool max calls preserved", budget.Stages["summary"])
	}
}
