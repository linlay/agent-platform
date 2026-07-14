package timecontract

import (
	"fmt"
	"strings"
	"time"
)

const (
	// OutputSchemaEpochMillis is the explicit JSON-schema extension for a
	// platform epoch-millisecond value. It is intentionally not inferred from
	// a property name such as createdAt or timestamp.
	OutputSchemaEpochMillis = "epoch-ms"
	outputSchemaTimeKey     = "x-platform-time"
	outputSchemaPairKey     = "x-platform-time-pair"
)

// ValidateOutputSchema validates only time semantics explicitly declared by a
// tool output schema. Unannotated properties are opaque business data,
// including properties whose names look like conventional time fields.
//
// The walker supports the structural keywords needed to reach declarations:
// properties, required, items, and oneOf. It is deliberately not a general
// JSON Schema implementation.
func ValidateOutputSchema(payload any, schema map[string]any, location string) error {
	if len(schema) == 0 {
		return nil
	}
	return validateOutputSchemaNode(payload, schema, strings.TrimSpace(location), "", nil)
}

func validateOutputSchemaNode(value any, schema map[string]any, location, field string, parent map[string]any) error {
	if location == "" {
		location = "tool.result"
	}
	if marker, exists := stringSchemaValue(schema[outputSchemaTimeKey]); exists {
		if marker != OutputSchemaEpochMillis {
			return &Violation{Field: fieldOrRoot(field), Location: location, Reason: "has unsupported x-platform-time declaration"}
		}
		if _, err := ParseEpochMillis(value, fieldOrRoot(field), location); err != nil {
			return err
		}
	}
	if format, _ := stringSchemaValue(schema["format"]); format == "date-time" {
		if _, err := ParseRFC3339(value, fieldOrRoot(field), location); err != nil {
			return err
		}
	}
	if pairField, exists := stringSchemaValue(schema[outputSchemaPairKey]); exists {
		if parent == nil {
			return &Violation{Field: fieldOrRoot(field), Location: location, Reason: "x-platform-time-pair requires an object parent"}
		}
		paired, ok := parent[pairField]
		if !ok {
			return &Violation{Field: fieldOrRoot(field), Location: location, Reason: "requires matching " + pairField}
		}
		readable, err := ParseRFC3339(value, fieldOrRoot(field), location)
		if err != nil {
			return err
		}
		millis, err := ParseEpochMillis(paired, pairField, parentLocation(location)+"."+pairField)
		if err != nil {
			return err
		}
		if readable.Nanosecond()%int(time.Millisecond) != 0 || readable.UnixMilli() != millis {
			return &Violation{Field: fieldOrRoot(field), Location: location, Reason: "must represent the same instant as " + pairField}
		}
	}

	if alternatives, ok := schema["oneOf"].([]any); ok && len(alternatives) > 0 {
		for _, raw := range alternatives {
			candidate, ok := raw.(map[string]any)
			if !ok || !schemaMatches(value, candidate) {
				continue
			}
			return validateOutputSchemaNode(value, candidate, location, field, parent)
		}
		return nil
	}

	object, _ := value.(map[string]any)
	if properties, ok := schema["properties"].(map[string]any); ok && object != nil {
		required := requiredSchemaProperties(schema)
		for property, rawPropertySchema := range properties {
			propertySchema, ok := rawPropertySchema.(map[string]any)
			if !ok {
				continue
			}
			propertyValue, exists := object[property]
			if !exists {
				if required[property] && schemaContainsTimeDeclaration(propertySchema) {
					return &Violation{Field: property, Location: location + "." + property, Reason: "is required"}
				}
				continue
			}
			if err := validateOutputSchemaNode(propertyValue, propertySchema, location+"."+property, property, object); err != nil {
				return err
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if values, ok := value.([]any); ok {
			for index, item := range values {
				if err := validateOutputSchemaNode(item, items, fmt.Sprintf("%s[%d]", location, index), "", nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func stringSchemaValue(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func requiredSchemaProperties(schema map[string]any) map[string]bool {
	required := map[string]bool{}
	values, _ := schema["required"].([]any)
	for _, value := range values {
		if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
			required[name] = true
		}
	}
	return required
}

func schemaContainsTimeDeclaration(schema map[string]any) bool {
	if _, exists := stringSchemaValue(schema[outputSchemaTimeKey]); exists {
		return true
	}
	if format, _ := stringSchemaValue(schema["format"]); format == "date-time" {
		return true
	}
	if _, exists := stringSchemaValue(schema[outputSchemaPairKey]); exists {
		return true
	}
	return false
}

func schemaMatches(value any, schema map[string]any) bool {
	if expected, exists := schema["const"]; exists && fmt.Sprint(value) != fmt.Sprint(expected) {
		return false
	}
	if typeName, exists := stringSchemaValue(schema["type"]); exists {
		switch typeName {
		case "object":
			if _, ok := value.(map[string]any); !ok {
				return false
			}
		case "array":
			if _, ok := value.([]any); !ok {
				return false
			}
		case "string":
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for property, rawSchema := range properties {
			propertySchema, ok := rawSchema.(map[string]any)
			if !ok {
				continue
			}
			if expected, exists := propertySchema["const"]; exists {
				actual, found := object[property]
				if !found || fmt.Sprint(actual) != fmt.Sprint(expected) {
					return false
				}
			}
		}
	}
	return true
}

func parentLocation(location string) string {
	if index := strings.LastIndex(location, "."); index > 0 {
		return location[:index]
	}
	return location
}

func fieldOrRoot(field string) string {
	if strings.TrimSpace(field) == "" {
		return "result"
	}
	return field
}
