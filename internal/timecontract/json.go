package timecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ValidateJSONPayload validates the JSON representation of a public payload.
// It is intentionally performed after marshaling with UseNumber so maps from
// storage and typed DTOs receive identical checks for quoted numbers, floats,
// nulls and out-of-range integers.
func ValidateJSONPayload(payload any, location string) error {
	location = strings.TrimSpace(location)
	// json.Marshal renders an integral float64 such as 1700000000000 as the
	// same JSON token as an int64. Inspect the original Go value first so a
	// generic map/DTO cannot smuggle a float through a later UseNumber decode.
	if err := validateOriginalTimeValue(reflect.ValueOf(payload), location, "", map[originalVisit]struct{}{}); err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		if IsViolation(err) {
			return err
		}
		// The caller preserves its existing JSON-encoding failure behavior for
		// values which cannot be marshaled. This function owns time validation.
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return validateJSONValue(value, location)
}

type originalVisit struct {
	typ reflect.Type
	ptr uintptr
}

var jsonNumberReflectType = reflect.TypeOf(json.Number(""))

// validateOriginalTimeValue complements the JSON-token validation below.
// The JSON encoder erases the distinction between a Go integral float and an
// integer, while the public contract explicitly forbids all float values.
func validateOriginalTimeValue(value reflect.Value, location, field string, seen map[originalVisit]struct{}) error {
	if !value.IsValid() {
		return nil
	}
	for {
		switch value.Kind() {
		case reflect.Interface:
			if value.IsNil() {
				return nil
			}
			value = value.Elem()
		case reflect.Pointer:
			if value.IsNil() {
				return nil
			}
			// Check the pointed value before applying the cycle guard. The same
			// *float64 can legitimately appear under an earlier non-time key and
			// a later timestamp key; the latter must still be rejected.
			if field != "" && IsPublicTimePointField(field) {
				pointed := value.Elem()
				for pointed.IsValid() && pointed.Kind() == reflect.Interface && !pointed.IsNil() {
					pointed = pointed.Elem()
				}
				if pointed.IsValid() && (pointed.Kind() == reflect.Float32 || pointed.Kind() == reflect.Float64) {
					return &Violation{Field: field, Location: location, Reason: "must be an unquoted JSON integer"}
				}
			}
			visit := originalVisit{typ: value.Type(), ptr: value.Pointer()}
			if _, alreadySeen := seen[visit]; alreadySeen {
				return nil
			}
			seen[visit] = struct{}{}
			value = value.Elem()
		default:
			goto resolved
		}
	}

resolved:
	if field != "" && IsPublicTimePointField(field) {
		if value.Type() == jsonNumberReflectType {
			if _, err := ParseEpochMillis(value.Interface(), field, location); err != nil {
				return err
			}
		}
		switch value.Kind() {
		case reflect.Float32, reflect.Float64:
			return &Violation{Field: field, Location: location, Reason: "must be an unquoted JSON integer"}
		}
	}

	switch value.Kind() {
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		visit := originalVisit{typ: value.Type(), ptr: value.Pointer()}
		if _, alreadySeen := seen[visit]; alreadySeen {
			return nil
		}
		seen[visit] = struct{}{}
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			if key.Kind() != reflect.String {
				continue
			}
			name := key.String()
			if err := validateOriginalTimeValue(iterator.Value(), location+"."+name, name, seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		typ := value.Type()
		for index := 0; index < value.NumField(); index++ {
			structField := typ.Field(index)
			if structField.PkgPath != "" && !structField.Anonymous {
				continue // encoding/json ignores unexported non-embedded fields.
			}
			name, include := jsonFieldName(structField)
			if !include {
				continue
			}
			if err := validateOriginalTimeValue(value.Field(index), location+"."+name, name, seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if value.Kind() == reflect.Slice && value.Len() > 0 {
			visit := originalVisit{typ: value.Type(), ptr: value.Pointer()}
			if _, alreadySeen := seen[visit]; alreadySeen {
				return nil
			}
			seen[visit] = struct{}{}
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateOriginalTimeValue(value.Index(index), fmt.Sprintf("%s[%d]", location, index), "", seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		name = field.Name
	}
	return name, true
}

func validateJSONValue(value any, location string) error {
	if location == "" {
		location = "response"
	}
	switch typed := value.(type) {
	case []any:
		for index, item := range typed {
			if err := validateJSONValue(item, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
	case map[string]any:
		for field, item := range typed {
			fieldLocation := location + "." + field
			if IsPublicTimePointField(field) {
				if err := validateJSONEpochMillis(item, field, fieldLocation); err != nil {
					return err
				}
			}
			if IsPublicReadableTimeField(field) {
				if err := validateJSONReadableTime(item, field, fieldLocation); err != nil {
					return err
				}
			}
			if err := validateJSONValue(item, fieldLocation); err != nil {
				return err
			}
		}
		for field, readable := range typed {
			if strings.HasSuffix(field, "Time") {
				pointField := strings.TrimSuffix(field, "Time") + "At"
				point, ok := typed[pointField]
				if !ok {
					return &Violation{Field: field, Location: location + "." + field, Reason: "requires matching " + pointField}
				}
				if err := validateReadableMatchesEpochMillis(readable, point, field, pointField, location); err != nil {
					return err
				}
			}
			// `iso` is also used by the datetime tool, where it is intentionally
			// standalone. When a payload does include a structured timestamp,
			// however, the two values must still represent the same instant.
			if field == "iso" {
				if point, ok := typed["timestamp"]; ok {
					if err := validateReadableMatchesEpochMillis(readable, point, field, "timestamp", location); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func validateReadableMatchesEpochMillis(readable any, point any, readableField string, pointField string, location string) error {
	ms, ok := jsonEpochMillis(point)
	if !ok {
		return nil // the individual point validation reports the useful error.
	}
	raw, ok := readable.(string)
	if !ok {
		return nil // the individual readable validation reports the useful error.
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || parsed.Nanosecond()%int(time.Millisecond) != 0 || parsed.UnixMilli() != ms {
		return &Violation{Field: readableField, Location: location + "." + readableField, Reason: "must represent the same instant as " + pointField}
	}
	return nil
}

func validateJSONEpochMillis(value any, field, location string) error {
	ms, ok := jsonEpochMillis(value)
	if !ok {
		return &Violation{Field: field, Location: location, Reason: "must be an unquoted JSON integer"}
	}
	return ValidateEpochMillis(ms, field, location)
}

func jsonEpochMillis(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	raw := number.String()
	if strings.ContainsAny(raw, ".eE") {
		return 0, false
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return ms, true
}

func validateJSONReadableTime(value any, field, location string) error {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return &Violation{Field: field, Location: location, Reason: "must be an RFC3339/RFC3339Nano string with timezone"}
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil || !hasRFC3339Offset(raw) {
		return &Violation{Field: field, Location: location, Reason: "must be an RFC3339/RFC3339Nano string with Z or offset"}
	}
	return nil
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

// IsPublicReadableTimeField enforces the naming allowance without treating
// local datetime-tool fields such as `time` as a structured instant.
func IsPublicReadableTimeField(field string) bool {
	return field == "iso" || strings.HasSuffix(field, "Time")
}

// IsPublicTimePointField accepts conventional *At names plus the explicit
// timestamp and file-millisecond aliases already exposed by the platform.
// triggeredAt is intentionally excluded: it is a log prefix, not an API
// instant, per the published contract.
func IsPublicTimePointField(field string) bool {
	if strings.HasSuffix(field, "At") && field != "triggeredAt" {
		return true
	}
	if strings.HasSuffix(field, "UnixMs") {
		return true
	}
	switch field {
	case "timestamp", "ts", "mtimeMs":
		return true
	default:
		return false
	}
}
