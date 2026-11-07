package catalog

import (
	"agent-platform/internal/interaction"
	"testing"
)

func TestBotOnlySupportsNative(t *testing.T) {
	mode, engine, err := ParseAgentModeAndEngine("bot", "native")
	if err != nil || mode != AgentModeBot || engine != AgentEngineNative {
		t.Fatalf("%s/%s %v", mode, engine, err)
	}
	if _, _, err := ParseAgentModeAndEngine("BOT", "acp"); err == nil {
		t.Fatal("ACP BOT accepted")
	}
	if err := ValidateAgentCoderBackend(AgentDefinition{Mode: AgentModeBot, ProxyConfig: &ProxyConfig{}}); err == nil {
		t.Fatal("proxy BOT accepted")
	}
	cfg := interaction.DefaultsForEngine("BOT", false)
	if cfg.ShowPlanningMode || cfg.ShowTerminal {
		t.Fatal("BOT offers planning or terminal")
	}
}

func TestBotCannotBeInlineChildOrTeamMember(t *testing.T) {
	bot := AgentDefinition{Key: "bot", Mode: AgentModeBot, VisibilityScopes: []string{"invoke"}}
	if err := AgentInvocationError(bot); err == nil {
		t.Fatal("BOT admitted as inline child")
	}
	registry := &FileRegistry{agents: map[string]AgentDefinition{"bot": bot}}
	if err := registry.validateBotTeamMembers(map[string]any{"key": "team", "mode": "TEAM", "teamConfig": map[string]any{"members": []any{"bot"}}}); err == nil {
		t.Fatal("BOT accepted in TEAM configuration")
	}
	snapshot := NewTeamSnapshot(AgentDefinition{Key: "team", Mode: "TEAM", TeamConfig: &TeamConfig{Members: []string{"bot"}, MaxParallel: 1}}, map[string]AgentDefinition{"bot": bot})
	if snapshot.HasAgent("bot") || len(snapshot.InvalidAgentKeys) != 1 {
		t.Fatalf("BOT published as available TEAM member: %+v", snapshot)
	}
}
