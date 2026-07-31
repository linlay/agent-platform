package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestMountResolverUsesAgentLocalSkillsForRunAndAgentLevels(t *testing.T) {
	for _, level := range []string{"run", "agent"} {
		t.Run(level, func(t *testing.T) {
			paths := mountResolverTestPaths(t, "reader")
			resolver := NewContainerHubMountResolver(paths)

			mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", level, nil)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			mount, ok := mountByDestination(mounts, "/skills")
			if !ok {
				t.Fatalf("expected /skills mount, got %#v", mounts)
			}
			want := filepath.Join(paths.RUAgentsDir, "reader", "skills")
			if mount.Source != want {
				t.Fatalf("skills source = %q, want %q", mount.Source, want)
			}
			if mount.Source == paths.SkillsMarketDir {
				t.Fatalf("expected agent-local skills, got skills market source %q", mount.Source)
			}
		})
	}
}

func TestMountResolverRequiresValidChatID(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	resolver := NewContainerHubMountResolver(paths)

	for _, chatID := range []string{"", "../other-chat", "nested/chat"} {
		t.Run(strings.ReplaceAll(chatID, "/", "_"), func(t *testing.T) {
			if _, err := resolver.Resolve(mountResolverWorkspace(t, paths), chatID, "reader", "run", nil); err == nil || !strings.Contains(err.Error(), "valid chatId is required") {
				t.Fatalf("Resolve(%q) error = %v, want valid chatId error", chatID, err)
			}
		})
	}
}

func TestMountResolverMasksChatsRootWhenWorkspaceContainsIt(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	workspace := filepath.Dir(paths.ChatsDir)
	resolver := NewContainerHubMountResolver(paths)

	layout, err := resolver.ResolveLayout(workspace, "chat-1", "reader", "run", nil)
	if err != nil {
		t.Fatalf("ResolveLayout() error = %v", err)
	}
	if len(layout.MaskedPaths) != 1 || layout.MaskedPaths[0] != "/workspace/chats" {
		t.Fatalf("masked paths = %#v, want /workspace/chats", layout.MaskedPaths)
	}
	if mount, ok := mountByDestination(layout.Mounts, "/workspace"); !ok || mount.Source != realMountPath(t, workspace) {
		t.Fatalf("workspace mount = %#v, ok=%v", mount, ok)
	}
	if _, ok := mountByDestination(layout.Mounts, "/chat"); !ok {
		t.Fatalf("current chat mount missing: %#v", layout.Mounts)
	}
}

func TestMountResolverRejectsWorkspaceEqualToOrInsideChatsRoot(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	resolver := NewContainerHubMountResolver(paths)
	inside := filepath.Join(paths.ChatsDir, "project")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{paths.ChatsDir, inside} {
		if _, err := resolver.ResolveLayout(workspace, "chat-1", "reader", "run", nil); err == nil {
			t.Fatalf("workspace %q must be rejected", workspace)
		}
	}
}

func TestMountResolverDoesNotFallbackToSkillsMarketWhenAgentSkillsUnavailable(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	if err := os.RemoveAll(filepath.Join(paths.RUAgentsDir, "reader", "skills")); err != nil {
		t.Fatalf("remove skills fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.RUAgentsDir, "reader", "skills"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write skills file fixture: %v", err)
	}
	resolver := NewContainerHubMountResolver(paths)

	mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "run", nil)
	if err == nil {
		t.Fatalf("expected skills-dir error, got mounts %#v", mounts)
	}
	if !strings.Contains(err.Error(), "container-hub mount validation failed for skills-dir") {
		t.Fatalf("expected skills-dir validation error, got %v", err)
	}
}

func TestMountResolverGlobalLevelDoesNotMountSkillsMarketByDefault(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	resolver := NewContainerHubMountResolver(paths)

	mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "global", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if mount, ok := mountByDestination(mounts, "/skills"); ok {
		t.Fatalf("expected no default /skills mount in global level, got %#v", mount)
	}
	if mount, ok := mountByDestination(mounts, "/skills-market"); ok {
		t.Fatalf("expected no default /skills-market mount, got %#v", mount)
	}
}

