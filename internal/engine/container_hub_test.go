package engine

import (
	"context"
	"encoding/json"
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
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", nil)
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
	_, err := resolver.Resolve("chat-1", "", "run", nil)
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
	_, err := resolver.Resolve("chat-1", "missing-agent", "run", nil)
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
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "global", nil)
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/skills", paths.SkillsMarketDir, true)
}

func TestContainerHubMountResolverResolvePlatformOwnerOverrideMakesMountWritable(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "owner", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/owner", paths.OwnerDir, false)
}

func TestContainerHubMountResolverResolvePlatformMemoryOverrideMakesMountWritable(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "memory", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/memory", filepath.Join(paths.MemoryDir, "demo-agent"), false)
}

func TestContainerHubMountResolverResolvePlatformAgentOverrideMakesMountWritable(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "agent", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/agent", filepath.Join(paths.AgentsDir, "demo-agent"), false)
}

func TestContainerHubMountResolverResolvePlatformAgentOverrideKeepsMountReadOnly(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "agent", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/agent", filepath.Join(paths.AgentsDir, "demo-agent"), true)
}

func TestContainerHubMountResolverResolveDestinationOverrideMakesAgentMountWritable(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Destination: "/agent", Mode: "rw"},
		{Destination: "/owner", Mode: "rw"},
		{Destination: "/memory", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/agent", filepath.Join(paths.AgentsDir, "demo-agent"), false)
	assertMount(t, mounts, "/owner", paths.OwnerDir, false)
	assertMount(t, mounts, "/memory", filepath.Join(paths.MemoryDir, "demo-agent"), false)
}

func TestContainerHubMountResolverResolvePlatformToolsAddsMount(t *testing.T) {
	paths := setupMountResolverPaths(t)
	if err := os.MkdirAll(paths.ToolsDir, 0o755); err != nil {
		t.Fatalf("mkdir tools dir: %v", err)
	}
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "tools", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/tools", paths.ToolsDir, true)
}

func TestContainerHubMountResolverResolveCustomMountAddsMount(t *testing.T) {
	paths := setupMountResolverPaths(t)
	customDir := filepath.Join(filepath.Dir(paths.OwnerDir), "datasets")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir custom dir: %v", err)
	}
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Source: customDir, Destination: "/datasets", Mode: "ro"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	assertMount(t, mounts, "/datasets", customDir, true)
}

func TestContainerHubMountResolverResolveRejectsCustomMountOnDefaultDestination(t *testing.T) {
	paths := setupMountResolverPaths(t)
	customDir := filepath.Join(filepath.Dir(paths.OwnerDir), "datasets")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("mkdir custom dir: %v", err)
	}
	resolver := NewContainerHubMountResolver(paths)
	_, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Source: customDir, Destination: "/owner", Mode: "rw"},
	})
	if err == nil || !strings.Contains(err.Error(), "must omit source/platform and only declare destination + mode") {
		t.Fatalf("expected default override shape error, got %v", err)
	}
}

func TestContainerHubMountResolverResolveRejectsMissingMode(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	_, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "owner"},
	})
	if err == nil || !strings.Contains(err.Error(), "mode is required") {
		t.Fatalf("expected mode required error, got %v", err)
	}
}

func TestContainerHubMountResolverResolveSkipsUnknownPlatform(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "unknown-platform", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	if len(mounts) != 7 {
		t.Fatalf("expected unknown platform to be ignored, got %#v", mounts)
	}
}

func TestContainerHubMountResolverResolvePlatformAgentDoesNotDuplicateMount(t *testing.T) {
	paths := setupMountResolverPaths(t)
	resolver := NewContainerHubMountResolver(paths)
	mounts, err := resolver.Resolve("chat-1", "demo-agent", "run", []SandboxExtraMount{
		{Platform: "agent", Mode: "rw"},
	})
	if err != nil {
		t.Fatalf("resolve mounts: %v", err)
	}
	agentMounts := 0
	for _, mount := range mounts {
		if mount.Destination == "/agent" {
			agentMounts++
		}
	}
	if agentMounts != 1 {
		t.Fatalf("expected exactly one /agent mount, got %#v", mounts)
	}
}

func TestContainerHubSandboxServiceCreateSessionPayloadIncludesOwnerOverride(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/create" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode create session payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"sess-1","cwd":"/workspace/chat-1"}`))
	}))
	defer server.Close()

	paths := setupMountResolverPaths(t)
	service := NewContainerHubSandboxService(config.ContainerHubConfig{
		Enabled:              true,
		BaseURL:              server.URL,
		DefaultEnvironmentID: "shell",
		RequestTimeoutMs:     1000,
	}, paths)
	execCtx := &ExecutionContext{
		Session: QuerySession{
			RunID:                "run-1",
			ChatID:               "chat-1",
			AgentKey:             "demo-agent",
			SandboxEnvironmentID: "shell",
			SandboxLevel:         "run",
			SandboxExtraMounts: []SandboxExtraMount{
				{Platform: "owner", Mode: "rw"},
			},
		},
	}

	if err := service.OpenIfNeeded(context.Background(), execCtx); err != nil {
		t.Fatalf("open sandbox: %v", err)
	}
	mounts, ok := captured["mounts"].([]any)
	if !ok {
		t.Fatalf("expected mounts array in payload, got %#v", captured)
	}
	ownerMount := findPayloadMount(t, mounts, "/owner")
	if ownerMount["read_only"] != false {
		t.Fatalf("expected owner mount to be writable, got %#v", ownerMount)
	}
}

func setupMountResolverPaths(t *testing.T) config.PathsConfig {
	t.Helper()
	root := t.TempDir()
	paths := config.PathsConfig{
		RegistriesDir:   filepath.Join(root, "registries"),
		ToolsDir:        filepath.Join(root, "registries", "tools"),
		ChatsDir:        filepath.Join(root, "chats"),
		RootDir:         filepath.Join(root, "root"),
		PanDir:          filepath.Join(root, "pan"),
		OwnerDir:        filepath.Join(root, "owner"),
		AgentsDir:       filepath.Join(root, "agents"),
		TeamsDir:        filepath.Join(root, "teams"),
		SchedulesDir:    filepath.Join(root, "schedules"),
		MemoryDir:       filepath.Join(root, "memory"),
		SkillsMarketDir: filepath.Join(root, "skills-market"),
	}
	for _, dir := range []string{
		paths.RegistriesDir,
		paths.RootDir,
		paths.PanDir,
		paths.SkillsMarketDir,
		paths.TeamsDir,
		paths.SchedulesDir,
		filepath.Join(paths.AgentsDir, "demo-agent"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return paths
}

func findPayloadMount(t *testing.T, mounts []any, destination string) map[string]any {
	t.Helper()
	for _, raw := range mounts {
		mount, ok := raw.(map[string]any)
		if ok && mount["destination"] == destination {
			return mount
		}
	}
	t.Fatalf("missing payload mount for %s in %#v", destination, mounts)
	return nil
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
