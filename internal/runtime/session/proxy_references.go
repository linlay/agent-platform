package session

import (
	"net/url"
	"path/filepath"
	"strings"

	"agent-platform/internal/chat"
)

func ResourceFileParam(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if IsResourceURL(parsed, rawURL) {
		return parsed.Query().Get("file")
	}
	if raw == "@chat" || strings.HasPrefix(raw, "@chat/") ||
		raw == "@workspace" || strings.HasPrefix(raw, "@workspace/") ||
		raw == "/chat" || strings.HasPrefix(raw, "/chat/") ||
		raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") ||
		strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return ""
	}
	chatID, relativePath, err := chat.ParseResourceKey(raw)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join(chatID, relativePath))
}

func ResourceFileParamForChat(chatID string, rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	if parsed.IsAbs() || IsResourceURL(parsed, rawURL) {
		return ResourceFileParam(rawURL)
	}
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		raw == "@chat" || strings.HasPrefix(raw, "@chat/") ||
		raw == "@workspace" || strings.HasPrefix(raw, "@workspace/") ||
		raw == "/chat" || strings.HasPrefix(raw, "/chat/") ||
		raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") ||
		strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return ""
	}
	fileParam, err := chat.BuildResourceKey(chatID, parsed.Path)
	if err != nil {
		return ""
	}
	return fileParam
}

func IsResourceURL(parsed *url.URL, rawURL string) bool {
	if parsed == nil {
		return false
	}
	if parsed.Path == "" && strings.HasPrefix(strings.TrimSpace(rawURL), "/api/resource") {
		return true
	}
	return parsed.Path == "/api/resource" || strings.HasSuffix(parsed.Path, "/api/resource")
}
