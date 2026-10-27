package llm

import (
	"agent-platform/internal/contracts"
	"strings"
)

// Headers are materialized on a copy at the model boundary, never written into
// the caller's query or the persisted current-message snapshot.
func mailModelInput(raw map[string]any, id string, session contracts.QuerySession) map[string]any {
	if session.MailHeaders == nil || raw["role"] != "user" {
		return raw
	}
	header := session.MailHeaders(id)
	if header == "" {
		return raw
	}
	if raw["_mailHeader"] == header {
		return raw
	}
	result := make(map[string]any, len(raw))
	for k, v := range raw {
		result[k] = v
	}
	result["_mailId"], result["_mailHeader"] = id, header
	switch body := raw["content"].(type) {
	case string:
		result["content"] = header + body
	case []any:
		result["content"] = append([]any{map[string]any{"type": "text", "text": header}}, body...)
	case []map[string]any:
		result["content"] = append([]map[string]any{{"type": "text", "text": header}}, body...)
	}
	return result
}

func mailPersistentContent(content any, header string) any {
	if header == "" {
		return content
	}
	switch body := content.(type) {
	case string:
		return strings.TrimPrefix(body, header)
	case []any:
		if len(body) > 0 {
			if first, ok := body[0].(map[string]any); ok && first["type"] == "text" && first["text"] == header {
				return append([]any(nil), body[1:]...)
			}
		}
	case []map[string]any:
		if len(body) > 0 && body[0]["type"] == "text" && body[0]["text"] == header {
			return append([]map[string]any(nil), body[1:]...)
		}
	}
	return content
}
