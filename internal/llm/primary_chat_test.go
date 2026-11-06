package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"strings"
	"testing"
)

func TestPrimaryPromptExcludesExecutionInstructions(t *testing.T) {
	session := contracts.QuerySession{PrimaryChat: true, Mode: "GENERAL", AgentName: "Scheduler", ModeSystemPrompt: "Coordinate workers", SoulPrompt: "Be clear", CapabilityPrompts: []string{"Tools: bash"}, AgentsPrompt: "AGENT_SECRET", WorkspaceAgentsPrompt: "WORKSPACE_SECRET", SkillCatalogPrompt: "SKILL_SECRET", ConnectorCatalogPrompt: "CONNECTOR_SECRET"}
	prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{StageSystemPrompt: "STAGE_SECRET"})
	for _, secret := range []string{"AGENT_SECRET", "WORKSPACE_SECRET", "SKILL_SECRET", "CONNECTOR_SECRET", "STAGE_SECRET"} {
		if strings.Contains(prompt, secret) {
			t.Fatal("execution prompt leaked", secret)
		}
	}
	for _, text := range []string{"Coordinate workers", "Scheduler", "Be clear", "Tools: bash", "mode: GENERAL"} {
		if !strings.Contains(prompt, text) {
			t.Fatal("missing scheduling context", text)
		}
	}
}
