package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"agent-platform/internal/stream"
	terminalpkg "agent-platform/internal/terminal"
	"agent-platform/internal/ws"
)

type terminalStatusDetachPayload struct {
	StreamRequestID string `json:"streamRequestId"`
}

const terminalStatusInterval = time.Second

func (s *Server) wsTerminalStatus(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	if err := conn.ReserveTerminalStatusStream(req.ID); err != nil {
		if protoErr, ok := err.(*ws.ProtocolError); ok {
			conn.SendProtocolError(req.ID, protoErr)
		} else {
			conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}

	done := make(chan struct{})
	conn.AttachStreamCleanup(req.ID, func() {
		close(done)
	})

	ownerKey := conn.ClientBoundaryKey()
	var seq int64
	lastFingerprint := ""
	sendSnapshot := func() bool {
		sessions := s.terminals.List(ownerKey)
		fingerprint := terminalStatusFingerprint(sessions)
		if seq > 0 && fingerprint == lastFingerprint {
			return true
		}
		lastFingerprint = fingerprint
		seq++
		return conn.SendStreamEvent(req.ID, terminalStatusStreamEvent(seq, sessions))
	}

	if !sendSnapshot() {
		return
	}

	ticker := time.NewTicker(terminalStatusInterval)
	defer ticker.Stop()
	for {
		select {
		case <-conn.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if !sendSnapshot() {
				return
			}
		}
	}
}

func (s *Server) wsTerminalStatusDetach(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[terminalStatusDetachPayload](req)
	streamRequestID := strings.TrimSpace(payload.StreamRequestID)
	if err != nil || streamRequestID == "" {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "streamRequestId is required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	detached, ok := conn.ReleaseTerminalStatusStream(streamRequestID)
	if !ok {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "streamRequestId does not belong to terminal status stream", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{
		"streamId":         detached.StreamID,
		"streamRequestId":  streamRequestID,
		"statusStreamKind": "terminal-status",
	})
	conn.CompleteRequest(req.ID)
}

func terminalStatusStreamEvent(seq int64, sessions []terminalpkg.SessionInfo) stream.EventData {
	return stream.EventData{
		Seq:       seq,
		Type:      "terminal.status",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"scope":    terminalpkg.ScopeAgent,
			"sessions": sessions,
		},
	}
}

func terminalStatusFingerprint(sessions []terminalpkg.SessionInfo) string {
	var builder strings.Builder
	for _, session := range sessions {
		builder.WriteString(session.TerminalID)
		builder.WriteByte('\x00')
		builder.WriteString(session.AgentKey)
		builder.WriteByte('\x00')
		builder.WriteString(session.TerminalKey)
		builder.WriteByte('\x00')
		builder.WriteString(session.Status)
		builder.WriteByte('\x00')
		builder.WriteString(session.CWD)
		builder.WriteByte('\x00')
		builder.WriteString(session.Shell)
		builder.WriteByte('\x1e')
	}
	return builder.String()
}
