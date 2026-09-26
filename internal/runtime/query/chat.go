package query

import (
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

const (
	activeRunConflictCode    = "active_run_conflict"
	activeRunConflictMessage = "multiple active runs found for chat"
	activeRunFoundMessage    = "active run found for chat"
)

func activeRunConflictInfoFor(chatID string, message string, runIDs []string) *queryinput.ChatErrorInfo {
	if message == "" {
		message = activeRunConflictMessage
	}
	return &queryinput.ChatErrorInfo{
		Code:    activeRunConflictCode,
		Message: message,
		ChatID:  chatID,
		RunIDs:  append([]string(nil), runIDs...),
	}
}

func activeRunFoundInfo(chatID string, runIDs []string) *queryinput.ChatErrorInfo {
	return activeRunConflictInfoFor(chatID, activeRunFoundMessage, runIDs)
}

func isTerminalRunStreamEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "run.complete", "run.cancel", "run.error":
		return true
	default:
		return false
	}
}

func activeRunConflictInfo(conflict *contracts.ActiveRunConflictError) *queryinput.ChatErrorInfo {
	if conflict == nil {
		return activeRunConflictInfoFor("", activeRunConflictMessage, nil)
	}
	return activeRunConflictInfoFor(conflict.ChatID, activeRunConflictMessage, conflict.RunIDs)
}

func persistedLiveSeqCursor(events []stream.EventData, runID string) int64 {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0
	}
	var (
		currentRunID string
		cursor       int64
	)
	for _, event := range events {
		if eventRunID := strings.TrimSpace(event.String("runId")); eventRunID != "" {
			currentRunID = eventRunID
		}
		if currentRunID == runID {
			if liveSeq := int64(contracts.AnyIntNode(event.Value("liveSeq"))); liveSeq > cursor {
				cursor = liveSeq
			}
		}
		if isTerminalRunStreamEvent(event.Type) {
			currentRunID = ""
		}
	}
	return cursor
}

func chatCreatedPayload(chatID string, chatName string, agentKey string, createdAt int64, source string) map[string]any {
	payload := map[string]any{
		"chatId":    chatID,
		"chatName":  chatName,
		"agentKey":  agentKey,
		"createdAt": createdAt,
	}
	if strings.TrimSpace(source) != "" {
		payload["source"] = strings.TrimSpace(source)
	}
	return payload
}
