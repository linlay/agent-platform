package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/connector"
	"agent-platform/internal/mcp"
)

func (s *Server) handleConnectors(w http.ResponseWriter, r *http.Request) {
	sources := s.connectorSources()
	items, err := sources.Summaries()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusServiceUnavailable, "connector_catalog_unavailable", err.Error()))
		return
	}
	type mcpStatus struct {
		ServerKey string `json:"serverKey"`
		AgentKey  string `json:"agentKey,omitempty"`
		ToolCount int    `json:"toolCount"`
		api.MCPServerToolSyncStatus
	}
	type entry struct {
		connector.Summary
		MCP []mcpStatus `json:"mcp,omitempty"`
	}
	var mounts []connector.AgentRuntime
	if provider, ok := s.deps.Registry.(mcp.AgentConnectorSource); ok {
		mounts = provider.ConnectorRuntimes()
	}
	result := make([]entry, 0, len(items))
	for _, item := range items {
		value := entry{Summary: item}
		pkg, err := sources.Load(item.ID)
		if err != nil {
			s.writeAgentHTTPResponse(w, nil, err)
			return
		}

		for _, sourceKey := range pkg.ServerKeys() {
			mounted := false
			for _, mount := range mounts {
				if mount.ID != pkg.ID {
					continue
				}
				mounted = true
				key := connector.AgentServerKey(mount.AgentKey, sourceKey)
				status := api.MCPServerToolSyncStatus{Status: "pending"}
				if s.deps.MCPToolSyncStatus != nil {
					if current, ok := s.deps.MCPToolSyncStatus.ServerStatus(key); ok {
						status = current
					}
				}
				value.MCP = append(value.MCP, mcpStatus{ServerKey: key, AgentKey: mount.AgentKey, ToolCount: s.connectorMCPToolCount(key), MCPServerToolSyncStatus: status})
			}
			if !mounted {
				value.MCP = append(value.MCP, mcpStatus{ServerKey: sourceKey, MCPServerToolSyncStatus: api.MCPServerToolSyncStatus{Status: "unmounted"}})
			}
		}
		result = append(result, value)
	}
	s.writeAgentHTTPResponse(w, map[string]any{"connectors": result}, nil)
}

func (s *Server) handleConnectorDefinition(w http.ResponseWriter, r *http.Request) {
	sources := s.connectorSources()
	switch r.Method {
	case http.MethodGet:
		file, err := sources.ReadFile(r.URL.Query().Get("id"), r.URL.Query().Get("file"))
		if err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_connector", err.Error()))
			return
		}
		s.writeAgentHTTPResponse(w, file, nil)
	case http.MethodPut:
		var req struct {
			ID         string `json:"id"`
			File       string `json:"file"`
			Content    string `json:"content"`
			BaseSHA256 string `json:"baseSha256"`
		}
		if err := decodeJSON(r, &req); err != nil {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
			return
		}
		file, err := connector.SaveDefinition(sources.ExternalRoot, connector.File{ID: req.ID, File: req.File, Content: req.Content}, req.BaseSHA256, mcp.ValidateConnectorPackage, func() error {
			if s.deps.CatalogReloader == nil {
				return nil
			}
			return s.deps.CatalogReloader.Reload(context.WithoutCancel(r.Context()), "connectors")
		})
		if err != nil {
			if errors.Is(err, connector.ErrBuiltinReadOnly) {
				s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusForbidden, "builtin_connector_readonly", err.Error()))
				return
			}
			if errors.Is(err, connector.ErrConflict) {
				s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusConflict, "conflict", err.Error()))
				return
			}
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_connector", err.Error()))
			return
		}
		s.writeAgentHTTPResponse(w, file, nil)
	default:
		if r.Method == http.MethodDelete && connector.IsBuiltin(r.URL.Query().Get("id")) {
			s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusForbidden, "builtin_connector_readonly", connector.ErrBuiltinReadOnly.Error()))
			return
		}
		w.Header().Set("Allow", "GET, PUT")
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	}
}

func (s *Server) connectorSources() connector.Sources {
	return s.deps.Config.Paths.ConnectorSources()
}

func (s *Server) connectorMCPToolCount(serverKey string) int {
	serverKey = strings.TrimSpace(serverKey)
	if s == nil || s.deps.Tools == nil || serverKey == "" {
		return 0
	}
	count := 0
	seen := map[string]struct{}{}
	for _, tool := range s.deps.Tools.Definitions() {
		if canonical, ok := canonicalizePublicToolDefinition(tool); ok {
			tool = canonical
		}
		if toolSourceCategory(tool) != "mcp" {
			continue
		}
		sourceKey := strings.TrimSpace(anyStringValue(tool.Meta["sourceKey"]))
		if sourceKey == "" {
			sourceKey = strings.TrimSpace(anyStringValue(tool.Meta["serverKey"]))
		}
		if !strings.EqualFold(sourceKey, serverKey) {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(tool.Name))
		if name == "" {
			name = strings.ToLower(strings.TrimSpace(tool.Key))
		}
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		count++
	}
	return count
}
