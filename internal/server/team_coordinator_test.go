package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

type fixedTeamRegistry struct {
	testCatalogRegistry
	team   catalog.TeamDefinition
	agents map[string]catalog.AgentDefinition
}

func (r fixedTeamRegistry) DefaultAgentKey() string { return "writer" }
func (r fixedTeamRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := r.agents[key]
	return def, ok
}
func (r fixedTeamRegistry) TeamDefinition(teamID string) (catalog.TeamDefinition, bool) {
	if teamID != r.team.TeamID {
		return catalog.TeamDefinition{}, false
	}
	return r.team, true
}
func (r fixedTeamRegistry) ResolveTeam(teamID string) (catalog.TeamSnapshot, bool) {
	if teamID != r.team.TeamID {
		return catalog.TeamSnapshot{}, false
	}
	return catalog.NewTeamSnapshot(r.team, r.agents), true
}

func orchestratedTeamTestRegistry() fixedTeamRegistry {
	return fixedTeamRegistry{
		team: catalog.TeamDefinition{
			TeamID: "research", Name: "Research", Description: "Research team",
			RuntimeMode: catalog.TeamRuntimeModeOrchestrated,
			AgentKeys:   []string{"writer", "reviewer"},
			Orchestrator: catalog.TeamOrchestratorConfig{
				ModelKey: "mock-model", MaxParallel: 2,
			},
			SoulPrompt: "Be precise.",
		},
		agents: map[string]catalog.AgentDefinition{
			"writer":   {Key: "writer", Name: "Writer", Role: "draft", Description: "writes drafts", Mode: "REACT"},
			"reviewer": {Key: "reviewer", Name: "Reviewer", Role: "review", Description: "reviews drafts", Mode: "REACT"},
		},
	}
}

func TestResolveQueryTeamOrchestratedNeverSelectsDefaultMember(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	teamID, agentKey, snapshot, statusErr := resolveQueryTeam(registry, "research", "", nil)
	if statusErr != nil {
		t.Fatalf("resolveQueryTeam error: %v", statusErr)
	}
	if teamID != "research" || agentKey != "" || snapshot == nil || snapshot.RuntimeMode != catalog.TeamRuntimeModeOrchestrated {
		t.Fatalf("unexpected resolution team=%q agent=%q snapshot=%#v", teamID, agentKey, snapshot)
	}

	_, _, _, statusErr = resolveQueryTeam(registry, "research", "writer", nil)
	if statusErr == nil || statusErr.status != http.StatusBadRequest || !strings.Contains(statusErr.message, "must be omitted") {
		t.Fatalf("expected agentKey bypass rejection, got %#v", statusErr)
	}
}

func TestResolveQueryTeamRejectsUnrunnableMemberBeforeStartingRun(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	member := registry.agents["reviewer"]
	member.Mode = "UNSUPPORTED"
	registry.agents["reviewer"] = member
	_, _, _, statusErr := resolveQueryTeam(registry, "research", "", nil)
	if statusErr == nil || statusErr.status != http.StatusServiceUnavailable || !strings.Contains(statusErr.message, "reviewer") {
		t.Fatalf("expected unrunnable member rejection, got %#v", statusErr)
	}
}

func TestPrepareQueryAdmissionSynthesizesHiddenTeamCoordinator(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	server := &Server{deps: Dependencies{Registry: registry}}
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"teamId":"research","message":"compare approaches"}`))
	req.Header.Set("Content-Type", "application/json")

	admission, err := server.prepareQueryAdmission(req, true)
	if err != nil {
		t.Fatalf("prepareQueryAdmission: %v", err)
	}
	if !admission.orchestratedTeam || admission.req.AgentKey != "" || admission.agentDef.Mode != agentteam.Mode {
		t.Fatalf("unexpected Team admission %#v", admission)
	}
	if admission.agentDef.Key != hiddenTeamAgentKey("research") || admission.agentDef.ModelKey != "mock-model" {
		t.Fatalf("unexpected coordinator definition %#v", admission.agentDef)
	}
	if _, visible := registry.AgentDefinition(admission.agentDef.Key); visible {
		t.Fatal("synthetic coordinator leaked into Agent registry")
	}
}

func TestConfigureTeamCoordinatorSessionAddsOwnerPromptAndLocalTools(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	snapshot, _ := registry.ResolveTeam("research")
	session := contracts.QuerySession{AgentKey: hiddenTeamAgentKey("research"), TeamID: "research", Mode: agentteam.Mode}
	configureTeamCoordinatorSession(&session, snapshot)

	owner := contracts.ResolveRunOwner(session.RunOwner, session.AgentKey, session.TeamID)
	if !owner.IsTeam() || owner.TeamID != "research" || owner.AgentKey != "" || owner.ExecutionAgentKey != hiddenTeamAgentKey("research") {
		t.Fatalf("unexpected owner %#v", owner)
	}
	if session.TeamRuntime == nil || len(session.TeamRuntime.Members) != 2 || session.TeamRuntime.MaxParallel != 2 {
		t.Fatalf("unexpected Team runtime %#v", session.TeamRuntime)
	}
	if len(session.ModeToolDefinitions) != 2 || session.ModeToolDefinitions[0].Name != agentteam.ToolDelegate {
		t.Fatalf("unexpected local tools %#v", session.ModeToolDefinitions)
	}
	for _, required := range []string{"memberKey=writer", "memberKey=reviewer", "Be precise."} {
		if !strings.Contains(session.ModeSystemPrompt, required) {
			t.Fatalf("Team prompt missing %q:\n%s", required, session.ModeSystemPrompt)
		}
	}
}

var _ catalog.Registry = fixedTeamRegistry{}
var _ catalog.TeamResolver = fixedTeamRegistry{}
