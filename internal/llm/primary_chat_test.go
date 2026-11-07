package llm

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"strings"
	"testing"
)

func TestPrimaryPromptExcludesExecutionInstructions(t *testing.T) {
	session := contracts.QuerySession{PrimaryChat: true, PrimaryChatProtocol: "BUILTIN_PROTOCOL", Mode: "BOT", AgentName: "Scheduler", ModeSystemPrompt: "Coordinate workers", SoulPrompt: "Be clear", CapabilityPrompts: []string{"Tools: bash"}, AgentsPrompt: "AGENT_SECRET", WorkspaceAgentsPrompt: "WORKSPACE_SECRET", SkillCatalogPrompt: "SKILL_SECRET", ConnectorCatalogPrompt: "CONNECTOR_SECRET"}
	session.PromptAppend.Announce.InstructionsPrompt = "Call {{announce_tool_name}} first."
	prompt := buildSystemPrompt(session, api.QueryRequest{}, "", PromptBuildOptions{StageSystemPrompt: "STAGE_SECRET", ToolDefinitions: []api.ToolDetailResponse{{Name: "action_announce", Meta: map[string]any{"activityAnnounce": "message"}}}})
	for _, secret := range []string{"AGENT_SECRET", "WORKSPACE_SECRET", "SKILL_SECRET", "CONNECTOR_SECRET", "STAGE_SECRET"} {
		if strings.Contains(prompt, secret) {
			t.Fatal("execution prompt leaked", secret)
		}
	}
	for _, text := range []string{"Coordinate workers", "Scheduler", "Be clear", "Tools: bash", "mode: BOT", "BUILTIN_PROTOCOL", "Call action_announce first."} {
		if !strings.Contains(prompt, text) {
			t.Fatal("missing scheduling context", text)
		}
	}
}

func TestPrimaryToolDescriptionsDoNotRequireUnavailableSkillReads(t *testing.T) {
	defs := []api.ToolDetailResponse{}
	for _, name := range contracts.PrimaryChatTools() {
		defs = append(defs, api.ToolDetailResponse{Name: name, Description: "Read the task-control skill before use.", Parameters: map[string]any{"properties": map[string]any{
			"accessLevel": map[string]any{"description": "target default"}, "modelKey": map[string]any{"description": "caller model"}, "reasoningEffort": map[string]any{"description": "caller effort"}, "args": map[string]any{"description": "See its skill."},
		}}})
	}
	session := contracts.QuerySession{PrimaryChat: true, Mode: "BOT", AgentKey: "a", RunID: "r", ChatID: "c", RunOwner: contracts.AgentRunOwner("a")}
	primary := effectiveToolDefinitions(defs, contracts.PrimaryChatTools(), session)
	if len(primary) != 6 {
		t.Fatalf("primary tools missing: %+v", primary)
	}
	for _, def := range primary {
		if def.Description == "" || strings.Contains(def.Description, "Read the task-control skill") {
			t.Fatal("unavailable skill prerequisite", def.Name)
		}
		properties := def.Parameters["properties"].(map[string]any)
		if def.Name == "chat_send" && !strings.Contains(properties["accessLevel"].(map[string]any)["description"].(string), "default or omission inherits") {
			t.Fatal("wrong primary default selector contract")
		}
		if def.Name == "chat_query" && strings.Contains(properties["args"].(map[string]any)["description"].(string), "See its skill") {
			t.Fatal("query arguments still require skill read")
		}
	}
	session.PrimaryChat = false
	session.NativeConnectorTools = map[string]string{}
	for _, name := range contracts.PrimaryChatTools() {
		session.NativeConnectorTools[name] = "builtin.task-control"
	}
	session.ConnectorDirs = map[string]string{"builtin.task-control": "/mounted"}
	ordinary := effectiveToolDefinitions(defs, contracts.PrimaryChatTools(), session)
	if len(ordinary) != 6 {
		t.Fatal("ordinary tools missing")
	}
	for _, def := range ordinary {
		if def.Description != "Read the task-control skill before use." {
			t.Fatal("primary contract leaked to ordinary session", def.Name)
		}
		if def.Parameters["properties"].(map[string]any)["accessLevel"].(map[string]any)["description"] != "target default" {
			t.Fatal("catalog schema mutated")
		}
	}
}
