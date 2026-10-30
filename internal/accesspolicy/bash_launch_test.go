package accesspolicy

import (
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/bashsec"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

func TestUnsupportedShellIsAnExactHumanDecision(t *testing.T) {
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		binding := types.LaunchBinding{Dialect: dialect.PowerShell, RuleFingerprint: "shell", ContractFingerprint: "contract", Fingerprint: "launch"}
		env := &BashEnvironment{Launch: &binding}
		command := `echo "\" ; Remove-Item -Recurse C:\x ; echo "\"`
		p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
		if !p.UnknownDialect || p.RequiresApproval() != (level != AccessLevelFullAccess) {
			t.Fatalf("%s: %+v", level, p)
		}
		if p.SecurityReview(command, nil).Decision != bashsec.ReviewAllow {
			t.Fatal("unknown grammar must not be interpreted a second time as Bash")
		}
		if level != AccessLevelFullAccess {
			if RuleReusable(p.RuleKey) {
				t.Fatal("unknown grammar must only accept an exact approval")
			}
			ctx := &ExecutionContext{}
			RegisterExactApproval(ctx, p.Fingerprint)
			binding.Fingerprint = "changed-launch"
			fresh := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
			if HasApproval(ctx, fresh) || !ConsumeApproval(ctx, p) || ConsumeApproval(ctx, p) {
				t.Fatal("grant did not remain exact, launch bound and one-shot")
			}
		}
		f.session.WorkspaceReadOnly = true
		blocked := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
		if !blocked.Blocked() {
			t.Fatalf("%s: unsupported grammar bypassed workspace hard limit: %+v", level, blocked)
		}
	}
}

func TestLaunchBindingSurvivesLeafApprovalFiltering(t *testing.T) {
	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	binding := types.LaunchBinding{Dialect: dialect.Bash, SyntaxSupported: true, RuleFingerprint: "shell", ContractFingerprint: "contract", Fingerprint: "launch"}
	env := &BashEnvironment{Launch: &binding}
	command := "curl -o /outside/result -X DELETE https://example.invalid/item"
	p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
	if len(p.Requirements) < 2 {
		t.Fatalf("expected independent path and remote requirements: %+v", p)
	}
	ctx := &ExecutionContext{}
	whole := PendingBashPlan(ctx, p)
	if whole.Fingerprint != p.Fingerprint {
		t.Fatal("unfiltered combined plan must preserve its exact grant identity")
	}
	RegisterExactApproval(ctx, whole.Fingerprint)
	fresh := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
	if !HasApproval(ctx, fresh) || !ConsumeApproval(ctx, fresh) || ConsumeApproval(ctx, fresh) {
		t.Fatal("combined launch-bound approval did not survive re-review and consume once")
	}
	RegisterExactApproval(ctx, p.Requirements[0].Fingerprint)
	pending := PendingBashPlan(ctx, p)
	if !pending.RequiresApproval() || pending.LaunchFingerprint != binding.Fingerprint {
		t.Fatalf("pending plan lost its launch binding: %+v", pending)
	}
	for _, leaf := range p.Requirements {
		if !strings.Contains(leaf.RuleKey, ":launch:shell") {
			t.Fatalf("requirement was not launch-bound: %+v", leaf)
		}
	}
}

func TestUnknownShellRetainsConfiguredBlockAtEveryLevel(t *testing.T) {
	for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
		f := newCapabilityFixture(t, level)
		binding := types.LaunchBinding{Dialect: dialect.PowerShell, RuleFingerprint: "shell", ContractFingerprint: "contract", Fingerprint: "launch", Executable: "shell", Template: []string{"-c", "prefix; {{command}}"}}
		cfg := config.AccessPolicyConfig{Levels: map[string]config.AccessPolicyLevelConfig{
			level: {Approvals: config.AccessPolicyApprovalConfig{BashComplexFilesystem: "block"}},
		}}
		p := ReviewBashCommandInEnvironment(cfg, f.session, "echo ok", f.workspace, nil, &BashEnvironment{Launch: &binding}, nil)
		if !p.Blocked() || !strings.Contains(p.Reason, "prefix; {{command}}") {
			t.Fatalf("%s: block or actual startup description lost: %+v", level, p)
		}
	}
}

func TestPathRuleGrantSurvivesCwdAndEnvironmentButNotShellChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix cat identity fixture")
	}
	f := newCapabilityFixture(t, AccessLevelAskForApproval)
	binding := types.LaunchBinding{Dialect: dialect.Bash, SyntaxSupported: true, RuleFingerprint: "shell", ContractFingerprint: "cwd-env-one", Fingerprint: "call-one"}
	env := &BashEnvironment{Launch: &binding}
	command := "cat /outside/source"
	p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, env, nil)
	if !p.RequiresApproval() || !strings.HasPrefix(p.RuleKey, "bash-access:path:") {
		t.Fatalf("expected an absolute path requirement: %+v", p)
	}
	ctx := &ExecutionContext{}
	RegisterRuleApproval(ctx, p.RuleKey)
	binding.ContractFingerprint, binding.Fingerprint = "cwd-env-two", "call-two"
	fresh := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.chat, map[string]string{"CHANGED": "yes"}, env, nil)
	if fresh.Fingerprint == p.Fingerprint || fresh.RuleKey != p.RuleKey || !HasApproval(ctx, fresh) {
		t.Fatalf("path rule was narrowed by cwd/environment: %+v", fresh)
	}
	other := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, "cat /outside/other", f.chat, nil, env, nil)
	if HasApproval(ctx, other) {
		t.Fatal("path rule expanded to a different target")
	}
	binding.RuleFingerprint = "another-shell-or-template"
	changed := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.chat, nil, env, nil)
	if HasApproval(ctx, changed) {
		t.Fatal("path rule ignored a shell/template/dialect change")
	}
}

func TestUnsafeTextCannotBeApprovedInAnyDialect(t *testing.T) {
	for _, grammar := range []dialect.Dialect{dialect.Bash, dialect.PowerShell, dialect.CMD, dialect.MSYS, dialect.Unknown} {
		for _, level := range []string{AccessLevelAskForApproval, AccessLevelAutoApprove, AccessLevelFullAccess} {
			f := newCapabilityFixture(t, level)
			binding := types.LaunchBinding{Dialect: grammar, SyntaxSupported: grammar == dialect.Bash, RuleFingerprint: "shell", ContractFingerprint: "contract", Fingerprint: "launch"}
			for _, bad := range []string{"\x1b[2K", "\u00a0", "\u200b", "\u202e", "\u2066"} {
				command := "echo '" + bad + "hidden'"
				p := ReviewBashCommandInEnvironment(config.AccessPolicyConfig{}, f.session, command, f.workspace, nil, &BashEnvironment{Launch: &binding}, nil)
				ctx := &ExecutionContext{}
				RegisterExactApproval(ctx, p.Fingerprint)
				if !p.Blocked() || HasApproval(ctx, p) || p.SecurityReview(command, nil).Decision != bashsec.ReviewBlock {
					t.Fatalf("%s/%s unsafe text bypassed block: %+v", grammar, level, p)
				}
				projection := BashPlan{UnknownDialect: true, HasConnector: true, ReviewCommand: ":"}
				if projection.SecurityReview(command, nil).Decision != bashsec.ReviewBlock {
					t.Fatal("unknown dialect or connector projection bypassed raw-text check")
				}
			}
		}
	}
}
