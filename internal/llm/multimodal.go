package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/querymessages"
)

// buildUserMessageContent assembles the user message payload for the LLM.
// When the request has image references in the chat dir, it returns an
// OpenAI-compatible multimodal content array: [{type:text,...},{type:image_url,...},...].
// Otherwise it returns the plain text string so existing callers keep working.
func buildUserMessageContent(chatsDir string, chatID string, text string, references []api.Reference, isVision bool, logMedia bool) any {
	return querymessages.BuildContent(chatsDir, chatID, text, references, isVision, logMedia)
}
