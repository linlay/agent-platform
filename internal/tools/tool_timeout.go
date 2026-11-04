package tools

import (
	"time"

	"agent-platform/internal/contracts"
)

// defaultToolCallTimeout applies when the Run budget does not set a tool timeout.
const defaultToolCallTimeout = 30 * time.Second

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func toolTimeout(policy contracts.RetryPolicy) time.Duration {
	return policy.TimeoutDuration()
}

// toolCallTimeout is the per-call timeout the router applies to ordinary tools.
// Tools that own their timers use it for each bounded step instead.
func toolCallTimeout(execCtx *contracts.ExecutionContext) time.Duration {
	if execCtx != nil {
		if budget := contracts.NormalizeBudget(execCtx.Budget); budget.Tool.Timeout > 0 {
			return toolTimeout(budget.Tool)
		}
	}
	return defaultToolCallTimeout
}
