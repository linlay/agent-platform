package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/controlscope"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/ws"
)

func (s *Server) wsQuery(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if conn.QueryLane() != "main" {
		s.wsBTW(ctx, conn, req)
		return
	}
	s.wsQueryForLane(ctx, conn, req, false)
}
func (s *Server) wsQueryForLane(ctx context.Context, conn *ws.Conn, req ws.RequestFrame, side bool) {
	ctx = controlscope.WithContext(ctx, wsControlScope(conn))
	if wsQueryDetachedRequested(req) {
		s.wsQueryDetached(ctx, conn, req, side)
		return
	}
	if _, err := conn.ReserveStream(req.ID, ""); err != nil {
		if e, ok := err.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, e)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	forwarding := false
	defer func() {
		if !forwarding {
			conn.ReleaseStream(req.ID)
		}
	}()
	payload, statusErr := s.rewriteChannelRequestPayload(ctx, req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payload
	queryRequest, err := ws.DecodePayload[api.QueryRequest](req)
	if err != nil {
		if errors.Is(err, api.ErrRequiredSkillKeysRemoved) {
			conn.SendError(req.ID, "required_skill_keys_removed", http.StatusBadRequest, api.RequiredSkillKeysRemovedMessage, nil)
		} else if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, api.ReferenceSandboxPathRemovedMessage, nil)
		} else {
			conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid query payload", nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	command := trustedQueryCommand(ctx, queryRequest)
	command.SideQuery = side
	command.SideQueryID = queryRequest.BTWID
	command.Locale = conn.Locale()
	command.ResourceBaseURL = conn.RequestBaseURL()
	command.ChatSource = chatSourceFromContext(ctx)
	command.ClientTarget = runtimeClientTarget(conn.WebClientTarget())
	if principal := PrincipalFromContext(ctx); principal != nil {
		command.Caller.Subject = strings.TrimSpace(principal.Subject)
	}
	handle, err := s.deps.Runtime.StartQuery(ctx, command)
	if err != nil {
		s.sendWSQueryStartError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	if reserveErr := conn.BindStreamRun(req.ID, handle.RunID); reserveErr != nil {
		_, _ = s.deps.Runtime.Interrupt(ctx, runtimeSetupInterrupt(handle, contracts.InterruptReasonObserverAttachFailed, reserveErr.Error()))
		if protoErr, ok := reserveErr.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	subscription, err := s.deps.Runtime.AttachRun(ctx, runtimetypes.RunRef{
		RunID: handle.RunID, ChatID: handle.ChatID, AgentKey: handle.AgentKey,
		Caller: command.Caller,
	}, 0)
	if err != nil {
		_, _ = s.deps.Runtime.Interrupt(ctx, runtimeSetupInterrupt(handle, contracts.InterruptReasonObserverAttachFailed, err.Error()))
		conn.ReleaseStream(req.ID)
		s.sendWSAttachError(conn, req.ID, handle.RunID, handle.ChatID, err)
		return
	}
	conn.AttachStreamCleanup(req.ID, subscription.Close)
	forwarding = true
	conn.StartEventForward(req.ID, subscription.Events, subscription.Close)
}

// wsQueryDetachedRequested peeks the flag before stream admission: a detached
// query must never reserve, or be rejected by, the connection's Run stream.
func wsQueryDetachedRequested(req ws.RequestFrame) bool {
	peek, err := ws.DecodePayload[struct {
		Detached *bool `json:"detached,omitempty"`
	}](req)
	return err == nil && peek.Detached != nil && *peek.Detached
}

// wsQueryDetached starts a background Run and answers with a plain response.
// It opens no Run stream, so it is independent of the single-stream slot.
func (s *Server) wsQueryDetached(ctx context.Context, conn *ws.Conn, req ws.RequestFrame, side bool) {
	defer conn.CompleteRequest(req.ID)
	if side {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "detached queries require the main WebSocket lane", nil)
		return
	}
	payload, statusErr := s.rewriteChannelRequestPayload(ctx, req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		return
	}
	req.Payload = payload
	queryRequest, err := ws.DecodePayload[api.QueryRequest](req)
	if err != nil {
		if errors.Is(err, api.ErrRequiredSkillKeysRemoved) {
			conn.SendError(req.ID, "required_skill_keys_removed", http.StatusBadRequest, api.RequiredSkillKeysRemovedMessage, nil)
		} else if strings.Contains(err.Error(), api.ReferenceSandboxPathRemovedMessage) {
			conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, api.ReferenceSandboxPathRemovedMessage, nil)
		} else {
			conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid query payload", nil)
		}
		return
	}
	command := trustedQueryCommand(ctx, queryRequest)
	command.Locale = conn.Locale()
	command.ResourceBaseURL = conn.RequestBaseURL()
	command.ChatSource = chatSourceFromContext(ctx)
	command.ClientTarget = runtimeClientTarget(conn.WebClientTarget())
	if principal := PrincipalFromContext(ctx); principal != nil {
		command.Caller.Subject = strings.TrimSpace(principal.Subject)
	}
	handle, err := s.deps.Runtime.StartQuery(ctx, command)
	if err != nil {
		s.sendWSQueryStartError(conn, req.ID, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.QueryAcceptedResponse{
		Accepted: true, Status: "running", RunID: handle.RunID, ChatID: handle.ChatID,
		AgentKey: handle.AgentKey, StartedAt: handle.StartedAt,
	})
}

func (s *Server) sendWSQueryStartError(conn *ws.Conn, requestID string, err error) {
	if isTimeContractViolation(err) {
		sendTimeContractViolation(conn, requestID, err)
		return
	}
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		s.sendWSStatusError(conn, requestID, statusErr)
		return
	}
	var appErr *apperrors.Error
	if errors.As(err, &appErr) {
		status := apperrorsStatus(appErr, http.StatusInternalServerError)
		conn.SendError(requestID, string(appErr.Code()), status, appErr.Error(), appErr.Payload())
		return
	}
	conn.SendError(requestID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
}

func (s *Server) wsBTW(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if !conn.IsDesktopBTW() && !conn.IsDesktopExplain() {
		conn.SendError(req.ID, "btw_ws_lane_required", http.StatusForbidden, "side queries require a desktop-btw or desktop-explain connection", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	s.wsQueryForLane(ctx, conn, req, true)
}

func (s *Server) wsAttach(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		RunID    string `json:"runId"`
		AgentKey string `json:"agentKey,omitempty"`

		LastSeq int64 `json:"lastSeq"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid attach payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	if statusErr := s.validateRunOwner(payload.RunID, payload.AgentKey); statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
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
	if !conn.IsDesktopBTW() && !conn.IsDesktopExplain() {
		bindRunWebClientTarget(s.deps.Runs, payload.RunID, conn.WebClientTarget())
	}
	conn.StartStreamForward(req.ID, observer)
}

func (s *Server) wsDetach(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.DetachRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid detach payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	if statusErr := s.validateRunOwner(payload.RunID, payload.AgentKey); statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	detached, ok := conn.DetachRunStream(payload.RunID)
	if !ok {
		conn.SendResponse(req.Type, req.ID, 0, "success", api.DetachResponse{
			Accepted: false,
			Status:   "not_observing",
			RunID:    payload.RunID,
			Detail:   "Stream is not observed on this connection",
		})
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.DetachResponse{
		Accepted:        true,
		Status:          "detached",
		RunID:           detached.RunID,
		StreamRequestID: detached.StreamRequestID,
		StreamID:        detached.StreamID,
		LastSeq:         detached.LastSeq,
		Detail:          "Stream detached",
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSubmit(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payloadData, statusErr := s.rewriteChannelRequestPayload(conn.Context(), req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payloadData
	payload, err := ws.DecodePayload[api.SubmitRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid submit payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	payload.Locale = conn.Locale()
	result, err := s.deps.Runtime.Submit(conn.Context(), runtimeSubmitCommand(payload))
	if err != nil {
		s.sendWSRuntimeError(conn, req.ID, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.SubmitResponse{Accepted: result.Accepted, Status: result.Status, ChatID: result.ChatID, RunID: result.RunID, AwaitingID: result.AwaitingID, SubmitID: result.SubmitID, Continued: result.Continued, ErrorCode: result.ErrorCode, Detail: result.Detail})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsSteer(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payloadData, statusErr := s.rewriteChannelRequestPayload(conn.Context(), req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payloadData
	payload, err := ws.DecodePayload[api.SteerRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid steer payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	if len(payload.References) > 0 && channelIDFromContext(conn.Context()) != "" {
		conn.SendResponse(req.Type, req.ID, 0, "success", api.SteerResponse{Status: "unsupported", RunID: payload.RunID, SteerID: payload.SteerID, Detail: "attachment steer is not supported for channel runs"})
		conn.CompleteRequest(req.ID)
		return
	}
	result, err := s.deps.Runtime.Steer(conn.Context(), runtimeSteerCommand(payload))
	if err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			s.sendWSStatusError(conn, req.ID, statusErr)
		} else {
			conn.SendError(req.ID, "invalid_request", 400, err.Error(), nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.SteerResponse{
		Accepted: result.Accepted, Status: result.Status, RunID: result.RunID, SteerID: result.SteerID, Detail: result.Detail,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsInterrupt(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payloadData, statusErr := s.rewriteChannelRequestPayload(conn.Context(), req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payloadData
	payload, err := ws.DecodePayload[api.InterruptRequest](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	result, err := s.deps.Runtime.Interrupt(conn.Context(), runtimeInterruptCommand(wsAPIUserInterruptRequest(payload)))
	if err != nil {
		s.sendWSRuntimeError(conn, req.ID, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.InterruptResponse{Accepted: result.Accepted, Status: result.Status, RunID: result.RunID, Detail: result.Detail})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsAccessLevel(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[api.AccessLevelRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid access-level payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	result, err := s.deps.Runtime.SetAccessLevel(conn.Context(), runtimetypes.AccessLevelCommand{RunRef: runtimetypes.RunRef{RunID: payload.RunID, AgentKey: payload.AgentKey}, RequestID: payload.RequestID, AccessLevel: payload.AccessLevel, Reason: payload.Reason})
	if err != nil {
		s.sendWSRuntimeError(conn, req.ID, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", api.AccessLevelResponse{Accepted: result.Accepted, Status: result.Status, RunID: result.RunID, PreviousAccessLevel: result.PreviousAccessLevel, AccessLevel: result.AccessLevel, Version: result.Version, Detail: result.Detail})
	conn.CompleteRequest(req.ID)
}
func (s *Server) sendWSRuntimeError(conn *ws.Conn, id string, err error) {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		s.sendWSStatusError(conn, id, statusErr)
	} else {
		conn.SendError(id, "invalid_request", 400, err.Error(), nil)
	}
	conn.CompleteRequest(id)
}

func (s *Server) sendWSStatusError(conn *ws.Conn, requestID string, err *statusError) {
	if err == nil {
		return
	}
	code := strings.TrimSpace(err.Code)
	if code == "" {
		code = "invalid_request"
		switch err.Status {
		case http.StatusForbidden:
			code = "forbidden"
		case http.StatusNotFound:
			code = "run_not_found"
		case http.StatusInternalServerError:
			code = "internal_error"
		}
	}
	conn.SendError(requestID, code, err.Status, err.Message, err.Data)
}

func (s *Server) sendWSAttachError(conn *ws.Conn, requestID string, runID string, chatID string, err error) {
	var replayErr *stream.ReplayWindowExceededError
	if errors.As(err, &replayErr) {
		conn.SendError(requestID, "seq_expired", 409, "SEQ_EXPIRED", map[string]any{
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
