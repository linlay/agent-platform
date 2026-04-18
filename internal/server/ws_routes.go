package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/stream"
	"agent-platform-runner-go/internal/ws"
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
		ExpiresAt: numericDate(principal.Claims["exp"]) * 1000,
	}, nil
}

func (s *Server) newWSHandler(hub *ws.Hub) *ws.Handler {
	handler := ws.NewHandler(s.deps.Config.WebSocket, time.Duration(s.deps.Config.SSE.HeartbeatIntervalMs)*time.Millisecond, hub, wsTokenAuthenticator{server: s})
	s.registerWSRoutes(handler)
	return handler
}

func (s *Server) registerWSRoutes(handler *ws.Handler) {
	handler.RegisterRoute("/api/agents", s.wsAgents)
	handler.RegisterRoute("/api/agent", s.wsAgent)
	handler.RegisterRoute("/api/teams", s.wsTeams)
	handler.RegisterRoute("/api/skills", s.wsSkills)
	handler.RegisterRoute("/api/tools", s.wsTools)
	handler.RegisterRoute("/api/tool", s.wsTool)
	handler.RegisterRoute("/api/chats", s.wsChats)
	handler.RegisterRoute("/api/chat", s.wsChat)
	handler.RegisterRoute("/api/read", s.wsRead)
	handler.RegisterRoute("/api/query", s.wsQuery)
	handler.RegisterRoute("/api/attach", s.wsRunStream)
	handler.RegisterRoute("/api/run/stream", s.wsRunStream)
	handler.RegisterRoute("/api/runstatus", s.wsRunStatus)
	handler.RegisterRoute("/api/run/status", s.wsRunStatus)
	handler.RegisterRoute("/api/submit", s.wsSubmit)
	handler.RegisterRoute("/api/steer", s.wsSteer)
	handler.RegisterRoute("/api/interrupt", s.wsInterrupt)
	handler.RegisterRoute("/api/remember", s.wsRemember)
	handler.RegisterRoute("/api/learn", s.wsLearn)
	handler.RegisterRoute("/api/viewport", s.wsViewport)
}

