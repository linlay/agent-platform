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

func runFinishedPushPayload(runID, chatID string, finishedAt int64) map[string]any {
	return map[string]any{
		"runId":      runID,
		"chatId":     chatID,
		"finishedAt": finishedAt,
	}
}

func catalogUpdatedPushPayload(reason string, updatedAt int64) map[string]any {
	return map[string]any{
		"reason":    reason,
		"updatedAt": updatedAt,
	}
}
