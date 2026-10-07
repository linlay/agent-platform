package llm

import (
	agentcoder "agent-platform/internal/agent/coder"
	. "agent-platform/internal/contracts"
)

// coderRuntimeToolNamesForStage adds CODER's plan task tools to an ordinary Run.
// A Run started from a confirmed plan already had them added before the
// configured execution exclusions were applied, so adding them again here
// would undo an exclusion of those tools.
func coderRuntimeToolNamesForStage(session QuerySession, stage string, toolNames []string) []string {
	if session.ConfirmedPlanRun {
		return append([]string(nil), toolNames...)
	}
	return agentcoder.RuntimeToolNamesForStage(session.Mode, stage, toolNames)
}
