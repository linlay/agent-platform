package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
)

func TestContainerHubClientGetEnvironmentAgentPromptParsesSnakeCaseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/environments/toolbox/agent-prompt" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"environment_name":"toolbox","has_prompt":true,"prompt":"prompt-body","updated_at":"2026-04-02T14:20:53Z"}`))
	}))
	defer server.Close()

	client := NewContainerHubClient(config.ContainerHubConfig{
		BaseURL:          server.URL,
		RequestTimeoutMs: 1000,
	})
	result, err := client.GetEnvironmentAgentPrompt("toolbox")
	if err != nil {
		t.Fatalf("get environment agent prompt: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok result, got %#v", result)
	}
	if result.EnvironmentName != "toolbox" || !result.HasPrompt || result.Prompt != "prompt-body" || result.UpdatedAt != "2026-04-02T14:20:53Z" {
		t.Fatalf("unexpected parsed result: %#v", result)
	}
}

func TestContainerHubMountResolverResolveAddsDefaultOwnerAndMemoryMounts(t *testing.T) {
	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:        filepath.Join(root, "chats"),
		RootDir:         filepath.Join(root, "root"),
		PanDir:          filepath.Join(root, "pan"),
		OwnerDir:        filepath.Join(root, "owner"),
		AgentsDir:       filepath.Join(root, "agents"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{paths.RootDir, paths.PanDir, paths.SkillsMarketDir, filepath.Join(paths.AgentsDir, "demo-agent")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run")
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	if len(mounts) != 7 {
		t.Fatalf("expected 7 default mounts, got %#v", mounts)
	}
	assertMount(t, mounts, "/workspace", filepath.Join(paths.ChatsDir, "chat-1"), false)
	assertMount(t, mounts, "/root", paths.RootDir, false)
	assertMount(t, mounts, "/pan", paths.PanDir, false)
	assertMount(t, mounts, "/skills", filepath.Join(paths.AgentsDir, "demo-agent", "skills"), true)
	assertMount(t, mounts, "/agent", filepath.Join(paths.AgentsDir, "demo-agent"), true)
	assertMount(t, mounts, "/owner", paths.OwnerDir, true)
	assertMount(t, mounts, "/memory", filepath.Join(paths.MemoryDir, "demo-agent"), true)

	for _, dir := range []string{
		filepath.Join(paths.ChatsDir, "chat-1"),
		filepath.Join(paths.AgentsDir, "demo-agent", "skills"),
		paths.OwnerDir,
		filepath.Join(paths.MemoryDir, "demo-agent"),
	} {
		if stat, err := os.Stat(dir); err != nil || !stat.IsDir() {
			t.Fatalf("expected directory %s to be created, stat=%v err=%v", dir, stat, err)
		}
	}
}

func TestContainerHubMountResolverResolveRequiresAgentKey(t *testing.T) {
	resolver := NewContainerHubMountResolver(config.PathsConfig{})
	_, err := resolver.Resolve("chat-1", "", "run")
	if err == nil || !strings.Contains(err.Error(), "agentKey is required") {
		t.Fatalf("expected agentKey required error, got %v", err)
	}
}

func TestContainerHubMountResolverResolveFailsWhenAgentDirectoryMissing(t *testing.T) {
	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:        filepath.Join(root, "chats"),
		RootDir:         filepath.Join(root, "root"),
		PanDir:          filepath.Join(root, "pan"),
		OwnerDir:        filepath.Join(root, "owner"),
		AgentsDir:       filepath.Join(root, "agents"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{paths.RootDir, paths.PanDir, paths.SkillsMarketDir, paths.AgentsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	resolver := NewContainerHubMountResolver(paths)
	_, err := resolver.Resolve("chat-1", "missing-agent", "run")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(paths.AgentsDir, "missing-agent")) {
		t.Fatalf("expected missing agent directory error, got %v", err)
	}
}

func TestContainerHubMountResolverResolveUsesSkillsMarketForGlobalLevel(t *testing.T) {
	root := t.TempDir()
	paths := config.PathsConfig{
		ChatsDir:        filepath.Join(root, "chats"),
		RootDir:         filepath.Join(root, "root"),
		PanDir:          filepath.Join(root, "pan"),
		OwnerDir:        filepath.Join(root, "owner"),
		AgentsDir:       filepath.Join(root, "agents"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{paths.RootDir, paths.PanDir, paths.SkillsMarketDir, filepath.Join(paths.AgentsDir, "demo-agent")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "global")
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/skills", paths.SkillsMarketDir, true)
}

func assertMount(t *testing.T, mounts []MountSpec, destination string, source string, readOnly bool) {
	t.Helper()
	for _, mount := range mounts {
		if mount.Destination != destination {
			continue
		}
		if mount.Source != source || mount.ReadOnly != readOnly {
			t.Fatalf("unexpected mount for %s: %#v", destination, mount)
		}
		return
	}
	t.Fatalf("missing mount for %s in %#v", destination, mounts)
}
