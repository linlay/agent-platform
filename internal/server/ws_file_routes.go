package server

import (
	"context"
	"net/http"
	"strings"

	"agent-platform/internal/ws"
)

// wsAgentFile returns agent workspace file metadata and text content through
// the standard WebSocket response envelope. Raw file bytes remain on the HTTP
// /api/file?response=content data plane.
func (s *Server) wsAgentFile(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
		Path     string `json:"path"`
		Encoding string `json:"encoding"`
		Response string `json:"response"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "invalid file payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if response := strings.TrimSpace(payload.Response); response != "" && !strings.EqualFold(response, "json") {
		conn.SendError(req.ID, "invalid_request", http.StatusBadRequest, "response must be json over WebSocket", nil)
		conn.CompleteRequest(req.ID)
		return
	}

	resolved, err := s.resolveAgentFile(payload.AgentKey, payload.Path)
	if err != nil {
		s.sendAgentWSError(conn, req, err)
		return
	}
	response, err := s.readAgentFileMetadata(resolved, payload.Encoding)
	if err != nil {
		s.sendAgentWSError(conn, req, err)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", response)
	conn.CompleteRequest(req.ID)
}
