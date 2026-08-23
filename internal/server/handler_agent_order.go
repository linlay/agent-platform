package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/timecontract"
)

const maxPublicAgentOrderItems = 4096

func (s *Server) handleAgentOrder(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readAgentOrder()
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.UpdateAgentOrderRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		response, err := s.updateAgentOrder(r.Context(), req.Order)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) readAgentOrder() (api.AgentOrderResponse, error) {
	file, err := catalog.ReadAgentOrderFile(s.deps.Config.Paths.AgentsDir)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	updatedAt, err := timecontract.OptionalEpochMillis(file.UpdatedAt, "updatedAt", "agent-order")
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	return api.AgentOrderResponse{
		Version:   file.Version,
		Order:     runtimeAgentKeys(s.deps.Registry.Agents("all")),
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Server) updateAgentOrder(ctx context.Context, order []string) (api.AgentOrderResponse, error) {
	s.adminAgentMutationMu.Lock()
	defer s.adminAgentMutationMu.Unlock()

	currentValidKeys := runtimeAgentKeys(s.deps.Registry.Agents("all"))
	normalized, err := validatePublicAgentOrder(order, currentValidKeys)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	registry, err := s.adminAgentRegistry()
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	fullAdminKeys := adminAgentKeysInOrder(registry.AdminAgents())
	merged, err := mergeValidAgentOrderIntoAdminSlots(normalized, currentValidKeys, fullAdminKeys)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	return s.persistAgentOrder(ctx, merged, normalized, "agent-order")
}

func runtimeAgentKeys(items []api.AgentSummary) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if key := strings.TrimSpace(item.Key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func adminAgentKeysInOrder(items []catalog.AdminAgent) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if key := strings.TrimSpace(item.Key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func validatePublicAgentOrder(order, currentValidKeys []string) ([]string, error) {
	if order == nil {
		return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "order is required")
	}
	if len(order) > maxPublicAgentOrderItems {
		return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "order exceeds the supported item limit")
	}
	known := keySet(currentValidKeys)
	seen := make(map[string]struct{}, len(order))
	normalized := make([]string, 0, len(currentValidKeys))
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
	for _, key := range currentValidKeys {
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized, nil
}

func mergeValidAgentOrderIntoAdminSlots(validOrder, currentValidKeys, fullAdminKeys []string) ([]string, error) {
	validSet := keySet(currentValidKeys)
	if len(validOrder) != len(validSet) {
		return nil, fmt.Errorf("valid agent order does not match the current catalog")
	}
	merged := append([]string(nil), fullAdminKeys...)
	validIndex := 0
	for index, key := range merged {
		if _, ok := validSet[key]; !ok {
			continue
		}
		if validIndex >= len(validOrder) {
			return nil, fmt.Errorf("valid agent slots exceed the current catalog")
		}
		merged[index] = validOrder[validIndex]
		validIndex++
	}
	if validIndex != len(validOrder) {
		return nil, fmt.Errorf("valid agent slots do not match the admin catalog")
	}
	return merged, nil
}
