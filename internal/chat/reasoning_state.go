package chat

import "agent-platform/internal/modelcontent"

func hasEncryptedReasoning(value any) bool {
	return len(modelcontent.EncryptedReasoningParts(value)) > 0
}
func hasReasoning(value any) bool { return anyCompactText(value) != "" || hasEncryptedReasoning(value) }
func normalizeReasoningContent(value any) any {
	if hasEncryptedReasoning(value) {
		return value
	}
	return extractTextFromContent(value)
}

// Text-only consumers must never serialize opaque provider reasoning.
func readableReasoningMessage(message map[string]any) map[string]any {
	out := cloneMessageMap(message)
	if hasEncryptedReasoning(out["reasoning_content"]) {
		text := anyCompactText(out["reasoning_content"])
		if text == "" {
			delete(out, "reasoning_content")
		} else {
			out["reasoning_content"] = text
		}
	}
	return out
}
