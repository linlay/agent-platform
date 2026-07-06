package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func (s *Server) listChatSummaries(lastRunID string, agentKey string) ([]api.ChatSummaryResponse, error) {
	items, err := s.deps.Chats.ListChats(lastRunID, agentKey)
	if err != nil {
		return nil, err
	}
	return mapChatSummaries(items), nil
}

func mapChatSummaries(items []chat.Summary) []api.ChatSummaryResponse {
	return mapChatSummariesWithUsage(items, true)
}

func mapChatSummariesWithoutUsage(items []chat.Summary) []api.ChatSummaryResponse {
	return mapChatSummariesWithUsage(items, false)
}

func mapChatSummariesWithUsage(items []chat.Summary, includeUsage bool) []api.ChatSummaryResponse {
	response := make([]api.ChatSummaryResponse, 0, len(items))
	for _, item := range items {
		resp := api.ChatSummaryResponse{
			ChatID:         item.ChatID,
			ChatName:       item.ChatName,
			AgentKey:       item.AgentKey,
			TeamID:         item.TeamID,
			Source:         item.Source,
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
			LastRunID:      item.LastRunID,
			LastRunContent: item.LastRunContent,
			Read:           toAPIReadState(item.Read),
		}
		if item.PendingAwaiting != nil {
			resp.Awaiting = &api.Awaiting{
				AwaitingID: item.PendingAwaiting.AwaitingID,
				RunID:      item.PendingAwaiting.RunID,
				Mode:       item.PendingAwaiting.Mode,
				Status:     "awaiting",
				CreatedAt:  item.PendingAwaiting.CreatedAt,
			}
		}
		if includeUsage {
			usage := mapUsageDataPtr(item.Usage)
			resp.Usage = usage
		}
		response = append(response, resp)
	}
	return response
}

func chatCreatedPayload(chatID string, chatName string, agentKey string, timestamp int64, source string) map[string]any {
	payload := map[string]any{
		"chatId":    chatID,
		"chatName":  chatName,
		"agentKey":  agentKey,
		"timestamp": timestamp,
	}
	if strings.TrimSpace(source) != "" {
		payload["source"] = strings.TrimSpace(source)
	}
	return payload
}

func (s *Server) loadChatDetail(ctx context.Context, chatID string, includeRawMessages bool) (api.ChatDetailResponse, error) {
	detail, err := s.deps.Chats.LoadChat(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}
	summary, err := s.deps.Chats.Summary(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}

	s.enrichToolMetadata(detail.Events, summaryAgentKey(summary))

	response := api.ChatDetailResponse{
		ChatID:        detail.ChatID,
		ChatName:      detail.ChatName,
		Events:        detail.Events,
		ContextWindow: mapChatContextWindow(detail.ContextWindow),
		References:    nil,
	}
	runs, err := s.deps.Chats.ListRuns(chatID)
	if err != nil {
		return api.ChatDetailResponse{}, err
	}
	response.Runs = make([]api.RunSummary, 0, len(runs))
	for _, run := range runs {
		response.Runs = append(response.Runs, mapRunSummary(run))
	}
	if principal := PrincipalFromContext(ctx); principal != nil {
		response.ResourceTicket = s.ticketService.Issue(principal.Subject, detail.ChatID)
	}
	if includeRawMessages {
		response.RawMessages = detail.RawMessages
	}
	if detail.Plan != nil {
		response.Plan = detail.Plan
	}
	if detail.Planning != nil {
		response.Planning = detail.Planning
	}
	if detail.Artifact != nil {
		response.Artifact = detail.Artifact
	}
	if summary != nil {
		response.Usage = chatUsageBreakdown(summary.Usage, runs, detail.ReplayUsage, detail.ContextWindow, s.deps.Models, s.deps.Config.Billing)
	} else {
		response.Usage = chatUsageBreakdown(nil, runs, detail.ReplayUsage, detail.ContextWindow, s.deps.Models, s.deps.Config.Billing)
	}
	if s.deps.Runs != nil {
		activeRun, ok, activeErr := s.deps.Runs.ActiveRunForChat(chatID)
		if activeErr != nil {
			return api.ChatDetailResponse{}, activeErr
		}
		if ok {
			response.ActiveRun = toAPIActiveRunInfo(activeRun)
			if query, queryErr := s.deps.Chats.LoadRunQuery(chatID, activeRun.RunID); queryErr == nil {
				response.ActiveRun.PlanningMode = activeRunInPlanningStage(activeRun.RunID, query, response.Events, summary)
			}

			// Drop synthesized run.complete for the still-active run so events stay
			// consistent with the live run state reported from memory.
			activeRunID := activeRun.RunID
			filtered := response.Events[:0]
			for _, ev := range response.Events {
				if ev.Type == "run.complete" && ev.String("runId") == activeRunID {
					continue
				}
				filtered = append(filtered, ev)
			}
			response.Events = filtered
			response.ActiveRun.LastSeq = persistedLiveSeqCursor(response.Events, activeRunID)
		}
	}
	return response, nil
}

