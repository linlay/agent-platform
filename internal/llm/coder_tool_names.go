package llm

import (
	agentcoder "agent-platform/internal/agent/coder"
	. "agent-platform/internal/contracts"
)

func coderRuntimeToolNamesForStage(session QuerySession, stage string, toolNames []string) []string {
	return agentcoder.RuntimeToolNamesForStage(session.Mode, stage, toolNames)
}
