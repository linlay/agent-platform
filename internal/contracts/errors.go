package contracts

import (
	"errors"
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
