package runexec

import (
	"strings"

	"agent-platform/internal/chat"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

// Result uses the exact completion record passed to storage, independently of
// EventBus subscribers, their replay window, and their delivery speed.
func (r *ProxyEventRecorder) Result(completion chat.RunCompletion) runtimetypes.QueryResult {
	result := runtimetypes.QueryResult{
		ChatID: completion.ChatID, RunID: completion.RunID, Completion: &completion,
		Content: completion.AssistantText, Usage: completion.Usage, FinishReason: completion.FinishReason,
		ErrorPayload: r.errorPayload, ErrorMessage: r.errorMessage,
	}
	if r.req.IncludeFullText {
		result.FullText = r.fullText.Text(completion.AssistantText)
	}
	return result
}

func proxyResultErrorMessage(event stream.EventData) string {
	if message := strings.TrimSpace(event.String("message")); message != "" {
		return message
	}
	if message := strings.TrimSpace(event.String("error")); message != "" {
		return message
	}
	if payload, ok := event.Value("error").(map[string]any); ok {
		for _, key := range []string{"message", "error", "code"} {
			if message, _ := payload[key].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
	}
	if value := event.Value("error"); value != nil {
		return strings.TrimSpace(FormatFullTextValue(value))
	}
	return "run failed"
}
