package query

import (
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func submitRequestReferencesRunTask(req queryinput.SubmitRequest, runID string) bool {
	return isTaskIDForRun(req.RunID, runID) || isTaskIDForRun(submitAwaitingTaskID(req.AwaitingID), runID)
}

func (s *Service) activeRunStatusForSubmitTask(taskID string) (contracts.RunStatusInfo, bool) {
	if s == nil || s.deps.Runs == nil {
		return contracts.RunStatusInfo{}, false
	}
	parentRunID := parentRunIDFromTaskID(taskID)
	if parentRunID == "" {
		return contracts.RunStatusInfo{}, false
	}
	status, ok := s.deps.Runs.RunStatus(parentRunID)
	if !ok || !isTaskIDForRun(taskID, status.RunID) {
		return contracts.RunStatusInfo{}, false
	}
	return status, true
}

func (s *Service) normalizeActiveSubmitRun(req queryinput.SubmitRequest) queryinput.SubmitRequest {
	if s == nil || s.deps.Runs == nil {
		return req
	}
	runID := strings.TrimSpace(req.RunID)
	if status, ok := s.deps.Runs.RunStatus(runID); ok {
		return fillSubmitChatIDFromStatus(req, status)
	}
	for _, taskID := range []string{runID, submitAwaitingTaskID(req.AwaitingID)} {
		if status, ok := s.activeRunStatusForSubmitTask(taskID); ok {
			return fillSubmitChatIDFromStatus(rewriteSubmitRunID(req, status.RunID), status)
		}
	}
	chatID := strings.TrimSpace(req.ChatID)
	if chatID == "" {
		return req
	}
	status, ok, err := s.deps.Runs.ActiveRunForChat(chatID)
	if err != nil || !ok {
		return req
	}
	if submitRequestReferencesRunTask(req, status.RunID) {
		return fillSubmitChatIDFromStatus(rewriteSubmitRunID(req, status.RunID), status)
	}
	return req
}

func parentRunIDFromTaskID(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	index := strings.LastIndex(taskID, "_t_")
	if index <= 0 || index+3 >= len(taskID) {
		return ""
	}
	return strings.TrimSpace(taskID[:index])
}

func rewriteSubmitRunID(req queryinput.SubmitRequest, runID string) queryinput.SubmitRequest {
	req.RunID = strings.TrimSpace(runID)
	return req
}

func fillSubmitChatIDFromStatus(req queryinput.SubmitRequest, status contracts.RunStatusInfo) queryinput.SubmitRequest {
	if strings.TrimSpace(req.ChatID) == "" {
		req.ChatID = strings.TrimSpace(status.ChatID)
	}
	return req
}

func submitAwaitingTaskID(awaitingID string) string {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return ""
	}
	index := strings.Index(awaitingID, ":")
	if index <= 0 {
		return ""
	}
	return strings.TrimSpace(awaitingID[:index])
}

func isTaskIDForRun(taskID string, runID string) bool {
	taskID = strings.TrimSpace(taskID)
	runID = strings.TrimSpace(runID)
	return taskID != "" && runID != "" && strings.HasPrefix(taskID, runID+"_t_")
}
