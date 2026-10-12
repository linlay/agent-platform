package llm

import (
	"agent-platform/internal/api"
	"context"
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

func TestCurrentRunModelOptionsUsesEffectiveStage(t *testing.T) {
	engine := newTraceTestEngine(t, t.TempDir(), "https://example.invalid", nil)
	for _, mode := range []string{"GENERAL", "CODER", "KBASE", "TEAM"} {
		session := contracts.QuerySession{Mode: mode, ModelKey: "configured"}
		session.ResolvedPlanningSettings.Execute = contracts.StageSettings{ReasoningEnabled: true, ReasoningEffort: "XHIGH"}
		session.ResolvedPlanningSettings.Planning = contracts.StageSettings{ReasoningEnabled: false, ReasoningEffort: "HIGH"}
		for _, tc := range []struct{ stage, effort string }{{"execute", "XHIGH"}, {"planning", "NONE"}} {
			exec := &contracts.ExecutionContext{}
			_, err := engine.newRunStreamWithOptions(context.Background(), api.QueryRequest{}, session, true, runStreamOptions{
				Stage: tc.stage, ModelKey: "mock-model", ExecCtx: exec, EstimateOnly: true,
				Messages: []openAIMessage{{Role: "user", Content: "task"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := exec.CurrentModel
			if got == nil {
				t.Fatal("effective model missing from tool execution context")
			}
			if got.Key != "mock-model" || got.ReasoningEffort != tc.effort {
				t.Fatalf("%s/%s: %#v", mode, tc.stage, got)
			}
		}
	}
}
