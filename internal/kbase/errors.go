package kbase

import "errors"

type ErrorKind string

const (
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorNotFound    ErrorKind = "not_found"
	ErrorDisabled    ErrorKind = "capability_disabled"
	// ErrorWrongMode is retained as an API source-compatibility alias.
	ErrorWrongMode ErrorKind = ErrorDisabled
	ErrorInvalid   ErrorKind = "invalid"
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
	var engineErr *LanceEngineError
	if errors.As(err, &engineErr) {
		switch engineErr.Code {
		case "invalid_request", "dimension_mismatch", "query_invalid":
			return ErrorInvalid
		default:
			return ErrorUnavailable
		}
	}
	return ErrorInvalid
}
