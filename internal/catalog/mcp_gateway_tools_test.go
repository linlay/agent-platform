package catalog

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestMCPGatewayToolsCannotBeDeclared(t *testing.T) {
	for _, name := range []string{"mcp_search", "MCP_CALL"} {
		if err := ValidateOrdinaryAgentTools([]string{"bash", name}); err == nil || !strings.Contains(err.Error(), "mcp.tool-mode") {
			t.Fatalf("agent declaration %s: %v", name, err)
		}
	}
	defs := []api.ToolDetailResponse{{Name: "mcp_search", Meta: map[string]any{"sourceType": "local"}}}
	if err := validatePresetTools([]string{"mcp_search"}, defs); err == nil || !strings.Contains(err.Error(), "mcp.tool-mode") {
		t.Fatalf("preset: %v", err)
	}
}
