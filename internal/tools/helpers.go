package tools

import (
	"time"

	"agent-platform-runner-go/internal/contracts"
)

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func toolTimeout(policy contracts.RetryPolicy) time.Duration {
	return time.Duration(maxInt(policy.TimeoutMs, 1)) * time.Millisecond
}