func TestMountResolverExplicitSkillsMarketExtraMount(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	resolver := NewContainerHubMountResolver(paths)

	mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "run", []contracts.SandboxExtraMount{
		{Platform: "skills-market", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	mount, ok := mountByDestination(mounts, "/skills-market")
	if !ok {
		t.Fatalf("expected explicit /skills-market mount, got %#v", mounts)
	}
	if mount.Source != paths.SkillsMarketDir || !mount.ReadOnly {
		t.Fatalf("unexpected skills-market mount: %#v", mount)
	}
}

func TestMountResolverIgnoresNonAllowlistedPathEnv(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	envRoot := filepath.Join(t.TempDir(), "env-agents")
	if err := os.MkdirAll(filepath.Join(envRoot, "reader", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir env agent fixture: %v", err)
	}
	t.Setenv("AGENTS_DIR", envRoot)
	resolver := NewContainerHubMountResolver(paths)

	mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "run", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	agentMount, ok := mountByDestination(mounts, "/agent")
	if !ok {
		t.Fatalf("expected /agent mount, got %#v", mounts)
	}
	if want := filepath.Join(paths.RUAgentsDir, "reader"); agentMount.Source != want {
		t.Fatalf("agent source = %q, want %q", agentMount.Source, want)
	}
	skillsMount, ok := mountByDestination(mounts, "/skills")
	if !ok {
		t.Fatalf("expected /skills mount, got %#v", mounts)
	}
	if want := filepath.Join(paths.RUAgentsDir, "reader", "skills"); skillsMount.Source != want {
		t.Fatalf("skills source = %q, want %q", skillsMount.Source, want)
	}
}

func TestMountResolverUsesAPRuntimeHostPathEnv(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	paths.PanDir = filepath.Join(t.TempDir(), "configured-pan")
	paths.RegistriesDir = filepath.Join(t.TempDir(), "configured-registries")

	hostRoot := t.TempDir()
	hostChats := filepath.Join(hostRoot, "chats")
	hostMemory := filepath.Join(hostRoot, "memory")
	hostPan := filepath.Join(hostRoot, "pan")
	hostRegistries := filepath.Join(hostRoot, "registries")
	for _, dir := range []string{
		hostChats,
		hostMemory,
		hostPan,
		filepath.Join(hostRegistries, "providers"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir host dir %q: %v", dir, err)
		}
	}
	t.Setenv("AP_RUNTIME_CHATS_DIR", hostChats)
	t.Setenv("AP_RUNTIME_MEMORY_DIR", hostMemory)
	t.Setenv("AP_RUNTIME_PAN_DIR", hostPan)
	t.Setenv("AP_RUNTIME_REGISTRIES_DIR", hostRegistries)

	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "run", []contracts.SandboxExtraMount{
		{Platform: "providers", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	wantWorkspace, err := filepath.EvalSymlinks(mountResolverWorkspace(t, paths))
	if err != nil {
		t.Fatal(err)
	}
	wantChat, err := filepath.EvalSymlinks(filepath.Join(hostChats, "chat-1"))
	if err != nil {
		t.Fatal(err)
	}
	if mount, ok := mountByDestination(mounts, "/workspace"); !ok || mount.Source != wantWorkspace || mount.ReadOnly {
		t.Fatalf("workspace mount = %#v, ok=%v", mount, ok)
	}
	if mount, ok := mountByDestination(mounts, "/chat"); !ok || mount.Source != wantChat || mount.ReadOnly {
		t.Fatalf("chat mount = %#v, ok=%v", mount, ok)
	}
	if mount, ok := mountByDestination(mounts, "/memory"); !ok || mount.Source != filepath.Join(hostMemory, "reader") {
		t.Fatalf("memory mount = %#v, ok=%v", mount, ok)
	}
	if mount, ok := mountByDestination(mounts, "/pan"); !ok || mount.Source != hostPan {
		t.Fatalf("pan mount = %#v, ok=%v", mount, ok)
	}
	if mount, ok := mountByDestination(mounts, "/providers"); !ok || mount.Source != filepath.Join(hostRegistries, "providers") {
		t.Fatalf("providers mount = %#v, ok=%v", mount, ok)
	}
}

func TestMountResolverRejectsContainerAPRuntimeHostPath(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	t.Setenv("AP_RUNTIME_CHATS_DIR", "/opt/runtime/chats")
	resolver := NewContainerHubMountResolver(paths)

	_, err := resolver.Resolve(mountResolverWorkspace(t, paths), "chat-1", "reader", "run", nil)
	if err == nil {
		t.Fatal("expected Resolve() to reject container runtime path")
	}
	if !strings.Contains(err.Error(), "missing AP_RUNTIME_CHATS_DIR host path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mountResolverTestPaths(t *testing.T, agentKey string) config.PathsConfig {
	t.Helper()

	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:        filepath.Join(root, "chats"),
		AgentsDir:       filepath.Join(root, "agents"),
		RUAgentsDir:     filepath.Join(root, "ru-agents"),
		OwnerDir:        filepath.Join(root, "owner"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{
		paths.ChatsDir,
		filepath.Join(paths.RUAgentsDir, agentKey, "skills"),
		paths.OwnerDir,
		paths.MemoryDir,
		paths.SkillsMarketDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir test dir %q: %v", dir, err)
		}
	}
	return paths
}

func mountResolverWorkspace(t *testing.T, paths config.PathsConfig) string {
	t.Helper()
	workspace := filepath.Join(filepath.Dir(paths.ChatsDir), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir test workspace %q: %v", workspace, err)
	}
	return workspace
}

func mountByDestination(mounts []MountSpec, destination string) (MountSpec, bool) {
	for _, mount := range mounts {
		if mount.Destination == destination {
			return mount, true
		}
	}
	return MountSpec{}, false
}

func realMountPath(t *testing.T, rawPath string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
