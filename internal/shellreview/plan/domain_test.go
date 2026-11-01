package plan

import (
	"testing"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

func TestExactApprovalCannotCrossRequirementOrAccessLevel(t *testing.T) {
	ctx := &ExecutionContext{}
	first := ForAction("same command", AccessLevelAutoApprove, "hitl", "remote", "bash-access:remote")
	RegisterExactApproval(ctx, first.Fingerprint)
	for _, other := range []BashPlan{
		ForAction("same command", AccessLevelAutoApprove, "hitl", "destructive", "bash-access:destructive"),
		ForAction("same command", AccessLevelAskForApproval, "hitl", "remote", "bash-access:remote"),
	} {
		if HasApproval(ctx, other) {
			t.Fatalf("exact grant crossed a requirement/level boundary: %+v", other)
		}
	}
}
func TestLaunchCapabilitiesBindVisibleFactsWhenBackendHashesStayEqual(t *testing.T) {
	p := New("same command", AccessLevelAskForApproval, DecisionRequiresApproval, "path", "bash-access:path", "input")
	first := types.LaunchBinding{Executable: "/shell", Template: []string{"-c", "{{command}}"}, Dialect: dialect.Bash, SyntaxSupported: true, RuleFingerprint: "opaque-rule", ContractFingerprint: "opaque-contract", Fingerprint: "opaque-call"}
	approved := BindLaunch(p, first)
	ctx := &ExecutionContext{}
	RegisterExactApproval(ctx, approved.Fingerprint)
	RegisterRuleApproval(ctx, approved.RuleKey)
	for _, change := range []func(*types.LaunchBinding){
		func(b *types.LaunchBinding) { b.Dialect = dialect.PowerShell },
		func(b *types.LaunchBinding) { b.SyntaxSupported = false },
		func(b *types.LaunchBinding) { b.Executable = "/other-shell" },
		func(b *types.LaunchBinding) { b.Template = []string{"-lc", "{{command}}"} },
	} {
		modified := first
		change(&modified)
		changed := BindLaunch(p, modified)
		if approved.Fingerprint == changed.Fingerprint || approved.RuleKey == changed.RuleKey || HasApproval(ctx, changed) {
			t.Fatal("a changed launch contract reused authority through stale backend hashes")
		}
	}
}
