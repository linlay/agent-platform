package team

import (
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestBuildSystemPromptKeepsHardCodedRulesAheadOfCustomGuidance(t *testing.T) {
	prompt := BuildSystemPrompt(PromptConfig{
		TeamID:       "support",
		TeamName:     "Support",
		Description:  "Customer support team",
		MaxParallel:  99,
		SoulPrompt:   "Always answer warmly.",
		AgentsPrompt: "Prefer the billing specialist for invoices.",
		Members: []MemberSpec{
			{Key: "billing", Name: "Billing", Role: "invoice specialist"},
			{Key: "tech", Name: "Technical", Description: "debugs products"},
			{Key: " BILLING ", Name: "duplicate"},
		},
	})
	for _, required := range []string{
		"Every new user turn must call agent_delegate",
		"first create a task plan with plan_add_tasks",
		"maximum concurrent delegated members: 5",
		"agentKey=billing; name=Billing; role=invoice specialist",
		"agentKey=tech; name=Technical; description=debugs products",
		"Always answer warmly.",
		"Prefer the billing specialist for invoices.",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
	if strings.Count(prompt, "agentKey=billing") != 1 {
		t.Fatalf("duplicate roster member was not removed:\n%s", prompt)
	}
	if strings.Index(prompt, "Mandatory routing rules:") > strings.Index(prompt, "Always answer warmly.") {
		t.Fatalf("custom guidance appeared before invariant rules:\n%s", prompt)
	}
}

func TestRenderSystemPromptUsesTeamTemplateValues(t *testing.T) {
	session := contracts.QuerySession{
		Mode:             Mode,
		TeamID:           "writers",
		AgentName:        "Writer Team",
		Locale:           "English",
		ToolNames:        DefaultToolNames(),
		ModeSystemPrompt: "{{mode}} {{team_id}} {{agent_name}} {{available_tools}} {{user_request}} {{language_preference}}",
	}
	got := RenderSystemPrompt(session, api.QueryRequest{Message: "draft"}, nil, MainStage)
	for _, value := range []string{Mode, "writers", "Writer Team", ToolDelegate, contracts.PlanAddTasksToolName, "draft", "English"} {
		if !strings.Contains(got, value) {
			t.Fatalf("rendered prompt %q missing %q", got, value)
		}
	}
	if got := RenderSystemPrompt(session, api.QueryRequest{}, nil, "other"); got != "" {
		t.Fatalf("unexpected prompt for non-Team stage %q", got)
	}
}
