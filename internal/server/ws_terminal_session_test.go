package server

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
)

func TestTerminalEnvironmentUsesAgentConfigAndRuntimeOverrides(t *testing.T) {
	agentDir := filepath.Join("/runtime", "agents", "reader")
	entries := terminalEnvironment(catalog.AgentDefinition{
		AgentDir: agentDir,
		Runtime: map[string]any{
			"env": map[string]string{"XDG_CONFIG_HOME": "/custom", "HTTP_PROXY": "http://proxy"},
		},
	})
	got := map[string]string{}
	for _, entry := range entries {
		for index, char := range entry {
			if char == '=' {
				got[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	if got["XDG_CONFIG_HOME"] != "/custom" {
		t.Fatalf("XDG_CONFIG_HOME = %q, want runtime override", got["XDG_CONFIG_HOME"])
	}
	if got["AP_AGENT_CONFIG_HOME"] != filepath.Join(agentDir, ".config") {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q", got["AP_AGENT_CONFIG_HOME"])
	}
	if got["HTTP_PROXY"] != "http://proxy" || got["TERM"] != "xterm-256color" || got["COLORTERM"] != "truecolor" {
		t.Fatalf("unexpected terminal environment: %#v", got)
	}
}
