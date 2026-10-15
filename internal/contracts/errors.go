package contracts

import (
	"errors"
)

var (
	ErrRunInterrupted                  = errors.New("run interrupted")
	ErrRunFinished                     = errors.New("run finished")
	ErrRunControlUnavailable           = errors.New("run control unavailable")
	ErrContextCompactPending           = errors.New("context compact pending")
	ErrInteractionSubmitMissingAwaitID = errors.New("tool interaction submit missing awaiting id")
	ErrInteractionSubmitAlreadyWaiting = errors.New("tool interaction submit waiter already exists")
	ErrToolArgsTemplateMissingValue    = errors.New("tool args template missing value")
	ErrBudgetExceeded                  = errors.New("budget exceeded")
	ErrMCPCallFailed                   = errors.New("mcp call failed")
	ErrNotImplemented                  = errors.New("not implemented")

	// ErrMCPCallOutcomeUnknown marks a tools/call that may have reached the
	// server without a response, such as a timeout or a dropped connection.
	ErrMCPCallOutcomeUnknown = errors.New("mcp call outcome unknown")
)

func IsRunInterrupted(err error) bool {
	return errors.Is(err, ErrRunInterrupted)
}
