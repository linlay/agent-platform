package coder

import (
	"strings"

	"agent-platform/internal/models"
)

func NormalizeReasoningEffort(value string) (string, bool) {
	return models.NormalizeReasoningEffort(value)
}

func NormalizeServiceTier(value string) (string, bool) {
	text := strings.ToUpper(strings.TrimSpace(value))
	switch text {
	case "", "STANDARD", "DEFAULT", "AUTO":
		return "", true
	default:
		return text, text != ""
	}
}
