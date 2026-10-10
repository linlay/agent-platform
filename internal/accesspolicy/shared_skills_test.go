package accesspolicy

import (
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedSkillAliasesAndReadonlyBoundary(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	sibling := filepath.Join(root, "sibling")
	for _, dir := range []string{selected, sibling} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	session := contracts.QuerySession{AccessLevel: contracts.AccessLevelFullAccess, SharedSkillsRoot: root, SkillDirs: map[string]string{"office/pdf": selected}, AgentHasRuntimeSandbox: true, RunAccessRoots: contracts.RunAccessRoots{ReadRoots: []string{selected}, ReadonlyRoots: []string{selected}}}
	for _, alias := range []string{"@skills/office/pdf/guide.md", "/skills/office/pdf/guide.md"} {
		got, err := ResolveSessionPath(session, alias)
		if err != nil || got != filepath.Join(selected, "guide.md") {
			t.Fatalf("%s: %s %v", alias, got, err)
		}
	}
	if _, err := ResolveSessionPath(session, "@skills/office/pdf/../escape"); err == nil {
		t.Fatal("escape allowed")
	}
	if _, err := ResolveSessionPath(session, "@skills/unselected/guide.md"); err == nil {
		t.Fatal("unselected alias allowed")
	}
	for _, dir := range []string{selected, sibling} {
		plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, WriteAccess, filepath.Join(dir, "file"))
		if err != nil || plan.Decision != DecisionBlock {
			t.Fatalf("write: %#v %v", plan, err)
		}
	}
	plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, filepath.Join(sibling, "file"))
	if err != nil || plan.Decision != DecisionBlock {
		t.Fatalf("sibling read: %#v %v", plan, err)
	}
}
