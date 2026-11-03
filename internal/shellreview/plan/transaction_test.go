package plan

import (
	. "agent-platform/internal/accesspolicy"
	. "agent-platform/internal/contracts"
	"testing"
)

func TestApprovalTransactionCannotSpendOneGrantForTwoRequirements(t *testing.T) {
	first := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "first", Fingerprint: "shared"}
	second := first
	second.RuleKey = "second"
	p := Combine("command", AccessLevelAskForApproval, []BashPlan{first, second})
	ctx := &ExecutionContext{}
	RegisterExactApproval(ctx, "shared")
	if HasApproval(ctx, p) || ConsumeApproval(ctx, p) || ctx.AccessPolicyApprovals["shared"] != 1 {
		t.Fatal("partial or duplicated consumption")
	}
	RegisterExactApproval(ctx, "shared")
	if !ConsumeApproval(ctx, p) || len(ctx.AccessPolicyApprovals) != 0 {
		t.Fatal("complete transaction did not consume exactly two grants")
	}
}
func TestLaunchReservationRestoresOnlyKnownUnstartedInvocation(t *testing.T) {
	p := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "invocation", Fingerprint: "exact"}
	ctx := &ExecutionContext{}
	RegisterExactApproval(ctx, p.Fingerprint)
	reservation, ok := ReserveApproval(ctx, p)
	if !ok || HasApproval(ctx, p) {
		t.Fatal("grant not reserved")
	}
	reservation.Restore()
	reservation.Restore()
	if ctx.AccessPolicyApprovals[p.Fingerprint] != 1 {
		t.Fatal("restore duplicated exact grant")
	}
	reservation, ok = ReserveApproval(ctx, p)
	if !ok {
		t.Fatal("restored grant unavailable")
	}
	reservation.Commit()
	reservation.Restore()
	if HasApproval(ctx, p) {
		t.Fatal("started invocation refunded approval")
	}
}

func TestPendingProjectionCannotMultiplyOrRetainExactLeafGrants(t *testing.T) {
	first := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "first", Fingerprint: "shared"}
	second := first
	second.RuleKey = "second"
	p := Combine("command", AccessLevelAskForApproval, []BashPlan{first, second})
	ctx := &ExecutionContext{}
	RegisterExactApproval(ctx, "shared")
	if pending := PendingBashPlan(ctx, p); !pending.RequiresApproval() {
		t.Fatal("projection multiplied one grant across two requirements")
	}
	RegisterExactApproval(ctx, "shared")
	if !HasApproval(ctx, p) || !ConsumeApproval(ctx, p) || len(ctx.AccessPolicyApprovals) != 0 {
		t.Fatal("approved exact leaves were not spent")
	}
	if HasApproval(ctx, p) {
		t.Fatal("exact leaves reused after execution")
	}
}
func TestPartialAggregateAndLeafApprovalAreBothSpent(t *testing.T) {
	a := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "a", Fingerprint: "a"}
	b := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "b", Fingerprint: "b"}
	c := BashPlan{Decision: DecisionRequiresApproval, RuleKey: "c", Fingerprint: "c"}
	p := Combine("command", AccessLevelAskForApproval, []BashPlan{a, b, c})
	ctx := &ExecutionContext{}
	RegisterExactApproval(ctx, a.Fingerprint)
	pending := PendingBashPlan(ctx, p)
	if !pending.RequiresApproval() || len(pending.Requirements) != 2 {
		t.Fatal("missing requirements were not projected")
	}
	RegisterExactApproval(ctx, pending.Fingerprint)
	private := map[string]int{}
	TransferExactApprovals(ctx, p, private)
	TransferExactApprovals(ctx, p, private)
	if len(ctx.AccessPolicyApprovals) != 0 || len(private) != 2 {
		t.Fatal("transfer duplicated or lost exact grants")
	}
	execution := &ExecutionContext{AccessPolicyApprovals: private}
	if !HasApproval(execution, p) || !ConsumeApproval(execution, p) || len(private) != 0 {
		t.Fatal("partial aggregate approval was not consumed with its preceding leaf")
	}
}
