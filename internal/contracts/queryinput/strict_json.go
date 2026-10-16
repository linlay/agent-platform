package queryinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	// encoding/json matches field names case-insensitively; the contract does not.
	return requireExactFieldNames(data, reflect.TypeOf(target))
}

// requireExactFieldNames rejects keys that differ from a field name only in
// case. Keys unrelated to any field are left to each type's own decoding rules.
func requireExactFieldNames(data []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil {
			return nil
		}
		fields := map[string]reflect.Type{}
		collectJSONFields(t, fields)
		for key, value := range object {
			if fieldType, ok := fields[key]; ok {
				if err := requireExactFieldNames(value, fieldType); err != nil {
					return err
				}
				continue
			}
			for name := range fields {
				if strings.EqualFold(name, key) {
					return fmt.Errorf("%w %q", ErrUnknownField, key)
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return nil
		}
		for _, item := range items {
			if err := requireExactFieldNames(item, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) != nil {
			return nil
		}
		for _, value := range object {
			if err := requireExactFieldNames(value, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectJSONFields(t reflect.Type, fields map[string]reflect.Type) {
	for index := 0; index < t.NumField(); index++ {
		field := t.Field(index)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				collectJSONFields(embedded, fields)
				continue
			}
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
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
