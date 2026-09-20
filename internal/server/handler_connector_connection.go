package server

import (
	"agent-platform/internal/connector"
	"agent-platform/internal/connectorauth"
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleConnectorConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager := s.connectorAuth
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
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		data, readErr := io.ReadAll(r.Body)
		r.Body.Close()
		if readErr != nil || connector.DecodeJSON(data, &request) != nil || request.Enabled == nil || !connector.ValidID(request.ConnectorID) {
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
	manager := s.connectorAuth
	result, err := manager.Connect(r.URL.Query().Get("id"))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
func (s *Server) handleConnectorDisconnect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	manager := s.connectorAuth
	result, err := manager.Disconnect(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		s.writeConnectorError(w, err)
		return
	}
	s.writeAgentHTTPResponse(w, result, nil)
}
