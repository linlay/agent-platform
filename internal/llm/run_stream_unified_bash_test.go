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

type openingSandboxExecutor struct{ stubToolExecutor }

func (*openingSandboxExecutor) ReviewBashAccess(context.Context, map[string]any, *contracts.ExecutionContext, config.AccessPolicyConfig) shellplan.BashPlan {
	return shellplan.BashPlan{Decision: accesspolicy.DecisionAllow}
}
func (*openingSandboxExecutor) SupportsToolOutput(string, *contracts.ExecutionContext) bool {
	return false
}
func (*openingSandboxExecutor) Invoke(_ context.Context, _ string, _ map[string]any, ctx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	ctx.SandboxSession = &contracts.SandboxSession{SessionID: "opened-after-approval"}
	ctx.AccessPolicyApprovals["private-only"] = 1
	return contracts.ToolExecutionResult{Output: "done"}, nil
}
func TestSandboxSessionOpenedInPrivateInvocationReturnsToRunForCleanup(t *testing.T) {
	execution := &contracts.ExecutionContext{Session: contracts.QuerySession{AgentHasRuntimeSandbox: true}, AccessPolicyApprovals: map[string]int{"parent-only": 1}}
	s := &llmRunStream{ctx: context.Background(), execCtx: execution, engine: &LLMAgentEngine{tools: &openingSandboxExecutor{}}}
	invocation := &preparedToolInvocation{toolID: "tool", toolName: "bash", args: map[string]any{"command": "echo done"}, bashAuthorization: &bashAuthorization{toolID: "tool", access: map[string]int{}}}
	if err := s.invokeToolAndPublishResult(invocation); err != nil {
		t.Fatal(err)
	}
	if execution.SandboxSession == nil || execution.SandboxSession.SessionID != "opened-after-approval" {
		t.Fatal("Run lost the session opened after authorization")
	}
	if len(execution.AccessPolicyApprovals) != 1 || execution.AccessPolicyApprovals["parent-only"] != 1 {
		t.Fatal("returning session state merged private approval grants")
	}
}
