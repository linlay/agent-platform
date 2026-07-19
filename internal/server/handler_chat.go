package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func (s *Server) listChatSummaries(lastRunID string, agentKey string) ([]api.ChatSummaryResponse, error) {
	return s.listChatSummariesWithAgentModes(lastRunID, agentKey, nil)
}

func (s *Server) listChatSummariesWithAgentModes(lastRunID string, agentKey string, agentModes []string) ([]api.ChatSummaryResponse, error) {
	return s.listChatSummariesWithAgentModesAndLimit(lastRunID, agentKey, agentModes, 0)
}

func (s *Server) listChatSummariesWithAgentModesAndLimit(lastRunID string, agentKey string, agentModes []string, limit int) ([]api.ChatSummaryResponse, error) {
	items, err := s.deps.Chats.ListChatsWithAgentModesAndLimit(lastRunID, agentKey, agentModes, limit)
	if err != nil {
		return nil, err
	}
	return s.mapChatSummariesWithActiveRuns(items, true)
}

func requestedModes(values []string) ([]string, error) {
	items := make([]string, 0, len(values))
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			mode, err := catalog.ParsePublicAgentMode(raw)
			if err != nil {
				return nil, err
			}
			items = append(items, catalog.AgentModeForAPI(mode))
		}
	}
	return chat.NormalizeAgentModes(items), nil
}

const deprecatedAgentModeMessage = "agentMode is no longer supported; use mode instead"

func hasDeprecatedAgentModeQuery(r *http.Request) bool {
	if r == nil {
		return false
	}
	_, present := r.URL.Query()["agentMode"]
	return present
}

const invalidChatListLimitMessage = "limit must be a positive integer"

func parseChatListLimit(r *http.Request) (int, error) {
	if r == nil {
		return 0, nil
	}
	values, present := r.URL.Query()["limit"]
	if !present {
		return 0, nil
	}
	raw := ""
	if len(values) > 0 {
		raw = strings.TrimSpace(values[0])
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, errors.New(invalidChatListLimitMessage)
	}
	return limit, nil
}

func optionalChatListLimit(limit *int) (int, error) {
	if limit == nil {
		return 0, nil
	}
	if *limit <= 0 {
		return 0, errors.New(invalidChatListLimitMessage)
	}
	return *limit, nil
}

func mapChatSummaries(items []chat.Summary) []api.ChatSummaryResponse {
	return mapChatSummariesWithUsage(items, true)
}

func mapChatSummariesWithUsage(items []chat.Summary, includeUsage bool) []api.ChatSummaryResponse {
	response := make([]api.ChatSummaryResponse, 0, len(items))
	for _, item := range items {
		resp := api.ChatSummaryResponse{
			ChatID:         item.ChatID,
			ChatName:       item.ChatName,
			AgentKey:       item.AgentKey,
			Mode:           item.AgentMode,
			TeamID:         item.TeamID,
			Source:         item.Source,
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
			LastRunID:      item.LastRunID,
			LastRunContent: item.LastRunContent,
			Read:           toAPIReadState(item.Read),
		}
		resp.Awaiting = toAPIAwaiting(item.PendingAwaiting)
		if includeUsage {
			usage := mapUsageDataPtr(item.Usage)
			resp.Usage = usage
		}
		response = append(response, resp)
	}
	return response
}

func toAPIAwaiting(pending *chat.PendingAwaiting) *api.Awaiting {
	if pending == nil {
		return nil
	}
	return &api.Awaiting{
		AwaitingID: pending.AwaitingID,
		RunID:      pending.RunID,
		Mode:       pending.Mode,
		Status:     "awaiting",
		CreatedAt:  pending.CreatedAt,
	}
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
		CreatedAt:     summary.CreatedAt,
		UpdatedAt:     summary.UpdatedAt,
		Source:        summary.Source,
		Awaiting:      toAPIAwaiting(summary.PendingAwaiting),
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
		if strings.TrimSpace(pending.RunID) == runID && strings.EqualFold(strings.TrimSpace(pending.Mode), "planning") {
			return true
		}
	}

	latestDecision := ""
	for _, event := range events {
		if event.Type != "awaiting.answer" || strings.TrimSpace(event.String("runId")) != runID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(event.String("mode")), "planning") {
			continue
		}
		latestDecision = activeRunPlanningDecision(event.Value("planning"))
	}
	return !strings.EqualFold(latestDecision, "approve")
}

func activeRunPlanningDecision(value any) string {
	planning := contracts.AnyMapNode(value)
	if len(planning) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(planning["decision"])))
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
	if hasDeprecatedAgentModeQuery(r) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, deprecatedAgentModeMessage))
		return
	}
	limit, err := parseChatListLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	modes, err := requestedModes(r.URL.Query()["mode"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	response, err := s.listChatSummariesWithAgentModesAndLimit(r.URL.Query().Get("lastRunId"), r.URL.Query().Get("agentKey"), modes, limit)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
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
			if isTimeContractViolation(err) {
				writeTimeContractViolation(w, err)
				return
			}
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
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(s.buildMarkReadResponse(summary, agentUnreadCount)))
	s.broadcastChatReadState("chat.read", summary, agentUnreadCount)
}
