package reload

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

type recordingCatalogReloader struct {
	reasons chan string
}

func (r recordingCatalogReloader) Reload(_ context.Context, reason string) error {
	r.reasons <- reason
	return nil
}

func TestStartBackgroundReloadersIgnoresDSStoreChanges(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}

	reasons := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartBackgroundReloaders(ctx, config.Config{
		Paths: config.PathsConfig{
			AgentsDir: agentsDir,
		},
	}, recordingCatalogReloader{reasons: reasons})

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(agentsDir, ".DS_Store"), []byte("finder"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}
	assertNoReloadReason(t, reasons, reloadDebounce+300*time.Millisecond)

	if err := os.WriteFile(filepath.Join(agentsDir, "demo.yml"), []byte("key: demo\n"), 0o644); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}
	assertReloadReason(t, reasons, "agents", 2*time.Second)
}

func TestBackgroundWatchEntriesExcludeConfigs(t *testing.T) {
	cfg := config.Config{
		Paths: config.PathsConfig{
			AgentsDir:       filepath.Join("runtime", "agents"),
			TeamsDir:        filepath.Join("runtime", "teams"),
			SkillsMarketDir: filepath.Join("runtime", "skills-market"),
			RegistriesDir:   filepath.Join("runtime", "registries"),
			ToolsDir:        filepath.Join("runtime", "tools"),
		},
	}

	entries := backgroundWatchEntries(cfg)
	gotReasons := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotReasons = append(gotReasons, entry.reason)
		clean := filepath.ToSlash(filepath.Clean(entry.path))
		if strings.HasPrefix(clean, "configs/") || clean == "configs" || strings.Contains(clean, "/configs/") {
			t.Fatalf("background watcher must not include configs path: %#v", entry)
		}
	}

	wantReasons := []string{
		"agents",
		"teams",
		"skills",
		"models",
		"providers",
		"tools",
		"viewports",
		"mcp-servers",
		"viewport-servers",
	}
	if !reflect.DeepEqual(gotReasons, wantReasons) {
		t.Fatalf("watch reasons = %#v, want %#v", gotReasons, wantReasons)
	}
}

func TestMergePendingReloadReasonEscalatesMixedChangesToConfig(t *testing.T) {
	tests := []struct {
		name    string
		pending string
		next    string
		want    string
	}{
		{name: "empty pending", pending: "", next: "providers", want: "providers"},
		{name: "same reason", pending: "models", next: "models", want: "models"},
		{name: "models and providers", pending: "models", next: "providers", want: "config"},
		{name: "providers and agents", pending: "providers", next: "agents", want: "config"},
		{name: "keep config", pending: "config", next: "models", want: "config"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergePendingReloadReason(tc.pending, tc.next); got != tc.want {
				t.Fatalf("mergePendingReloadReason(%q, %q) = %q, want %q", tc.pending, tc.next, got, tc.want)
			}
		})
	}
}

func TestRuntimeCatalogReloaderCascadesSkillsToAgents(t *testing.T) {
	registry := &recordingRuntimeRegistry{}
	reloader := NewRuntimeCatalogReloader(registry, nil, nil, nil, "", nil)

	if err := reloader.Reload(context.Background(), "skills"); err != nil {
		t.Fatalf("reload skills: %v", err)
	}

	want := []string{"skills", "agents"}
	if !reflect.DeepEqual(registry.reasons, want) {
		t.Fatalf("reload reasons = %#v, want %#v", registry.reasons, want)
	}
}

type recordingRuntimeRegistry struct {
	reasons []string
}

func (r *recordingRuntimeRegistry) Agents(string) []api.AgentSummary       { return nil }
func (r *recordingRuntimeRegistry) Teams() []api.TeamSummary               { return nil }
func (r *recordingRuntimeRegistry) Skills(string) []api.SkillSummary       { return nil }
func (r *recordingRuntimeRegistry) Tools(string, string) []api.ToolSummary { return nil }
func (r *recordingRuntimeRegistry) Tool(string) (api.ToolDetailResponse, bool) {
	return api.ToolDetailResponse{}, false
}
func (r *recordingRuntimeRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, false
}
func (r *recordingRuntimeRegistry) DefaultAgentKey() string { return "" }
func (r *recordingRuntimeRegistry) AgentDefinition(string) (catalog.AgentDefinition, bool) {
	return catalog.AgentDefinition{}, false
}
func (r *recordingRuntimeRegistry) TeamDefinition(string) (catalog.TeamDefinition, bool) {
	return catalog.TeamDefinition{}, false
}
func (r *recordingRuntimeRegistry) Reload(_ context.Context, reason string) error {
	r.reasons = append(r.reasons, reason)
	return nil
}

func assertNoReloadReason(t *testing.T, reasons <-chan string, timeout time.Duration) {
	t.Helper()
	select {
	case reason := <-reasons:
		t.Fatalf("unexpected reload reason %q", reason)
	case <-time.After(timeout):
	}
}

func assertReloadReason(t *testing.T, reasons <-chan string, want string, timeout time.Duration) {
	t.Helper()
	select {
	case reason := <-reasons:
		if reason != want {
			t.Fatalf("reload reason = %q, want %q", reason, want)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for reload reason %q", want)
	}
}
