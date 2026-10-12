package llm

import (
	"testing"

	"agent-platform/internal/contracts"
)

func TestOrdinaryNativeRunsShareTheExecuteStage(t *testing.T) {
	for mode, stage := range map[string]string{"GENERAL": "react", "CODER": "coder", "KBASE": "kbase", "TEAM": "team"} {
		session := contracts.QuerySession{Mode: mode}
		session.ResolvedPlanningSettings.Execute.ReasoningEffort = "HIGH"
		if got := budgetStageForName(session, stage); got != "execute" {
			t.Fatalf("%s budget stage = %q, want execute", mode, got)
		}
		if got := stageSettingsForSession(session, stage).ReasoningEffort; got != "HIGH" {
			t.Fatalf("%s must read stageSettings.execute, got %q", mode, got)
		}
		if got := budgetStageForName(session, "planning"); got != "planning" {
			t.Fatalf("%s planning budget stage = %q", mode, got)
		}
	}
}
