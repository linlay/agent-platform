package llm

import (
	"context"

	"agent-platform/internal/bashsec"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

// Capture the child's execution environment and invocation bytes. Re-evaluation
// substitutes only the permission level; it never borrows the parent's paths,
// approvals, credentials, editing capability, or tool admission.
func (s *llmRunStream) mailApprovalDelegator(calls ...*preparedToolInvocation) func(context.Context, string, queryinput.SubmitRequest) error {
	if s.execCtx == nil || s.engine == nil {
		return nil
	}
	engine, checker := s.engine, s.checker
	execution := *s.execCtx
	frozen := make([]preparedToolInvocation, len(calls))
	for i, c := range calls {
		frozen[i] = preparedToolInvocation{toolID: c.toolID, toolName: c.toolName, args: contracts.CloneMap(c.args), toolApprovalInputPath: c.toolApprovalInputPath}
	}
	return func(ctx context.Context, level string, submit queryinput.SubmitRequest) error {
		items, _ := queryinput.DecodeSubmitParams(submit.Params)
		for i, call := range frozen {
			approve := submit.Param != nil && submit.Param["decision"] == "approve"
			if submit.Param == nil && i < len(items) {
				approve = items[i]["decision"] == "approve" || items[i]["decision"] == "approve_rule_run"
			}
			if !approve {
				continue
			}
			exec := execution
			exec.CurrentToolID = call.toolID
			exec.CurrentToolName = call.toolName
			exec.Session.AccessLevel = level
			exec.AccessLevel = level
			control := contracts.NewRunControl(ctx, exec.Session.RunID)
			control.SetInitialAccessLevel(level)
			exec.RunControl = control
			exec.ToolApprovals = nil
			exec.BashSecurityApprovals = nil
			review := &llmRunStream{engine: engine, checker: checker, ctx: ctx, session: exec.Session, execCtx: &exec, runControl: control}
			inv := call
			inv.args = contracts.CloneMap(call.args)
			deny := func() error {
				return &contracts.RunToolError{Code: "approval_requires_human", Message: "the frozen child operation still requires human approval at the caller's current access level"}
			}
			if request, ok := review.toolApprovalRequest(&inv); ok && !review.canAutoApproveTool(request) {
				return deny()
			}
			if plan, ok := review.buildFileAccessPlan(&inv); ok {
				if plan.Blocked || review.fileAccessPlanNeedsApproval(*plan) {
					return deny()
				}
			}
			if plan := review.lookupFileWritePlan(&inv); plan != nil && review.fileWritePlanNeedsApproval(*plan) {
				return deny()
			}
			if _, pending := review.imageAccessApprovalRequest(&inv); pending {
				return deny()
			}
			if isBashTool(inv.toolName) {
				plan := review.rawBashAccessReview(&inv)
				if plan.Blocked() || plan.RequiresApproval() {
					return deny()
				}
				security := plan.SecurityReview(mapStringArg(inv.args, "command"), review.knownRuntimeVariables())
				if security.Decision == bashsec.ReviewBlock || (security.Decision == bashsec.ReviewRequiresApproval && !review.shouldAutoApproveBashSecurity(security)) {
					return deny()
				}
				if match := review.checkBashHITL(&inv); match.Intercepted && !review.shouldAutoApproveHITL(match) {
					return deny()
				}
			}
		}
		return nil
	}
}
