package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/ws"
	"context"
	"net/http"
	"strings"
)

type waitSkipRequest struct {
	RequestID string `json:"requestId"`
	RunID     string `json:"runId"`
	ToolID    string `json:"toolId"`
}

func (s *Server) skipWait(req waitSkipRequest) map[string]any {
	status := "not_found"
	if runs, ok := s.deps.Runs.(interface{ SkipWait(string, string) string }); ok {
		status = runs.SkipWait(req.RunID, req.ToolID)
	}
	return map[string]any{"runId": req.RunID, "toolId": req.ToolID, "accepted": status == "accepted" || status == "already_resolved", "status": status}
}
func (s *Server) handleWaitSkip(w http.ResponseWriter, r *http.Request) {
	var req waitSkipRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.ToolID) == "" {
		writeJSON(w, 400, api.Failure(400, "runId and toolId are required"))
		return
	}
	if !s.validateHTTPRunControl(w, r, req.RunID) {
		return
	}
	writeJSON(w, 200, api.Success(s.skipWait(req)))
}
func (s *Server) wsWaitSkip(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[waitSkipRequest](req)
	if err != nil || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.ToolID) == "" {
		conn.SendError(req.ID, "invalid_request", 400, "runId and toolId are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if !s.validateWSRunControl(conn, req.ID, payload.RunID) {
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", s.skipWait(payload))
	conn.CompleteRequest(req.ID)
}

// AuthorizationWaitStatus exposes only an attempt's identity and outcome.
func (s *Server) AuthorizationWaitStatus(connectorID, authorizationID string) (string, error) {
	session, err := s.connectorAuth.SessionStatus(connectorID, authorizationID)
	return session.Status, err
}
