package server

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func (s *Server) normalizeActiveSubmitRun(req api.SubmitRequest) api.SubmitRequest {
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

func (s *Server) activeRunStatusForSubmitTask(taskID string) (contracts.RunStatusInfo, bool) {
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

func submitRequestReferencesRunTask(req api.SubmitRequest, runID string) bool {
	return isTaskIDForRun(req.RunID, runID) || isTaskIDForRun(submitAwaitingTaskID(req.AwaitingID), runID)
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

func isTaskIDForRun(taskID string, runID string) bool {
	taskID = strings.TrimSpace(taskID)
	runID = strings.TrimSpace(runID)
	return taskID != "" && runID != "" && strings.HasPrefix(taskID, runID+"_t_")
}

func rewriteSubmitRunID(req api.SubmitRequest, runID string) api.SubmitRequest {
	req.RunID = strings.TrimSpace(runID)
	return req
}

func fillSubmitChatIDFromStatus(req api.SubmitRequest, status contracts.RunStatusInfo) api.SubmitRequest {
	if strings.TrimSpace(req.ChatID) == "" {
		req.ChatID = strings.TrimSpace(status.ChatID)
	}
	return req
}
