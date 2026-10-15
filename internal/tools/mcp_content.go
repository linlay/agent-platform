package tools

import (
	"strings"

	. "agent-platform/internal/contracts"
)

// Content is separate from Structured: a remote tool's business output schema
// must not gain platform fields merely because it also returned content blocks.
func withMCPContentBlocks(result ToolExecutionResult, payload map[string]any, contentOnly bool) ToolExecutionResult {
	blocks, _ := payload["content"].([]any)
	if len(blocks) == 0 {
		return result
	}
	// SDK payloads are JSON values. Detach the retained blocks from the caller's
	// payload before they are handed to streaming and persistence consumers.
	result.ContentBlocks = make([]any, len(blocks))
	for index, block := range blocks {
		result.ContentBlocks[index] = cloneMCPContentValue(block)
	}
	content := mcpContentModelText(result.ContentBlocks)
	if contentOnly {
		result.Output = content
		return result
	}
	if content == "" || content == result.Output || content == AnyStringNode(result.Structured["message"]) {
		return result
	}
	// The usual SDK text fallback repeats structuredContent verbatim. Keep its
	// historical single-result output instead of duplicating that same JSON.
	if structured, ok := payload["structuredContent"].(map[string]any); ok && content == MarshalJSON(structured) {
		return result
	}
	if result.Output == "" {
		result.Output = content
	} else {
		result.Output += "\n\n" + content
	}
	return result
}

func cloneMCPContentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneMCPContentValue(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneMCPContentValue(item)
		}
		return cloned
	default:
		return value
	}
}

func mcpContentModelText(blocks []any) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		content, ok := block.(map[string]any)
		if ok && strings.EqualFold(AnyStringNode(content["type"]), "text") {
			text, _ := content["text"].(string)
			parts = append(parts, text)
			continue
		}
		if ok {
			content = CloneMap(content)
			// This is a textual projection, not image/audio ingestion. Retain the
			// original bytes in ContentBlocks while the model sees type, references,
			// annotations and omission metadata without base64 filling its context.
			switch strings.ToLower(AnyStringNode(content["type"])) {
			case "image", "audio":
				omitMCPBinary(content, "data")
			case "resource":
				if resource, ok := content["resource"].(map[string]any); ok {
					resource = CloneMap(resource)
					omitMCPBinary(resource, "blob")
					content["resource"] = resource
				}
			}
			block = content
		}
		parts = append(parts, MarshalJSON(block))
	}
	return strings.Join(parts, "\n")
}

func omitMCPBinary(value map[string]any, field string) {
	if data, ok := value[field].(string); ok {
		delete(value, field)
		value[field+"Omitted"] = true
		value[field+"Chars"] = len(data)
	}
}
