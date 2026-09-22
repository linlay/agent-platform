package llm

import "agent-platform/internal/chat"

func (s *llmRunStream) compactRunCategories(keepRecent int) ([]openAIMessage, int, int, int) {
	raw := modelMessagesToMaps(s.messages)
	projection := chat.ProjectL1(raw, keepRecent, s.pinnedMessageStart, s.pinnedMessageEnd, preserveReasoningContent(s.protocolConfig, s.stageSettings))
	out := make([]openAIMessage, 0, len(raw))
	for i, message := range projection.Messages {
		if message == nil {
			continue
		}
		// Preserve provider representations and private origin fields exactly.
		value := cloneModelMessages(s.messages[i : i+1])[0]
		if _, ok := message["reasoning_content"]; !ok {
			value.ReasoningContent = ""
		}
		if _, ok := message["tool_calls"]; !ok {
			value.ToolCalls = nil
		}
		out = append(out, value)
	}
	return out, projection.ToolsCleared, projection.ToolsKept, projection.ReasoningCleared
}
