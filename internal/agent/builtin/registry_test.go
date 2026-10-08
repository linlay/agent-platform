package builtin

import (
	"strings"
	"testing"

	agentcoder "agent-platform/internal/agent/coder"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestTeamRegistration(t *testing.T) {
	descriptor, ok := Lookup(" team ")
	if !ok || descriptor.Mode != agentteam.Mode || descriptor.MainStage != agentteam.MainStage || descriptor.CreatePrefix != "" {
		t.Fatalf("unexpected TEAM descriptor %#v, ok=%t", descriptor, ok)
	}
	descriptor.Profile.ToolNames[0] = "changed"
	second, _ := Lookup(agentteam.Mode)
	if second.Profile.ToolNames[0] != agentteam.ToolDelegate {
		t.Fatalf("Lookup leaked descriptor mutation %#v", second.Profile.ToolNames)
	}

	spec, ok := MainSystemInitSpec(agentteam.Mode)
	if !ok || spec.CacheKey != agentteam.MainCacheKey || !spec.Initial {
		t.Fatalf("unexpected TEAM system-init spec %#v, ok=%t", spec, ok)
	}
	if prompt := ConfiguredSystemPrompt(agentteam.Mode, "coder", "kbase"); !strings.Contains(prompt, "hidden coordinator") {
		t.Fatalf("unexpected TEAM configured prompt %q", prompt)
	}
}

func TestCoderProfileDoesNotGrantTools(t *testing.T) {
	descriptor, ok := Lookup(agentcoder.Mode)
	if !ok || len(descriptor.Profile.ToolNames) != 0 {
		t.Fatalf("CODER profile grants tools: %#v", descriptor.Profile.ToolNames)
	}
}

func TestRenderTeamSystemPrompt(t *testing.T) {
	got := RenderSystemPrompt(contracts.QuerySession{
		Mode:             agentteam.Mode,
		TeamID:           "team-1",
		ModeSystemPrompt: "team={{team_id}} tools={{available_tools}} request={{user_request}}",
	}, api.QueryRequest{Message: "hello"}, agentteam.DefaultToolNames(), agentteam.MainStage)
	for _, value := range []string{"team=team-1", agentteam.ToolDelegate, contracts.PlanAddTasksToolName, "request=hello"} {
		if !strings.Contains(got, value) {
			t.Fatalf("rendered prompt %q missing %q", got, value)
		}
	}
}

func TestPlanningModeIsANativeCapabilityNotAMode(t *testing.T) {
	for _, mode := range []string{"GENERAL", "REACT", "CODER", "KBASE"} {
		if !PlanningModeSupported(mode) || !PlanningModeEnabled(mode, true) || PlanningModeEnabled(mode, false) {
			t.Fatalf("%s must support planning only when requested", mode)
		}
		if !NativePlanning(mode, "") || NativePlanning(mode, "codex") {
			t.Fatalf("%s planning is platform-run only without an ACP bridge", mode)
		}
	}
	for _, mode := range []string{"TEAM", "PLAN_EXECUTE", "ONESHOT", "PROXY", "CHANNEL", ""} {
		if PlanningModeSupported(mode) || NativePlanning(mode, "") {
			t.Fatalf("%s must not support planning", mode)
		}
	}
	answer := map[string]any{"planning": map[string]any{"decision": "approve"}}
	if !StartsNewExecutionRun("planning", answer, "GENERAL", "") || StartsNewExecutionRun("planning", answer, "TEAM", "") || StartsNewExecutionRun("planning", answer, "CODER", "codex") {
		t.Fatal("only platform-run planning hands off to a new execution Run")
	}
}
