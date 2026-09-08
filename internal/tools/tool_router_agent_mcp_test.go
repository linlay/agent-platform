package tools

import (
	"context"
	"testing"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type recordingAgentMCPClient struct {
	calls        int
	server, tool string
}

func (c *recordingAgentMCPClient) CallTool(_ context.Context, server, tool string, _, _ map[string]any) (any, error) {
	c.calls++
	c.server, c.tool = server, tool
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil
}

func TestAgentMCPRouteRejectsOtherAgentAndSendsOriginalWireName(t *testing.T) {
	client := &recordingAgentMCPClient{}
	router := &ToolRouter{mcp: client}
	def := api.ToolDetailResponse{Name: "mcp_scoped_search", Meta: map[string]any{"serverKey": "first-instance", "agentKey": "first", "mcpToolName": "search"}}
	for _, ctx := range []*ExecutionContext{nil, {Session: QuerySession{AgentKey: "second"}}} {
		result := router.invokeMCPTool(context.Background(), def, nil, ctx)
		if result.Error != "mcp_agent_mismatch" || client.calls != 0 {
			t.Fatalf("cross-Agent invocation: %#v", result)
		}
	}
	result := router.invokeMCPTool(context.Background(), def, nil, &ExecutionContext{Session: QuerySession{AgentKey: "first"}})
	if result.Error != "" || client.calls != 1 || client.server != "first-instance" || client.tool != "search" {
		t.Fatalf("incorrect MCP wire route: %#v %#v", result, client)
	}
}
