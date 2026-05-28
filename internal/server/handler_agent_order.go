package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/ws"
)

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
		response, err := s.updateAgentOrder(req.Order)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) wsAgentOrder(_ context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payload, err := ws.DecodePayload[struct {
		Order *[]string `json:"order"`
	}](req)
	if err != nil {
		s.sendAgentWSError(conn, req, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	if payload.Order == nil {
		response, readErr := s.readAgentOrder()
		s.sendAgentWSResponse(conn, req, response, readErr)
		return
	}
	response, updateErr := s.updateAgentOrder(*payload.Order)
	s.sendAgentWSResponse(conn, req, response, updateErr)
}

func (s *Server) readAgentOrder() (api.AgentOrderResponse, error) {
	file, err := catalog.ReadAgentOrderFile(s.deps.Config.Paths.AgentsDir)
	if err != nil {
		return api.AgentOrderResponse{}, err
	}
	return api.AgentOrderResponse{
		Version:   file.Version,
		Order:     append([]string(nil), file.Order...),
		UpdatedAt: file.UpdatedAt,
	}, nil
}

func (s *Server) updateAgentOrder(order []string) (api.AgentOrderResponse, error) {
	normalized, err := s.validateAgentOrder(order)
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
	s.broadcast("catalog.updated", map[string]any{"reason": "agents"})
	return api.AgentOrderResponse{
		Version:   file.Version,
		Order:     append([]string(nil), file.Order...),
		UpdatedAt: file.UpdatedAt,
	}, nil
}

func (s *Server) validateAgentOrder(order []string) ([]string, error) {
	if s.deps.Registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent registry is not configured")
	}
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
		if _, ok := s.deps.Registry.AgentDefinition(key); !ok {
			return nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", fmt.Sprintf("unknown agent key: %s", key))
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	return normalized, nil
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
