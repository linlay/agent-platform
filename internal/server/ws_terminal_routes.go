package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/stream"
	terminalpkg "agent-platform/internal/terminal"
	"agent-platform/internal/ws"
)

type terminalOpenPayload struct {
	AgentKey    string `json:"agentKey"`
	TerminalKey string `json:"terminalKey,omitempty"`
	ChatID      string `json:"chatId,omitempty"`
	Cols        int    `json:"cols,omitempty"`
	Rows        int    `json:"rows,omitempty"`
}

type terminalIDPayload struct {
	TerminalID      string `json:"terminalId"`
	StreamRequestID string `json:"streamRequestId,omitempty"`
}

type terminalDetachPayload struct {
	TerminalID      string `json:"terminalId"`
	StreamRequestID string `json:"streamRequestId"`
}

type terminalInputPayload struct {
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
}

type terminalResizePayload struct {
	TerminalID string `json:"terminalId"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

func (s *Server) wsTerminalOpen(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalOpenPayload](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid terminal open payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	ownerKey := conn.ClientBoundaryKey()
	openResult, statusErr := s.openTerminalSession(payload, ownerKey)
	if statusErr != nil {
		s.sendTerminalStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	session := openResult.Session
	if err := conn.ReserveTerminalStream(req.ID, session.ID()); err != nil {
		if !openResult.Reused {
			s.terminals.Discard(session)
		}
		if protoErr, ok := err.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		} else {
			conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	s.terminals.Start(session)
	subscription := session.Subscribe(openResult.Reused)
	defer subscription.Close()
	conn.AttachStreamCleanup(req.ID, func() {
		subscription.Close()
	})

	var seq int64 = 1
	if !conn.SendStreamEvent(req.ID, terminalStreamEvent(seq, terminalpkg.Event{
		Type:        terminalpkg.EventOpened,
		TerminalID:  session.ID(),
		AgentKey:    session.AgentKey(),
		TerminalKey: session.TerminalKey(),
		Scope:       terminalpkg.ScopeAgent,
		ChatID:      strings.TrimSpace(payload.ChatID),
		CWD:         session.CWD(),
		Shell:       session.Shell(),
		Reused:      openResult.Reused,
	})) {
		return
	}

	reason := "closed"
	for {
		select {
		case <-conn.Done():
			subscription.Close()
			return
		case event, ok := <-subscription.Events():
			if !ok {
				conn.FinishStream(req.ID, reason, seq)
				return
			}
			seq++
			if event.Type == terminalpkg.EventExit {
				if event.Reason != "" {
					reason = event.Reason
				} else {
					reason = "exit"
				}
			}
			if !conn.SendStreamEvent(req.ID, terminalStreamEvent(seq, event)) {
				return
			}
			if event.Type == terminalpkg.EventExit {
				conn.FinishStream(req.ID, reason, seq)
				return
			}
		}
	}
}

func (s *Server) wsTerminalDetach(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalDetachPayload](req)
	terminalID := strings.TrimSpace(payload.TerminalID)
	streamRequestID := strings.TrimSpace(payload.StreamRequestID)
	if err != nil || streamRequestID == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "streamRequestId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	detached, ok := conn.ReleaseTerminalStream(streamRequestID, terminalID)
	if !ok {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "streamRequestId does not belong to terminalId", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if terminalID == "" {
		terminalID = detached.StreamID
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{
		"terminalId":      terminalID,
		"streamRequestId": streamRequestID,
	})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTerminalInput(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalInputPayload](req)
	if err != nil || strings.TrimSpace(payload.TerminalID) == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := s.terminals.Input(conn.ClientBoundaryKey(), payload.TerminalID, payload.Data); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": strings.TrimSpace(payload.TerminalID)})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTerminalResize(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalResizePayload](req)
	if err != nil || strings.TrimSpace(payload.TerminalID) == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := s.terminals.Resize(conn.ClientBoundaryKey(), payload.TerminalID, payload.Cols, payload.Rows); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": strings.TrimSpace(payload.TerminalID)})
	conn.CompleteRequest(req.ID)
}

func (s *Server) wsTerminalClose(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalIDPayload](req)
	terminalID := strings.TrimSpace(payload.TerminalID)
	streamRequestID := strings.TrimSpace(payload.StreamRequestID)
	if err != nil || (terminalID == "" && streamRequestID == "") {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "terminalId or streamRequestId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if terminalID == "" {
		var ok bool
		terminalID, ok = conn.TerminalIDForStream(streamRequestID)
		if !ok {
			if _, cancelled := conn.ReleaseTerminalStream(streamRequestID, ""); cancelled {
				conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"streamRequestId": streamRequestID})
				conn.CompleteRequest(req.ID)
				return
			}
			conn.SendError(req.ID, "terminal_not_found", http.StatusNotFound, "terminal not found", nil)
			conn.CompleteRequest(req.ID)
			return
		}
	}
	if err := s.terminals.Close(conn.ClientBoundaryKey(), terminalID); err != nil {
		s.sendTerminalError(conn, req.ID, err)
		conn.CompleteRequest(req.ID)
		return
	}
	if streamRequestID != "" {
		conn.ReleaseTerminalStream(streamRequestID, terminalID)
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"terminalId": terminalID})
	conn.CompleteRequest(req.ID)
}

func terminalStreamEvent(seq int64, event terminalpkg.Event) stream.EventData {
	payload := map[string]any{
		"terminalId": event.TerminalID,
	}
	if event.AgentKey != "" {
		payload["agentKey"] = event.AgentKey
	}
	if event.TerminalKey != "" {
		payload["terminalKey"] = event.TerminalKey
	}
	if event.Scope != "" {
		payload["scope"] = event.Scope
	}
	if event.ChatID != "" {
		payload["chatId"] = event.ChatID
	}
	if event.CWD != "" {
		payload["cwd"] = event.CWD
	}
	if event.Shell != "" {
		payload["shell"] = event.Shell
	}
	if event.Data != "" {
		payload["data"] = event.Data
	}
	if event.ExitCode != nil {
		payload["exitCode"] = *event.ExitCode
	}
	if event.Reason != "" {
		payload["reason"] = event.Reason
	}
	if event.Type == terminalpkg.EventOpened {
		payload["reused"] = event.Reused
	}
	if event.Replay {
		payload["replay"] = true
	}
	return stream.EventData{
		Seq:       seq,
		Type:      event.Type,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func (s *Server) sendTerminalStatusError(conn *ws.Conn, requestID string, err *statusError) {
	if err == nil {
		return
	}
	frameType := "invalid_request"
	switch err.status {
	case http.StatusForbidden:
		frameType = "forbidden"
	case http.StatusNotFound:
		frameType = "terminal_not_found"
	case http.StatusConflict:
		frameType = "conflict"
	case http.StatusNotImplemented:
		frameType = "unsupported"
	case http.StatusTooManyRequests:
		frameType = "too_many_requests"
	case http.StatusInternalServerError, http.StatusServiceUnavailable:
		frameType = "internal_error"
	}
	conn.SendError(requestID, frameType, err.status, err.message, nil)
}

func (s *Server) sendTerminalError(conn *ws.Conn, requestID string, err error) {
	if errors.Is(err, terminalpkg.ErrNotFound) {
		conn.SendError(requestID, "terminal_not_found", http.StatusNotFound, "terminal not found", nil)
		return
	}
	if errors.Is(err, terminalpkg.ErrForbidden) {
		conn.SendError(requestID, "forbidden", http.StatusForbidden, "terminal access denied", nil)
		return
	}
	if errors.Is(err, terminalpkg.ErrInvalidKey) {
		conn.SendError(requestID, "invalid_request", http.StatusBadRequest, err.Error(), nil)
		return
	}
	if errors.Is(err, terminalpkg.ErrSessionLimit) {
		conn.SendError(requestID, "too_many_requests", http.StatusTooManyRequests, err.Error(), nil)
		return
	}
	if errors.Is(err, terminalpkg.ErrUnsupported) {
		conn.SendError(requestID, "unsupported", http.StatusNotImplemented, "terminal is unsupported", nil)
		return
	}
	conn.SendError(requestID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
}
