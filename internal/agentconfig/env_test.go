package agentconfig

import (
	"path/filepath"
	"testing"
)

func TestEnvironmentUsesAgentConfigAndPreservesSystemXDGHome(t *testing.T) {
	t.Setenv(EnvConfigHome, "/system/config")
	env := Environment(filepath.Join("/runtime", "agents", "reader"))

	if got, want := env[EnvConfigHome], "/runtime/agents/reader/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvConfigHome, got, want)
	}
	if got, want := env[EnvAgentConfigHome], "/runtime/agents/reader/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if got, want := env[EnvSystemConfigHome], "/system/config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvSystemConfigHome, got, want)
	}
}

func TestEnvironmentSkipsAgentsWithoutDirectory(t *testing.T) {
	if got := Environment(""); got != nil {
		t.Fatalf("Environment(\"\") = %#v, want nil", got)
	}
}

func TestContainerEnvironmentUsesSlashSeparatedConfigPath(t *testing.T) {
	env := ContainerEnvironment("/agent")
	if got, want := env[EnvConfigHome], "/agent/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvConfigHome, got, want)
	}
}

func TestMergeUsesLaterMapsAsOverrides(t *testing.T) {
	got := Merge(
		map[string]string{"XDG_CONFIG_HOME": "/agent", "SYSTEM": "keep"},
		map[string]string{"XDG_CONFIG_HOME": "/agent-custom", "SKILL": "value"},
		map[string]string{"SKILL": "invocation"},
	)
	if got["XDG_CONFIG_HOME"] != "/agent-custom" || got["SYSTEM"] != "keep" || got["SKILL"] != "invocation" {
		t.Fatalf("Merge() = %#v", got)
	}
}
