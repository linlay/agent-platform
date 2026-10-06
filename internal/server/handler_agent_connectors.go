package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

func (s *Server) connectorPresets(key string) ([]string, error) {
	if key == "" {
		return s.deps.Config.AllPresetConnectors(), nil
	}
	provider, ok := s.deps.Registry.(interface {
		PresetConnectorIDs(string) ([]string, error)
	})
	if !ok {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent connector presets are not configured")
	}
	ids, err := provider.PresetConnectorIDs(key)
	return ids, mapAdminSourceAgentError(err)
}

func (s *Server) handleAgentConnectors(w http.ResponseWriter, r *http.Request) {
	s.handleAgentConnectorSelection(w, r, false)
}

func (s *Server) handleAdminAgentConnectors(w http.ResponseWriter, r *http.Request) {
	s.handleAgentConnectorSelection(w, r, true)
}

func (s *Server) handleAgentConnectorSelection(w http.ResponseWriter, r *http.Request, management bool) {
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
	if r.Method == http.MethodPut {
		if def, found := s.deps.Registry.AgentDefinition(key); found && !def.Interaction().Connectors {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "interaction_disabled", "interactionConfig.connectors is disabled"))
			return
		}
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
	declared := slices.Clone(ids)
	presets := []string{}
	if provider, ok := s.deps.Registry.(interface {
		PresetConnectorIDs(string) ([]string, error)
	}); ok {
		presets, err = provider.PresetConnectorIDs(key)
		if err != nil {
			s.writeAgentHTTPResponse(w, nil, mapAdminSourceAgentError(err))
			return
		}
	}
	for _, id := range presets {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	configuredSet, activeSet := slices.Clone(ids), slices.Clone(active)
	slices.Sort(configuredSet)
	slices.Sort(activeSet)
	reloadPending := !slices.Equal(configuredSet, activeSet)
	if !management {
		s.writeAgentHTTPResponse(w, api.AgentConnectorsResponse{
			AgentKey: key, ConnectorIDs: catalog.SelectableConnectorIDs(ids, presets), ReloadPending: reloadPending,
		}, nil)
		return
	}
	s.writeAgentHTTPResponse(w, api.AdminAgentConnectorsResponse{
		AgentKey: key, ConnectorIDs: ids, ActiveConnectorIDs: active, PresetConnectorIDs: presets, DeclaredConnectorIDs: declared,
		ReloadPending: reloadPending,
	}, nil)
}
