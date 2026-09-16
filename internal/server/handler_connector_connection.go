package server

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"context"
	"net/http"
	"regexp"
	"strings"
)

// Desktop app tokens represent a device unless Desktop has issued a verified
// enterprise-user subject. Never bind a private connector to the shared app account.
func (s *Server) connectorOwnerUser(ctx context.Context) (string, error) {
	if principal := PrincipalFromContext(ctx); principal != nil {
		if _, desktop := principal.Claims["device_id"]; desktop && !regexp.MustCompile(`^desktop-user:[a-f0-9]{64}$`).MatchString(principal.Subject) {
			return "", newAgentStatusError(http.StatusUnauthorized, "connector_user_required", "Sign in to Desktop before connecting a personal tool")
		}
	}
	return s.catalogOrderUser(ctx)
}

func (s *Server) connectorOwnerManager(r *http.Request) (*connectorauth.Manager, error) {
	owner, err := s.connectorOwnerUser(r.Context())
	if err != nil {
		return nil, err
	}
	return s.connectorAuth.ForOwner(owner), nil
}
func (s *Server) handleConnectorConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager, err := s.connectorOwnerManager(r)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if r.Method == http.MethodGet && id == "" {
		packages, err := s.connectorSources().Summaries()
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		result := make([]connectorauth.Connection, 0, len(packages))
		for _, pkg := range packages {
			state, err := manager.Connection(r.Context(), pkg.ID)
			if err != nil {
				s.writeConnectorError(w, err)
				return
			}
			result = append(result, state)
		}
		s.writeAgentHTTPResponse(w, map[string]any{"connections": result}, nil)
		return
	}
	if r.Method == http.MethodPut {
		var request struct {
			ConnectorID string `json:"connectorId"`
			Enabled     *bool  `json:"enabled"`
		}
		if err := decodeStrictJSON(r, &request); err != nil || request.Enabled == nil || !connector.ValidID(request.ConnectorID) {
			s.writeConnectorError(w, newConnectionError())
			return
		}
		result, err := manager.SetEnabled(r.Context(), request.ConnectorID, *request.Enabled)
		if err != nil && err.Error() == "connection_required" {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusConflict, "connection_required", "Connect this connector before enabling it"))
			return
		}
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		s.writeAgentHTTPResponse(w, result, nil)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, PUT")
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(405, "method_not_allowed", "method not allowed"))
		return
	}
	if !connector.ValidID(id) {
		s.writeConnectorError(w, newConnectionError())
		return
	}
	result, err := manager.Connection(r.Context(), id)
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func newConnectionError() error {
	return newAgentStatusError(http.StatusBadRequest, "invalid_request", "valid connectorId and explicit enabled are required")
}
func (s *Server) handleConnectorConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager, err := s.connectorOwnerManager(r)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	result, err := manager.Connect(r.URL.Query().Get("id"))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func (s *Server) handleConnectorDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager, err := s.connectorOwnerManager(r)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	result, err := manager.Disconnect(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
