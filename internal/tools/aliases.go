package tools

import (
	"time"

	"agent-platform-runner-go/internal/bashsec"
	"agent-platform-runner-go/internal/contracts"
)

type ToolExecutor = contracts.ToolExecutor
type ActionInvoker = contracts.ActionInvoker
type SandboxClient = contracts.SandboxClient
type McpClient = contracts.McpClient
type ExecutionContext = contracts.ExecutionContext
type ToolExecutionResult = contracts.ToolExecutionResult
type SandboxExecutionResult = contracts.SandboxExecutionResult
type Budget = contracts.Budget
type RetryPolicy = contracts.RetryPolicy
type PlanTask = contracts.PlanTask
type PlanRuntimeState = contracts.PlanRuntimeState
type QuerySession = contracts.QuerySession

const (
	ErrorScopeRun       = contracts.ErrorScopeRun
	ErrorScopeTool      = contracts.ErrorScopeTool
	ErrorCategoryTool   = contracts.ErrorCategoryTool
	ErrorCategorySystem = contracts.ErrorCategorySystem
)

var ErrToolArgsTemplateMissingValue = contracts.ErrToolArgsTemplateMissingValue

func NewErrorPayload(code string, message string, scope string, category contracts.ErrorCategory, diagnostics map[string]any) map[string]any {
	return contracts.NewErrorPayload(code, message, scope, category, diagnostics)
}

func anyIntNode(value any) int {
	return contracts.AnyIntNode(value)
}

func anyBoolNode(value any) bool {
	return contracts.AnyBoolNode(value)
}

func anyStringNode(value any) string {
	return contracts.AnyStringNode(value)
}

func anyMapNode(value any) map[string]any {
	return contracts.AnyMapNode(value)
}

func anyListStrings(value any) []string {
	return contracts.AnyListStrings(value)
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func normalizeBudget(b Budget) Budget {
	return contracts.NormalizeBudget(b)
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

func marshalJSON(value any) string {
	return contracts.MarshalJSON(value)
}

func checkBashSecurity(command string) (bool, string) {
	return bashsec.CheckBashSecurity(command)
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func maxInt64(value int64, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func toolTimeout(policy RetryPolicy) time.Duration {
	return time.Duration(maxInt(policy.TimeoutMs, 1)) * time.Millisecond
}

func normalizePlanTaskStatus(raw string) string {
	return contracts.NormalizePlanTaskStatus(raw)
}

func planTasksArray(state *PlanRuntimeState) []map[string]any {
	return contracts.PlanTasksArray(state)
}
