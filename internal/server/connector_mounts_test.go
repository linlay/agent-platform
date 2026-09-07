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
	def := catalog.AgentDefinition{Connectors: []string{"selected"}, ConnectorMounts: []catalog.ConnectorMount{{ID: "selected", Dir: filepath.Join(packages, "selected")}}, RuntimeDir: filepath.Join(root, "ru-agents", "demo"), Runtime: map[string]any{"environmentId": "linux"}, ConnectorSkills: []catalog.ConnectorSkill{{Key: "connector-8-selected-usage"}}}
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
