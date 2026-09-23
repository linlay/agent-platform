package chat

import "strings"

func validateResponseState(line map[string]any) error {
	if id, exists := line["responseId"]; exists {
		text, ok := id.(string)
		if !ok || strings.TrimSpace(text) == "" || text != strings.TrimSpace(text) || line["_type"] != StepLineTypeReact {
			return newJSONLSchemaViolation(line, "responseId", "non-empty string on react line", jsonValueType(id), "invalid model response ID")
		}
	}
	for _, m := range anyMessageSlice(line["messages"]) {
		for _, part := range anyMessageSlice(m["reasoning_content"]) {
			if part["type"] != "encrypted_text" {
				continue
			}
			text, ok := part["encrypted_text"].(string)
			id, idOK := part["id"].(string)
			if !ok || text == "" || !idOK || id == "" || part["text"] != nil || m["role"] != "assistant" {
				return newJSONLSchemaViolation(line, "reasoning_content", "assistant encrypted_text with id and encrypted_text", "invalid", "invalid encrypted reasoning part")
			}
			if summary, exists := part["summary"]; exists {
				parts, ok := summary.([]any)
				if !ok {
					return newJSONLSchemaViolation(line, "reasoning_content.summary", "array", jsonValueType(summary), "invalid reasoning summary")
				}
				for _, value := range parts {
					s, ok := value.(map[string]any)
					if !ok || s["type"] != "summary_text" {
						return newJSONLSchemaViolation(line, "reasoning_content.summary", "summary_text parts", "invalid", "invalid summary part")
					}
					if _, ok := s["text"].(string); !ok {
						return newJSONLSchemaViolation(line, "reasoning_content.summary.text", "string", "invalid", "invalid summary text")
					}
				}
			}
		}
	}
	return nil
}
