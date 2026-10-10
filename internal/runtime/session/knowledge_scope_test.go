package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/knowledge"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestRunFreezesEditableCollectionsOnlyForDedicatedHostKBase(t *testing.T) {
	root := t.TempDir()
	workspace, source, replacement := filepath.Join(root, "workspace"), filepath.Join(root, "source"), filepath.Join(root, "replacement")
	for _, dir := range []string{workspace, source, replacement} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source, _ = filepath.EvalSymlinks(source)
	replacement, _ = filepath.EvalSymlinks(replacement)
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: filepath.Join(root, "chats")}}
	cfg.ContainerHub.Enabled = true
	collections := []knowledge.CollectionScope{{Name: "docs", SourcePath: source, Description: "规范", Editable: true}, {Name: "reference", SourcePath: replacement, Editable: false}}
	calls := 0
	builder := New(Dependencies{Config: cfg, KnowledgeCollections: func(id string) ([]knowledge.CollectionScope, error) {
		calls++
		if id != "lib" {
			t.Fatalf("binding changed: %s", id)
		}
		return append([]knowledge.CollectionScope(nil), collections...), nil
	}})
	def := catalog.AgentDefinition{Key: "docs", Mode: "KBASE", Workspace: catalog.AgentWorkspaceConfig{Root: workspace}, KBaseConfig: knowledge.Config{Enabled: true, LibraryID: "lib"}, Tools: []string{"file_write", "file_read"}}
	yes := true
	req := runtimetypes.QueryCommand{AgentKey: "docs", ChatID: "chat", RunID: "run", EditingMode: &yes}
	session, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := accesspolicy.SessionEditableCollectionRoot(session, filepath.Join(source, "file.md")); !ok {
		t.Fatal("missing editable root")
	}
	if _, ok := accesspolicy.SessionEditableCollectionRoot(session, filepath.Join(replacement, "file.md")); ok {
		t.Fatal("noneditable collection granted")
	}
	if !strings.Contains(session.KBaseCollectionsPrompt, "规范") {
		t.Fatal("missing description")
	}
	collections[0].SourcePath = replacement
	collections[0].Description = "changed"
	if session.ScopedFilePolicy.EditableCollectionRoots[0] != source || strings.Contains(session.KBaseCollectionsPrompt, "changed") {
		t.Fatal("running session changed with library")
	}
	next, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{})
	if err != nil || next.ScopedFilePolicy.EditableCollectionRoots[0] != replacement {
		t.Fatalf("next admission: %+v %v", next.ScopedFilePolicy, err)
	}
	req.PlanningMode = &yes
	planned, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{})
	if err != nil || planned.ScopedFilePolicy.WorkspaceMutationEnabled {
		t.Fatalf("planning grant: %+v %v", planned.ScopedFilePolicy, err)
	}
	req.PlanningMode = nil
	def.Runtime = map[string]any{"environmentId": "sandbox"}
	roots, prompt, err := builder.knowledgeScope(def)
	if err != nil || len(roots) != 0 || strings.Contains(prompt, `"editable":true`) {
		t.Fatalf("sandbox grant: %v %s %v", roots, prompt, err)
	}

	def.Runtime = nil
	previousCalls := calls
	for _, mode := range []string{"GENERAL", "CODER"} {
		def.Mode = mode
		ordinary, err := builder.BuildQuerySession(context.Background(), req, chat.Summary{ChatID: "chat"}, def, Options{})
		if err != nil || ordinary.ScopedFilePolicy != nil || ordinary.KBaseCollectionsPrompt != "" || calls != previousCalls {
			t.Fatalf("ordinary grant %s: %+v %v", mode, ordinary.ScopedFilePolicy, err)
		}
	}
}
