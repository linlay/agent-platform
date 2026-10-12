package queryinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrUnknownField = errors.New("unknown field")

// DecodeStrictJSON is for public request DTOs. Internal recorded events may
// contain trusted metadata and do not use the transport decoder.
func DecodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return fmt.Errorf("%w %s", ErrUnknownField, strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return err
	}
	return nil
}

// ValidateFields checks map-based tool and YAML inputs against their contract.
func ValidateFields(scope string, fields map[string]any, allowed ...string) error {
	valid := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		valid[field] = true
	}
	unknown := make([]string, 0)
	for field := range fields {
		if !valid[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%w %q in %s", ErrUnknownField, unknown[0], scope)
	}
	return nil
}
