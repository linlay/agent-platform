package mcp

import (
	"context"
	"log"
	"sync"

	"agent-platform-runner-go/internal/api"
)

type ToolSync struct {
	registry *Registry
	client   *Client

	mu        sync.RWMutex
	snapshots map[string][]api.ToolDetailResponse
}

func NewToolSync(registry *Registry, client *Client) *ToolSync {
	return &ToolSync{
		registry:  registry,
		client:    client,
		snapshots: map[string][]api.ToolDetailResponse{},
	}
}

func (s *ToolSync) Load(ctx context.Context) ([]api.ToolDetailResponse, error) {
	var out []api.ToolDetailResponse
	seen := map[string]bool{}
	for _, server := range s.registry.Servers() {
		tools := server.Tools
		if len(tools) == 0 {
			// Try initialize first
			_ = s.client.Initialize(ctx, server.Key)
			discovered, err := s.client.ListTools(ctx, server.Key)
			if err != nil {
				log.Printf("[mcp] skip unavailable server %q during tool sync: %v", server.Key, err)
				// Use cached snapshot if available
				s.mu.RLock()
				cached := s.snapshots[server.Key]
				s.mu.RUnlock()
				out = append(out, cached...)
				continue
			}
			tools = discovered
		}

		serverTools := make([]api.ToolDetailResponse, 0, len(tools))
		for _, tool := range tools {
			apiTool := tool.ToAPITool(server.Key)
			// Conflict detection: skip duplicate tool names
			if seen[apiTool.Name] {
				log.Printf("[mcp] skip duplicate tool %q from server %q", apiTool.Name, server.Key)
				continue
			}
			seen[apiTool.Name] = true
			serverTools = append(serverTools, apiTool)
		}

		// Cache snapshot
		s.mu.Lock()
		s.snapshots[server.Key] = serverTools
		s.mu.Unlock()

		out = append(out, serverTools...)
	}
	return out, nil
}

// RefreshServer re-syncs tools for a specific server and returns the updated tools.
func (s *ToolSync) RefreshServer(ctx context.Context, serverKey string) ([]api.ToolDetailResponse, error) {
	_ = s.client.Initialize(ctx, serverKey)
	discovered, err := s.client.ListTools(ctx, serverKey)
	if err != nil {
		return nil, err
	}
	serverTools := make([]api.ToolDetailResponse, 0, len(discovered))
	for _, tool := range discovered {
		serverTools = append(serverTools, tool.ToAPITool(serverKey))
	}

	s.mu.Lock()
	s.snapshots[serverKey] = serverTools
	s.mu.Unlock()

	return serverTools, nil
}

// CachedTools returns all cached tool snapshots across all servers.
func (s *ToolSync) CachedTools() []api.ToolDetailResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []api.ToolDetailResponse
	for _, tools := range s.snapshots {
		out = append(out, tools...)
	}
	return out
}
