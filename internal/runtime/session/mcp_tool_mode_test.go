package session

import (
	"reflect"
	"testing"

	"agent-platform/internal/catalog"
)

func TestSessionMCPToolsFollowsToolMode(t *testing.T) {
	mounted := func() []string { return []string{"mcp_a_create", "mcp_a_search"} }
	agent := catalog.AgentDefinition{ConnectorMCPServers: []string{"agent-mcp-1"}}
	// A stray declaration never survives; the mode alone decides.
	base := []string{"bash", "mcp_call"}

	tools, keys := SessionMCPTools(base, agent, false, mounted)
	if !reflect.DeepEqual(tools, []string{"bash", "mcp_a_create", "mcp_a_search"}) || keys != nil {
		t.Fatalf("direct mode = %#v %#v", tools, keys)
	}

	tools, keys = SessionMCPTools(base, agent, true, func() []string { t.Fatal("tool mode must not list MCP tools"); return nil })
	if !reflect.DeepEqual(tools, []string{"bash", "mcp_search", "mcp_call"}) || !reflect.DeepEqual(keys, []string{"agent-mcp-1"}) {
		t.Fatalf("tool mode = %#v %#v", tools, keys)
	}

	tools, keys = SessionMCPTools(base, catalog.AgentDefinition{}, true, mounted)
	if !reflect.DeepEqual(tools, []string{"bash"}) || keys != nil {
		t.Fatalf("no MCP mounted = %#v %#v", tools, keys)
	}

	agent.ExcludedTools = []string{"mcp_search"}
	tools, _ = SessionMCPTools(base, agent, true, mounted)
	if !reflect.DeepEqual(tools, []string{"bash", "mcp_call"}) {
		t.Fatalf("excluded = %#v", tools)
	}
}