func activeRunInPlanningStage(runID string, query *chat.QueryLine, events []stream.EventData, summary *chat.Summary) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" || query == nil || !contracts.AnyBoolNode(query.Query["planningMode"]) {
		return false
	}
	if summary != nil && summary.PendingAwaiting != nil {
		pending := summary.PendingAwaiting
		if strings.TrimSpace(pending.RunID) == runID && strings.EqualFold(strings.TrimSpace(pending.Mode), "plan") {
			return true
		}
	}

	latestDecision := ""
	for _, event := range events {
		if event.Type != "awaiting.answer" || strings.TrimSpace(event.String("runId")) != runID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(event.String("mode")), "plan") {
			continue
		}
		latestDecision = activeRunPlanDecision(event.Value("plan"))
	}
	return !strings.EqualFold(latestDecision, "approve")
}

func activeRunPlanDecision(value any) string {
	plan := contracts.AnyMapNode(value)
	if len(plan) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(plan["decision"])))
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

func isTerminalRunStreamEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "run.complete", "run.cancel", "run.error":
		return true
	default:
		return false
	}
}

const (
	activeRunConflictCode    = "active_run_conflict"
	activeRunConflictMessage = "multiple active runs found for chat"
	activeRunFoundMessage    = "active run found for chat"
)

func activeRunConflictInfo(conflict *contracts.ActiveRunConflictError) *api.ChatErrorInfo {
	if conflict == nil {
		return activeRunConflictInfoFor("", activeRunConflictMessage, nil)
	}
	return activeRunConflictInfoFor(conflict.ChatID, activeRunConflictMessage, conflict.RunIDs)
}

func activeRunFoundInfo(chatID string, runIDs []string) *api.ChatErrorInfo {
	return activeRunConflictInfoFor(chatID, activeRunFoundMessage, runIDs)
}

func activeRunConflictInfoFor(chatID string, message string, runIDs []string) *api.ChatErrorInfo {
	if message == "" {
		message = activeRunConflictMessage
	}
	return &api.ChatErrorInfo{
		Code:    activeRunConflictCode,
		Message: message,
		ChatID:  chatID,
		RunIDs:  append([]string(nil), runIDs...),
	}
}

func writeActiveRunConflict(w http.ResponseWriter, conflict *contracts.ActiveRunConflictError) {
	writeJSON(w, http.StatusConflict, api.ApiResponse[*api.ChatErrorInfo]{
		Code: http.StatusConflict,
		Msg:  activeRunConflictCode,
		Data: activeRunConflictInfo(conflict),
	})
}

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	response, err := s.listChatSummaries(r.URL.Query().Get("lastRunId"), r.URL.Query().Get("agentKey"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.URL.Query().Get("chatId")
	if chatID == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	response, err := s.loadChatDetail(r.Context(), chatID, strings.EqualFold(r.URL.Query().Get("includeRawMessages"), "true"))
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(err, &conflictErr) {
		writeActiveRunConflict(w, conflictErr)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	var req api.MarkChatReadRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if strings.TrimSpace(req.ChatID) == "" {
		agentKey := strings.TrimSpace(req.AgentKey)
		if agentKey == "" {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId or agentKey is required"))
			return
		}
		updatedCount, err := s.deps.Chats.MarkAllRead(agentKey)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
			return
		}
		response := api.MarkChatReadResponse{
			AgentKey:         agentKey,
			AgentUnreadCount: 0,
			UpdatedCount:     updatedCount,
		}
		writeJSON(w, http.StatusOK, api.Success(response))
		s.broadcast("chat.read_all", map[string]any{
			"agentKey":         agentKey,
			"updatedCount":     updatedCount,
			"agentUnreadCount": 0,
		})
		return
	}
	summary, err := s.deps.Chats.MarkRead(req.ChatID, req.RunID)
	if errors.Is(err, chat.ErrChatNotFound) {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "chat not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.buildMarkReadResponse(summary, agentUnreadCount)))
	s.broadcastChatReadState("chat.read", summary, agentUnreadCount)
}
