package server

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
)

func TestTerminalEnvironmentUsesReservedAgentAndChatContext(t *testing.T) {
	agentDir := filepath.Join("/runtime", "agents", "reader")
	chatDir := filepath.Join("/runtime", "chats", "chat-a")
	entries := terminalEnvironment(catalog.AgentDefinition{
		AgentDir: agentDir,
		Runtime: map[string]any{
			"env": map[string]string{"HTTP_PROXY": "http://proxy"},
		},
	}, chatDir)
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
	if got["AP_CHAT_DIR"] != chatDir {
		t.Fatalf("AP_CHAT_DIR = %q, want %q", got["AP_CHAT_DIR"], chatDir)
	}
}

func TestTerminalEnvironmentRejectsRuntimeOverridesByApplyingPlatformContextLast(t *testing.T) {
	agentDir := filepath.Join("/runtime", "agents", "reader")
	chatDir := filepath.Join("/runtime", "chats", "chat-a")
	got := terminalEnvironmentValues(terminalEnvironment(catalog.AgentDefinition{
		AgentDir: agentDir,
		Runtime: map[string]any{
			"env": map[string]string{
				"AP_AGENT_CONFIG_HOME": "/custom",
				"AP_CHAT_DIR":          "/wrong-chat",
			},
		},
	}, chatDir))
	if want := filepath.Join(agentDir, ".config"); got["AP_AGENT_CONFIG_HOME"] != want {
		t.Fatalf("AP_AGENT_CONFIG_HOME = %q, want %q", got["AP_AGENT_CONFIG_HOME"], want)
	}
	if got["AP_CHAT_DIR"] != chatDir {
		t.Fatalf("AP_CHAT_DIR = %q, want %q", got["AP_CHAT_DIR"], chatDir)
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
