package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/runtime/controlscope"
	"agent-platform/internal/ws"
)

func (s *Server) runControlScopes() controlscope.Store {
	return controlscope.NewStore(s.deps.Runs)
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
func (s *Server) validateRunControl(runID string, caller controlscope.Scope, authenticatedDesktopMain bool) *statusError {
	if strings.TrimSpace(runID) == "" {
		return btwStatusError(400, "invalid_request", "runId is required")
	}
	owner, err := s.runControlScopes().Load(runID)
	if err != nil {
		if errors.Is(err, controlscope.ErrMissing) {
			return btwStatusError(http.StatusConflict, "run_control_identity_unavailable", "run has no control identity")
		}
		return btwStatusError(500, "run_control_identity_unavailable", "cannot read run control identity")
	}
	// In-process Runs retain their immutable empty identity. Authorize the
	// authenticated Desktop Main separately without claiming or rewriting it.
	code := controlscope.Check(owner, caller)
	if owner == (controlscope.Scope{}) {
		if authenticatedDesktopMain && caller.Transport == "ws" && caller.Lane == "main" && caller.Subject != "" && caller.Boundary != "" {
			return nil
		}
		if code == "" {
			code = "run_control_identity_mismatch"
		}
	}
	if code != "" {
		return btwStatusError(403, code, "request does not match run control identity")
	}
	return nil
}
func (s *Server) validateWSRunControl(conn *ws.Conn, requestID, runID string) bool {
	if err := s.validateRunControl(runID, wsControlScope(conn), conn.IsAuthenticatedDesktopMain()); err != nil {
		s.sendWSStatusError(conn, requestID, err)
		conn.CompleteRequest(requestID)
		return false
	}
	return true
}
func (s *Server) validateHTTPRunControl(w http.ResponseWriter, r *http.Request, runID string) bool {
	if err := s.validateRunControl(runID, httpControlScope(r.Context(), ""), false); err != nil {
		writeStatusError(w, err)
		return false
	}
	return true
}
