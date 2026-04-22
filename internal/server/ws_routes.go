package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	handler.RegisterRoute("/api/attach", s.wsAttach)
	handler.RegisterRoute("/api/submit", s.wsSubmit)
	handler.RegisterRoute("/api/steer", s.wsSteer)
	handler.RegisterRoute("/api/interrupt", s.wsInterrupt)
	handler.RegisterRoute("/api/remember", s.wsRemember)
	handler.RegisterRoute("/api/learn", s.wsLearn)
	handler.RegisterRoute("/api/viewport", s.wsViewport)
	handler.RegisterRoute("/api/upload", s.wsUpload)
}

// wsUpload lets the gateway deliver a user-uploaded file to the platform without
// spawning a second HTTP connection. The gateway sends
// {chatId, requestId, fileName, url, mimeType?, sizeBytes?} where `url` points
// to a gateway-hosted resource; the platform downloads it (optionally with the
// configured gateway auth token) and persists it via the shared internal upload
// pipeline so chat-side tickets stay identical to the HTTP multipart path.
func (s *Server) wsUpload(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		ChatID    string `json:"chatId"`
		RequestID string `json:"requestId"`
		FileName  string `json:"fileName"`
		URL       string `json:"url"`
		MimeType  string `json:"mimeType"`
		SizeBytes int64  `json:"sizeBytes"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid upload payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}

	chatID := strings.TrimSpace(payload.ChatID)
	requestID := strings.TrimSpace(payload.RequestID)
	fileName := strings.TrimSpace(payload.FileName)
	rawURL := strings.TrimSpace(payload.URL)
	if chatID == "" || fileName == "" || rawURL == "" {
		conn.SendError(req.ID, "invalid_request", 400, "chatId, fileName and url are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}

	data, err := s.fetchGatewayUpload(ctx, rawURL)
	if err != nil {
		conn.SendError(req.ID, "download_failed", 502, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}

	status, body, err := s.ExecuteInternalUpload(ctx, chatID, requestID, fileName, data)
	if err != nil {
		conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if status < 200 || status >= 300 {
		conn.SendError(req.ID, "upload_failed", status, strings.TrimSpace(string(body)), nil)
		conn.CompleteRequest(req.ID)
		return
	}

	var parsed api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data.Upload.Name == "" {
		conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"raw": string(body)})
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", parsed.Data)
	conn.CompleteRequest(req.ID)
}

// fetchGatewayUpload resolves the gateway-side URL (absolute or relative to the
// configured GATEWAY_BASE_URL) and downloads the bytes, attaching the optional
// GATEWAY_AUTH_TOKEN bearer.
func (s *Server) fetchGatewayUpload(ctx context.Context, rawURL string) ([]byte, error) {
	downloadURL := s.buildGatewayURL(rawURL)
	if downloadURL == "" {
		return nil, fmt.Errorf("empty download url")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if token := strings.TrimSpace(s.deps.Config.GatewayWS.AuthToken); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

func (s *Server) buildGatewayURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return raw
	}
	base := strings.TrimRight(strings.TrimSpace(s.deps.Config.GatewayWS.BaseURL), "/")
	if base == "" {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	downloadPath := strings.Trim(strings.TrimSpace(s.deps.Config.GatewayWS.DownloadPath), "/")
	if downloadPath == "" {
		return base + "/" + raw
	}
	return base + "/" + downloadPath + "/" + raw
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
	items, listErr := s.listAgentSummaries(payload.Tag)
	if listErr != nil {
		conn.SendError(req.ID, "internal_error", 500, listErr.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", items)
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
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req))
	principal := &Principal{Subject: prepared.session.Subject}
	if strings.TrimSpace(principal.Subject) == "" {
		principal = nil
	}
	StartRunExecutor(RunExecutorParams{
		RunCtx:            runCtx,
		Request:           prepared.req,
		Session:           prepared.session,
		Summary:           prepared.summary,
		Agent:             s.deps.Agent,
		Registry:          s.deps.Registry,
		Assembler:         assembler,
		Mapper:            mapper,
		SSE:               s.deps.Config.SSE,
		StepWriter:        stepWriter,
		EventBus:          eventBus,
		Chats:             s.deps.Chats,
		RunControl:        control,
		BuildQuerySession: s.BuildQuerySession,
		Notifications:     s.deps.Notifications,
		OnUnreadChanged: func(summary chat.Summary) {
			agentUnreadCount, err := s.agentUnreadCount(summary.AgentKey)
			if err != nil {
				return
			}
			s.broadcastChatReadState("chat.unread", summary, agentUnreadCount)
		},
		OnPersisted: func(completion chat.RunCompletion) {
			s.autoLearnIfEnabled(completion.ChatID, completion.RunID, prepared.session.AgentKey, prepared.session.TeamID, principal, prepared.req.RequestID)
		},
		OnComplete: func(runID string) {
			s.deps.Runs.Finish(runID)
			s.broadcast("run.finished", map[string]any{"runId": runID, "chatId": prepared.req.ChatID})
		},
	})
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) wsAttach(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
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

func (s *Server) wsSubmit(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.SubmitRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid submit payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if _, err := s.validateSubmitRequest(payload); err != nil {
		conn.SendError(req.ID, "invalid_request", 400, err.Error(), nil)
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
