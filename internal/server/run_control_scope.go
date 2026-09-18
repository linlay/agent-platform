package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/ws"
)

func (s *Server) runControlScopes() controlscope.Store {
	root := s.deps.Config.Paths.StateDir
	if root == "" {
		root = filepath.Join(s.deps.Config.Paths.ChatsDir, ".state")
	}
	return controlscope.Store{Root: filepath.Join(root, "run-controls")}
}
func httpControlScope(ctx context.Context, lane string) controlscope.Scope {
	scope := controlscope.Scope{Transport: "http", Lane: lane}
	if p := PrincipalFromContext(ctx); p != nil {
		scope.Subject = strings.TrimSpace(p.Subject)
	}
	return scope
}
func wsControlScope(conn *ws.Conn) controlscope.Scope {
	target := conn.WebClientTarget()
	boundary := target.BoundaryKey
	// Connections without a device are scoped to their authenticated subject,
	// not the ephemeral session ID. Anonymous mode retains its existing scope.
	if strings.HasPrefix(boundary, "conn:") {
		boundary = ""
	}
	return controlscope.Scope{Transport: "ws", Lane: conn.QueryLane(), Subject: target.Subject, Boundary: boundary}
}
func (s *Server) validateRunControl(runID string, caller controlscope.Scope) *statusError {
	if strings.TrimSpace(runID) == "" {
		return btwStatusError(400, "invalid_request", "runId is required")
	}
	owner, err := s.runControlScopes().Load(runID)
	if err != nil {
		if errors.Is(err, controlscope.ErrMissing) {
			return btwStatusError(http.StatusConflict, "run_control_identity_unavailable", "run has no persisted control identity")
		}
		return btwStatusError(500, "run_control_identity_unavailable", "cannot read run control identity")
	}
	if code := controlscope.Check(owner, caller); code != "" {
		return btwStatusError(403, code, "request does not match run control identity")
	}
	return nil
}
func (s *Server) validateWSRunControl(conn *ws.Conn, requestID, runID string) bool {
	if err := s.validateRunControl(runID, wsControlScope(conn)); err != nil {
		s.sendWSStatusError(conn, requestID, err)
		conn.CompleteRequest(requestID)
		return false
	}
	return true
}
func (s *Server) validateHTTPRunControl(w http.ResponseWriter, r *http.Request, runID string) bool {
	if err := s.validateRunControl(runID, httpControlScope(r.Context(), "")); err != nil {
		writeStatusError(w, err)
		return false
	}
	return true
}
