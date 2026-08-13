package server

// These helpers define the public WebSocket push time-field contract. Stream
// events deliberately retain their envelope timestamp; push notifications use
// the business-specific instant instead.
func runStartedPushPayload(runID, chatID, agentKey string, startedAt int64) map[string]any {
	return map[string]any{
		"runId":     runID,
		"chatId":    chatID,
		"agentKey":  agentKey,
		"startedAt": startedAt,
	}
}

func runFinishedPushPayload(runID, chatID, finishReason string, finishedAt int64) map[string]any {
	finishReason = normalizedRunFinishedReason(finishReason)
	return map[string]any{
		"runId":        runID,
		"chatId":       chatID,
		"status":       runFinishedStatus(finishReason),
		"finishReason": finishReason,
		"finishedAt":   finishedAt,
	}
}

func normalizedRunFinishedReason(finishReason string) string {
	switch finishReason {
	case "complete", "error", "cancel":
		return finishReason
	default:
		return "error"
	}
}

func runFinishedStatus(finishReason string) string {
	switch finishReason {
	case "complete":
		return "completed"
	case "cancel":
		return "interrupted"
	default:
		return "failed"
	}
}

func catalogUpdatedPushPayload(reason string, updatedAt int64) map[string]any {
	return map[string]any{
		"reason":    reason,
		"updatedAt": updatedAt,
	}
}
