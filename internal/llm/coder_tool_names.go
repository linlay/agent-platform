package llm

import (
	"strings"

	. "agent-platform/internal/contracts"
)

func coderRuntimeToolNamesForStage(session QuerySession, stage string, toolNames []string) []string {
	out := append([]string(nil), toolNames...)
	if !strings.EqualFold(strings.TrimSpace(session.Mode), "CODER") {
		return out
	}
	stage = strings.ToLower(strings.TrimSpace(stage))
	if stage == "coder" || strings.HasPrefix(stage, "coder-execute") {
		return AppendPlanTaskToolNames(out)
	}
	return out
}
