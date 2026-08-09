package chat

import (
	"path/filepath"
	"strings"
)

// PendingChatName marks a chat that was allocated before a real query, such
// as a new chat created by an attachment upload. The first accepted query
// promotes it to the message-derived name.
const PendingChatName = "<default>"

const legacyPendingChatName = "default"
const legacyNoChatName = "<no chat name>"

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return PendingChatName
	}
	return truncateRunes(message, 24)
}

func isPendingChatName(chatName string) bool {
	switch strings.TrimSpace(chatName) {
	case "", PendingChatName, legacyPendingChatName, legacyNoChatName:
		return true
	default:
		return false
	}
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) > max {
		return string(runes[:max])
	}
	return text
}

func ValidChatID(chatID string) bool {
	if strings.TrimSpace(chatID) == "" {
		return false
	}
	if strings.Contains(chatID, "..") || strings.Contains(chatID, "/") || strings.Contains(chatID, `\`) {
		return false
	}
	clean := filepath.Clean(chatID)
	return clean == chatID && clean != "." && clean != string(filepath.Separator)
}
