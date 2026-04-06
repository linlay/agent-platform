package mcp

import (
	"context"

	"agent-platform-runner-go/internal/api"
)

type ToolSync struct {
	registry *Registry
	client   *Client
}

func NewToolSync(registry *Registry, client *Client) *ToolSync {
	return &ToolSync{registry: registry, client: client}
}

func (s *ToolSync) Load(ctx context.Context) ([]api.ToolDetailResponse, error) {
	var out []api.ToolDetailResponse
	for _, server := range s.registry.Servers() {
		tools := server.Tools
		if len(tools) == 0 {
			discovered, err := s.client.ListTools(ctx, server.Key)
			if err != nil {
				return nil, err
			}
			tools = discovered
		}
		for _, tool := range tools {
			out = append(out, tool.ToAPITool(server.Key))
		}
	}
	return out, nil
}
