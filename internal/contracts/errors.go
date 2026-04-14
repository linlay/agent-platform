package contracts

import "errors"

type ErrorCategory string

const (
	ErrorCategorySystem    ErrorCategory = "system"
	ErrorCategoryTimeout   ErrorCategory = "timeout"
	ErrorCategoryInterrupt ErrorCategory = "interrupt"
	ErrorCategoryTool      ErrorCategory = "tool"
	ErrorCategoryModel     ErrorCategory = "model"
)

const (
	ErrorScopeRun            = "run"
	ErrorScopeTask           = "task"
	ErrorScopeTool           = "tool"
	ErrorScopeModel          = "model"
	ErrorScopeFrontendSubmit = "frontend_submit"
)

var (
	ErrRunInterrupted               = errors.New("run interrupted")
	ErrRunFinished                  = errors.New("run finished")
	ErrRunControlUnavailable        = errors.New("run control unavailable")
	ErrFrontendSubmitMissingAwaitID = errors.New("frontend submit missing awaiting id")
	ErrFrontendSubmitAlreadyWaiting = errors.New("frontend submit waiter already exists")
	ErrToolArgsTemplateMissingValue = errors.New("tool args template missing value")
	ErrBudgetExceeded               = errors.New("budget exceeded")
	ErrMCPCallFailed                = errors.New("mcp call failed")
	ErrNotImplemented               = errors.New("not implemented")
)

func NewErrorPayload(code string, message string, scope string, category ErrorCategory, diagnostics map[string]any) map[string]any {
	payload := map[string]any{
		"code":     code,
		"message":  message,
		"scope":    scope,
		"category": string(category),
	}
	if len(diagnostics) > 0 {
		payload["diagnostics"] = cloneAnyMap(diagnostics)
	}
	return payload
}

func NewSystemErrorPayload(code string, message string, diagnostics map[string]any) map[string]any {
	return NewErrorPayload(code, message, ErrorScopeRun, ErrorCategorySystem, diagnostics)
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
