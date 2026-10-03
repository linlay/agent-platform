package platformcontrol

import (
	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func controlExecution() *contracts.ExecutionContext {
	s := contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", Mode: "GENERAL", RunOwner: contracts.AgentRunOwner("caller", ""), NativeConnectorTools: map[string]string{}, ConnectorDirs: map[string]string{connector.PlatformControlConnectorID: "/mounted"}}
	for _, a := range connector.ControlActions() {
		s.NativeConnectorTools[a.Tool] = connector.PlatformControlConnectorID
		s.ToolNames = append(s.ToolNames, a.Tool)
	}
	return &contracts.ExecutionContext{Session: s, CurrentToolID: "call", AccessLevel: "full_access"}
}
func TestControlAdmissionAndExactApproval(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents"), TeamsDir: filepath.Join(t.TempDir(), "teams"), SkillsCenterDir: filepath.Join(t.TempDir(), "skills")}}
	registry, e := catalog.NewFileRegistry(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	source := &adminsource.ControlService{Config: cfg, Registry: registry, Mutations: adminsource.NewService()}
	h := NewToolHandler(cfg, registry, nil).ConfigureControl(source, nil, nil)
	execution := controlExecution()
	args := map[string]any{"action": "apply", "args": map[string]any{"resourceType": "skill", "resourceKey": "demo", "content": "---\nname: demo\ndescription: Demo skill\n---\nUse carefully.\n"}}
	result, e := h.Invoke(context.Background(), "catalog_manage", args, execution)
	if e != nil || result.Error != "approval_required" {
		t.Fatalf("full_access bypassed approval: %v %v", result, e)
	}
	plan, e := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution)
	if e != nil {
		t.Fatal(e)
	}
	execution.ToolApprovals = map[string]bool{plan.Fingerprint: true}
	execution.CurrentToolID = "sibling"
	result, _ = h.Invoke(context.Background(), "catalog_manage", args, execution)
	if result.Error != "approval_required" {
		t.Fatal("sibling reused grant")
	}
	execution.CurrentToolID = "call"
	result, e = h.Invoke(context.Background(), "catalog_manage", args, execution)
	if e != nil || result.Error != "" || result.Structured["status"] != "applied" {
		t.Fatalf("approved apply %v %v", result, e)
	}
	if execution.ToolApprovals[plan.Fingerprint] {
		t.Fatal("grant not consumed")
	}
	for _, alter := range []func(*contracts.ExecutionContext){func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" }, func(e *contracts.ExecutionContext) { e.Session.TeamID = "team" }, func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil }, func(e *contracts.ExecutionContext) { e.Session.Mode = "ACP" }} {
		exec := controlExecution()
		alter(exec)
		result, _ := h.Invoke(context.Background(), "catalog_query", map[string]any{"action": "defaults", "args": map[string]any{"type": "general"}}, exec)
		if result.Error == "" {
			t.Fatal("invalid caller accepted")
		}
	}
}

type packageListRegistry struct{ catalog.Registry }

func (packageListRegistry) SkillDefinition(string) (catalog.SkillDefinition, bool) {
	return catalog.SkillDefinition{}, true
}
func TestCatalogListUsesPackageManifestMembers(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "bundle")
	for _, name := range []string{"real", "assets", "references"} {
		if err := os.MkdirAll(filepath.Join(pkg, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"bundle","skills":[{"id":"real"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewToolHandler(config.Config{Paths: config.PathsConfig{SkillsCenterDir: root}}, packageListRegistry{}, nil)
	value, err := h.catalogQuery(context.Background(), "list", map[string]any{"resourceType": "skill"})
	if err != nil {
		t.Fatal(err)
	}
	items := value.(map[string]any)["items"].([]map[string]any)
	if len(items) != 1 || items[0]["resourceKey"] != "bundle/real" {
		t.Fatalf("nonmember directories leaked into skill catalog: %#v", items)
	}
}

func TestControlBatchArchiveAdmission(t *testing.T) {
	h := NewToolHandler(config.Config{}, nil, nil)
	for _, action := range []string{"archive", "restore"} {
		for _, params := range []map[string]any{{"chatId": "one"}, {"chatIds": []any{"one", "two"}}} {
			if _, _, err := h.admitted("chat_manage", map[string]any{"action": action, "args": params}, controlExecution()); err != nil {
				t.Fatal(err)
			}
		}
		for _, params := range []map[string]any{{}, {"chatIds": []any{}}, {"chatId": "one", "chatIds": []any{"one"}}, {"chatIds": []any{"one", "one"}}, {"chatIds": []any{"one", 7}}} {
			if _, _, err := h.admitted("chat_manage", map[string]any{"action": action, "args": params}, controlExecution()); err == nil {
				t.Fatalf("accepted %v", params)
			}
		}
	}
	if _, _, err := h.admitted("chat_manage", map[string]any{"action": "delete", "args": map[string]any{"chatIds": []any{"one"}}}, controlExecution()); err == nil {
		t.Fatal("batch delete accidentally enabled")
	}
}
