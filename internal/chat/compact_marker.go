package chat

// A compact marker contains policy only, never a second copy of message content.
// An absent keep list excludes the entire line. Legacy string markers do too.
func compactKeep(line map[string]any) map[string]bool {
	marker, ok := line["_compact"].(map[string]any)
	if !ok {
		return nil
	}
	keep := map[string]bool{}
	switch values := marker["keep"].(type) {
	case []any:
		for _, value := range values {
			if key, ok := value.(string); ok {
				keep[key] = true
			}
		}
	case []string:
		for _, key := range values {
			keep[key] = true
		}
	}
	return keep
}

func compactMarker(level, id string, keep map[string]bool) map[string]any {
	marker := map[string]any{"level": level, "id": id}
	var values []string
	for _, category := range []string{"content", "reasoning", "tool"} {
		if keep[category] {
			values = append(values, category)
		}
	}
	if len(values) > 0 {
		marker["keep"] = values
	}
	return marker
}

func projectCompactMessage(message map[string]any, keep map[string]bool) map[string]any {
	out := cloneMessageMap(message)
	role := stringFromAny(message["role"])
	if role == "tool" {
		if !keep["tool"] {
			return nil
		}
		return out
	}
	if !keep["content"] {
		delete(out, "content")
	}
	if !keep["reasoning"] {
		delete(out, "reasoning_content")
	}
	if !keep["tool"] {
		delete(out, "tool_calls")
	}
	if !hasCompactContent(out["content"]) && anyCompactText(out["reasoning_content"]) == "" && len(anyMessageSlice(out["tool_calls"])) == 0 {
		return nil
	}
	return out
}

// Projection is read-only: replay and audit always see the original fields.
func projectCompactLine(line map[string]any) map[string]any {
	if _, exists := line["_compact"]; !exists {
		return line
	}
	keep := compactKeep(line)
	out := cloneJSONLineMap(line)
	var messages []map[string]any
	for _, message := range anyMessageSlice(line["messages"]) {
		if projected := projectCompactMessage(message, keep); projected != nil {
			messages = append(messages, projected)
		}
	}
	out["messages"] = messagesToAny(messages)
	return out
}

func messagesToAny(messages []map[string]any) []any {
	out := make([]any, len(messages))
	for i, m := range messages {
		out[i] = m
	}
	return out
}

func compactMarkerID(value any) string {
	if marker, ok := value.(map[string]any); ok {
		return stringFromAny(marker["id"])
	}
	return stringFromAny(value)
}

func hasCompactContent(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	default:
		return true
	}
}
