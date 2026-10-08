package toolinput

// ParseBool accepts native booleans and only the exact strings "true" and
// "false". The second result distinguishes invalid input from a valid false.
func ParseBool(value any) (bool, bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case string:
		switch value {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// NormalizeBooleanFields normalizes only boolean fields in a native compact
// contract. Invalid values remain intact for the existing validator.
func NormalizeBooleanFields(values map[string]any, fields map[string]string) {
	for key, rule := range fields {
		if rule == "b" || rule == "b!" {
			if value, ok := ParseBool(values[key]); ok {
				values[key] = value
			}
		}
	}
}

// NormalizeSchemaBooleans normalizes explicitly typed properties and array
// items in a platform-owned schema. Opaque objects, additionalProperties and
// unspecified fields are intentionally left untouched. It does not validate.
func NormalizeSchemaBooleans(value any, schema map[string]any) any {
	if schema["type"] == "boolean" {
		if parsed, ok := ParseBool(value); ok {
			return parsed
		}
		return value
	}
	switch value := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		for key, raw := range properties {
			child, ok := raw.(map[string]any)
			if item, exists := value[key]; ok && exists {
				value[key] = NormalizeSchemaBooleans(item, child)
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range value {
				value[i] = NormalizeSchemaBooleans(item, items)
			}
		}
	}
	return value
}
