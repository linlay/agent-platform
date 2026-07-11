package kbase

import "errors"

type ErrorKind string

const (
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorNotFound    ErrorKind = "not_found"
	ErrorWrongMode   ErrorKind = "wrong_mode"
	ErrorInvalid     ErrorKind = "invalid"
)

type PolicyError struct {
	Kind    ErrorKind
	Message string
}

func (e *PolicyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func KindOf(err error) ErrorKind {
	var policyErr *PolicyError
	if errors.As(err, &policyErr) {
		return policyErr.Kind
	}
	return ErrorInvalid
}
