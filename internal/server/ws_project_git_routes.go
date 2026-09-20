package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"agent-platform/internal/api"
	projectpkg "agent-platform/internal/project"
	"agent-platform/internal/ws"
)

func (s *Server) wsProjectGit(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	defer conn.CompleteRequest(req.ID)
	payload, err := ws.DecodePayload[struct {
		AgentKey string `json:"agentKey"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		return
	}
	response, err := s.projectService().Git(ctx, payload.AgentKey)
	s.writeProjectWSResponse(conn, req, response, err)
}

// Like the order endpoints, reads and mutations share a WS route. Presence of
// any mutation field selects the write contract, including malformed writes.
func (s *Server) wsProjectGitBranches(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	defer conn.CompleteRequest(req.ID)
	payload, err := ws.DecodePayload[api.ProjectGitBranchRequest](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(req.Payload, &fields); err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid payload", nil)
		return
	}
	_, operation := fields["operation"]
	_, branch := fields["branch"]
	_, revision := fields["expectedRevision"]
	if operation || branch || revision {
		response, err := s.projectService().ChangeGitBranch(ctx, payload)
		s.writeProjectWSResponse(conn, req, response, err)
		return
	}
	response, err := s.projectService().GitBranches(ctx, payload.AgentKey)
	s.writeProjectWSResponse(conn, req, response, err)
}

func (s *Server) writeProjectWSResponse(conn *ws.Conn, req ws.RequestFrame, response any, err error) {
	if err == nil {
		conn.SendResponse(req.Type, req.ID, 0, "success", response)
		return
	}
	var projectErr projectpkg.Error
	if errors.As(err, &projectErr) {
		conn.SendError(req.ID, projectErr.Code, projectErr.Status, projectErr.Message, map[string]any{"code": projectErr.Code})
		return
	}
	conn.SendError(req.ID, "internal_error", http.StatusInternalServerError, err.Error(), nil)
}
