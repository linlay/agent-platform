package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

type adminAgentRegistry interface {
	AdminAgents() []catalog.AdminAgent
	AdminAgent(key string) (catalog.AdminAgent, bool)
	AdminAgentKeys() []string
}

func (s *Server) adminAgentRegistry() (adminAgentRegistry, error) {
	registry, ok := s.deps.Registry.(adminAgentRegistry)
	if !ok || registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent admin registry is not configured")
	}
	return registry, nil
}

func (s *Server) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	registry, err := s.adminAgentRegistry()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	items := registry.AdminAgents()
	response := make([]api.AdminAgentSummary, 0, len(items))
	for _, item := range items {
		response = append(response, buildAdminAgentSummary(item))
	}
	writeJSON(w, http.StatusOK, api.Success(response))
}

func (s *Server) handleAdminAgentDetail(w http.ResponseWriter, r *http.Request) {
	agentKey := strings.TrimSpace(r.URL.Query().Get("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	response, err := s.adminAgentDetail(agentKey)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminAgentOrder(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readAdminAgentOrder()
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.UpdateAgentOrderRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		response, err := s.updateAdminAgentOrder(req.Order)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) adminAgentDetail(agentKey string) (api.AdminAgentDetailResponse, error) {
	registry, err := s.adminAgentRegistry()
	if err != nil {
		return api.AdminAgentDetailResponse{}, err
	}
	item, ok := registry.AdminAgent(agentKey)
	if !ok {
		return api.AdminAgentDetailResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
	}
	if strings.EqualFold(item.Status, catalog.AdminAgentStatusReady) {
		if def, ok := s.deps.Registry.AgentDefinition(item.Key); ok {
			detail, err := s.buildEditableAgentDetailResponse(def)
			if err != nil {
				return api.AdminAgentDetailResponse{}, err
			}
			return adminAgentDetailFromAgentDetail(detail, item), nil
		}
	}
	return adminAgentDetailFromAdminAgent(item), nil
}

func buildAdminAgentSummary(item catalog.AdminAgent) api.AdminAgentSummary {
	return api.AdminAgentSummary{
		Key:          item.Key,
		Name:         firstNonBlank(item.Name, item.Key),
		Icon:         item.Icon,
		Mode:         item.Mode,
		WorkspaceDir: item.Workspace.Root,
		Role:         item.Role,
		Status:       firstNonBlank(item.Status, catalog.AdminAgentStatusInvalid),
		Diagnostics:  adminAgentDiagnostics(item.Diagnostics),
		Source:       adminAgentSource(item.Source),
		Meta:         cloneMeta(item.Meta),
	}
}

func adminAgentDetailFromAgentDetail(detail api.AgentDetailResponse, item catalog.AdminAgent) api.AdminAgentDetailResponse {
	return api.AdminAgentDetailResponse{
		Key:          detail.Key,
		Name:         detail.Name,
		Icon:         detail.Icon,
		Description:  detail.Description,
		Role:         detail.Role,
		Model:        detail.Model,
		Mode:         detail.Mode,
		Tools:        append([]string{}, detail.Tools...),
		Skills:       append([]string{}, detail.Skills...),
		Controls:     cloneListMaps(detail.Controls),
		Meta:         cloneMeta(detail.Meta),
		Definition:   cloneMeta(detail.Definition),
		SoulPrompt:   detail.SoulPrompt,
		AgentsPrompt: detail.AgentsPrompt,
		Source:       detail.Source,
		Status:       firstNonBlank(item.Status, catalog.AdminAgentStatusReady),
		Diagnostics:  adminAgentDiagnostics(item.Diagnostics),
	}
}

func adminAgentDetailFromAdminAgent(item catalog.AdminAgent) api.AdminAgentDetailResponse {
	return api.AdminAgentDetailResponse{
		Key:          item.Key,
		Name:         firstNonBlank(item.Name, item.Key),
		Icon:         item.Icon,
		Description:  item.Description,
		Role:         item.Role,
		Model:        item.ModelKey,
		Mode:         item.Mode,
		Tools:        append([]string{}, item.Tools...),
		Skills:       append([]string{}, item.Skills...),
		Controls:     cloneListMaps(item.Controls),
		Meta:         cloneMeta(item.Meta),
		Definition:   cloneMeta(item.Definition),
		SoulPrompt:   item.SoulPrompt,
		AgentsPrompt: item.AgentsPrompt,
		Source:       adminAgentSource(item.Source),
		Status:       firstNonBlank(item.Status, catalog.AdminAgentStatusInvalid),
		Diagnostics:  adminAgentDiagnostics(item.Diagnostics),
	}
}

func adminAgentSource(source catalog.EditableAgentSource) *api.AgentSource {
	if strings.TrimSpace(source.Path) == "" {
		return nil
	}
	return &api.AgentSource{
		Kind:     source.Kind,
		Path:     source.Path,
		AgentDir: source.AgentDir,
	}
}

func adminAgentDiagnostics(items []catalog.AdminAgentDiagnostic) []api.AdminAgentDiagnostic {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.AdminAgentDiagnostic, 0, len(items))
	for _, item := range items {
		out = append(out, api.AdminAgentDiagnostic{
			Severity:   item.Severity,
			Code:       item.Code,
			Message:    item.Message,
			SourcePath: item.SourcePath,
		})
	}
	return out
}

func cloneMeta(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	return contracts.CloneMap(src)
}

func (s *Server) readAdminAgentOrder() (api.AgentOrderResponse, error) {
	registry, err := s.adminAgentRegistry()
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	file, err := catalog.ReadAgentOrderFile(s.deps.Config.Paths.AgentsDir)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	updatedAt, err := timecontract.OptionalEpochMillis(file.UpdatedAt, "updatedAt", "admin.agent-order")
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	return api.AgentOrderResponse{
		Version:   file.Version,
		Order:     filterKnownAgentOrder(file.Order, keySet(registry.AdminAgentKeys())),
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Server) updateAdminAgentOrder(order []string) (api.AgentOrderResponse, error) {
	normalized, err := s.validateAdminAgentOrder(order)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	file := catalog.AgentOrderFile{
		Version:   1,
		Order:     normalized,
		UpdatedAt: time.Now().UnixMilli(),
	}
	if err := writeAgentOrderFile(s.deps.Config.Paths.AgentsDir, file); err != nil {
		return api.AgentOrderResponse{}, err
	}
	s.broadcast("catalog.updated", catalogUpdatedPushPayload("agents", time.Now().UnixMilli()))
	updatedAt, err := timecontract.OptionalEpochMillis(file.UpdatedAt, "updatedAt", "admin.agent-order")
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	return api.AgentOrderResponse{
		Version:   file.Version,
		Order:     append([]string(nil), file.Order...),
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Server) validateAdminAgentOrder(order []string) ([]string, error) {
	registry, err := s.adminAgentRegistry()
	if err != nil {
		return nil, err
	}
	known := keySet(registry.AdminAgentKeys())
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(order))
	for _, raw := range order {
		key := strings.TrimSpace(raw)
		if key == "" {
			return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "order contains empty agent key")
		}
		if _, exists := seen[key]; exists {
			return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", fmt.Sprintf("duplicate agent key: %s", key))
		}
		if _, ok := known[key]; !ok {
			return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", fmt.Sprintf("unknown agent key: %s", key))
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized, nil
}

func keySet(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func filterKnownAgentOrder(order []string, known map[string]struct{}) []string {
	if len(order) == 0 || len(known) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(order))
	for _, raw := range order {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := known[key]; !ok {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func writeAgentOrderFile(agentsDir string, file catalog.AgentOrderFile) error {
	if strings.TrimSpace(agentsDir) == "" {
		return fmt.Errorf("agents directory is not configured")
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(agentsDir, ".agent-order-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(agentsDir, catalog.AgentOrderFileName))
}
