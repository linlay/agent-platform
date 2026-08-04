package chat

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// BuildResourceRef returns the model-visible, relative URI reference for a
// file stored inside a chat. It deliberately does not expose the HTTP
// transport endpoint or any host filesystem path.
func BuildResourceRef(chatID string, relativePath string) (string, error) {
	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return "", fmt.Errorf("invalid chat id")
	}
	segments, err := validatedResourceSegments(relativePath)
	if err != nil {
		return "", err
	}
	encoded := make([]string, 0, len(segments)+1)
	encoded = append(encoded, url.PathEscape(chatID))
	for _, segment := range segments {
		encoded = append(encoded, url.PathEscape(segment))
	}
	return strings.Join(encoded, "/"), nil
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
	return segment != "" && segment != "." && segment != ".." &&
		!strings.ContainsAny(segment, `/\`) && !strings.ContainsRune(segment, 0)
}
