package accesspolicy

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func authoredFixture(t *testing.T) (*ExecutionContext, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "task with space.sh")
	data := []byte("#!/bin/sh\nprintf 'ok\\n'\n")
	if err := os.WriteFile(p, data, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := &ExecutionContext{Session: QuerySession{AgentKey: "ordinary", RunID: "run-1", WorkspaceRoot: dir}}
	ctx.EnsureAuthoredScripts()
	ctx.AuthoredScripts.Record(ctx.ScriptOwner(), p, data, "", true)
	return ctx, p
}
func TestAuthoredExecutionFormsAndLevels(t *testing.T) {
	for _, level := range []string{AccessLevelDefault, AccessLevelAutoApprove, AccessLevelFullAccess} {
		for _, authored := range []bool{true, false} {
			for _, form := range []string{"bash './task with space.sh'", "sh './task with space.sh'", "'./task with space.sh'", "absolute"} {
				t.Run(fmt.Sprintf("%s/%t/%s", level, authored, form), func(t *testing.T) {
					ctx, p := authoredFixture(t)
					ctx.Session.AccessLevel = level
					if !authored {
						ctx.AuthoredScripts = nil
					}
					if form == "absolute" {
						form = "'" + p + "'"
					}
					got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, form, "", nil, ctx)
					want := DecisionAllow
					if !authored && level == AccessLevelDefault {
						want = DecisionRequiresApproval
					}
					if !authored && level == AccessLevelAutoApprove {
						want = DecisionAutoApproved
					}
					if got.Decision != want {
						t.Fatalf("got %+v want %s", got, want)
					}
				})
			}
		}
	}
}

func TestAuthoredDoesNotAuthorizeOtherRequirements(t *testing.T) {
	ctx, p := authoredFixture(t)
	cfg := config.AccessPolicyConfig{}
	for _, command := range []string{
		"sh './task with space.sh'; sh other.sh",
		"sh './task with space.sh' > /outside-review/result",
		"sh './task with space.sh'; touch /outside-review/result",
	} {
		got := ReviewBashCommand(cfg, ctx.Session, command, "", nil, ctx)
		if !got.RequiresApproval() {
			t.Fatalf("lost independent requirement for %s: %+v", command, got)
		}
	}
	own := ReviewBashCommand(cfg, ctx.Session, "sh './task with space.sh'", "", nil, ctx)
	if len(ApprovalRules(own)) != 0 || HasApproval(ctx, ReviewBashCommand(cfg, ctx.Session, "sh other.sh", "", nil, ctx)) {
		t.Fatal("authored exemption granted interpreter rule")
	}
	old := ReviewBashCommand(cfg, ctx.Session, "sh other.sh", "", nil, ctx)
	RegisterRuleApproval(ctx, old.RuleKey)
	if !HasApproval(ctx, ReviewBashCommand(cfg, ctx.Session, "sh third.sh", "", nil, ctx)) {
		t.Fatal("interpreter cwd rule reuse lost")
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf changed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx.AccessPolicyRuleApprovals = nil
	if !ReviewBashCommand(cfg, ctx.Session, "sh './task with space.sh'", "", nil, ctx).RequiresApproval() {
		t.Fatal("changed file retained self exemption")
	}
}

func TestExecutionWrappersAndLookalikes(t *testing.T) {
	ctx, p := authoredFixture(t)
	cfg := config.AccessPolicyConfig{}
	for _, command := range []string{"env sh './task with space.sh'", "command sh './task with space.sh'", "env -C '" + filepath.Dir(p) + "' sh './task with space.sh'"} {
		if got := ReviewBashCommand(cfg, ctx.Session, command, "", nil, ctx); !got.Allowed() {
			t.Fatalf("%s: %+v", command, got)
		}
	}
	fake := filepath.Join(filepath.Dir(p), "ls")
	if err := os.WriteFile(fake, []byte("custom executable"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"./ls", "env PATH='" + filepath.Dir(p) + "' ls", "./missing-binary"} {
		if got := ReviewBashCommand(cfg, ctx.Session, command, "", nil, ctx); !got.RequiresApproval() {
			t.Fatalf("lookalike bypassed: %s: %+v", command, got)
		}
	}
	for _, command := range []string{"PATH='" + filepath.Dir(p) + "'; ls", "export PATH='" + filepath.Dir(p) + "'; ls"} {
		if got := ReviewBashCommand(cfg, ctx.Session, command, "", nil, ctx); !got.RequiresApproval() {
			t.Fatalf("shell assignment bypassed executable identity: %s: %+v", command, got)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(p), "echo"), []byte("custom executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := ReviewBashCommand(cfg, ctx.Session, "env PATH='"+filepath.Dir(p)+"' echo ok", "", nil, ctx); !got.RequiresApproval() {
		t.Fatalf("exec wrapper treated external echo as a shell builtin: %+v", got)
	}
	if commandFamily(`C:\tools\PYTHON3.EXE`) != "python3" {
		t.Fatal("Windows family normalization")
	}
	if got := ReviewBashCommand(cfg, ctx.Session, "env -S 'sh task'", "", nil, ctx); !strings.Contains(got.RuleKey, "complex") {
		t.Fatalf("uncertain wrapper: %+v", got)
	}
}

func TestAuthoredReadonlyAndHardBlockPriority(t *testing.T) {
	ctx, _ := authoredFixture(t)
	readonly := filepath.Join(ctx.Session.WorkspaceRoot, "readonly")
	if err := os.Mkdir(readonly, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelDefault: {ReadonlyRoots: []string{readonly}}}}
	for _, command := range []string{"sh './task with space.sh' > readonly/result", "sh './task with space.sh'; touch readonly/result", "> readonly/result", "sh foreign.sh > readonly/result"} {
		got := ReviewBashCommand(cfg, ctx.Session, command, "", nil, ctx)
		RegisterExactApproval(ctx, got.Fingerprint)
		RegisterRuleApproval(ctx, got.RuleKey)
		if !got.Blocked() || ConsumeApproval(ctx, got) {
			t.Fatalf("readonly relaxed: %s: %+v", command, got)
		}
	}
	wrapper := "env -C '" + ctx.Session.WorkspaceRoot + "' sh './task with space.sh' > result"
	if got := ReviewBashCommand(cfg, ctx.Session, wrapper, readonly, nil, ctx); !got.Blocked() {
		t.Fatalf("wrapper redirected under inner rather than outer cwd: %+v", got)
	}
	blockOpaque := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{AccessLevelDefault: {Approvals: config.AccessPolicyApprovalConfig{BashOpaqueCommand: "block"}}}}
	if got := ReviewBashCommand(blockOpaque, ctx.Session, "sh './task with space.sh'", "", nil, ctx); !got.Blocked() {
		t.Fatalf("explicit opaque block bypassed: %+v", got)
	}
	ctx.Session.ScopedFilePolicy = &ScopedFilePolicy{WorkspaceRoot: ctx.Session.WorkspaceRoot}
	if got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "sh './task with space.sh'; touch workspace.txt", "", nil, ctx); !got.Blocked() {
		t.Fatalf("KBASE gate: %+v", got)
	}
}

