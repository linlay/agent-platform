package policy

import (
	. "agent-platform/internal/accesspolicy"
	shellplan "agent-platform/internal/shellreview/plan"
)

const workspaceReadOnlyExecutionReason = "workspace editing is disabled: programs whose effects cannot be analyzed may not run inside the Workspace; use cwd @chat"

func (r Reviewer) Block(reason, rule, input string) shellplan.BashPlan {
	return shellplan.New(r.command, r.accessLevel, DecisionBlock, reason, rule, input)
}

func (r Reviewer) WorkspaceExecution(cwd string) shellplan.BashPlan {
	if SessionWorkspaceEditingDisabled(r.session) && PathInSessionWorkspace(r.session, cwd) {
		return r.Block(workspaceReadOnlyExecutionReason, "bash-access:workspace-readonly", cwd)
	}
	return r.Allow()
}

func (r Reviewer) Subtree(mode AccessMode, target string) shellplan.BashPlan {
	if p := BuildSubtreePlan(r.cfg, r.session, mode, target); p.Blocked() {
		return r.Block(p.Reason, "bash-access:subtree", target)
	}
	return r.Allow()
}
