package query

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

func runStartedPushPayload(runID, chatID, agentKey string, startedAt int64) map[string]any {
	return map[string]any{
		"runId":     runID,
		"chatId":    chatID,
		"agentKey":  agentKey,
		"startedAt": startedAt,
	}
}
