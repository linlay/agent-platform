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
	return r.Walk(mode, target, nil)
}

// Walk is Subtree for a traversal that provably passes by the protected
// roots skip reports.
func (r Reviewer) Walk(mode AccessMode, target string, skip func(target, root string) bool) shellplan.BashPlan {
	if p := BuildWalkPlan(r.cfg, r.session, mode, target, skip); p.Blocked() {
		return r.Block(p.Reason, "bash-access:subtree", target)
	}
	return r.Allow()
}
