package stream

import "agent-platform/internal/i18n"

// Shared events use the default language; publication resolves the viewer's locale.
func ReasoningLabelForID(reasoningID string) string {
	return i18n.ReasoningLabelForID(i18n.DefaultLocale, reasoningID)
}
