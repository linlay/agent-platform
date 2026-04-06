package engine

import (
	"encoding/json"
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
