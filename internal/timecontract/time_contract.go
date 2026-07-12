// Package timecontract defines the wire-level contract for structured time
// points exposed by agent-platform.
package timecontract

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// MinEpochMillis deliberately rejects every ten-digit Unix-seconds value.
	MinEpochMillis int64 = 1_000_000_000_000
	// MaxEpochMillis is the largest integer that JavaScript can represent
	// exactly. Keeping the API below this value preserves int64 -> number
	// fidelity across Desktop and web clients.
	MaxEpochMillis int64 = 9_007_199_254_740_991
	Expected             = "epoch_ms_int64"
)

// Violation identifies a value which cannot be represented by the public
// time-point contract. Field and Location are deliberately wire-safe: they
// are returned to API callers without leaking storage details.
type Violation struct {
	Field    string
	Location string
	Reason   string
}

func (e *Violation) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s at %s: %s", e.Field, e.Location, e.Reason)
	}
	return fmt.Sprintf("%s at %s violates %s", e.Field, e.Location, Expected)
}

// ValidateEpochMillis accepts only positive, JSON/JavaScript-safe Unix epoch
// milliseconds. It intentionally does not coerce strings, floats, seconds or
// zero: callers must surface a contract violation instead of guessing.
func ValidateEpochMillis(value int64, field, location string) error {
	if value >= MinEpochMillis && value <= MaxEpochMillis {
		return nil
	}
	return &Violation{
		Field:    field,
		Location: location,
		Reason:   fmt.Sprintf("must be an integer in [%d,%d]", MinEpochMillis, MaxEpochMillis),
	}
}

// ParseEpochMillis accepts the integer representations that can be produced
// by a JSON decoder configured with UseNumber or by a typed Go DTO. It does
// not coerce float64 or string values: doing so would allow JSON values such
// as 1.0, 1e12, and "1700000000000" to silently cross the wire boundary.
func ParseEpochMillis(value any, field, location string) (int64, error) {
	var parsed int64
	switch typed := value.(type) {
	case json.Number:
		var err error
		parsed, err = typed.Int64()
		if err != nil {
			return 0, &Violation{
				Field:    field,
				Location: location,
				Reason:   "must be a JSON integer",
			}
		}
	case int64:
		parsed = typed
	case int:
		parsed = int64(typed)
	case int32:
		parsed = int64(typed)
	case int16:
		parsed = int64(typed)
	case int8:
		parsed = int64(typed)
	default:
		return 0, &Violation{
			Field:    field,
			Location: location,
			Reason:   "must be a JSON integer",
		}
	}
	if err := ValidateEpochMillis(parsed, field, location); err != nil {
		return 0, err
	}
	return parsed, nil
}

// OptionalEpochMillis validates an optional time point. A zero value means
// absent and returns nil; API DTOs must omit that field rather than emit zero
// or null. Negative and out-of-range values remain violations.
func OptionalEpochMillis(value int64, field, location string) (*int64, error) {
	if value == 0 {
		return nil, nil
	}
	if err := ValidateEpochMillis(value, field, location); err != nil {
		return nil, err
	}
	result := value
	return &result, nil
}

func IsViolation(err error) bool {
	var violation *Violation
	return errors.As(err, &violation)
}

// ErrorData is the shared public error payload used by HTTP and WebSocket
// boundaries. Keep the keys flat so a caller can identify exactly which
// producer/storage location needs repair.
func ErrorData(err error) map[string]any {
	var violation *Violation
	if !errors.As(err, &violation) || violation == nil {
		return map[string]any{
			"code":     "time_contract_violation",
			"field":    "unknown",
			"location": "unknown",
			"expected": Expected,
		}
	}
	return map[string]any{
		"code":     "time_contract_violation",
		"field":    violation.Field,
		"location": violation.Location,
		"expected": Expected,
	}
}
