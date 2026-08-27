package chat

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// BuildChatScopeRef returns the model-visible URI reference for a file stored
// inside the current chat. The current chat id is runtime context and must not
// be embedded in Markdown.
func BuildChatScopeRef(relativePath string) (string, error) {
	segments, err := validatedResourceSegments(relativePath)
	if err != nil {
		return "", err
	}
	encoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	return strings.Join(encoded, "/"), nil
}

// BuildResourceKey returns the logical key consumed by the HTTP resource data
// plane. Unlike BuildChatScopeRef, this value is never written into Markdown
// or public tool/event URL fields.
func BuildResourceKey(chatID string, relativePath string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return "", fmt.Errorf("invalid chat id")
	}
	reference, err := BuildChatScopeRef(relativePath)
	if err != nil {
		return "", err
	}
	return url.PathEscape(chatID) + "/" + reference, nil
}

// ParseResourceKey parses the decoded value of /api/resource's file query.
// New tool results contain an encoded relative URI reference, while legacy
// records contain an already-decoded chat-relative key. Valid escapes are
// decoded once; a legacy literal percent remains literal. Encoded traversal is
// still rejected after decoding.
func ParseResourceKey(resourceKey string) (string, string, error) {
	raw := strings.TrimSpace(resourceKey)
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return "", "", fmt.Errorf("invalid resource key")
	}
	if strings.Contains(raw, "://") {
		return "", "", fmt.Errorf("invalid resource key")
	}
	rawSegments := strings.Split(raw, "/")
	if len(rawSegments) < 2 {
		return "", "", fmt.Errorf("resource key must include chat id and file path")
	}
	segments := make([]string, 0, len(rawSegments))
	for _, rawSegment := range rawSegments {
		segment := rawSegment
		if decoded, err := url.PathUnescape(rawSegment); err == nil {
			segment = decoded
		}
		if !validResourceSegment(segment) {
			return "", "", fmt.Errorf("invalid resource key")
		}
		segments = append(segments, segment)
	}
	chatID := segments[0]
	if !ValidChatID(chatID) {
		return "", "", fmt.Errorf("invalid resource chat id")
	}
	relativePath := strings.Join(segments[1:], "/")
	if IsToolInternalPath(relativePath) || IsBTWInternalPath(relativePath) {
		return "", "", fmt.Errorf("resource access denied")
	}
	return chatID, relativePath, nil
}

func validatedResourceSegments(relativePath string) ([]string, error) {
	raw := strings.TrimSpace(strings.ReplaceAll(relativePath, `\`, "/"))
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil, fmt.Errorf("invalid resource path")
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != raw {
		return nil, fmt.Errorf("invalid resource path")
	}
	segments := strings.Split(clean, "/")
	for _, segment := range segments {
		if !validResourceSegment(segment) {
			return nil, fmt.Errorf("invalid resource path")
		}
	}
	if IsToolInternalPath(clean) || IsBTWInternalPath(clean) {
		return nil, fmt.Errorf("resource access denied")
	}
	return segments, nil
}

func validResourceSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `/\`) {
		return false
	}
	for _, value := range segment {
		if value == 0 || value < 0x20 || value == 0x7f {
			return false
		}
	}
	decoded := segment
	for depth := 0; depth < 4; depth++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return true
		}
		if next == "." || next == ".." || strings.ContainsAny(next, `/\`) {
			return false
		}
		if next == decoded {
			return true
		}
		decoded = next
	}
	next, err := url.PathUnescape(decoded)
	return err != nil || next == decoded
}
