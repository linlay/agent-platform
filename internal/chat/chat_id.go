package chat

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	thinkingBlockPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<thinking\b[^>]*>.*?</thinking\s*>`),
		regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think\s*>`),
	}
	thinkingOpenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<thinking\b[^>]*>.*\z`),
		regexp.MustCompile(`(?is)<think\b[^>]*>.*\z`),
	}
)

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "default"
	}
	return truncateRunes(message, 24)
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

// PreviewLastRunContent strips model thinking tags then truncates for sidebar/API preview.
func PreviewLastRunContent(assistantText string) string {
	return truncateRunes(stripThinkingTags(assistantText), 200)
}

func stripThinkingTags(text string) string {
	out := text
	for _, re := range thinkingBlockPatterns {
		out = re.ReplaceAllString(out, "")
	}
	for _, re := range thinkingOpenPatterns {
		out = re.ReplaceAllString(out, "")
	}
	return strings.TrimSpace(out)
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
