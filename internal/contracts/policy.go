package contracts

import (
	"time"

	"agent-platform-runner-go/internal/config"
)

type RetryPolicy struct {
	MaxCalls   int `json:"maxCalls,omitempty"`
	TimeoutMs  int `json:"timeoutMs,omitempty"`
	RetryCount int `json:"retryCount,omitempty"`
}

type Budget struct {
	RunTimeoutMs int         `json:"runTimeoutMs,omitempty"`
	Model        RetryPolicy `json:"model,omitempty"`
	Tool         RetryPolicy `json:"tool,omitempty"`
}

func DefaultBudget(cfg config.Config) Budget {
	return Budget{
		RunTimeoutMs: cfg.Defaults.Budget.RunTimeoutMs,
		Model: RetryPolicy{
			MaxCalls:   cfg.Defaults.Budget.Model.MaxCalls,
			TimeoutMs:  cfg.Defaults.Budget.Model.TimeoutMs,
			RetryCount: cfg.Defaults.Budget.Model.RetryCount,
		},
		Tool: RetryPolicy{
			MaxCalls:   cfg.Defaults.Budget.Tool.MaxCalls,
			TimeoutMs:  cfg.Defaults.Budget.Tool.TimeoutMs,
			RetryCount: cfg.Defaults.Budget.Tool.RetryCount,
		},
	}
}

func ResolveBudget(cfg config.Config, overrides map[string]any) Budget {
	budget := normalizeBudget(DefaultBudget(cfg))
	if len(overrides) == 0 {
		return budget
	}
	if value := anyIntNode(overrides["runTimeoutMs"]); value > 0 {
		budget.RunTimeoutMs = value
	}
	if model := anyMapNode(overrides["model"]); len(model) > 0 {
		budget.Model = mergeRetryPolicy(budget.Model, model)
	}
	if tool := anyMapNode(overrides["tool"]); len(tool) > 0 {
		budget.Tool = mergeRetryPolicy(budget.Tool, tool)
	}
	return normalizeBudget(budget)
}

func normalizeBudget(b Budget) Budget {
	if b.RunTimeoutMs <= 0 {
		b.RunTimeoutMs = 300000
	}
	b.Model = normalizeRetryPolicy(b.Model, RetryPolicy{MaxCalls: 30, TimeoutMs: 120000, RetryCount: 0})
	b.Tool = normalizeRetryPolicy(b.Tool, RetryPolicy{MaxCalls: 50, TimeoutMs: 300000, RetryCount: 0})
	return b
}

func normalizeRetryPolicy(policy RetryPolicy, fallback RetryPolicy) RetryPolicy {
	if policy.MaxCalls <= 0 {
		policy.MaxCalls = fallback.MaxCalls
	}
	if policy.TimeoutMs <= 0 {
		policy.TimeoutMs = fallback.TimeoutMs
	}
	if policy.RetryCount < 0 {
		policy.RetryCount = 0
	}
	return policy
}

func mergeRetryPolicy(base RetryPolicy, overrides map[string]any) RetryPolicy {
	policy := base
	if value := anyIntNode(overrides["maxCalls"]); value > 0 {
		policy.MaxCalls = value
	}
	if value := anyIntNode(overrides["timeoutMs"]); value > 0 {
		policy.TimeoutMs = value
	}
	if value, ok := readOptionalInt(overrides["retryCount"]); ok {
		policy.RetryCount = maxInt(value, 0)
	}
	return policy
}

func readOptionalInt(value any) (int, bool) {
	number := anyIntNode(value)
	switch value.(type) {
	case int, int64, float64, string:
		return number, true
	default:
		return 0, false
	}
}

func (b Budget) RunTimeout() time.Duration {
	return time.Duration(maxInt(b.RunTimeoutMs, 1)) * time.Millisecond
}

func (p RetryPolicy) Timeout() time.Duration {
	return time.Duration(maxInt(p.TimeoutMs, 1)) * time.Millisecond
}
