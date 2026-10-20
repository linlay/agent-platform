package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

// DeepSeek supplies already extracted Markdown. Preserve that evidence while
// limiting the actual model message, including JSON escaping and metadata.
func deepSeekWebFetchResult(payload map[string]any, profile config.WebFetchProfileConfig, execCtx *ExecutionContext) ToolExecutionResult {
	content, _ := payload["result"].(string)
	readbackReason := "chat_context_unavailable"
	chatDir := webFetchChatDir(execCtx)
	canPersist := chatDir != "" && filepath.IsAbs(chatDir)
	if execCtx != nil && IsReadOnlyToolExecutionPolicy(execCtx.ToolExecutionPolicy) {
		canPersist = false
		readbackReason = "read_only_run"
	}

	preview, output := webFetchModelPreview(payload, profile.MaxMarkdownChars, nil, readbackReason)
	if preview["contentTruncatedForLLM"] == true && canPersist {
		readbackReason = "save_failed"
		if path, err := persistWebFetchBinary([]byte(content), "text/markdown", execCtx); err == nil {
			relative, relErr := filepath.Rel(chatDir, path)
			if relErr == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				sum := sha256.Sum256([]byte(content))
				payload["resultRef"] = map[string]any{
					"path":        filepath.ToSlash(relative),
					"sizeBytes":   len(content),
					"sha256":      hex.EncodeToString(sum[:]),
					"contentType": "text/markdown",
				}
			}
		}
		ref, _ := payload["resultRef"].(map[string]any)
		preview, output = webFetchModelPreview(payload, profile.MaxMarkdownChars, ref, readbackReason)
	}
	// These describe the model preview, not the complete Structured.result.
	for _, key := range []string{"contentTruncatedForLLM", "contentChars", "metadataTruncatedForLLM", "urlOmittedForLLM", "urlNotice", "readbackAvailable", "readbackUnavailableReason", "recovery"} {
		if value, exists := preview[key]; exists {
			payload[key] = value
		}
	}
	return ToolExecutionResult{Structured: payload, Output: output}
}

func webFetchModelPreview(payload map[string]any, maxMarkdownBytes int, ref map[string]any, readbackReason string) (map[string]any, string) {
	content, _ := payload["result"].(string)
	preview := make(map[string]any, len(payload)+6)
	for key, value := range payload {
		if text, ok := value.(string); ok && key != "result" && key != "url" {
			limit := 256
			switch key {
			case "title":
				limit = 1024
			case "extractionPrompt":
				limit = 2048
			}
			if encoded, _ := json.Marshal(text); len(encoded) > limit {
				value = webFetchPreviewMetadata(text, limit)
				preview["metadataTruncatedForLLM"] = true
			}
		}
		preview[key] = value
	}
	// Preserve the source URL verbatim or omit it. Cutting it could invent a
	// different link, and escaped Unicode URLs can consume the entire budget.
	if encoded, _ := json.Marshal(payload["url"]); len(encoded) > 4096 {
		delete(preview, "url")
		preview["urlOmittedForLLM"] = true
		preview["urlNotice"] = "The original source URL is in the web_fetch call arguments; it was omitted from this preview."
	}
	build := func(limit int) string {
		text := webFetchPreviewPrefix(content, limit)
		preview["result"] = text
		if len(text) < len(content) {
			preview["contentTruncatedForLLM"] = true
			preview["contentChars"] = len(content)
			preview["readbackAvailable"] = ref != nil
			if ref != nil {
				preview["resultRef"] = ref
				delete(preview, "readbackUnavailableReason")
				preview["recovery"] = "Read the complete original Markdown with file_read: use filePath '@chat/' + resultRef.path and offset/limit for line ranges. Do not assume the omitted content."
			} else {
				preview["readbackUnavailableReason"] = readbackReason
				preview["recovery"] = "Only this bounded preview is available to the model; the complete Markdown was not saved for file_read. Do not assume the omitted content."
			}
		} else {
			for _, key := range []string{"contentTruncatedForLLM", "contentChars", "readbackAvailable", "readbackUnavailableReason", "recovery"} {
				delete(preview, key)
			}
		}
		encoded, _ := json.Marshal(preview)
		return string(encoded)
	}
	limit := min(len(content), maxInt(maxMarkdownBytes, 100000), ToolModelOutputMaxChars)
	if output := build(limit); len(output) <= ToolModelOutputMaxChars {
		return preview, output
	}
	low, high, best := 0, limit, 0
	for low <= high {
		mid := low + (high-low)/2
		if len(build(mid)) <= ToolModelOutputMaxChars {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return preview, build(best)
}

func webFetchPreviewPrefix(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) && len(text) > 0 {
		text = text[:len(text)-1]
	}
	return text
}

func webFetchPreviewMetadata(text string, maxJSONBytes int) string {
	low, high, best := 0, min(len(text), maxJSONBytes), 0
	for low <= high {
		mid := low + (high-low)/2
		encoded, _ := json.Marshal(webFetchPreviewPrefix(text, mid))
		if len(encoded) <= maxJSONBytes {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return webFetchPreviewPrefix(text, best)
}
