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
			"env": map[string]string{"HTTP_PROXY": "http://proxy"},
		},
	})
	got := terminalEnvironmentValues(entries)
	if want := filepath.Join(agentDir, ".config"); got["AP_AGENT_CONFIG_HOME"] != want {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want %q", got["AP_AGENT_CONFIG_HOME"], want)
	}
	if _, ok := got["XDG_CONFIG_HOME"]; ok {
		t.Fatalf("terminal environment must not synthesize XDG_CONFIG_HOME: %#v", got)
	}
	if _, ok := got["AP_SYSTEM_XDG_CONFIG_HOME"]; ok {
		t.Fatalf("terminal environment must not synthesize AP_SYSTEM_XDG_CONFIG_HOME: %#v", got)
	}
	if got["HTTP_PROXY"] != "http://proxy" || got["TERM"] != "xterm-256color" || got["COLORTERM"] != "truecolor" {
		t.Fatalf("unexpected terminal environment: %#v", got)
	}
}

func TestTerminalEnvironmentAllowsRuntimeAgentConfigOverride(t *testing.T) {
	got := terminalEnvironmentValues(terminalEnvironment(catalog.AgentDefinition{
		AgentDir: filepath.Join("/runtime", "agents", "reader"),
		Runtime: map[string]any{
			"env": map[string]string{"AP_AGENT_CONFIG_HOME": "/custom"},
		},
	}))
	if got["AP_AGENT_CONFIG_HOME"] != "/custom" {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want runtime override", got["AP_AGENT_CONFIG_HOME"])
	}
}

func terminalEnvironmentValues(entries []string) map[string]string {
	got := map[string]string{}
	for _, entry := range entries {
		for index, char := range entry {
			if char == '=' {
				got[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	return got
}
