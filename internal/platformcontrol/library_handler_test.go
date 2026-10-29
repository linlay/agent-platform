package platformcontrol

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/kbases"
)

func libraryHandler(t *testing.T) *ToolHandler {
	t.Helper()
	s, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), nil, kbases.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return NewToolHandler(config.Config{}, nil, nil).ConfigureControl(&adminsource.ControlService{Mutations: adminsource.NewService()}, nil, nil).ConfigureLibraries(s)
}

func TestLibraryManagementRejectsRemovedEditableBeforeReview(t *testing.T) {
	h := libraryHandler(t)
	for _, editable := range []any{false, true, "false", "true"} {
		args := map[string]any{"action": "create", "args": map[string]any{
			"definition": map[string]any{"name": "Docs", "collections": []any{
				map[string]any{"name": "docs", "sourcePath": t.TempDir(), "editable": editable},
			}},
		}}
		plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, controlExecution())
		if err == nil || plan != nil {
			t.Fatalf("removed editable=%v reached approval: %+v %v", editable, plan, err)
		}
	}
}

func TestLibraryManagementSourceChangesAlwaysRequireHumanReview(t *testing.T) {
	h := libraryHandler(t)
	source := t.TempDir()
	d, err := h.libraries.Create(kbases.Input{Name: "Docs", Collections: []kbases.Collection{{Name: "docs", SourcePath: source, Include: []string{"**/*.md", "**/*.txt"}, Exclude: []string{"private/**", "build/**"}}}})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "source-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"ask_for_approval", "auto_approve", "full_access"} {
		for _, tc := range []struct {
			name   string
			change func(map[string]any, map[string]any)
			human  bool
		}{
			{"metadata", func(p, _ map[string]any) { p["name"], p["description"] = "Renamed", "Description" }, false},
			{"query-settings", func(p, _ map[string]any) { p["retrieval"] = map[string]any{"rerank": nil, "topK": 12} }, false},
			{"models", func(p, _ map[string]any) { p["models"] = map[string]any{"reranker": nil} }, false},
			{"collection-metadata", func(_, c map[string]any) { c["name"], c["description"] = "renamed-docs", "Description" }, false},
			{"canonical-alias-and-filter-order", func(_, c map[string]any) {
				c["sourcePath"] = alias
				c["include"] = []any{"**/*.txt", "**/*.md"}
				c["exclude"] = []any{"build/**", "private/**"}
			}, false},
			{"source", func(_, c map[string]any) { c["sourcePath"] = t.TempDir() }, true},
			{"include", func(_, c map[string]any) { c["include"] = []any{"**/*.md"} }, true},
			{"exclude", func(_, c map[string]any) { c["exclude"] = []any{} }, true},
		} {
			t.Run(level+"/"+tc.name, func(t *testing.T) {
				collection := map[string]any{"name": "docs", "sourcePath": source, "include": []any{"**/*.md", "**/*.txt"}, "exclude": []any{"private/**", "build/**"}}
				patch := map[string]any{"collections": []any{collection}}
				tc.change(patch, collection)
				args := map[string]any{"action": "update", "args": map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision, "patch": patch}}
				e := controlExecution()
				e.AccessLevel = level
				plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e)
				if err != nil || plan == nil || plan.AllowAutoApprove == tc.human || plan.Form["sourceGrantChanged"] != tc.human {
					t.Fatalf("incorrect source review policy: %+v %v", plan, err)
				}
			})
		}
		for _, action := range []string{"create", "delete"} {
			args := map[string]any{"action": action, "args": map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision}}
			if action == "create" {
				args["args"] = map[string]any{"definition": map[string]any{"name": "New library", "sourcePath": t.TempDir()}}
			}
			e := controlExecution()
			e.AccessLevel = level
			plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e)
			if err != nil || plan == nil || plan.AllowAutoApprove {
				t.Fatalf("%s bypassed human review at %s: %+v %v", action, level, plan, err)
			}
		}
	}
}

func TestKBASEDefaultsDoNotRequireLibraryBinding(t *testing.T) {
	cfg := config.Config{}
	cfg.KBase.DefaultAgent.ModelKey = "chat-model"
	h := NewToolHandler(cfg, nil, nil)
	result := h.get(KBaseCreationPath)
	// A binding is optional for every mode, so it is never a missing field.
	missing, _ := result.Structured["missingFields"].([]string)
	if result.Structured["ready"] != true || slices.Contains(missing, "kbaseConfig.libraryId") {
		t.Fatalf("KBASE defaults still require a library binding: %+v", result)
	}
}

