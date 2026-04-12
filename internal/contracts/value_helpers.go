package contracts

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func anyIntNode(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		number, _ := v.Int64()
		return int(number)
	case string:
		number, _ := strconv.Atoi(strings.TrimSpace(v))
		return number
	default:
		return 0
	}
}

func anyBoolNode(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func anyStringNode(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func anyMapNode(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func anyListStrings(value any) []string {
	switch v := value.(type) {
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if text := anyStringNode(item); text != "" {
				items = append(items, text)
			}
		}
		return items
	case []string:
		return append([]string(nil), v...)
	case string:
		if text := strings.TrimSpace(v); text != "" {
			return []string{text}
		}
	}
	return nil
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func AnyIntNode(value any) int {
	return anyIntNode(value)
}

func AnyBoolNode(value any) bool {
	return anyBoolNode(value)
}

func AnyStringNode(value any) string {
	return anyStringNode(value)
}

func AnyMapNode(value any) map[string]any {
	return anyMapNode(value)
}

func AnyListStrings(value any) []string {
	return anyListStrings(value)
}

func StringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.Itoa(int(v))
	case json.Number:
		return v.String()
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func IntValue(value any) int {
	return anyIntNode(value)
}

func FirstNonEmptyString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(StringValue(value)); text != "" {
			return text
		}
	}
	return ""
}
