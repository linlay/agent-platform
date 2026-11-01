package plan

import (
	"testing"

	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
)

func TestMixedRunRuleAndExactApprovalAudit(t *testing.T) {
	ctx := &ExecutionContext{}
	first := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "first", Fingerprint: "1"}
	second := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "second", Fingerprint: "2"}
	third := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "third", Fingerprint: "3"}
	combined := Combine("three commands", AccessLevelAskForApproval, []BashPlan{first, second, third})
	RegisterRuleApproval(ctx, first.RuleKey)
	pending := PendingBashPlan(ctx, combined)
	RegisterExactApproval(ctx, pending.Fingerprint)
	if source := BashApprovalSource(ctx, combined); source != "exact" {
		t.Fatalf("mixed approval source missing: %s", source)
	}
}
