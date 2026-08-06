package agentconfig

import (
	"path/filepath"
	"testing"
)

func TestHostEnvironmentUsesAgentWorkspaceAndChatPaths(t *testing.T) {
	env := HostEnvironment(
		filepath.Join("/runtime", "agents", "reader"),
		filepath.Join("/projects", "reader"),
		filepath.Join("/runtime", "chats", "chat-1"),
	)

	if got, want := env[EnvAgentConfigHome], "/runtime/agents/reader/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if got, want := env[EnvWorkspaceDir], "/projects/reader"; got != want {
		t.Fatalf("%s = %q, want %q", EnvWorkspaceDir, got, want)
	}
	if got, want := env[EnvChatDir], "/runtime/chats/chat-1"; got != want {
		t.Fatalf("%s = %q, want %q", EnvChatDir, got, want)
	}
	if len(env) != 3 {
		t.Fatalf("HostEnvironment() = %#v, want three platform variables", env)
	}
}

func TestHostEnvironmentSkipsMissingPaths(t *testing.T) {
	if got := HostEnvironment("", "", ""); got != nil {
		t.Fatalf("HostEnvironment(\"\", \"\", \"\") = %#v, want nil", got)
	}
}

func TestContainerEnvironmentUsesSlashSeparatedPlatformPaths(t *testing.T) {
	env := ContainerEnvironment("/agent", "/workspace", "/chat")
	if got, want := env[EnvAgentConfigHome], "/agent/.config"; got != want {
		t.Fatalf("%s = %q, want %q", EnvAgentConfigHome, got, want)
	}
	if got, want := env[EnvWorkspaceDir], "/workspace"; got != want {
		t.Fatalf("%s = %q, want %q", EnvWorkspaceDir, got, want)
	}
	if got, want := env[EnvChatDir], "/chat"; got != want {
		t.Fatalf("%s = %q, want %q", EnvChatDir, got, want)
	}
	if len(env) != 3 {
		t.Fatalf("ContainerEnvironment() = %#v, want three platform variables", env)
	}
}

func TestMergeUsesPlatformEnvironmentAsFinalOverrides(t *testing.T) {
	got := Merge(
		map[string]string{EnvAgentConfigHome: "/agent-custom", EnvWorkspaceDir: "/wrong-workspace", EnvChatDir: "/wrong-chat", "SYSTEM": "keep"},
		map[string]string{"SKILL": "invocation"},
		ContainerEnvironment("/agent", "/workspace", "/chat"),
	)
	if got[EnvAgentConfigHome] != "/agent/.config" || got[EnvWorkspaceDir] != "/workspace" || got[EnvChatDir] != "/chat" || got["SYSTEM"] != "keep" || got["SKILL"] != "invocation" {
		t.Fatalf("Merge() = %#v", got)
	}
}

func TestValidateUserEnvironmentRejectsPlatformVariables(t *testing.T) {
	for _, key := range []string{EnvAgentConfigHome, EnvWorkspaceDir, EnvChatDir, EnvAccessToken, "ap_workspace_dir", "ap_chat_dir", "ap_access_token"} {
		if err := ValidateUserEnvironment(map[string]string{key: "/custom"}); err == nil {
			t.Fatalf("expected reserved environment error for %q", key)
		}
	}
	if err := ValidateUserEnvironment(map[string]string{"HTTP_PROXY": "http://proxy"}); err != nil {
		t.Fatalf("unexpected ordinary environment error: %v", err)
	}
}
