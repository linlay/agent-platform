package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
)

func runtimeAliasFixture(t *testing.T) (home, workspace, chatDir, otherChat, stateDir string) {
	t.Helper()
	home = t.TempDir()
	workspace = t.TempDir()
	chatDir = filepath.Join(home, "chats", "chat-1")
	otherChat = filepath.Join(home, "chats", "chat-2")
	stateDir = filepath.Join(home, ".state")
	for _, dir := range []string{chatDir, otherChat, stateDir, workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestSandboxCwdMapsRuntimeAliasToExistingMounts(t *testing.T) {
	home, workspace, chatDir, _, _ := runtimeAliasFixture(t)
	if err := os.MkdirAll(filepath.Join(chatDir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	execCtx := &ExecutionContext{Session: QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: RuntimeRequestContext{
			LocalPaths:   LocalPaths{RuntimeHome: home, WorkspaceDir: workspace, ChatDir: chatDir, ChatsDir: filepath.Join(home, "chats")},
			SandboxPaths: SandboxPaths{WorkspaceDir: "/workspace", ChatDir: "/chat"},
		},
	}}

	for raw, want := range map[string]string{
		"@runtime/chats/chat-1":      "/chat",
		"@runtime/chats/chat-1/work": "/chat/work",
	} {
		got, err := resolveSandboxCwd(execCtx, raw)
		if err != nil || got != want {
			t.Fatalf("resolveSandboxCwd(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"@runtime", "@runtime/chats/chat-2", "@runtime/.state", "@runtime/../outside"} {
		got, err := resolveSandboxCwd(execCtx, raw)
		if err == nil {
			t.Fatalf("resolveSandboxCwd(%q) = %q, want rejection", raw, got)
		}
		if strings.Contains(got, home) {
			t.Fatalf("resolveSandboxCwd(%q) leaked a Host path: %q", raw, got)
		}
	}
}

func TestArtifactSourceRuntimeAliasKeepsPublishScope(t *testing.T) {
	home, workspace, chatDir, otherChat, stateDir := runtimeAliasFixture(t)
	for _, file := range []string{filepath.Join(chatDir, "report.md"), filepath.Join(otherChat, "report.md"), filepath.Join(stateDir, "secret")} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	resolved, code, message := resolveArtifactSourcePath("@runtime/chats/chat-1/report.md", workspace, chatDir, home)
	if code != "" || resolved != realPath(t, filepath.Join(chatDir, "report.md")) {
		t.Fatalf("resolve @runtime chat file: path=%q code=%q message=%q", resolved, code, message)
	}
	// The fixture lives under the system temp root, which artifact_publish
	// accepts, so compare with the absolute spelling instead of a fixed code:
	// @runtime must never resolve differently from the path it names.
	for _, suffix := range []string{"chats/chat-1/report.md", "chats/chat-2/report.md", ".state/secret", "chats/missing.md"} {
		viaAlias, aliasCode, _ := resolveArtifactSourcePath("@runtime/"+suffix, workspace, chatDir, home)
		viaAbsolute, absoluteCode, _ := resolveArtifactSourcePath(filepath.Join(home, filepath.FromSlash(suffix)), workspace, chatDir, home)
		if viaAlias != viaAbsolute || aliasCode != absoluteCode {
			t.Fatalf("%q: alias = %q/%q, absolute = %q/%q", suffix, viaAlias, aliasCode, viaAbsolute, absoluteCode)
		}
	}
	if resolved, code, _ := resolveArtifactSourcePath("@runtime/../outside", workspace, chatDir, home); resolved != "" || code != "path_not_allowed" {
		t.Fatalf("expected @runtime escape to be rejected, got %q %q", resolved, code)
	}
	if resolved, code, message := resolveArtifactSourcePath("@runtime/chats/chat-1/report.md", workspace, chatDir, ""); resolved != "" || code != "path_not_allowed" || !strings.Contains(message, "@runtime") {
		t.Fatalf("expected @runtime unavailable without a runtime root, got %q %q %q", resolved, code, message)
	}
}

func TestDesktopAndWebControlRuntimeAliasKeepBoundaries(t *testing.T) {
	// The Workspace contains the runtime root so Chat files are previewable.
	workspace := t.TempDir()
	home := filepath.Join(workspace, "runtime")
	chatDir := filepath.Join(home, "chats", "chat-1")
	otherChat := filepath.Join(home, "chats", "chat-2")
	for _, dir := range []string{chatDir, otherChat, filepath.Join(home, ".state")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	session := QuerySession{
		WorkspaceRoot: workspace,
		RuntimeContext: RuntimeRequestContext{
			LocalPaths: LocalPaths{RuntimeHome: home, WorkspaceDir: workspace, ChatDir: chatDir, ChatsDir: filepath.Join(home, "chats")},
		},
	}

	viaRuntime, err := resolveDesktopActionAlias(session, "@runtime/chats/chat-1/app.zip")
	if err != nil {
		t.Fatalf("resolve @runtime: %v", err)
	}
	viaChat, err := resolveDesktopActionAlias(session, "@chat/app.zip")
	if err != nil || viaRuntime != viaChat {
		t.Fatalf("@runtime = %q, @chat = %q, %v", viaRuntime, viaChat, err)
	}
	target, _, failed := resolveWebControlTarget(session, "@runtime/chats/chat-1/report.html")
	if failed || target.relativePath != "runtime/chats/chat-1/report.html" {
		t.Fatalf("web control @runtime target = %#v, failed=%v", target, failed)
	}

	// Installation has no Workspace check of its own, so @runtime must not
	// widen it beyond the current Chat and Workspace.
	separate := session
	separate.WorkspaceRoot = t.TempDir()
	separate.RuntimeContext.LocalPaths.WorkspaceDir = separate.WorkspaceRoot
	if _, _, err := resolveDesktopActionAliasTarget(separate, "@runtime/chats/chat-1/app.zip"); err != nil {
		t.Fatalf("current Chat via @runtime: %v", err)
	}
	for _, raw := range []string{"@runtime/chats/chat-2/app.zip", "@runtime/.state/secret", "@runtime/../outside"} {
		if _, candidate, err := resolveDesktopActionAliasTarget(separate, raw); err == nil {
			t.Fatalf("accepted %q -> %q", raw, candidate.Host)
		}
	}
	if _, _, failed := resolveWebControlTarget(separate, "@runtime/chats/chat-2/report.html"); !failed {
		t.Fatal("web control accepted another Chat through @runtime")
	}
}
