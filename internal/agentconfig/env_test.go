package agentconfig

import (
	"path/filepath"
	"testing"
)

func TestEnvironmentUsesSharedAgentConfigHome(t *testing.T) {
	env := Environment(filepath.Join("/runtime", "agents", "reader"))

	if got, want := env[EnvAgentConfigHome], "/runtime/agents/reader/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if len(env) != 1 {
		t.Fatalf("Environment() = %#v, want only %s", env, EnvAgentConfigHome)
	}
}

func TestEnvironmentSkipsAgentsWithoutDirectory(t *testing.T) {
	if got := Environment(""); got != nil {
		t.Fatalf("Environment(\"\") = %#v, want nil", got)
	}
}

func TestContainerEnvironmentUsesSlashSeparatedConfigPath(t *testing.T) {
	env := ContainerEnvironment("/agent")
	if got, want := env[EnvAgentConfigHome], "/agent/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if len(env) != 1 {
		t.Fatalf("ContainerEnvironment() = %#v, want only %s", env, EnvAgentConfigHome)
	}
}

func TestMergeUsesLaterMapsAsOverrides(t *testing.T) {
	got := Merge(
		map[string]string{EnvAgentConfigHome: "/agent", "SYSTEM": "keep"},
		map[string]string{EnvAgentConfigHome: "/agent-custom", "SKILL": "value"},
		map[string]string{"SKILL": "invocation"},
	)
	if got[EnvAgentConfigHome] != "/agent-custom" || got["SYSTEM"] != "keep" || got["SKILL"] != "invocation" {
		t.Fatalf("Merge() = %#v", got)
	}
}
