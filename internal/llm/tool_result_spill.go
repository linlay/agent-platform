package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
)

const toolResultSpillThresholdBytes = 64 * 1024
const toolResultPreviewBytes = 2000

func (s *llmRunStream) maybeSpillToolResult(invocation *preparedToolInvocation, result ToolExecutionResult) ToolExecutionResult {
	if s != nil && s.execCtx != nil && IsReadOnlyToolExecutionPolicy(s.execCtx.ToolExecutionPolicy) {
		return result
	}
	if invocation == nil || !toolResultSpillEligible(invocation.toolName) {
		return result
	}
	if result.RawParams != nil {
		return result
	}
	chatDir := s.toolResultChatDir()
	if strings.TrimSpace(chatDir) == "" {
		return result
	}

	fullPayload := fullToolResultPayload(invocation.toolName, result)
	fullJSON, err := json.Marshal(fullPayload)
	if err != nil || len(fullJSON) <= toolResultSpillThresholdBytes {
		return result
	}

	sum := sha256.Sum256(fullJSON)
	sha := hex.EncodeToString(sum[:])
	toolFile := safeToolResultFileName(invocation.toolID, sha)
	relPath := filepath.ToSlash(filepath.Join(chat.ToolRootDirName, chat.ToolResultsDirName, toolFile))
	absPath := filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName, toolFile)
	if err := atomicWriteToolResult(absPath, fullJSON); err != nil {
		return result
	}

	ref := map[string]any{
		"path":      relPath,
		"sizeBytes": len(fullJSON),
		"sha256":    sha,
	}
	previewPayload := previewToolResultPayload(invocation.toolName, result, fullPayload, ref)
	previewJSON, err := json.Marshal(previewPayload)
	if err != nil {
		return result
	}
	result.Output = string(previewJSON)
	result.Structured = previewPayload
	return result
}

func (s *llmRunStream) toolResultChatDir() string {
	if s == nil {
		return ""
	}
	if dir := strings.TrimSpace(s.session.RuntimeContext.LocalPaths.ChatAttachmentsDir); dir != "" {
		return dir
	}
	if s.engine != nil {
		chatsDir := strings.TrimSpace(s.engine.cfg.Paths.ChatsDir)
		chatID := strings.TrimSpace(s.session.ChatID)
		if chatsDir != "" && chatID != "" {
			return filepath.Join(chatsDir, chatID)
		}
	}
	return ""
}

func toolResultSpillEligible(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "bash", "file_read", "file_glob", "file_grep", "vision_recognize", "regex":
		return true
	default:
		return false
	}
}

func fullToolResultPayload(toolName string, result ToolExecutionResult) any {
	if len(result.Structured) > 0 {
		return result.Structured
	}
	if strings.TrimSpace(toolName) == "bash" {
		return map[string]any{
			"stdout":   result.Output,
			"stderr":   "",
			"exitCode": result.ExitCode,
		}
	}
	return result.Output
}

func previewToolResultPayload(toolName string, result ToolExecutionResult, fullPayload any, ref map[string]any) map[string]any {
	budget := toolResultPreviewBytes
	preview := previewValue(fullPayload, &budget)
	out, ok := preview.(map[string]any)
	if !ok {
		key := "output"
		if strings.TrimSpace(toolName) == "bash" {
			key = "stdout"
		}
		out = map[string]any{
			key:        preview,
			"exitCode": result.ExitCode,
		}
		if strings.TrimSpace(toolName) == "bash" {
			out["stderr"] = ""
		}
	}
	out["truncated"] = true
	out["resultRef"] = ref
	return out
}

func previewValue(value any, budget *int) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for _, key := range previewMapKeys(typed) {
			item := typed[key]
			out[key] = previewValue(item, budget)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = previewValue(item, budget)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for index, item := range typed {
			if budget == nil || *budget <= 0 {
				out[index] = ""
				continue
			}
			out[index] = truncateForToolResultPreview(item, budget)
		}
		return out
	case string:
		if budget == nil || *budget <= 0 {
			return ""
		}
		return truncateForToolResultPreview(typed, budget)
	default:
		return value
	}
}

func previewMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		leftLarge := isLikelyLargeToolResultField(keys[i])
		rightLarge := isLikelyLargeToolResultField(keys[j])
		if leftLarge != rightLarge {
			return !leftLarge
		}
		return keys[i] < keys[j]
	})
	return keys
}

func isLikelyLargeToolResultField(key string) bool {
	switch key {
	case "stdout", "stderr", "content", "contentBase64", "raw", "results":
		return true
	default:
		return false
	}
}

func truncateForToolResultPreview(value string, budget *int) string {
	if budget == nil {
		return value
	}
	if *budget <= 0 {
		return ""
	}
	if len(value) <= *budget {
		*budget -= len(value)
		return value
	}
	suffix := "\n...[truncated; see resultRef]"
	limit := *budget - len(suffix)
	if limit < 0 {
		limit = *budget
		suffix = ""
	}
	truncated := truncateUTF8Bytes(value, limit)
	*budget = 0
	return truncated + suffix
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	truncated := value[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func safeToolResultFileName(toolID string, sha string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(toolID) {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	name := strings.Trim(builder.String(), ". ")
	if name == "" {
		prefix := sha
		if len(prefix) > 16 {
			prefix = prefix[:16]
		}
		name = "tool_" + prefix
	}
	return name + ".json"
}

func atomicWriteToolResult(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tool-result-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
