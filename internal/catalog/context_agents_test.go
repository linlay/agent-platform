package catalog

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestResolveContextAgentKeys(t *testing.T) {
	for _, tc := range []struct {
		name             string
		tags, refs, want []string
		warning          bool
	}{
		{"missing", []string{"agents"}, []string{"missing"}, nil, true},
		{"mixed", []string{"agents"}, []string{"second", "missing", "first", "parent", "second"}, []string{"second", "first"}, true},
		{"valid", []string{"agents"}, []string{"second", "first"}, []string{"second", "first"}, false},
		{"all", []string{"agents"}, nil, []string{"first", "second"}, false},
		{"self", []string{"agents"}, []string{"parent"}, nil, false},
		{"disabled", []string{"system"}, []string{"missing"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, diagnostic := ResolveContextAgentKeys(AgentDefinition{Key: "parent", ContextTags: tc.tags, ContextAgents: tc.refs}, "", []string{"parent", "first", "second"})
			if !reflect.DeepEqual(got, tc.want) || (diagnostic != nil) != tc.warning {
				t.Fatalf("selection=%v diagnostic=%#v", got, diagnostic)
			}
			if diagnostic != nil && (diagnostic.Severity != "warning" || diagnostic.Code != "context_agents_unavailable") {
				t.Fatalf("unexpected diagnostic: %#v", diagnostic)
			}
		})
	}
}

func TestContextAgentDiagnosticIsBoundedAndEscapesControls(t *testing.T) {
	refs := []string{"missing\nforged-log"}
	for i := 0; i < 100; i++ {
		refs = append(refs, fmt.Sprintf("%d-%s", i, strings.Repeat("x", 1000)))
	}
	_, diagnostic := ResolveContextAgentKeys(AgentDefinition{ContextTags: []string{"agents"}, ContextAgents: refs}, "parent", nil)
	if diagnostic == nil || !strings.Contains(diagnostic.Message, "skipped 101") || !strings.Contains(diagnostic.Message, `missing\nforged-log`) || strings.Contains(diagnostic.Message, "\n") || len(diagnostic.Message) > 1600 {
		t.Fatalf("unbounded or unsafe diagnostic: %#v", diagnostic)
	}
}

func TestContextAgentAdminWarningTracksCatalogWithoutInvalidatingParent(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(root, "agents"), RUAgentsDir: filepath.Join(root, "ru-agents"), SkillsCenterDir: filepath.Join(root, "skills-center"), TeamsDir: filepath.Join(root, "teams")}}
	parentPath := filepath.Join(cfg.Paths.AgentsDir, "parent", "agent.yml")
	targetPath := filepath.Join(cfg.Paths.AgentsDir, "target", "agent.yml")
	writeRuntimeAssemblerFile(t, parentPath, "key: parent\nmodelConfig: {modelKey: test}\ncontextConfig:\n  tags:\n    - agents\n  agents:\n    - target\n")
	r, err := NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := func(warning bool) {
		t.Helper()
		item, ok := r.AdminAgent("parent")
		wantDiagnostics := 0
		if warning {
			wantDiagnostics = 1
		}
		if !ok || item.Status != AdminAgentStatusReady || len(item.Diagnostics) != wantDiagnostics {
			t.Fatalf("parent=%#v", item)
		}
		if warning && (item.Diagnostics[0].Severity != "warning" || item.Diagnostics[0].SourcePath != parentPath) {
			t.Fatalf("diagnostic=%#v", item.Diagnostics)
		}
		if _, ok := r.AgentDefinition("parent"); !ok {
			t.Fatal("parent was invalidated")
		}
		for _, listed := range r.AdminAgents() {
			if listed.Key == "parent" && !reflect.DeepEqual(listed.Diagnostics, item.Diagnostics) {
				t.Fatal("list/detail diagnostics differ")
			}
		}
	}
	check(true) // Missing source.
	writeRuntimeAssemblerFile(t, targetPath, "key: target\nmodelConfig: {modelKey: test}\nconnectorConfig:\n  connectors:\n    - missing.connector\n")
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	check(true) // Invalid source remains excluded from effective catalog.
	if _, ok := r.AgentDefinition("target"); ok {
		t.Fatal("invalid target became executable")
	}
	target, _ := r.AdminAgent("target")
	if target.Status != AdminAgentStatusInvalid || target.Diagnostics[0].Code != "invalid_connector" {
		t.Fatalf("target=%#v", target)
	}
	writeRuntimeAssemblerFile(t, targetPath, "key: target\nmodelConfig: {modelKey: test}\n")
	if err := r.Reload(context.Background(), "agents"); err != nil {
		t.Fatal(err)
	}
	check(false) // Repair clears warning, without persisting derived state.
	r.InvalidateRuntimeAgent("target", "invalid_runtime_storage", fmt.Errorf("unavailable storage"))
	check(true)
}
