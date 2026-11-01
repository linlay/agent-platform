package llm

import (
	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	shellplan "agent-platform/internal/shellreview/plan"
	"context"
	"testing"
)

type changingBashReviewExecutor struct {
	stubToolExecutor
	plan shellplan.BashPlan
}

func (e *changingBashReviewExecutor) ReviewBashAccess(context.Context, map[string]any, *contracts.ExecutionContext, config.AccessPolicyConfig) shellplan.BashPlan {
	return e.plan
}

func TestSandboxBashAuthorizationCannotBorrowSiblingExactGrant(t *testing.T) {
	s, _, _ := newApprovedBashStream(t, 2)
	s.session.AgentHasRuntimeSandbox = true
	s.execCtx.Session.AgentHasRuntimeSandbox = true
	p := shellplan.New("opaque", contracts.AccessLevelAskForApproval, accesspolicy.DecisionRequiresApproval, "unknown behavior", "bash-access:invocation", "same")
	s.engine.tools = &changingBashReviewExecutor{plan: p}
	shellplan.RegisterExactApproval(s.execCtx, p.Fingerprint)
	first, second := s.queuedToolCalls[0], s.queuedToolCalls[1]
	if !s.usesBashAuthorization(first) {
		t.Fatal("sandbox bypassed invocation ownership")
	}
	if request := s.prepareBashAuthorization(first); request != nil {
		t.Fatal("first invocation lost its grant")
	}
	if len(first.bashAuthorization.access) != 1 {
		t.Fatal("first grant was not reserved")
	}
	if request := s.prepareBashAuthorization(second); request == nil {
		t.Fatal("sibling borrowed first invocation's grant")
	}
	if len(second.bashAuthorization.access) != 0 || len(s.execCtx.AccessPolicyApprovals) != 0 {
		t.Fatal("exact grant leaked across sandbox invocations")
	}
}
