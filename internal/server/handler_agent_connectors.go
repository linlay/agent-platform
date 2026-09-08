package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
)

func (s *Server) handleAdminAgentConnectors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	editor, ok := s.deps.Registry.(adminsource.AgentConnectorEditor)
	if !ok {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent connector editor is not configured"))
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	var req api.SetAgentConnectorRequest
	if r.Method == http.MethodPut {
		if err := decodeStrictJSON(r, &req); err != nil || req.Enabled == nil || strings.TrimSpace(req.ConnectorID) == "" {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey, connectorId and enabled are required"))
			return
		}
		key = strings.TrimSpace(req.AgentKey)
	}
	if key == "" {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey is required"))
		return
	}
	var ids []string
	var err error
	if r.Method == http.MethodGet {
		ids, err = editor.ReadAgentConnectors(key)
	} else {
		ids, err = s.adminSources.SetAgentConnector(r.Context(), editor, key, strings.TrimSpace(req.ConnectorID), *req.Enabled, s.reloadAgentCatalog)
	}
	if err != nil {
		var reloadErr *adminsource.AgentConnectorReloadError
		if errors.As(err, &reloadErr) {
			s.writeAgentHTTPResponse(w, nil, reloadErr)
		} else {
			s.writeAgentHTTPResponse(w, nil, mapAdminSourceAgentError(err))
		}
		return
	}
	active := []string{}
	if def, found := s.deps.Registry.AgentDefinition(key); found {
		active = append(active, def.Connectors...)
	}
	configuredSet, activeSet := slices.Clone(ids), slices.Clone(active)
	slices.Sort(configuredSet)
	slices.Sort(activeSet)
	s.writeAgentHTTPResponse(w, api.AgentConnectorsResponse{
		AgentKey: key, ConnectorIDs: ids, ActiveConnectorIDs: active,
		ReloadPending: !slices.Equal(configuredSet, activeSet),
	}, nil)
}
