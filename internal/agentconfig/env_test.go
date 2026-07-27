package agentconfig

import (
	"path/filepath"
	"testing"
)

func TestHostEnvironmentUsesAgentAndChatPaths(t *testing.T) {
	env := HostEnvironment(
		filepath.Join("/runtime", "agents", "reader"),
		filepath.Join("/runtime", "chats", "chat-1"),
	)

	if got, want := env[EnvAgentConfigHome], "/runtime/agents/reader/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if got, want := env[EnvChatDir], "/runtime/chats/chat-1"; got != want {
		t.Fatalf("%s = %q, want %q", EnvChatDir, got, want)
	}
	if len(env) != 2 {
		t.Fatalf("HostEnvironment() = %#v, want two platform variables", env)
	}
}

func TestHostEnvironmentSkipsMissingPaths(t *testing.T) {
	if got := HostEnvironment("", ""); got != nil {
		t.Fatalf("HostEnvironment(\"\", \"\") = %#v, want nil", got)
	}
}

func TestContainerEnvironmentUsesSlashSeparatedPlatformPaths(t *testing.T) {
	env := ContainerEnvironment("/agent", "/workspace")
	if got, want := env[EnvAgentConfigHome], "/agent/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if got, want := env[EnvChatDir], "/workspace"; got != want {
		t.Fatalf("%s = %q, want %q", EnvChatDir, got, want)
	}
	if len(env) != 2 {
		t.Fatalf("ContainerEnvironment() = %#v, want two platform variables", env)
	}
}

func TestMergeUsesPlatformEnvironmentAsFinalOverrides(t *testing.T) {
	got := Merge(
		map[string]string{EnvAgentConfigHome: "/agent-custom", EnvChatDir: "/wrong-chat", "SYSTEM": "keep"},
		map[string]string{"SKILL": "invocation"},
		ContainerEnvironment("/agent", "/workspace"),
	)
	if got[EnvAgentConfigHome] != "/agent/.config" || got[EnvChatDir] != "/workspace" || got["SYSTEM"] != "keep" || got["SKILL"] != "invocation" {
		t.Fatalf("Merge() = %#v", got)
	}
}

func TestValidateUserEnvironmentRejectsPlatformVariables(t *testing.T) {
	for _, key := range []string{EnvAgentConfigHome, EnvChatDir, "ap_chat_dir"} {
		if err := ValidateUserEnvironment(map[string]string{key: "/custom"}); err == nil {
			t.Fatalf("expected reserved environment error for %q", key)
		}
	}
	if err := ValidateUserEnvironment(map[string]string{"HTTP_PROXY": "http://proxy"}); err != nil {
		t.Fatalf("unexpected ordinary environment error: %v", err)
	}
}
