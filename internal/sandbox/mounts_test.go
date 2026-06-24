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

			mounts, err := resolver.Resolve("chat-1", "reader", level, nil)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			mount, ok := mountByDestination(mounts, "/skills")
			if !ok {
				t.Fatalf("expected /skills mount, got %#v", mounts)
			}
			want := filepath.Join(paths.AgentsDir, "reader", "skills")
			if mount.Source != want {
				t.Fatalf("skills source = %q, want %q", mount.Source, want)
			}
			if mount.Source == paths.SkillsMarketDir {
				t.Fatalf("expected agent-local skills, got skills market source %q", mount.Source)
			}
		})
	}
}

func TestMountResolverDoesNotFallbackToSkillsMarketWhenAgentSkillsUnavailable(t *testing.T) {
	paths := mountResolverTestPaths(t, "reader")
	if err := os.WriteFile(filepath.Join(paths.AgentsDir, "reader", "skills"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write skills file fixture: %v", err)
	}
	resolver := NewContainerHubMountResolver(paths)

	mounts, err := resolver.Resolve("chat-1", "reader", "run", nil)
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

	mounts, err := resolver.Resolve("chat-1", "reader", "global", nil)
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

	mounts, err := resolver.Resolve("chat-1", "reader", "run", []contracts.SandboxExtraMount{
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

	mounts, err := resolver.Resolve("chat-1", "reader", "run", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	agentMount, ok := mountByDestination(mounts, "/agent")
	if !ok {
		t.Fatalf("expected /agent mount, got %#v", mounts)
	}
	if want := filepath.Join(paths.AgentsDir, "reader"); agentMount.Source != want {
		t.Fatalf("agent source = %q, want %q", agentMount.Source, want)
	}
	skillsMount, ok := mountByDestination(mounts, "/skills")
	if !ok {
		t.Fatalf("expected /skills mount, got %#v", mounts)
	}
	if want := filepath.Join(paths.AgentsDir, "reader", "skills"); skillsMount.Source != want {
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
	mounts, err := resolver.Resolve("chat-1", "reader", "run", []contracts.SandboxExtraMount{
		{Platform: "providers", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if mount, ok := mountByDestination(mounts, "/workspace"); !ok || mount.Source != filepath.Join(hostChats, "chat-1") {
		t.Fatalf("workspace mount = %#v, ok=%v", mount, ok)
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

	_, err := resolver.Resolve("chat-1", "reader", "run", nil)
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
		OwnerDir:        filepath.Join(root, "owner"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{
		paths.ChatsDir,
		filepath.Join(paths.AgentsDir, agentKey),
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

func mountByDestination(mounts []MountSpec, destination string) (MountSpec, bool) {
	for _, mount := range mounts {
		if mount.Destination == destination {
			return mount, true
		}
	}
	return MountSpec{}, false
}
