package engine

import "agent-platform-runner-go/internal/config"

type RetryPolicy struct {
	MaxCalls   int
	TimeoutMs  int
	RetryCount int
}

type ComputePolicy struct {
	RunTimeoutMs int
	Model        RetryPolicy
	Tool         RetryPolicy
}

func ResolveComputePolicy(cfg config.Config) ComputePolicy {
	return ComputePolicy{
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
