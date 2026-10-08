package server

import (
	"context"
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
	var req api.SetAgentConnectorRequest
	if r.Method == http.MethodPut {
		if err := decodeStrictJSON(r, &req); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey, connectorId and enabled are required"))
			return
		}
	} else {
		req.AgentKey = r.URL.Query().Get("agentKey")
	}
	state, err := s.agentConnectorSelection(r.Context(), req, r.Method == http.MethodPut)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	if management {
		s.writeAgentHTTPResponse(w, state, nil)
		return
	}
	s.writeAgentHTTPResponse(w, agentConnectorUsageResponse(state), nil)
}

// HTTP usage, HTTP management and WS usage share source mutation, publication
// tracking and preset validation; only their outward projections differ.
func (s *Server) agentConnectorSelection(ctx context.Context, req api.SetAgentConnectorRequest, write bool) (api.AdminAgentConnectorsResponse, error) {
	editor, ok := s.deps.Registry.(adminsource.AgentConnectorEditor)
	if !ok {
		return api.AdminAgentConnectorsResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent connector editor is not configured")
	}
	key := strings.TrimSpace(req.AgentKey)
	if write && (req.Enabled == nil || strings.TrimSpace(req.ConnectorID) == "") {
		return api.AdminAgentConnectorsResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey, connectorId and enabled are required")
	}
	if key == "" {
		return api.AdminAgentConnectorsResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey is required")
	}
	if write {
		if def, found := s.deps.Registry.AgentDefinition(key); found && !def.Interaction().Connectors {
			return api.AdminAgentConnectorsResponse{}, newAgentStatusError(http.StatusBadRequest, "interaction_disabled", "interactionConfig.connectors is disabled")
		}
	}
	var ids []string
	var err error
	if !write {
		ids, err = editor.ReadAgentConnectors(key)
	} else {
		ids, err = s.adminSources.SetAgentConnector(ctx, editor, key, strings.TrimSpace(req.ConnectorID), *req.Enabled, s.reloadAgentCatalog)
	}
	if err != nil {
		var reloadErr *adminsource.AgentConnectorReloadError
		if errors.As(err, &reloadErr) {
			return api.AdminAgentConnectorsResponse{}, reloadErr
		}
		return api.AdminAgentConnectorsResponse{}, mapAdminSourceAgentError(err)
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
			return api.AdminAgentConnectorsResponse{}, mapAdminSourceAgentError(err)
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
	return api.AdminAgentConnectorsResponse{
		AgentKey: key, ConnectorIDs: ids, ActiveConnectorIDs: active, PresetConnectorIDs: presets, DeclaredConnectorIDs: declared,
		ReloadPending: reloadPending,
	}, nil
}

func agentConnectorUsageResponse(state api.AdminAgentConnectorsResponse) api.AgentConnectorsResponse {
	return api.AgentConnectorsResponse{
		AgentKey: state.AgentKey, ConnectorIDs: catalog.SelectableConnectorIDs(state.ConnectorIDs, state.PresetConnectorIDs), ReloadPending: state.ReloadPending,
	}
}