func TestLibraryManagementApprovalAndStatusBeforeIndex(t *testing.T) {
	h := libraryHandler(t)
	e := controlExecution()
	args := map[string]any{"action": "create", "args": map[string]any{"definition": map[string]any{"name": "Docs", "collections": []any{map[string]any{"name": "docs", "sourcePath": t.TempDir(), "defaultQuery": "false"}}}}}
	plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e)
	if err != nil || plan == nil || plan.AllowAutoApprove {
		t.Fatalf("create approval: %#v %v", plan, err)
	}
	var review map[string]any
	if err := json.Unmarshal([]byte(plan.Form["after"].(string)), &review); err != nil || review["name"] != "Docs" || plan.Form["resourceType"] != "knowledge_library" {
		t.Fatalf("review configuration is not renderable: %#v %v", plan.Form, err)
	}
	permissions := plan.Form["sourcePermissions"].([]kbases.Collection)
	if permissions[0].DefaultQuery == nil || *permissions[0].DefaultQuery || !filepath.IsAbs(permissions[0].SourcePath) || len(permissions[0].Include) == 0 {
		t.Fatal("source grant is not explicit")
	}
	result, _ := h.Invoke(context.Background(), "kbase_library_manage", args, e)
	if result.Error != "approval_required" {
		t.Fatalf("full access bypassed exact approval: %+v", result)
	}
	e.ToolApprovals = map[string]bool{plan.Fingerprint: true}
	result, err = h.Invoke(context.Background(), "kbase_library_manage", args, e)
	if err != nil || result.Error != "" {
		t.Fatalf("create: %+v %v", result, err)
	}
	id := result.Structured["libraryId"].(string)
	status, err := h.Invoke(context.Background(), "kbase_library_query", map[string]any{"action": "status", "args": map[string]any{"libraryId": id}}, e)
	if err != nil || status.Error != "" || status.Structured["state"] != "unindexed" || status.Structured["statsAvailable"] != false {
		t.Fatalf("status: %+v %v", status, err)
	}
	e.CurrentToolID = "repeat"
	if plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e); err != nil || plan != nil {
		t.Fatalf("reuse requests another mutation grant: %#v %v", plan, err)
	}
	result, _ = h.Invoke(context.Background(), "kbase_library_manage", args, e)
	if result.Error != "" || result.Structured["reused"] != true || result.Structured["libraryId"] != id {
		t.Fatalf("reuse: %+v", result)
	}
	e.CurrentToolID = "delete"
	remove := map[string]any{"action": "delete", "args": map[string]any{"libraryId": id, "baseRevision": result.Structured["baseRevision"]}}
	plan, err = h.PrepareToolApproval(context.Background(), "kbase_library_manage", remove, e)
	if err != nil || plan == nil || plan.AllowAutoApprove {
		t.Fatalf("delete did not require human approval: %#v %v", plan, err)
	}
	if plan.Form["resourceKey"] != id {
		t.Fatal("deletion review omitted library identity")
	}
}

func TestLibraryManagementAdmissionAndChangedCandidate(t *testing.T) {
	h := libraryHandler(t)
	args := map[string]any{"action": "create", "args": map[string]any{"definition": map[string]any{"name": "Docs", "sourcePath": t.TempDir()}}}
	for _, modify := range []func(*contracts.ExecutionContext){
		func(e *contracts.ExecutionContext) { e.Session.NativeConnectorTools = nil },
		func(e *contracts.ExecutionContext) { e.Session.SubTaskID = "child" },
		func(e *contracts.ExecutionContext) { e.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly },
	} {
		e := controlExecution()
		modify(e)
		if _, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e); err == nil {
			t.Fatal("unauthorized mutation reached review")
		}
	}
	e := controlExecution()
	plan, err := h.PrepareToolApproval(context.Background(), "kbase_library_manage", args, e)
	if err != nil {
		t.Fatal(err)
	}
	e.ToolApprovals = map[string]bool{plan.Fingerprint: true}
	args["args"].(map[string]any)["definition"].(map[string]any)["sourcePath"] = t.TempDir()
	result, _ := h.Invoke(context.Background(), "kbase_library_manage", args, e)
	if result.Error != "approval_required" {
		t.Fatalf("changed source reused approval: %+v", result)
	}
	e.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly
	result, _ = h.Invoke(context.Background(), "kbase_library_query", map[string]any{"action": "list"}, e)
	if result.Error != "" {
		t.Fatalf("planning cannot list libraries: %+v", result)
	}
}
