package server

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/observability"
	"agent-platform/internal/ws"
)

type wsTokenAuthenticator struct {
	server *Server
}

func (a wsTokenAuthenticator) VerifyToken(ctx context.Context, token string) (ws.AuthSession, error) {
	if a.server == nil {
		return ws.AuthSession{Context: ctx}, nil
	}
	if !a.server.deps.Config.Auth.Enabled {
		if ctx == nil {
			ctx = context.Background()
		}
		return ws.AuthSession{Context: ctx}, nil
	}
	principal, err := a.server.authVerifier.Verify(strings.TrimSpace(token))
	if err != nil {
		return ws.AuthSession{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return ws.AuthSession{
		Context:   WithPrincipal(ctx, principal),
		Subject:   principal.Subject,
		DeviceID:  firstStringClaim(principal.Claims, "deviceId", "device_id"),
		ExpiresAt: numericDate(principal.Claims["exp"]) * 1000,
	}, nil
}

func (s *Server) newWSHandler(hub *ws.Hub) *ws.Handler {
	handler := ws.NewHandler(s.deps.Config.WebSocket, time.Duration(s.deps.Config.SSE.HeartbeatInterval)*time.Second, hub, wsTokenAuthenticator{server: s})
	s.registerWSRoutes(handler)
	handler.SetDispatch(s.logWSDispatch(handler.Dispatch))
	return handler
}

func (s *Server) logWSDispatch(next ws.RouteHandler) ws.RouteHandler {
	return func(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
		if next == nil {
			return
		}
		if !s.deps.Config.Logging.Request.Enabled {
			next(ctx, conn, req)
			return
		}
		startedAt := time.Now()
		frameType := observability.SanitizeLog(req.Type)
		requestID := observability.SanitizeLog(req.ID)
		sessionID := ""
		if conn != nil {
			sessionID = conn.SessionID()
		}
		log.Printf("WS %s id=%s (arrived)", frameType, requestID)
		next(ctx, conn, req)
		cost := time.Since(startedAt)
		observability.LogWSRequest(req.Type, req.ID, sessionID, cost)
		log.Printf("WS %s id=%s -> done (%s)", frameType, requestID, cost.Round(time.Millisecond))
	}
}

func (s *Server) registerWSRoutes(handler *ws.Handler) {
	handler.RegisterRoute("/api/agents", s.wsAgents)
	handler.RegisterRoute("/api/agents/order", s.wsAgentOrder)
	handler.RegisterRoute("/api/channels", s.wsChannels)
	handler.RegisterRoute("/api/agent", s.wsAgent)
	handler.RegisterRoute("/api/agent/create", s.wsAgentCreate)
	handler.RegisterRoute("/api/agent/update", s.wsAgentUpdate)
	handler.RegisterRoute("/api/agent/model-config", s.wsAgentModelConfig)
	handler.RegisterRoute("/api/agent/delete", s.wsAgentDelete)
	handler.RegisterRoute("/api/agent/editor-options", s.wsAgentEditorOptions)
	handler.RegisterRoute("/api/model-options", s.wsModelOptions)
	handler.RegisterRoute("/api/teams", s.wsTeams)
	handler.RegisterRoute("/api/skills", s.wsSkills)
	handler.RegisterRoute("/api/tools", s.wsTools)
	handler.RegisterRoute("/api/tool", s.wsTool)
	handler.RegisterRoute("/api/chats", s.wsChats)
	handler.RegisterRoute("/api/chat", s.wsChat)
	handler.RegisterRoute("/api/read", s.wsRead)
	handler.RegisterRoute("/api/feedback", s.wsFeedback)
	handler.RegisterRoute("/api/chat/delete", s.wsChatDelete)
	handler.RegisterRoute("/api/chat/rename", s.wsChatRename)
	handler.RegisterRoute("/api/chat/archive", s.wsChatArchive)
	handler.RegisterRoute("/api/archives", s.wsArchives)
	handler.RegisterRoute("/api/archive", s.wsArchive)
	handler.RegisterRoute("/api/archives/search", s.wsArchiveSearch)
	handler.RegisterRoute("/api/archive/delete", s.wsArchiveDelete)
	handler.RegisterRoute("/api/automations", s.wsAutomations)
	handler.RegisterRoute("/api/automation", s.wsAutomation)
	handler.RegisterRoute("/api/automation/create", s.wsAutomationCreate)
	handler.RegisterRoute("/api/automation/update", s.wsAutomationUpdate)
	handler.RegisterRoute("/api/automation/delete", s.wsAutomationDelete)
	handler.RegisterRoute("/api/automation/toggle", s.wsAutomationToggle)
	handler.RegisterRoute("/api/automation/executions", s.wsAutomationExecutions)
	handler.RegisterRoute("/api/chats/search", s.wsGlobalSearch)
	handler.RegisterRoute("/api/query/availability", s.wsQueryAvailability)
	handler.RegisterRoute("/api/query", s.wsQuery)
	handler.RegisterRoute("/api/attach", s.wsAttach)
	handler.RegisterRoute("/api/detach", s.wsDetach)
	handler.RegisterRoute("/api/submit", s.wsSubmit)
	handler.RegisterRoute("/api/steer", s.wsSteer)
	handler.RegisterRoute("/api/interrupt", s.wsInterrupt)
	handler.RegisterRoute("/api/access-level", s.wsAccessLevel)
	handler.RegisterRoute("/api/remember", s.wsRemember)
	handler.RegisterRoute("/api/learn", s.wsLearn)
	handler.RegisterRoute("/api/memory/meta", s.wsMemoryMeta)
	handler.RegisterRoute("/api/memory/context-preview", s.wsMemoryContextPreview)
	handler.RegisterRoute("/api/memory/scope/list", s.wsMemoryScopes)
	handler.RegisterRoute("/api/memory/scope/detail", s.wsMemoryScopeDetail)
	handler.RegisterRoute("/api/memory/scope/save", s.wsMemoryScopeSaveRoute)
	handler.RegisterRoute("/api/memory/scope/validate", s.wsMemoryScopeValidate)
	handler.RegisterRoute("/api/memory/record/list", s.wsMemoryRecords)
	handler.RegisterRoute("/api/memory/record/detail", s.wsMemoryRecord)
	handler.RegisterRoute("/api/viewport", s.wsViewport)
	handler.RegisterRoute("/api/resource", s.wsResource)
	handler.RegisterRoute("/api/upload", s.wsDownload)
	handler.RegisterRoute("/api/pull", s.wsDownload)
}

func (s *Server) wsAgents(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		IncludeChats int    `json:"includeChats"`
		Scope        string `json:"scope"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if payload.IncludeChats < 0 || payload.IncludeChats > maxAgentSummaryIncludeChats {
		conn.SendError(req.ID, "invalid_request", 400, "includeChats must be between 0 and 50", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	scope, err := catalog.NormalizeAgentSummaryScope(payload.Scope)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	items, listErr := s.listAgentSummaries(payload.IncludeChats, scope)
	if listErr != nil {
		conn.SendError(req.ID, "internal_error", 500, listErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", items)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsChannels(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	conn.SendResponse(req.Type, req.ID, 0, "success", s.listChannelSummaries())
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsAgent(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
	}](req)
	if err != nil || strings.TrimSpace(payload.AgentKey) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "agentKey is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	def, ok := s.deps.Registry.AgentDefinition(payload.AgentKey)
	if !ok {
		conn.SendError(req.ID, "not_found", 404, "agent not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, detailErr := s.buildEditableAgentDetailResponse(def)
	if detailErr != nil {
		conn.SendError(req.ID, "internal_error", 500, detailErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsAgentEditorOptions(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	conn.SendResponse(req.Type, req.ID, 0, "success", s.buildAgentEditorOptions())
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsModelOptions(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	response := s.buildModelOptions()
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTeams(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	conn.SendResponse(req.Type, req.ID, 0, "success", s.deps.Registry.Teams())
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSkills(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if _, err := ws.DecodePayload[struct{}](req); err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.deps.Registry.Skills(""))
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTools(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Kind string `json:"kind"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.listTools(payload.Kind, ""))
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTool(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ToolName string `json:"toolName"`
	}](req)
	if err != nil || strings.TrimSpace(payload.ToolName) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "toolName is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	tool, ok := s.lookupTool(payload.ToolName)
	if !ok {
		conn.SendError(req.ID, "not_found", 404, "tool not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", tool)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsChats(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		LastRunID string `json:"lastRunId"`
		AgentKey  string `json:"agentKey"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, listErr := s.listChatSummaries(payload.LastRunID, payload.AgentKey)
	if listErr != nil {
		conn.SendError(req.ID, "internal_error", 500, listErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsChat(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ChatID             string `json:"chatId"`
		IncludeRawMessages bool   `json:"includeRawMessages"`
	}](req)
	if err != nil || strings.TrimSpace(payload.ChatID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, loadErr := s.loadChatDetail(ctx, payload.ChatID, payload.IncludeRawMessages)
	if errors.Is(loadErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "chat not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	var conflictErr *contracts.ActiveRunConflictError
	if errors.As(loadErr, &conflictErr) {
		conn.SendError(req.ID, activeRunConflictCode, 409, activeRunConflictMessage, activeRunConflictInfo(conflictErr))
		conn.CompleteRequest(req.ID)
		return
	}
	if loadErr != nil {
		conn.SendError(req.ID, "internal_error", 500, loadErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsRead(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.MarkChatReadRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if strings.TrimSpace(payload.ChatID) == "" {
		agentKey := strings.TrimSpace(payload.AgentKey)
		if agentKey == "" {
			conn.SendError(req.ID, "invalid_request", 400, "chatId or agentKey is required", nil)
			conn.CompleteRequest(req.ID)
			return
		}
		updatedCount, markAllErr := s.deps.Chats.MarkAllRead(agentKey)
		if markAllErr != nil {
			conn.SendError(req.ID, "internal_error", 500, markAllErr.Error(), nil)
			conn.CompleteRequest(req.ID)
			return
		}
		response := api.MarkChatReadResponse{AgentKey: agentKey, AgentUnreadCount: 0, UpdatedCount: updatedCount}
		s.broadcast("chat.read_all", map[string]any{
			"agentKey":         agentKey,
			"updatedCount":     updatedCount,
			"agentUnreadCount": 0,
		})
		conn.SendResponse(req.Type, req.ID, 0, "success", response)
		conn.CompleteRequest(req.ID)
		return
	}
	summary, markErr := s.deps.Chats.MarkRead(payload.ChatID, payload.RunID)
	if errors.Is(markErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "chat not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if markErr != nil {
		conn.SendError(req.ID, "internal_error", 500, markErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
	if err != nil {
		conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	s.broadcastChatReadState("chat.read", summary, agentUnreadCount)
	conn.SendResponse(req.Type, req.ID, 0, "success", s.buildMarkReadResponse(summary, agentUnreadCount))
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsFeedback(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.FeedbackRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	chatID := strings.TrimSpace(payload.ChatID)
	runID := strings.TrimSpace(payload.RunID)
	feedbackType := strings.TrimSpace(payload.Type)
	if chatID == "" || runID == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId and runId are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if feedbackType != "thumbs_down" && feedbackType != "clear" {
		conn.SendError(req.ID, "invalid_request", 400, "type must be thumbs_down or clear", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	setAt, setErr := s.deps.Chats.SetFeedback(chatID, runID, feedbackType, payload.Comment)
	if errors.Is(setErr, chat.ErrRunNotFound) {
		conn.SendError(req.ID, "not_found", 404, "run not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if setErr != nil {
		conn.SendError(req.ID, "internal_error", 500, setErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.FeedbackResponse{ChatID: chatID, RunID: runID, Type: feedbackType, SetAt: setAt})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsChatDelete(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.DeleteChatRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	chatID := strings.TrimSpace(payload.ChatID)
	if !chat.ValidChatID(chatID) {
		conn.SendError(req.ID, "invalid_request", 400, "invalid chatId", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if s.deps.Runs != nil {
		activeRun, ok, activeErr := s.deps.Runs.ActiveRunForChat(chatID)
		var conflictErr *contracts.ActiveRunConflictError
		if errors.As(activeErr, &conflictErr) {
			conn.SendError(req.ID, activeRunConflictCode, 409, activeRunConflictMessage, activeRunConflictInfo(conflictErr))
			conn.CompleteRequest(req.ID)
			return
		}
		if activeErr != nil {
			conn.SendError(req.ID, "internal_error", 500, activeErr.Error(), nil)
			conn.CompleteRequest(req.ID)
			return
		}
		if ok {
			conn.SendError(req.ID, activeRunConflictCode, 409, activeRunFoundMessage, activeRunFoundInfo(chatID, []string{activeRun.RunID}))
			conn.CompleteRequest(req.ID)
			return
		}
	}
	if err := s.deps.Chats.DeleteChat(chatID); errors.Is(err, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "chat not found", nil)
		conn.CompleteRequest(req.ID)
		return
	} else if err != nil {
		conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	s.broadcast("chat.deleted", map[string]any{"chatId": chatID})
	conn.SendResponse(req.Type, req.ID, 0, "success", api.DeleteChatResponse{ChatID: chatID, Deleted: true})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsChatRename(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.RenameChatRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	chatID := strings.TrimSpace(payload.ChatID)
	chatName := strings.TrimSpace(payload.ChatName)
	if !chat.ValidChatID(chatID) || chatName == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId and chatName are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	summary, renameErr := s.deps.Chats.RenameChat(chatID, chatName)
	if errors.Is(renameErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "chat not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if renameErr != nil {
		conn.SendError(req.ID, "internal_error", 500, renameErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	s.broadcast("chat.renamed", map[string]any{"chatId": summary.ChatID, "chatName": summary.ChatName, "agentKey": summary.AgentKey})
	conn.SendResponse(req.Type, req.ID, 0, "success", api.RenameChatResponse{ChatID: summary.ChatID, ChatName: summary.ChatName, Updated: true})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsGlobalSearch(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.GlobalSearchRequest](req)
	if err != nil || strings.TrimSpace(payload.Query) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "query is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	limit := payload.Limit
	if limit <= 0 {
		limit = 20
	}
	hits, searchErr := s.deps.Chats.SearchGlobal(payload.Query, payload.AgentKey, payload.TeamID, limit)
	if searchErr != nil {
		conn.SendError(req.ID, "internal_error", 500, searchErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	results := make([]api.GlobalSearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, api.GlobalSearchResult{
			ChatID:    hit.ChatID,
			ChatName:  hit.ChatName,
			AgentKey:  hit.AgentKey,
			TeamID:    hit.TeamID,
			RunID:     hit.RunID,
			Kind:      hit.Kind,
			Role:      hit.Role,
			Timestamp: hit.Timestamp,
			Snippet:   hit.Snippet,
			Score:     hit.Score,
		})
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.GlobalSearchResponse{
		Query:   strings.TrimSpace(payload.Query),
		Count:   len(results),
		Results: results,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsRemember(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.RememberRequest](req)
	if err != nil || strings.TrimSpace(payload.RequestID) == "" || strings.TrimSpace(payload.ChatID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "requestId and chatId are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, rememberErr := s.executeRemember(payload)
	if errors.Is(rememberErr, chat.ErrChatNotFound) {
		conn.SendError(req.ID, "not_found", 404, "chat not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if rememberErr != nil {
		conn.SendError(req.ID, "internal_error", 500, rememberErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsLearn(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.LearnRequest](req)
	if err != nil || strings.TrimSpace(payload.ChatID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.LearnResponse{
		Accepted:  false,
		Status:    "not_connected",
		RequestID: payload.RequestID,
		ChatID:    payload.ChatID,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsViewport(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ViewportKey string `json:"viewportKey"`
	}](req)
	if err != nil || strings.TrimSpace(payload.ViewportKey) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "viewportKey is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	response, getErr := s.deps.Viewport.Get(ctx, payload.ViewportKey)
	if getErr != nil {
		conn.SendError(req.ID, "internal_error", 500, getErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}

func (s *Server) broadcast(eventType string, data map[string]any) {
	if s == nil || s.deps.Notifications == nil {
		return
	}
	s.deps.Notifications.Broadcast(eventType, data)
}
