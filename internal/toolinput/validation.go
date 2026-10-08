// Package toolinput provides value-free, actionable native tool input diagnostics.
package toolinput

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Error struct{ Field, Expected, Actual, Recovery string }

func (e *Error) Error() string {
	message := fmt.Sprintf("Invalid parameter %s: expected %s; received %s.", e.Field, e.Expected, e.Actual)
	if e.Recovery != "" {
		message += " " + e.Recovery
	}
	return message
}
func (e *Error) Details() map[string]any {
	return map[string]any{"field": e.Field, "expected": e.Expected, "actual": e.Actual, "recovery": map[string]any{"strategy": "fix_input", "message": e.Recovery}}
}
func Type(v any, present bool) string {
	if !present {
		return "missing"
	}
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "non-JSON value"
	}
}
func New(field, expected string, v any, present bool, recovery string) *Error {
	return &Error{field, expected, Type(v, present), recovery}
}
func Keys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func Unknown(path string, allowed []string) *Error {
	return &Error{path + "<unknown>", "only fields: " + strings.Join(allowed, ", "), "unknown field present", "Remove unsupported fields; use only the listed fields. Unknown names and values are omitted for privacy."}
}

// Validate uses the existing compact native contract: s string, b boolean,
// n integer 1..100, a string array, o object; ! marks required fields (strings must be non-empty).
func Validate(values map[string]any, fields map[string]string, prefix string) error {
	for _, key := range Keys(values) {
		if _, ok := fields[key]; !ok {
			return Unknown(prefix, Keys(fields))
		}
	}
	for _, key := range Keys(fields) {
		rule := fields[key]
		v, present := values[key]
		required := strings.HasSuffix(rule, "!")
		if !present && !required {
			continue
		}
		field := prefix + key
		valid := false
		expected := ""
		switch rule[0] {
		case 's':
			expected = "JSON string"
			s, ok := v.(string)
			valid = ok
			if required {
				expected = "non-empty JSON string"
				valid = ok && strings.TrimSpace(s) != ""
			}
		case 'b':
			expected = "JSON boolean"
			_, valid = v.(bool)
		case 'n':
			expected = "JSON integer in range 1–100"
			n, ok := v.(float64)
			valid = ok && n >= 1 && n <= 100 && math.Trunc(n) == n
		case 'a':
			expected = "JSON array of strings"
			items, ok := v.([]any)
			valid = ok
			if ok {
				for i, item := range items {
					if _, ok := item.(string); !ok {
						return New(fmt.Sprintf("%s[%d]", field, i), "JSON string", item, true, "Provide a JSON string.")
					}
				}
			}
		case 'o':
			expected = "JSON object"
			_, valid = v.(map[string]any)
		}
		if !present || !valid {
			return New(field, expected, v, present, "Provide "+expected+".")
		}
	}
	return nil
}
func Enum(field string, v any, allowed []string) error {
	return Choice(field, v, true, allowed)
}

// Choice also distinguishes a missing required choice from an explicit null.
func Choice(field string, v any, present bool, allowed []string) error {
	for _, s := range allowed {
		if present && v == s {
			return nil
		}
	}
	names := make([]string, 0, len(allowed))
	example := ""
	for _, s := range allowed {
		if s == "" {
			names = append(names, `"" (default)`)
		} else {
			names = append(names, s)
			if example == "" {
				example = s
			}
		}
	}
	recovery := "Use one of the listed exact strings."
	if example != "" {
		recovery += fmt.Sprintf(" Example: %s:%q.", field, example)
	}
	return New(field, "JSON string; one of: "+strings.Join(names, ", "), v, present, recovery)
}
