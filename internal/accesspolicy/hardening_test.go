package accesspolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func policyFixture(t *testing.T) QuerySession {
	t.Helper()
	root := t.TempDir()
	s := QuerySession{AccessLevel: AccessLevelAskForApproval, WorkspaceRoot: filepath.Join(root, "workspace"), TempRoot: filepath.Join(root, "temp")}
	s.TempRoots = []string{s.TempRoot}
	for _, dir := range []string{s.WorkspaceRoot, s.TempRoot} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestDesktopSecretsHardBlockAcrossLevels(t *testing.T) {
	s := policyFixture(t)
	runtimeRoot := t.TempDir()
	vault := filepath.Join(runtimeRoot, ".desktop", "secrets", "website-credentials", "vault")
	if err := os.MkdirAll(filepath.Dir(vault), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vault, []byte("ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{RuntimeMode: config.RuntimeModeDesktop, Paths: config.PathsConfig{StateDir: filepath.Join(runtimeRoot, ".state")}}
	s.ProtectedPaths = PlatformProtectedPaths(cfg)
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
			plan, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, mode, vault)
			if err != nil || !plan.Blocked() {
				t.Fatalf("%s %s: %+v %v", level, mode, plan, err)
			}
		}
	}
	// Standalone has no Desktop data root to guard.
	cfg.RuntimeMode = config.RuntimeModeStandalone
	for _, path := range PlatformProtectedPaths(cfg) {
		if strings.Contains(path, ".desktop") {
			t.Fatalf("standalone protects %s", path)
		}
	}
}

func TestMailDatabaseHardBlockAcrossLevels(t *testing.T) {
	s := policyFixture(t)
	root := t.TempDir()
	cfg := config.Config{Paths: config.PathsConfig{ChatsDir: root}}
	s.ProtectedPaths = PlatformProtectedPaths(cfg)
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		s.AccessLevel = level
		for _, name := range []string{"mail.db", "mail.db-wal", "mail.db-shm"} {
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte("frozen permissions"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []AccessMode{ReadAccess, WriteAccess} {
				plan, err := BuildPathPlan(config.DefaultAccessPolicyConfig(), s, mode, path)
				if err != nil || !plan.Blocked() {
					t.Fatal(level, mode, name, plan, err)
				}
			}
		}
	}
}
