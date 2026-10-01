package accesspolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/scriptstate"
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

func review(f capabilityFixture, command string, ctx ...*ExecutionContext) BashPlan {
	return ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, "", nil, ctx...)
}

func TestWorkspaceEditingCapabilityIsIndependentOfAccessLevel(t *testing.T) {
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		if p := review(f, "touch notes.txt"); !p.Allowed() || p.AutoApproved() {
			t.Fatalf("%s editing=true: ordinary workspace write must be allowed: %+v", level, p)
		}
		f.session.WorkspaceReadOnly = true
		plan, err := BuildPathPlan(config.AccessPolicyConfig{}, f.session, WriteAccess, filepath.Join(f.workspace, "notes.txt"))
		if err != nil || !plan.Blocked() {
			t.Fatalf("%s editing=false: workspace write must be a hard block: %+v %v", level, plan, err)
		}
		if DefaultBashCwd(f.session) != "@chat" {
			t.Fatal("read-only workspace must start programs in the chat directory")
		}
		if p := review(f, "touch notes.txt"); !p.Allowed() {
			t.Fatalf("%s editing=false: default cwd is the chat, writes there are allowed: %+v", level, p)
		}
		for _, command := range []string{"python3 build.py", "./tool", "sh -c 'rm -rf x'"} {
			p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, "@workspace", nil)
			if !p.Blocked() {
				t.Fatalf("%s editing=false: unanalyzable program inside the workspace must be blocked: %s %+v", level, command, p)
			}
		}
		if p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, "cat README.md", "@workspace", nil); !p.Allowed() {
			t.Fatalf("%s editing=false: reads stay allowed: %+v", level, p)
		}
	}
}

func TestExecutableGitConfigurationRequiresApproval(t *testing.T) {
	targets := []string{".git/hooks/pre-commit", ".git/config", "sub/.git/hooks/post-checkout", ".git"}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		targets = append(targets, ".GIT/hooks/pre-commit", ".Git/Config")
	}
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
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

func TestDestructiveAndRemoteActionsFollowLevelApprovals(t *testing.T) {
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		for _, command := range []string{"rm -rf build", "find . -name '*.tmp' -delete", "curl -d a=1 https://example.invalid", "curl -X DELETE https://example.invalid/x"} {
			p := review(f, command)
			if level == AccessLevelFullAccess {
				if !p.Allowed() {
					t.Fatalf("full_access allows %s: %+v", command, p)
				}
				continue
			}
			if !p.RequiresApproval() {
				t.Fatalf("%s must ask for %s: %+v", level, command, p)
			}
		}
		if p := review(f, "rm old.txt"); !p.Allowed() {
			t.Fatalf("%s: single-file removal is an ordinary edit: %+v", level, p)
		}
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelFullAccess: {Approvals: config.AccessPolicyApprovalConfig{RemoteMutation: "hitl", Destructive: "hitl"}}}}
	f := newCapabilityFixture(t, AccessLevelFullAccess)
	for _, command := range []string{"rm -rf build", "curl -d a=1 https://example.invalid"} {
		if p := ReviewBashCommand(cfg, f.session, command, "", nil); !p.RequiresApproval() {
			t.Fatalf("full_access can be configured to confirm %s: %+v", command, p)
		}
	}
}

func TestWrappersCannotHideRemoteMutationInAutoApprove(t *testing.T) {
	f := newCapabilityFixture(t, AccessLevelAutoApprove)
	for _, command := range []string{"bash -c 'curl -X DELETE https://example.invalid/x'", "env -S 'curl -d a=1 https://example.invalid'", "sh -c \"bash -c 'curl -F f=@notes https://example.invalid'\""} {
		if p := review(f, command); !p.RequiresApproval() {
			t.Fatalf("wrapped remote mutation was auto-approved: %s %+v", command, p)
		}
	}
	if p := review(f, "bash -c 'ls -la'"); !p.Allowed() || p.AutoApproved() {
		t.Fatalf("a literal harmless script is analyzed, not treated as opaque: %+v", p)
	}
}

