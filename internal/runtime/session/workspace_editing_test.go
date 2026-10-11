package session

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/knowledge"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestWorkspaceEditingIsCommonAndLibrariesGrantNoFileAccess(t *testing.T) {
	workspace, source, chatRoot := t.TempDir(), t.TempDir(), t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: chatRoot}}
	builder := New(Dependencies{Config: cfg})
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		for _, sandbox := range []bool{false, true} {
			for _, editing := range []bool{false, true} {
				def := catalog.AgentDefinition{Key: "docs", Mode: mode, Workspace: catalog.AgentWorkspaceConfig{Root: workspace}, KBaseConfig: knowledge.Config{Enabled: true, LibraryID: "lib"}}
				req := runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat", RunID: "run", EditingMode: &editing}
				session, err := builder.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "chat"}, def, Options{})
				if err != nil || session.EditingMode != editing || session.WorkspaceReadOnly == editing {
					t.Fatalf("%s sandbox=%v editing=%v: %+v %v", mode, sandbox, editing, session, err)
				}
				// Exercise the Host and container policy gates without starting Container Hub.
				session.AgentHasRuntimeSandbox = sandbox
				plan, err := accesspolicy.BuildPathPlan(config.AccessPolicyConfig{}, session, accesspolicy.WriteAccess, filepath.Join(workspace, "new.md"))
				if err != nil || plan.Blocked() == editing {
					t.Fatalf("workspace gate %s sandbox=%v editing=%v: %+v %v", mode, sandbox, editing, plan, err)
				}
				// Keep the test source outside the separately frozen temporary roots.
				session.TempRoot = t.TempDir()
				session.TempRoots = []string{session.TempRoot}
				// An unrelated library source is external, even for editing KBASE.
				for _, access := range []accesspolicy.AccessMode{accesspolicy.ReadAccess, accesspolicy.WriteAccess} {
					plan, err := accesspolicy.BuildPathPlan(config.AccessPolicyConfig{}, session, access, filepath.Join(source, "source.md"))
					if err != nil || !plan.RequiresApproval() || plan.Blocked() {
						t.Fatalf("library granted file access %s: %+v %v", mode, plan, err)
					}
				}
				// Explicit external grants remain independent of Workspace editing.
				session.RuntimeHostAccess.WriteRoots = []string{source}
				plan, err = accesspolicy.BuildPathPlan(config.AccessPolicyConfig{}, session, accesspolicy.WriteAccess, filepath.Join(source, "source.md"))
				if err != nil || !plan.Allowed() {
					t.Fatalf("external grant tied to editing %s: %+v %v", mode, plan, err)
				}
				planning := true
				req.PlanningMode = &planning
				session, err = builder.BuildQuerySession(t.Context(), req, chat.Summary{ChatID: "chat"}, def, Options{})
				if err != nil || session.EditingMode || !session.WorkspaceReadOnly {
					t.Fatalf("planning workspace writable %s: %+v %v", mode, session, err)
				}
			}
		}
	}
}

func TestWorkspaceEditingTemplateDefaults(t *testing.T) {
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		def := catalog.AgentDefinition{Key: "agent", Mode: mode, Workspace: catalog.AgentWorkspaceConfig{Root: t.TempDir()}}
		builder := New(Dependencies{Config: config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}}})
		session, err := builder.BuildQuerySession(t.Context(), runtimetypes.QueryCommand{AgentKey: def.Key, ChatID: "chat"}, chat.Summary{ChatID: "chat"}, def, Options{})
		wantEditing := mode != "KBASE"
		if err != nil || session.EditingMode != wantEditing || session.WorkspaceReadOnly == wantEditing {
			t.Fatalf("%s default: %+v %v", mode, session, err)
		}
	}
}
