package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
)

func (s *Server) handleConnectorConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(405, "method_not_allowed", "method not allowed"))
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		packages, err := s.connectorSources().Summaries()
		if err != nil {
			s.writeConnectorError(w, err)
			return
		}
		result := make([]connectorauth.Connection, 0, len(packages))
		for _, pkg := range packages {
			state, err := s.connectorAuth.Connection(r.Context(), pkg.ID)
			if err != nil {
				s.writeConnectorError(w, err)
				return
			}
			result = append(result, state)
		}
		s.writeAgentHTTPResponse(w, map[string]any{"connections": result}, nil)
		return
	}
	if !connector.ValidID(id) {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(400, "invalid_request", "valid connector id is required"))
		return
	}
	result, err := s.connectorAuth.Connection(r.Context(), id)
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func (s *Server) handleConnectorConnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.connectorAuth.ConnectComponent(strings.TrimSpace(r.URL.Query().Get("id")), strings.TrimSpace(r.URL.Query().Get("component")))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func (s *Server) handleConnectorDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.connectorAuth.Disconnect(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func (s *Server) handleConnectorCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.connectorAuth.Check(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")), strings.TrimSpace(r.URL.Query().Get("component")))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
