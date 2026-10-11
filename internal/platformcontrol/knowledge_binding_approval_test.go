package platformcontrol

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/kbases"
	"agent-platform/internal/runtimeskills"
)

func knowledgeCatalogHandler(t *testing.T) (*ToolHandler, *kbases.Service) {
	t.Helper()
	cfg := config.Config{Paths: config.PathsConfig{AgentsDir: filepath.Join(t.TempDir(), "agents"), SkillsCenterDir: filepath.Join(t.TempDir(), "skills")}}
	t.Cleanup(func() {
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUAgentsDir())
		_ = runtimeskills.Remove(cfg.Paths.EffectiveRUSkillsDir())
	})
	registry, err := catalog.NewFileRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	libraries, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sources := &adminsource.ControlService{Config: cfg, Registry: registry, Mutations: adminsource.NewService(), ValidateLibrary: libraries.ValidateBinding, LibraryDefinition: libraries.BindingDefinition}
	return NewToolHandler(cfg, registry, nil).ConfigureControl(sources, nil, nil), libraries
}

func bindingCandidate(mode, id, name, workspace string) string {
	binding := ""
	if id != "" {
		binding = "kbaseConfig:\n  libraryId: " + id + "\n"
	}
	return fmt.Sprintf("key: candidate\nname: %s\nmode: %s\nmodelConfig:\n  modelKey: test\nruntimeConfig:\n  workspaceRoot: %q\n%s", name, mode, workspace, binding)
}

func TestCatalogEditableLibraryBindingRequiresHumanApproval(t *testing.T) {
	for _, level := range []string{"default", "auto_approve", "full_access"} {
		for _, tc := range []struct {
			name, beforeMode, afterMode string
			before, after               string
			human                       bool
		}{
			{"new-readonly", "", "KBASE", "", "readonly", false},
			{"new-editable", "", "KBASE", "", "editable", true},
			{"general-editable", "", "GENERAL", "", "editable", true},
			{"coder-editable", "", "CODER", "", "editable", true},
			{"attach-existing", "GENERAL", "GENERAL", "", "editable", true},
			{"switch-editable", "KBASE", "KBASE", "readonly", "editable", true},
			{"switch-readonly", "KBASE", "KBASE", "editable", "readonly", false},
			{"same-editable-metadata", "KBASE", "KBASE", "editable", "editable", false},
			{"enable-kbase", "GENERAL", "KBASE", "editable", "editable", true},
			{"enable-kbase-from-coder", "CODER", "KBASE", "editable", "editable", true},
			{"enable-readonly-kbase", "GENERAL", "KBASE", "readonly", "readonly", false},
			{"unbind-general", "GENERAL", "GENERAL", "editable", "", false},
		} {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				h, libraries := knowledgeCatalogHandler(t)
				workspace := t.TempDir()
				ids := map[string]string{}
				for _, name := range []string{"readonly", "editable"} {
					library, err := libraries.Create(kbases.Input{Name: name, Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir(), Editable: name == "editable"}}})
					if err != nil {
						t.Fatal(err)
					}
					ids[name] = library.ID
				}
				params := map[string]any{"resourceType": "agent", "resourceKey": "candidate", "content": bindingCandidate(tc.afterMode, ids[tc.after], "Updated", workspace)}
				if tc.beforeMode != "" {
					dir := filepath.Join(h.cfg.Paths.AgentsDir, "candidate")
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "agent.yml"), []byte(bindingCandidate(tc.beforeMode, ids[tc.before], "Original", workspace)), 0600); err != nil {
						t.Fatal(err)
					}
					current, err := h.sources.Read(adminsource.ControlTarget{ResourceType: "agent", ResourceKey: "candidate"})
					if err != nil {
						t.Fatal(err)
					}
					params["baseRevision"] = current.BaseRevision
				}
				execution := controlExecution()
				execution.AccessLevel = level
				plan, err := h.PrepareToolApproval(context.Background(), "catalog_manage", map[string]any{"action": "apply", "args": params}, execution)
				if err != nil || plan == nil || plan.AllowAutoApprove == tc.human {
					t.Fatalf("incorrect binding approval: %+v %v", plan, err)
				}
				if tc.human {
					review, ok := plan.Form["knowledgeBinding"].(*adminsource.KnowledgeBindingReview)
					if !ok || !review.RequiresHuman || review.LibraryID != ids[tc.after] || review.BaseRevision == "" || !review.SourcePermissions[0].Editable || !filepath.IsAbs(review.SourcePermissions[0].SourcePath) {
						t.Fatalf("missing frozen library grant: %+v", plan.Form)
					}
				}
			})
		}
	}
}

func TestCatalogReadOnlyBindingApprovalCannotSurviveEditableChange(t *testing.T) {
	h, libraries := knowledgeCatalogHandler(t)
	d, err := libraries.Create(kbases.Input{Name: "Docs", Collections: []kbases.Collection{{Name: "docs", SourcePath: t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "apply", "args": map[string]any{"resourceType": "agent", "resourceKey": "candidate", "content": bindingCandidate("KBASE", d.ID, "Candidate", t.TempDir())}}
	execution := controlExecution()
	old, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution)
	if err != nil || !old.AllowAutoApprove {
		t.Fatalf("readonly binding: %+v %v", old, err)
	}
	d.Collections[0].Editable = true
	if _, err := libraries.Edit(d.ID, kbases.Input{Name: d.Name, BaseRevision: d.BaseRevision, Collections: d.Collections}); err != nil {
		t.Fatal(err)
	}
	execution.ToolApprovals = map[string]bool{old.Fingerprint: true}
	result, err := h.Invoke(context.Background(), "catalog_manage", args, execution)
	if err != nil || result.Error != "approval_required" {
		t.Fatalf("old automatic approval survived editable change: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Paths.AgentsDir, "candidate")); !os.IsNotExist(err) {
		t.Fatalf("candidate published with stale library approval: %v", err)
	}
	current, err := h.PrepareToolApproval(context.Background(), "catalog_manage", args, execution)
	if err != nil || current.AllowAutoApprove || current.Fingerprint == old.Fingerprint {
		t.Fatalf("editable change did not require fresh human approval: %+v %v", current, err)
	}
	execution.ToolApprovals = map[string]bool{current.Fingerprint: true}
	result, err = h.Invoke(context.Background(), "catalog_manage", args, execution)
	if err != nil || result.Error != "" || result.Structured["status"] != "applied" {
		t.Fatalf("approved binding failed: %+v %v", result, err)
	}
}
