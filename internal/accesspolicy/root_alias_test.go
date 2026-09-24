package accesspolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestRootAliasPermissions(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.VolumeName(cwd) + string(filepath.Separator)
	session := contracts.QuerySession{AccessLevel: contracts.AccessLevelDefault}
	if got := expandRootAlias("@root", session); got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
		contracts.AccessLevelDefault: {
			ReadRoots: []string{"@root"}, WriteRoots: []string{"@root"},
			Approvals: config.AccessPolicyApprovalConfig{ReadOutsideRoots: "block", WriteOutsideRoots: "block"},
		},
	}}
	for _, path := range []string{"@root", "@root/agent-platform-root-alias-test", filepath.Join(root, "agent-platform-root-alias-test")} {
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			plan, err := BuildPathPlan(cfg, session, mode, path)
			if err != nil || !plan.Allowed() {
				t.Fatalf("%s %s: %#v, %v", mode, path, plan, err)
			}
		}
	}
	level := cfg.Levels[contracts.AccessLevelDefault]
	level.ReadonlyRoots = []string{"@root"}
	cfg.Levels[contracts.AccessLevelDefault] = level
	plan, err := BuildPathPlan(cfg, session, WriteAccess, "@root/agent-platform-root-alias-test")
	if err != nil || plan.Allowed() || plan.RequiresApproval() {
		t.Fatalf("readonly root must block: %#v, %v", plan, err)
	}
}
