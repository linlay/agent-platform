package chat

import "strings"

const runInterruptedCode = "run_interrupted"

// isInterruptedAwaitingAnswerPayload identifies the compact persisted answer
// shape used only when a run is interrupted before an awaiting tool executes.
// Its event type and timestamp are carried by the enclosing submit line.
func isInterruptedAwaitingAnswerPayload(raw any) bool {
	answer, ok := raw.(map[string]any)
	if !ok || len(answer) == 0 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(stringValue(answer["status"])), "error") ||
		strings.TrimSpace(stringValue(answer["awaitingId"])) == "" ||
		strings.TrimSpace(stringValue(answer["mode"])) == "" {
		return false
	}
	errorPayload := mapValue(answer["error"])
	return strings.EqualFold(strings.TrimSpace(stringValue(errorPayload["code"])), runInterruptedCode) &&
		strings.EqualFold(strings.TrimSpace(stringValue(errorPayload["reason"])), runInterruptedCode)
}

func compactInterruptedAwaitingAnswer(answer map[string]any) map[string]any {
	if !isInterruptedAwaitingAnswerPayload(answer) {
		return answer
	}
	delete(answer, "type")
	delete(answer, "timestamp")
	return answer
}

func replayAwaitingAnswerPayload(answer map[string]any, lineUpdatedAt int64) map[string]any {
	answer = cloneStringAnyMap(answer)
	if !isInterruptedAwaitingAnswerPayload(answer) {
		return answer
	}
	if strings.TrimSpace(stringValue(answer["type"])) == "" {
		answer["type"] = "awaiting.answer"
	}
	if _, exists := answer["timestamp"]; !exists {
		answer["timestamp"] = lineUpdatedAt
	}
	return answer
}
