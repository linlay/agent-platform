package accesspolicy

import (
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectionMutationRoots(t *testing.T) {
	workspace, source, outside := t.TempDir(), t.TempDir(), t.TempDir()
	canonical, err := pathutil.Canonicalize(source)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelDefault:    {Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "hitl", WriteOutsideRoots: "hitl", ExecutableConfig: "hitl", BashOpaqueCommand: "hitl", BashComplexFilesystem: "hitl"}},
		contracts.AccessLevelFullAccess: {WriteRoots: []string{"@root"}},
	}}
	s := contracts.QuerySession{WorkspaceRoot: workspace, ScopedFilePolicy: &contracts.ScopedFilePolicy{WorkspaceRoot: workspace, EditableCollectionRoots: []string{canonical.Host}}}
	path := filepath.Join(source, "new.md")
	for _, level := range []string{contracts.AccessLevelDefault, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, p := range []string{path, filepath.Join(source, ".git", "config"), filepath.Join(workspace, "new.md")} {
			plan, err := BuildPathPlan(cfg, s, WriteAccess, p)
			if err != nil || !plan.Blocked() || plan.RequiresApproval() {
				t.Fatalf("editing=false %s: %+v %v", level, plan, err)
			}
		}
	}
	s.AccessLevel = contracts.AccessLevelDefault
	for _, command := range []string{"touch new.md", "unknown-program", "if true; then touch new.md; fi"} {
		if p := ReviewBashCommand(cfg, s, command, source, nil); !p.Blocked() {
			t.Fatalf("readonly bash: %s %+v", command, p)
		}
	}
	s.ScopedFilePolicy.WorkspaceMutationEnabled = true
	for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
		plan, err := BuildPathPlan(cfg, s, mode, path)
		if err != nil || !plan.Allowed() {
			t.Fatalf("editing grant: %+v %v", plan, err)
		}
	}
	// Readonly grants win even when Workspace and collection overlap.
	s.WorkspaceRoot = source
	s.RunAccessRoots.ReadonlyRoots = []string{source}
	plan, err := BuildPathPlan(cfg, s, WriteAccess, path)
	if err != nil || !plan.Blocked() {
		t.Fatalf("readonly overlap: %+v %v", plan, err)
	}
	s.WorkspaceRoot = workspace
	s.RunAccessRoots.ReadonlyRoots = nil
	level := cfg.Levels[contracts.AccessLevelDefault]
	level.ReadonlyRoots = []string{source}
	cfg.Levels[contracts.AccessLevelDefault] = level
	plan, err = BuildPathPlan(cfg, s, WriteAccess, path)
	if err != nil || !plan.Blocked() {
		t.Fatalf("admin readonly: %+v %v", plan, err)
	}
	level.ReadonlyRoots = nil
	cfg.Levels[contracts.AccessLevelDefault] = level
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	escaped := filepath.Join(source, "escape", "new.md")
	if _, ok := SessionEditableCollectionRoot(s, escaped); ok {
		t.Fatal("symlink escape granted")
	}
	plan, err = BuildPathPlan(cfg, s, WriteAccess, escaped)
	if err != nil || !plan.RequiresApproval() {
		t.Fatalf("external target gained grant: %+v %v", plan, err)
	}
	// A canonical root replaced after admission must not redirect the grant.
	if err := os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(source + "-old")
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	if _, ok := SessionEditableCollectionRoot(s, path); ok {
		t.Fatal("replaced root redirected grant")
	}
	s.ScopedFilePolicy.EditableCollectionRoots = []string{workspace}
	s.AgentHasRuntimeSandbox = true
	if _, ok := SessionEditableCollectionRoot(s, filepath.Join(workspace, "file")); ok {
		t.Fatal("container inherited Host grant")
	}
}
