package engine

import "errors"

var (
	ErrRunInterrupted               = errors.New("run interrupted")
	ErrRunFinished                  = errors.New("run finished")
	ErrRunControlUnavailable        = errors.New("run control unavailable")
	ErrFrontendToolMissingToolID    = errors.New("frontend tool missing tool id")
	ErrFrontendSubmitAlreadyWaiting = errors.New("frontend submit waiter already exists")
	ErrToolArgsTemplateMissingValue = errors.New("tool args template missing value")
	ErrBudgetExceeded               = errors.New("budget exceeded")
	ErrMCPCallFailed                = errors.New("mcp call failed")
)
