package contracts

import (
	"testing"

	"agent-platform/internal/config"
)

func TestResolveBudgetSupportsHitlTimeoutOverride(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				RunTimeoutMs: 300000,
				Model:        config.RetryBudgetConfig{MaxCalls: 30, TimeoutMs: 120000},
				Tool:         config.RetryBudgetConfig{MaxCalls: 20, TimeoutMs: 60000},
				Hitl: config.HitlBudgetConfig{
					TimeoutMs: 15000,
					Question:  config.HitlModeBudgetConfig{TimeoutMs: 16000},
				},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"hitl": map[string]any{
			"timeoutMs": 45000,
			"question":  map[string]any{"timeoutMs": 55000},
			"approval":  map[string]any{"timeoutMs": 65000},
		},
	})
	if budget.Hitl.TimeoutMs != 45000 || budget.Hitl.Question.TimeoutMs != 55000 || budget.Hitl.Approval.TimeoutMs != 65000 {
		t.Fatalf("expected HITL timeout override 45000, got %#v", budget)
	}
	if budget.Hitl.Form.TimeoutMs != 0 || budget.Hitl.Plan.TimeoutMs != 0 {
		t.Fatalf("did not expect unset HITL mode timeouts, got %#v", budget.Hitl)
	}
}

func TestNormalizeBudgetLeavesUnsetHitlTimeoutAtZero(t *testing.T) {
	budget := NormalizeBudget(Budget{})
	if budget.Hitl.TimeoutMs != 0 {
		t.Fatalf("expected unset HITL timeout to stay 0, got %#v", budget)
	}
	if budget.Hitl.Question.TimeoutMs != 0 || budget.Hitl.Approval.TimeoutMs != 0 ||
		budget.Hitl.Form.TimeoutMs != 0 || budget.Hitl.Plan.TimeoutMs != 0 {
		t.Fatalf("expected unset HITL mode timeouts to stay 0, got %#v", budget.Hitl)
	}
	if budget.MaxSteps != 100 {
		t.Fatalf("expected default max steps 100, got %#v", budget)
	}
	if budget.Model.MaxCalls != 100 {
		t.Fatalf("expected default model max calls 100, got %#v", budget)
	}
	if budget.Tool.MaxCalls != 60 {
		t.Fatalf("expected default tool max calls 60, got %#v", budget)
	}

	derived := NormalizeBudget(Budget{MaxSteps: 7})
	if derived.Tool.MaxCalls != 14 {
		t.Fatalf("expected explicit max steps to derive tool max calls 14, got %#v", derived)
	}
}

func TestResolveHITLTimeoutUsesModeItemAndFallbackPriority(t *testing.T) {
	budget := Budget{Hitl: HitlPolicy{
		TimeoutMs: 100000,
		Question:  HitlModePolicy{TimeoutMs: 110000},
		Approval:  HitlModePolicy{TimeoutMs: 120000},
		Form:      HitlModePolicy{TimeoutMs: 130000},
		Plan:      HitlModePolicy{TimeoutMs: 140000},
	}}

	// question: item-specific timeout > mode budget
	if got := ResolveHITLTimeout("question", 900000, budget); got != 900000 {
		t.Fatalf("question timeout = %d, want item override 900000", got)
	}
	// question: no item-specific → mode budget
	if got := ResolveHITLTimeout("question", 0, budget); got != 110000 {
		t.Fatalf("question timeout = %d, want mode override 110000", got)
	}
	// plan: item-specific timeout is NOT supported, should fall through to mode budget
	if got := ResolveHITLTimeout("plan", 900000, budget); got != 140000 {
		t.Fatalf("plan timeout = %d, want mode override 140000", got)
	}
	// approval: item-specific timeout > mode budget
	if got := ResolveHITLTimeout("approval", 900000, budget); got != 900000 {
		t.Fatalf("approval timeout = %d, want item override 900000", got)
	}
	// form: item-specific timeout > mode budget
	if got := ResolveHITLTimeout("form", 800000, budget); got != 800000 {
		t.Fatalf("form timeout = %d, want item override 800000", got)
	}
	// approval: no item-specific → mode budget
	if got := ResolveHITLTimeout("approval", 0, budget); got != 120000 {
		t.Fatalf("approval timeout = %d, want mode override 120000", got)
	}
	// question: no mode or item → global hitl
	if got := ResolveHITLTimeout("question", 0, Budget{Hitl: HitlPolicy{TimeoutMs: 100000}}); got != 100000 {
		t.Fatalf("question timeout = %d, want global override 100000", got)
	}
	// question: nothing set → default
	if got := ResolveHITLTimeout("question", 0, Budget{}); got != DefaultHITLTimeoutMs {
		t.Fatalf("question timeout = %d, want fallback %d", got, DefaultHITLTimeoutMs)
	}
}

func TestResolveBudgetMaxStepsAndLegacyModelFallback(t *testing.T) {
	cfg := config.Config{
		Defaults: config.DefaultsConfig{
			Budget: config.BudgetDefaultsConfig{
				RunTimeoutMs: 300000,
				MaxSteps:     100,
				Model:        config.RetryBudgetConfig{MaxCalls: 100, TimeoutMs: 120000},
				Tool:         config.RetryBudgetConfig{MaxCalls: 60, TimeoutMs: 60000},
			},
		},
	}

	budget := ResolveBudget(cfg, map[string]any{
		"maxSteps": 25,
	})
	if budget.MaxSteps != 25 || budget.Model.MaxCalls != 25 || budget.Tool.MaxCalls != 50 {
		t.Fatalf("resolved budget with maxSteps = %#v, want max/model 25 and derived tool 50", budget)
	}

	legacy := ResolveBudget(cfg, map[string]any{
		"model": map[string]any{"maxCalls": 12},
		"tool":  map[string]any{"maxCalls": 7},
	})
	if legacy.MaxSteps != 12 || legacy.Model.MaxCalls != 12 || legacy.Tool.MaxCalls != 7 {
		t.Fatalf("resolved legacy budget = %#v, want max/model 12 and explicit tool 7", legacy)
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
				RunTimeoutMs: 300000,
				MaxSteps:     100,
				Model:        config.RetryBudgetConfig{MaxCalls: 100, TimeoutMs: 120000},
				Tool:         config.RetryBudgetConfig{MaxCalls: 60, TimeoutMs: 60000},
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