func (s *Server) wsAgents(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Tag string `json:"tag"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.deps.Registry.Agents(payload.Tag))
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
	conn.SendResponse(req.Type, req.ID, 0, "success", s.buildAgentDetailResponse(def))
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTeams(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	conn.SendResponse(req.Type, req.ID, 0, "success", s.deps.Registry.Teams())
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSkills(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Tag string `json:"tag"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.deps.Registry.Skills(payload.Tag))
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTools(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Kind string `json:"kind"`
		Tag  string `json:"tag"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.listTools(payload.Kind, payload.Tag))
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
		conn.SendError(req.ID, "active_run_conflict", 409, "multiple active runs found for chat", map[string]any{
			"code":   "active_run_conflict",
			"chatId": conflictErr.ChatID,
			"runIds": append([]string(nil), conflictErr.RunIDs...),
		})
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
	if err != nil || strings.TrimSpace(payload.ChatID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	summary, markErr := s.deps.Chats.MarkRead(payload.ChatID)
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
	readAt := int64(0)
	if summary.ReadAt != nil {
		readAt = *summary.ReadAt
	}
	s.broadcast("chat.read", map[string]any{"chatId": summary.ChatID, "readStatus": summary.ReadStatus, "readAt": readAt})
	conn.SendResponse(req.Type, req.ID, 0, "success", api.MarkChatReadResponse{
		ChatID:     summary.ChatID,
		ReadStatus: summary.ReadStatus,
		ReadAt:     readAt,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsQuery(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/query", bytes.NewReader(req.Payload))
	if err != nil {
		conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	prepared, err := s.prepareQuery(httpReq)
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			conn.SendError(req.ID, "invalid_request", statusErr.status, statusErr.message, nil)
		} else {
			conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	if _, reserveErr := conn.ReserveStream(req.ID, prepared.req.RunID); reserveErr != nil {
		if protoErr, ok := reserveErr.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		}
		conn.CompleteRequest(req.ID)
		return
	}

	runCtx, control, _ := s.deps.Runs.Register(ctx, prepared.session)
	eventBus, ok := s.deps.Runs.EventBus(prepared.req.RunID)
	if !ok {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		conn.ReleaseStream(req.ID)
		conn.SendError(req.ID, "internal_error", 500, "run event bus unavailable", nil)
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(prepared.req.RunID, 0)
	if attachErr != nil {
		s.deps.Runs.Interrupt(api.InterruptRequest{RunID: prepared.req.RunID})
		conn.ReleaseStream(req.ID)
		s.sendWSAttachError(conn, req.ID, prepared.req.RunID, prepared.req.ChatID, attachErr)
		return
	}
	conn.AttachObserver(req.ID, observer.ID, func() {
		s.deps.Runs.DetachObserver(prepared.req.RunID, observer.ID)
	})
	s.broadcast("run.started", map[string]any{"runId": prepared.req.RunID, "chatId": prepared.req.ChatID, "agentKey": prepared.req.AgentKey})

	assembler, mapper := s.newAssemblerAndMapper(prepared)
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode)
	StartRunExecutor(RunExecutorParams{
		RunCtx:        runCtx,
		Request:       prepared.req,
		Session:       prepared.session,
		Summary:       prepared.summary,
		Agent:         s.deps.Agent,
		Assembler:     assembler,
		Mapper:        mapper,
		StepWriter:    stepWriter,
		EventBus:      eventBus,
		Chats:         s.deps.Chats,
		RunControl:    control,
		Notifications: s.deps.Notifications,
		OnComplete: func(runID string) {
			s.deps.Runs.Finish(runID)
			s.broadcast("run.finished", map[string]any{"runId": runID, "chatId": prepared.req.ChatID})
		},
	})
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) wsRunStream(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		RunID   string `json:"runId"`
		LastSeq int64  `json:"lastSeq"`
	}](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	status, ok := s.deps.Runs.RunStatus(payload.RunID)
	if !ok {
		conn.SendError(req.ID, "run_not_found", 404, "run not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if _, reserveErr := conn.ReserveStream(req.ID, payload.RunID); reserveErr != nil {
		if protoErr, ok := reserveErr.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	observer, attachErr := s.deps.Runs.AttachObserver(payload.RunID, payload.LastSeq)
	if attachErr != nil {
		conn.ReleaseStream(req.ID)
		s.sendWSAttachError(conn, req.ID, payload.RunID, status.ChatID, attachErr)
		return
	}
	conn.AttachObserver(req.ID, observer.ID, func() {
		s.deps.Runs.DetachObserver(payload.RunID, observer.ID)
	})
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) wsRunStatus(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		RunID string `json:"runId"`
	}](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	status, ok := s.deps.Runs.RunStatus(payload.RunID)
	if !ok {
		conn.SendError(req.ID, "run_not_found", 404, "run not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.RunStatusResponse{
		RunID:         status.RunID,
		ChatID:        status.ChatID,
		AgentKey:      status.AgentKey,
		State:         string(status.State),
		LastSeq:       status.LastSeq,
		OldestSeq:     status.OldestSeq,
		ObserverCount: status.ObserverCount,
		StartedAt:     status.StartedAt,
		CompletedAt:   status.CompletedAt,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSubmit(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.SubmitRequest](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.AwaitingID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId and awaitingId are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	ack := s.deps.Runs.Submit(payload)
	code := 0
	msg := "success"
	if ack.Status == "already_resolved" {
		code = 409
		msg = "already_resolved"
	}
	conn.SendResponse(req.Type, req.ID, code, msg, api.SubmitResponse{
		Accepted:   ack.Accepted,
		Status:     ack.Status,
		RunID:      payload.RunID,
		AwaitingID: payload.AwaitingID,
		Detail:     ack.Detail,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSteer(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.SteerRequest](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.Message) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId and message are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	ack := s.deps.Runs.Steer(payload)
	conn.SendResponse(req.Type, req.ID, 0, "success", api.SteerResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    payload.RunID,
		SteerID:  ack.SteerID,
		Detail:   ack.Detail,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsInterrupt(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.InterruptRequest](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	ack := s.deps.Runs.Interrupt(payload)
	conn.SendResponse(req.Type, req.ID, 0, "success", api.InterruptResponse{
		Accepted: ack.Accepted,
		Status:   ack.Status,
		RunID:    payload.RunID,
		Detail:   ack.Detail,
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

func (s *Server) sendWSAttachError(conn *ws.Conn, requestID string, runID string, chatID string, err error) {
	var replayErr *stream.ReplayWindowExceededError
	if errors.As(err, &replayErr) {
		conn.SendError(requestID, "SEQ_EXPIRED", 409, "SEQ_EXPIRED", map[string]any{
			"runId":     runID,
			"chatId":    chatID,
			"oldestSeq": replayErr.OldestSeq,
			"latestSeq": replayErr.LatestSeq,
			"lastSeq":   replayErr.AfterSeq,
		})
		return
	}
	var limitErr *stream.ObserverLimitExceededError
	if errors.As(err, &limitErr) {
		conn.SendError(requestID, "too_many_observers", 429, "too many observers", map[string]any{"runId": runID, "maxObservers": limitErr.Max})
		return
	}
	conn.SendError(requestID, "internal_error", 500, err.Error(), nil)
}

func (s *Server) broadcast(eventType string, data map[string]any) {
	if s == nil || s.deps.Notifications == nil {
		return
	}
	s.deps.Notifications.Broadcast(eventType, data)
}
