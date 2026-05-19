package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"
)

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
	prepared.resourceBaseURL = conn.RequestBaseURL()
	if _, reserveErr := conn.ReserveStream(req.ID, prepared.req.RunID); reserveErr != nil {
		if protoErr, ok := reserveErr.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	if strings.EqualFold(prepared.agentDef.Mode, "PROXY") {
		s.wsProxyQuery(ctx, conn, req, prepared)
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
	stepWriter := chat.NewStepWriter(s.deps.Chats, prepared.req.ChatID, prepared.req.RunID, prepared.agentDef.Mode, isHiddenRequest(prepared.req), chat.WithDebugEventsEnabled(s.deps.Config.Stream.DebugEventsEnabled))
	stepWriter.SetPendingSystemInits(prepared.systemInitLines)
	principal := &Principal{Subject: prepared.session.Subject}
	if strings.TrimSpace(principal.Subject) == "" {
		principal = nil
	}
	StartRunExecutor(RunExecutorParams{
		RunCtx:             runCtx,
		Request:            prepared.req,
		Session:            prepared.session,
		Summary:            prepared.summary,
		Agent:              s.deps.Agent,
		Registry:           s.deps.Registry,
		Assembler:          assembler,
		Mapper:             mapper,
		Stream:             s.deps.Config.Stream,
		StepWriter:         stepWriter,
		EventBus:           eventBus,
		Chats:              s.deps.Chats,
		RunControl:         control,
		ResourceBaseURL:    prepared.resourceBaseURL,
		ResourceTickets:    s.ticketService,
		BuildQuerySession:  s.BuildQuerySession,
		PrepareSystemInits: s.prepareSystemInitCache,
		BuildChildSystems:  s.buildSystemInitsForChildTask,
		Notifications:      s.deps.Notifications,
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
	if response, ok := s.forwardProxySubmit(payload); ok {
		conn.SendResponse(req.Type, req.ID, 0, "success", response)
		conn.CompleteRequest(req.ID)
		return
	}
	response, code, msg, err := s.resolveSubmit(payload)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, code, msg, response)
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
	if response, ok := s.forwardProxyInterrupt(payload); ok {
		conn.SendResponse(req.Type, req.ID, 0, "success", response)
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