func TestSandboxScriptIdentityUsesExecutionEnvironment(t *testing.T) {
	ctx, p := authoredFixture(t)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx.EnsureAuthoredScripts()
	ctx.AuthoredScripts.Record(ctx.ScriptOwner(), p, data, "", true)
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	canonical := "/workspace/task with space.sh"
	env := &BashEnvironment{
		Resolve: func(name, cwd string, vars map[string]string) (string, error) {
			if name == "sh" || name == "/usr/bin/sh" || name == "/bin/sh" {
				return "/usr/bin/sh", nil
			}
			return canonical, nil
		},
		Canonical: func(path, cwd string) (string, error) { return canonical, nil },
		Inspect:   func(path string) (string, string, error) { return string(data), hash, nil },
	}
	review := func(command string) BashPlan {
		return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, command, "/workspace", nil, env, ctx)
	}
	if got := review("sh './task with space.sh'"); !got.Allowed() {
		t.Fatalf("mapped own script: %+v", got)
	}
	if got := review("'./task with space.sh'"); !got.Allowed() {
		t.Fatalf("mapped direct own script: %+v", got)
	}
	hash = strings.Repeat("0", 64)
	if got := review("sh './task with space.sh'"); !got.RequiresApproval() {
		t.Fatalf("host proof trusted different container bytes: %+v", got)
	}
	canonical = "/etc/foreign.sh"
	if got := review("sh './task with space.sh'"); !got.RequiresApproval() {
		t.Fatalf("container symlink escape inherited host proof: %+v", got)
	}
	if got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "node /tmp/task.js", "/workspace", nil, ctx); !got.RequiresApproval() {
		t.Fatalf("unresolved container interpreter inherited host identity: %+v", got)
	}
}

func TestExactApprovalInvalidatedByContentAndEnvironment(t *testing.T) {
	ctx, p := authoredFixture(t)
	ctx.AuthoredScripts = nil
	cfg := config.AccessPolicyConfig{}
	review := func(vars map[string]string) BashPlan {
		return ReviewBashCommand(cfg, ctx.Session, "sh './task with space.sh'", "", vars, ctx)
	}
	original := review(nil)
	RegisterExactApproval(ctx, original.Fingerprint)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nprintf changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if HasApproval(ctx, review(nil)) {
		t.Fatal("content mutation inherited exact approval")
	}
	current := review(nil)
	RegisterExactApproval(ctx, current.Fingerprint)
	if HasApproval(ctx, review(map[string]string{"LANG": "C"})) {
		t.Fatal("environment mutation inherited exact approval")
	}
}

