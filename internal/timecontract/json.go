package timecontract

import (
	"strings"
	"time"
)

// ParseRFC3339 validates a readable instant that was explicitly declared by
// a platform DTO or an output schema. It never treats a field name as a time
// declaration.
func ParseRFC3339(value any, field, location string) (time.Time, error) {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return time.Time{}, &Violation{Field: field, Location: location, Reason: "must be an RFC3339/RFC3339Nano string with timezone"}
	}
	if !hasRFC3339Offset(raw) {
		return time.Time{}, &Violation{Field: field, Location: location, Reason: "must be an RFC3339/RFC3339Nano string with Z or offset"}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, &Violation{Field: field, Location: location, Reason: "must be an RFC3339/RFC3339Nano string with timezone"}
	}
	return parsed, nil
}

func hasRFC3339Offset(value string) bool {
	if strings.HasSuffix(value, "Z") {
		return true
	}
	if len(value) < len("+00:00") {
		return false
	}
	offset := value[len(value)-6:]
	return (offset[0] == '+' || offset[0] == '-') && offset[3] == ':'
}
