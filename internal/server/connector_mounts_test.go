package server

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestConnectorMountsGrantOnlySelectedReadonlyRoots(t *testing.T) {
	root := t.TempDir()
	packages := filepath.Join(root, "platform", "connectors")
	def := catalog.AgentDefinition{Connectors: []string{"selected"}, ConnectorMounts: []catalog.ConnectorMount{{ID: "selected", Dir: filepath.Join(packages, "selected")}}, RuntimeDir: filepath.Join(root, "ru-agents", "demo"), Runtime: map[string]any{"environmentId": "linux"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: "usage", RuntimeDir: filepath.Join(packages, "selected", "skills", "usage")}}}
	selected := filepath.Join(packages, "selected", "bin", "command")
	sibling := filepath.Join(packages, "sibling", "bin", "command")
	skill := filepath.Join(def.ConnectorRuntimeSkillDirs()[0], "SKILL.md")
	for _, file := range []string{selected, sibling, skill} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	roots := contracts.RunAccessRoots{}
	if err := addConnectorAccessRoots(&roots, def); err != nil {
		t.Fatal(err)
	}
	session := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault, RunAccessRoots: roots}
	policy := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{contracts.AccessLevelDefault: {Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "hitl", WriteOutsideRoots: "hitl"}}}}
	for _, file := range []string{selected, skill} {
		plan, err := accesspolicy.BuildPathPlan(policy, session, accesspolicy.ReadAccess, file)
		if err != nil || !plan.Allowed() {
			t.Fatalf("mounted read: %#v %v", plan, err)
		}
		session.AccessLevel = contracts.AccessLevelFullAccess
		plan, err = accesspolicy.BuildPathPlan(policy, session, accesspolicy.WriteAccess, file)
		if err != nil || !plan.Blocked() {
			t.Fatalf("mounted write not blocked: %#v %v", plan, err)
		}
		session.AccessLevel = contracts.AccessLevelDefault
	}
	plan, err := accesspolicy.BuildPathPlan(policy, session, accesspolicy.ReadAccess, sibling)
	if err != nil || plan.Decision != accesspolicy.DecisionRequiresApproval {
		t.Fatalf("sibling inherited access: %#v %v", plan, err)
	}
	mounts := runtimeConnectorMounts(nil, def)
	if len(mounts) != 1 || mounts[0].Source != filepath.Join(packages, "selected") || mounts[0].Mode != "ro" || mounts[0].Destination != "/connectors/selected" {
		t.Fatalf("mounts: %#v", mounts)
	}
}

func TestSharedPackagesCannotBypassMountOrReadonlyWithAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "ru-connectors")
	selected := filepath.Join(shared, "selected", "version")
	other := filepath.Join(shared, "other", "version")
	for _, dir := range []string{selected, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "doc"), []byte("doc"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	session := contracts.QuerySession{SharedConnectorsRoot: shared, ConnectorDirs: map[string]string{"selected": selected}, AccessLevel: contracts.AccessLevelFullAccess}
	for _, tc := range []struct {
		path    string
		mode    accesspolicy.AccessMode
		blocked bool
	}{{filepath.Join(selected, "doc"), accesspolicy.ReadAccess, false}, {filepath.Join(selected, "doc"), accesspolicy.WriteAccess, true}, {filepath.Join(other, "doc"), accesspolicy.ReadAccess, true}, {shared, accesspolicy.ReadAccess, true}} {
		plan, err := accesspolicy.BuildPathPlan(config.AccessPolicyConfig{}, session, tc.mode, tc.path)
		if err != nil || plan.Blocked() != tc.blocked {
			t.Fatalf("%s %s: %#v %v", tc.mode, tc.path, plan, err)
		}
	}
}
