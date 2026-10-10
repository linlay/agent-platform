package platformcontrol

import (
	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtimeskills"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func controlExecution() *contracts.ExecutionContext {
	s := contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "caller", Mode: "GENERAL", RunOwner: contracts.AgentRunOwner("caller"), NativeConnectorTools: map[string]string{}, ConnectorDirs: map[string]string{connector.PlatformControlConnectorID: "/mounted", connector.TaskControlConnectorID: "/task"}}
	for _, a := range connector.ControlActions() {
		s.NativeConnectorTools[a.Tool], _ = connector.NativeToolConnector(a.Tool)
		s.ToolNames = append(s.ToolNames, a.Tool)
	}
	return &contracts.ExecutionContext{Session: s, CurrentToolID: "call", AccessLevel: "full_access"}
}
func TestControlAdmissionAndExactApproval(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents"), SkillsCenterDir: filepath.Join(t.TempDir(), "skills")}}
	t.Cleanup(func() {
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUAgentsDir())
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUSkillsDir())
	})
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
	if !plan.AllowAutoApprove || plan.View != nil || plan.Form["resourceType"] != "skill" || plan.Form["permissionFields"] != nil {
		t.Fatalf("unexpected skill form: %#v", plan)
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
	for _, alter := range []func(*contracts.ExecutionContext){func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" }, func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil }, func(e *contracts.ExecutionContext) { e.Session.Mode = "ACP" }} {
		exec := controlExecution()
		alter(exec)
		result, _ := h.Invoke(context.Background(), "catalog_query", map[string]any{"action": "defaults", "args": map[string]any{"type": "general"}}, exec)
		if result.Error == "" {
			t.Fatal("invalid caller accepted")
		}
	}
}

func TestControlProjectValidationAndPublication(t *testing.T) {
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents")}}
	t.Cleanup(func() {
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUAgentsDir())
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUSkillsDir())
	})
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := &adminsource.ControlService{Config: cfg, Registry: registry, Mutations: adminsource.NewService()}
	h := NewToolHandler(cfg, registry, nil).ConfigureControl(source, nil, nil)
	execution := controlExecution()
	params := map[string]any{
		"resourceType": "agent", "resourceKey": "project-agent", "isProject": true,
		"content": "key: project-agent\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: '@root'\n",
	}
	for _, action := range []string{"validate", "apply"} {
		tool := "catalog_query"
		if action == "apply" {
			tool = "catalog_manage"
		}
		args := map[string]any{"action": action, "args": params}
		if _, _, err := h.admitted(tool, args, execution); err != nil {
			t.Fatalf("project intent rejected at admission: %v", err)
		}
		for _, invalid := range []any{"TRUE", " false", 1, nil} {
			params["isProject"] = invalid
			if _, _, err := h.admitted(tool, args, execution); err == nil {
				t.Fatalf("nonboolean project intent accepted: %#v", invalid)
			}
		}
		params["isProject"] = true
	}
	validation, err := h.catalogQuery(context.Background(), "validate", params)
	if err != nil || validation.(map[string]any)["valid"] != false {
		t.Fatalf("invalid project validated: %#v, %v", validation, err)
	}
	args := map[string]any{"action": "apply", "args": params}
	if _, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution); err == nil {
		t.Fatal("invalid project reached approval")
	}
	params["content"] = fmt.Sprintf("key: project-agent\nmode: GENERAL\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: %q\n", t.TempDir())
	validation, err = h.catalogQuery(context.Background(), "validate", params)
	if err != nil || validation.(map[string]any)["valid"] != true {
		t.Fatalf("valid project rejected: %#v, %v", validation, err)
	}
	approval, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution)
	if err != nil || approval.Form["isProject"] != true {
		t.Fatalf("project review: %#v, %v", approval, err)
	}
	result, err := h.Invoke(context.Background(), "catalog_manage", args, execution)
	if err != nil || result.Error != "approval_required" {
		t.Fatalf("project publication bypassed approval: %#v, %v", result, err)
	}
	execution.ToolApprovals = map[string]bool{approval.Fingerprint: true}
	result, err = h.Invoke(context.Background(), "catalog_manage", args, execution)
	if err != nil || result.Error != "" || result.Structured["status"] != "applied" {
		t.Fatalf("approved project publication: %#v, %v", result, err)
	}
	definition, found := registry.AgentDefinition("project-agent")
	if !found || definition.Workspace.ProjectDir() == "" {
		t.Fatal("published project has no public workspace identity")
	}
	content, err := os.ReadFile(filepath.Join(cfg.Paths.AgentsDir, "project-agent", "agent.yml"))
	if err != nil || strings.Contains(string(content), "isProject") {
		t.Fatalf("project intent persisted: %s, %v", content, err)
	}
}

func TestControlRollbackReturnsFailure(t *testing.T) {
	for _, restored := range []bool{true, false} {
		name := "rollback_reload_failed"
		if restored {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents")}}
			t.Cleanup(func() {
				_ = runtimeskills.Remove(cfg.Paths.EffectiveRUAgentsDir())
				_ = runtimeskills.Remove(cfg.Paths.EffectiveRUSkillsDir())
			})
			registry, err := catalog.NewFileRegistry(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.Paths.AgentsDir, "other", "SOUL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}
			reloads := 0
			source := &adminsource.ControlService{Config: cfg, Registry: registry, Mutations: adminsource.NewService(), Reload: func(context.Context, string) error {
				reloads++
				if restored && reloads == 2 {
					return nil
				}
				return errors.New("test reload failure")
			}}
			h := NewToolHandler(cfg, registry, nil).ConfigureControl(source, nil, nil)
			view, err := source.Read(adminsource.ControlTarget{ResourceType: "agent", ResourceKey: "other", Path: "SOUL.md"})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"action": "apply", "args": map[string]any{"resourceType": "agent", "resourceKey": "other", "path": "SOUL.md", "content": "replacement", "baseRevision": view.BaseRevision}}
			execution := controlExecution()
			plan, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution)
			if err != nil {
				t.Fatal(err)
			}
			execution.ToolApprovals = map[string]bool{plan.Fingerprint: true}
			result, err := h.Invoke(context.Background(), "catalog_manage", args, execution)
			if err != nil || result.ExitCode == 0 || result.Error == "" {
				t.Fatalf("failed mutation reported success: %+v %v", result, err)
			}
			state, code, strategy := "unknown", "control_failed", "inspect_state"
			if restored {
				state, code, strategy = "rolled_back", "control_rolled_back", "fix_input"
				if result.Structured["status"] != "rolled_back" {
					t.Fatalf("missing rollback status: %+v", result)
				}
			}
			if result.Structured["executionState"] != state || result.Error != code || result.Structured["recovery"].(map[string]any)["strategy"] != strategy {
				t.Fatalf("incorrect failure semantics: %+v", result)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "original" || reloads != 2 {
				t.Fatalf("source not restored: %q, reloads=%d, err=%v", b, reloads, err)
			}
			if execution.ToolApprovals[plan.Fingerprint] {
				t.Fatal("failed mutation retained approval")
			}
			result, err = h.Invoke(context.Background(), "catalog_manage", args, execution)
			if err != nil || result.Error != "approval_required" {
				t.Fatalf("retry bypassed fresh approval: %+v %v", result, err)
			}
		})
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
