package accesspolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

type capabilityFixture struct {
	session   QuerySession
	workspace string
	chat      string
	temp      string
}

func newCapabilityFixture(t *testing.T, level string) capabilityFixture {
	t.Helper()
	base := t.TempDir()
	f := capabilityFixture{workspace: filepath.Join(base, "ws"), chat: filepath.Join(base, "chats", "c1"), temp: filepath.Join(base, "tmp")}
	for _, dir := range []string{f.workspace, f.chat, f.temp, filepath.Join(base, "agent"), filepath.Join(base, "agent", "skills")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.session = QuerySession{AccessLevel: level, WorkspaceRoot: f.workspace, ChatID: "c1", RunID: "r1", AgentKey: "a", TempRoot: f.temp, TempRoots: []string{f.temp}}
	f.session.RuntimeContext.LocalPaths.ChatsDir = filepath.Dir(f.chat)
	f.session.RuntimeContext.LocalPaths.ChatDir = f.chat
	f.session.RuntimeContext.LocalPaths.AgentDir = filepath.Join(base, "agent")
	f.session.RuntimeContext.LocalPaths.SkillsDir = filepath.Join(base, "agent", "skills")
	return f
}

func TestExecutableGitConfigurationRequiresApproval(t *testing.T) {
	targets := []string{".git/hooks/pre-commit", ".git/config", "sub/.git/hooks/post-checkout", ".git"}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		targets = append(targets, ".GIT/hooks/pre-commit", ".Git/Config")
	}
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		if err := os.MkdirAll(filepath.Join(f.workspace, ".git", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			plan, err := BuildPathPlan(config.AccessPolicyConfig{}, f.session, WriteAccess, target)
			if err != nil {
				t.Fatal(err)
			}
			if want := level != AccessLevelFullAccess; plan.RequiresApproval() != want || plan.Blocked() {
				t.Fatalf("%s %s: %+v", level, target, plan)
			}
		}
		plan, _ := BuildPathPlan(config.AccessPolicyConfig{}, f.session, WriteAccess, ".git/info/exclude")
		if !plan.Allowed() {
			t.Fatalf("non-executable git metadata follows ordinary editing: %+v", plan)
		}
	}
}
