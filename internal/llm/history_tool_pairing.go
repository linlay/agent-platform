package llm

import (
	"log"
	"strings"
)

// repairUnpairedToolResults keeps rebuilt history valid for providers that
// reject a tool result without its call. A result whose call was never
// persisted gets a synthetic empty-arguments call placed directly before it;
// a result that cannot name its call or tool is dropped.
func (e *LLMAgentEngine) repairUnpairedToolResults(runID string, messages []openAIMessage) []openAIMessage {
	known := map[string]bool{}
	repaired := false
	out := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			known[call.ID] = true
		}
		if message.Role != "tool" || known[message.ToolCallID] {
			out = append(out, message)
			continue
		}
		repaired = true
		toolID, toolName := strings.TrimSpace(message.ToolCallID), strings.TrimSpace(message.Name)
		if toolID == "" || toolName == "" {
			log.Printf("[llm][run:%s][history] dropped tool result without call: toolCallId=%q name=%q", runID, toolID, toolName)
			continue
		}
		log.Printf("[llm][run:%s][history] restored missing tool call: toolCallId=%s name=%s", runID, toolID, toolName)
		known[message.ToolCallID] = true
		out = append(out, openAIMessage{
			Role:        "assistant",
			OriginRunID: message.OriginRunID,
			OriginActor: message.OriginActor,
			ToolCalls: []openAIToolCall{{
				ID:       message.ToolCallID,
				Type:     "function",
				Function: openAIFunctionCall{Name: toolName, Arguments: "{}"},
			}},
		}, message)
	}
	if !repaired {
		return messages
	}
	return out
}
