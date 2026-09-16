package connector

import "errors"

// ErrOutcomeUnknown means dispatch started but cancellation prevented a final
// response. Callers must inspect external state, never automatically replay it.
var ErrOutcomeUnknown = errors.New("OUTCOME_UNKNOWN")
