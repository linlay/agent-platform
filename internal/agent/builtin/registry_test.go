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

func TestProfileForAgentKeepsPlatformToolsNativeOnlyForCoder(t *testing.T) {
	native, ok := ProfileForAgent(agentcoder.Mode, "")
	if !ok || !containsToolName(native.ToolNames, "artifact_publish") {
		t.Fatalf("native CODER profile tools=%#v ok=%t, want artifact_publish", native.ToolNames, ok)
	}
	acp, ok := ProfileForAgent(agentcoder.Mode, "codex")
	if !ok || len(acp.ToolNames) != 0 {
		t.Fatalf("ACP CODER profile tools=%#v ok=%t, want none", acp.ToolNames, ok)
	}
}

func containsToolName(tools []string, want string) bool {
	for _, tool := range tools {
		if tool == want {
			return true
		}
	}
	return false
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
