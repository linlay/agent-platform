package accesspolicy

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestConnectorPrivateStateCannotBeApprovedByFileOrBashPolicy(t *testing.T) {
	host := t.TempDir()
	workspace := filepath.Join(host, "workspace")
	root := filepath.Join(host, "state", "connectors")
	secret := filepath.Join(root, "users", "opaque-owner", "demo", "credentials.json")
	legacy := filepath.Join(root, "demo", "oauth.json")
	binary := filepath.Join(root, "installations", "demo", "1.0.0", "bin", "demo")
	for _, p := range []string{secret, legacy, binary} {
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(workspace, 0700)
	alias := filepath.Join(workspace, "copied-key")
	symlink := os.Symlink(secret, alias) == nil
	for _, level := range []string{contracts.AccessLevelDefault, contracts.AccessLevelAutoApprove, contracts.AccessLevelFullAccess} {
		session := contracts.QuerySession{WorkspaceRoot: workspace, AccessLevel: level, ConnectorStateRoot: root}
		paths := []string{secret, legacy, filepath.Dir(root)}
		if symlink {
			paths = append(paths, alias)
		}
		for _, p := range paths {
			for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
				plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, mode, p)
				if err != nil || !plan.Blocked() {
					t.Fatalf("%s %s accepted private %s: %+v %v", level, mode, p, plan, err)
				}
			}
		}
		for _, cmd := range []string{"cat '" + secret + "'", "cat '" + legacy + "'", "cat < '" + secret + "'", "rg token '" + filepath.Dir(root) + "'"} {
			plan := ReviewBashCommand(config.AccessPolicyConfig{}, session, cmd, workspace, nil)
			if !plan.Blocked() {
				t.Fatalf("%s accepted command %s: %+v", level, cmd, plan)
			}
		}
		if plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, binary); err != nil || plan.Blocked() {
			t.Fatalf("shared installation blocked: %+v %v", plan, err)
		}
		if plan, err := BuildPathPlan(config.AccessPolicyConfig{}, session, ReadAccess, workspace); err != nil || plan.Blocked() {
			t.Fatalf("ordinary workspace blocked: %+v %v", plan, err)
		}
	}
}
