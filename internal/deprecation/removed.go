// Package deprecation identifies configuration that was deliberately removed.
package deprecation

import (
	"errors"
	"fmt"
)

var ErrRemoved = errors.New("removed configuration")

// RemovedError marks a retired configuration or public contract. Its message
// is written for startup and API diagnostics.
type RemovedError struct{ Message string }

func (e *RemovedError) Error() string {
	if e == nil {
		return ErrRemoved.Error()
	}
	return e.Message
}

func (e *RemovedError) Unwrap() error { return ErrRemoved }

func New(format string, args ...any) error {
	return &RemovedError{Message: fmt.Sprintf(format, args...)}
}

func Is(err error) bool { return errors.Is(err, ErrRemoved) }
