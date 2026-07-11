package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/catalog"
)

func TestRegistryAcceptsOrchestratedTeamAutomationWithoutAgentKey(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "team-job.yml")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		"name: Team Job",
		"description: Run the whole Team",
		"cron: \"17 9 * * *\"",
		"teamId: research",
		"query:",
		"  message: prepare report",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	teams := fakeTeamLookup{teams: map[string]catalog.TeamSnapshot{
		"research": {
			TeamID:           "research",
			RuntimeMode:      catalog.TeamRuntimeModeOrchestrated,
			AgentKeys:        []string{"writer", "reviewer"},
			ValidAgentKeys:   []string{"writer", "reviewer"},
			InvalidAgentKeys: nil,
		},
	}}
	registry := NewRegistry(root, teams)
	def, err := registry.parseDefinition(path)
	if err != nil {
		t.Fatalf("parseDefinition: %v", err)
	}
	if def.AgentKey != "" || def.TeamID != "research" {
		t.Fatalf("unexpected definition %#v", def)
	}
	if req := def.ToQueryRequest(); req.AgentKey != "" || req.TeamID != "research" || req.Message != "prepare report" {
		t.Fatalf("unexpected query request %#v", req)
	}
	if err := registry.Persist(def); err != nil {
		t.Fatalf("persist: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "agentKey:") || !strings.Contains(string(data), "teamId: research") {
		t.Fatalf("unexpected persisted YAML:\n%s", data)
	}
}

func TestRegistryRejectsAgentBypassForOrchestratedTeamAutomation(t *testing.T) {
	registry := NewRegistry(t.TempDir(), fakeTeamLookup{teams: map[string]catalog.TeamSnapshot{
		"research": {
			TeamID:         "research",
			RuntimeMode:    catalog.TeamRuntimeModeOrchestrated,
			AgentKeys:      []string{"writer"},
			ValidAgentKeys: []string{"writer"},
		},
	}})
	err := registry.Validate(Definition{
		ID: "job", Name: "Job", Description: "desc", Cron: "17 9 * * *",
		AgentKey: "writer", TeamID: "research", Query: Query{Message: "hello"},
	})
	if err == nil || !strings.Contains(err.Error(), "must be omitted") {
		t.Fatalf("expected orchestrated Team agentKey rejection, got %v", err)
	}
}
