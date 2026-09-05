package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/hitl"
	"agent-platform/internal/querymessages"
)

func normalizePlanningConfirmationSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitl.NormalizePlanningConfirmation(args, params)
}

func buildUserMessageContent(chatsDir string, chatID string, text string, references []api.Reference, isVision bool, logMedia bool) any {
	return querymessages.BuildContentWithOptions(chatsDir, chatID, text, references, isVision, logMedia, querymessages.BuildOptions{})
}