func TestAuthoredScriptExemptionRequiresChatLocationAndProof(t *testing.T) {
	if _, ok := systemExecutables["sh"]; !ok {
		t.Skip("system sh unavailable")
	}
	f := newCapabilityFixture(t, AccessLevelDefault)
	ctx := &ExecutionContext{Session: f.session}
	ctx.AuthoredScripts = scriptstate.New(ctx.ScriptOwner())
	write := func(path string, record bool) {
		content := []byte("echo hi\n")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if record {
			ctx.AuthoredScripts.Record(ctx.ScriptOwner(), path, content, "", true)
		}
	}
	chatScript := filepath.Join(f.chat, "task.sh")
	write(chatScript, true)
	if p := review(f, "sh '"+chatScript+"'", ctx); !p.Allowed() || p.RuleKey != "bash-access:authored-script" {
		t.Fatalf("self-written chat script must run without approval: %+v", p)
	}
	for _, path := range []string{filepath.Join(f.workspace, "task.sh"), filepath.Join(f.temp, "task.sh")} {
		write(path, true)
		if p := review(f, "sh '"+path+"'", ctx); !p.RequiresApproval() {
			t.Fatalf("authored script outside the chat directory is not exempt: %s %+v", path, p)
		}
	}
	foreign := filepath.Join(f.chat, "foreign.sh")
	write(foreign, false)
	if p := review(f, "sh '"+foreign+"'", ctx); !p.RequiresApproval() {
		t.Fatalf("unproven chat script is not exempt: %+v", p)
	}
	if err := os.WriteFile(chatScript, []byte("echo changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := review(f, "sh '"+chatScript+"'", ctx); !p.RequiresApproval() {
		t.Fatalf("externally modified script lost its proof: %+v", p)
	}
	write(chatScript, true)
	if p := review(f, "sh '"+chatScript+"' > "+filepath.Join(t.TempDir(), "out"), ctx); !p.RequiresApproval() {
		t.Fatalf("exemption must not cover an outside redirect: %+v", p)
	}
}

func TestUnresolvedAliasProtectsNothingAndBareWordsAreNotPaths(t *testing.T) {
	f := newCapabilityFixture(t, AccessLevelDefault)
	f.session.RuntimeContext.LocalPaths.AgentDir = ""
	f.session.RuntimeContext.LocalPaths.SkillsDir = ""
	if p := BuildSubtreePlan(config.AccessPolicyConfig{}, f.session, WriteAccess, f.workspace); p.Blocked() {
		t.Fatalf("unconfigured @agent alias must not become <workspace>/@agent: %+v", p)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "data"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := review(f, "mytool data")
	for _, leaf := range append([]BashPlan{p}, p.Requirements...) {
		if strings.Contains(leaf.RuleKey, "access-write") {
			t.Fatalf("a bare word that names an existing file is not a write target: %+v", p)
		}
	}
}

func TestSSHAgentOnlyForGitNetworkOperations(t *testing.T) {
	f := newCapabilityFixture(t, AccessLevelFullAccess)
	for command, want := range map[string]bool{"git fetch": true, "bash -c 'git pull'": true, "git status": false, "curl https://example.invalid": false} {
		if got := review(f, command).UsesSSHAgent; got != want {
			t.Fatalf("%s: UsesSSHAgent=%v", command, got)
		}
	}
}

func TestGitReadAndWriteDependOnRepositoryState(t *testing.T) {
	gitPath, ok := systemExecutables["git"]
	if !ok || gitPath == "" {
		t.Skip("system git unavailable")
	}
	f := newCapabilityFixture(t, AccessLevelDefault)
	home := t.TempDir()
	vars := map[string]string{"HOME": home, "PATH": os.Getenv("PATH"), "XDG_CONFIG_HOME": filepath.Join(home, ".config")}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.workspace
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	check := func(command string, wantAllowed bool) {
		t.Helper()
		p := ReviewBashCommand(config.AccessPolicyConfig{}, f.session, command, "", vars)
		if p.Allowed() != wantAllowed {
			t.Fatalf("%s: allowed=%v want %v: %+v", command, p.Allowed(), wantAllowed, p)
		}
	}
	check("git status", true)
	check("git diff --stat", true)
	check("git add -A", true)
	run("config", "core.fsmonitor", "./monitor.sh")
	check("git status", false)
	run("config", "--unset", "core.fsmonitor")
	check("git status", true)
	hook := filepath.Join(f.workspace, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	check("git status", true)
	check("git commit -m x", false)
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	check("git commit -m x", true)
	check("git reset --hard", false)
	check("git -c core.pager=less log", false)
}