func TestSandboxSystemBasenameCannotHideDifferentTarget(t *testing.T) {
	ctx, _ := authoredFixture(t)
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	env := &BashEnvironment{Resolve: func(name, cwd string, vars map[string]string) (string, error) {
		switch name {
		case "ls", "./ls":
			return "/usr/bin/python3.12", nil // workspace ls symlink, not system ls
		case "/usr/bin/ls", "/bin/ls":
			return "/usr/bin/ls", nil
		default:
			return "", fmt.Errorf("not installed")
		}
	}}
	for _, command := range []string{"ls", "./ls"} {
		got := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, command, "/workspace", nil, env, ctx)
		if !got.RequiresApproval() {
			t.Fatalf("container lookalike bypassed: %s: %+v", command, got)
		}
	}
}

func TestExactApprovalInvalidatedBySymlinkTarget(t *testing.T) {
	ctx, p := authoredFixture(t)
	ctx.AuthoredScripts = nil
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(ctx.Session.WorkspaceRoot, "other.sh")
	if err := os.WriteFile(other, data, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ctx.Session.WorkspaceRoot, "link.sh")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	review := func() BashPlan {
		return ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "sh link.sh", "", nil, ctx)
	}
	RegisterExactApproval(ctx, review().Fingerprint)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if HasApproval(ctx, review()) {
		t.Fatal("equal bytes at a different canonical target inherited exact approval")
	}
}

func TestMixedRunRuleAndExactApprovalAudit(t *testing.T) {
	ctx := &ExecutionContext{}
	first := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "first", Fingerprint: "1"}
	second := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "second", Fingerprint: "2"}
	third := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "third", Fingerprint: "3"}
	combined := combineBashPlans("three commands", AccessLevelDefault, []BashPlan{first, second, third})
	RegisterRuleApproval(ctx, first.RuleKey)
	pending := PendingBashPlan(ctx, combined)
	RegisterExactApproval(ctx, pending.Fingerprint)
	if source := BashApprovalSource(ctx, combined); source != "exact" {
		t.Fatalf("mixed approval source missing: %s", source)
	}
}

func TestConditionalEnvironmentAndLoopTargetsAreConservative(t *testing.T) {
	ctx, p := authoredFixture(t)
	dir := filepath.Dir(p)
	if err := os.WriteFile(filepath.Join(dir, "ls"), []byte("foreign program"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"true || PATH='" + dir + "'; ls",
		"if true; then PATH='" + dir + "'; fi; ls",
		"PATH='" + dir + "'; false && PATH=/usr/bin; ls",
		"PATH='" + dir + "'; PATH=/usr/bin & ls",
		"script='./task with space.sh'; for item in a b; do sh \"$script\"; script=foreign.sh; done",
		"for item in a b; do sh './task with space.sh'; cd nested; done",
	} {
		got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, ctx)
		if !got.RequiresApproval() {
			t.Fatalf("conditional or repeated execution reused a stale target: %s: %+v", command, got)
		}
	}
}

func TestSandboxInterpreterRuleUsesCanonicalGuestCwd(t *testing.T) {
	ctx, _ := authoredFixture(t)
	ctx.AuthoredScripts = nil
	ctx.Session.AgentHasRuntimeSandbox = true
	ctx.Session.RuntimeContext.SandboxPaths.WorkspaceDir = "/workspace"
	env := &BashEnvironment{
		Directory: func(raw string) (string, error) { return "/workspace", nil },
		Resolve:   func(name, cwd string, vars map[string]string) (string, error) { return "/usr/bin/dash", nil },
		Canonical: func(raw, cwd string) (string, error) { return "/workspace/task.sh", nil },
	}
	review := func(cwd string) BashPlan {
		return ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, ctx.Session, "sh task.sh", cwd, nil, env, ctx)
	}
	original := review("/workspace")
	RegisterRuleApproval(ctx, original.RuleKey)
	if !HasApproval(ctx, review("/workspace/alias")) {
		t.Fatal("guest cwd alias failed to reuse the canonical interpreter rule")
	}
}

func TestAuthoredExtensionlessAndForeignBinary(t *testing.T) {
	ctx, _ := authoredFixture(t)
	dir := ctx.Session.WorkspaceRoot
	p := filepath.Join(dir, "task")
	data := []byte("#!/bin/sh\necho ok\n")
	if err := os.WriteFile(p, data, 0700); err != nil {
		t.Fatal(err)
	}
	ctx.AuthoredScripts.Record(ctx.ScriptOwner(), p, data, "", true)
	for _, command := range []string{"sh task", "./task", "'" + p + "'"} {
		if got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, command, "", nil, ctx); !got.Allowed() {
			t.Fatalf("extensionless %s: %+v", command, got)
		}
	}
	binary := filepath.Join(dir, "node")
	if err := os.WriteFile(binary, []byte{'E', 'L', 'F', 0, 1, 2, 3}, 0700); err != nil {
		t.Fatal(err)
	}
	if got := ReviewBashCommand(config.AccessPolicyConfig{}, ctx.Session, "./node /tmp/task.js", "", nil, ctx); !got.RequiresApproval() {
		t.Fatalf("custom binary inherited temp Node exemption: %+v", got)
	}
}
