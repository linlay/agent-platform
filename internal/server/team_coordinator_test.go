package server

import (
	"net/http"
	"strings"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/i18n"
	toolruntime "agent-platform/internal/tools"
)

type fixedTeamRegistry struct {
	testCatalogRegistry
	team   catalog.AgentDefinition
	agents map[string]catalog.AgentDefinition
}

func (r fixedTeamRegistry) DefaultAgentKey() string { return "writer" }
func (r fixedTeamRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if key == r.team.Key && key != "" {
		return r.team, true
	}
	def, ok := r.agents[key]
	return def, ok
}

func (r fixedTeamRegistry) ResolveTeam(key string) (catalog.TeamSnapshot, bool) {

	return catalog.NewTeamSnapshot(r.team, r.agents), true
}

func orchestratedTeamTestRegistry() fixedTeamRegistry {
	return fixedTeamRegistry{
		team: catalog.AgentDefinition{
			Name: "Research", Description: "Research team",

			ModelKey: "mock-model",

			SoulPrompt: "Be precise.", Key: "research", Mode: "TEAM", TeamConfig: &catalog.TeamConfig{Members: []string{"writer", "reviewer"}, MaxParallel: 2},
		},
		agents: map[string]catalog.AgentDefinition{
			"writer":   {Key: "writer", Name: "Writer", Role: "draft", Description: "writes drafts", Mode: "REACT"},
			"reviewer": {Key: "reviewer", Name: "Reviewer", Role: "review", Description: "reviews drafts", Mode: "REACT"},
		},
	}
}

func TestResolveTEAMByAgentKeyAndEnforceOwner(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	key, snapshot, err := resolveAgentTarget(registry, "research", nil)
	if err != nil || key != "research" || snapshot == nil {
		t.Fatalf("resolution: %s %#v %v", key, snapshot, err)
	}
	_, _, err = resolveAgentTarget(registry, "writer", &chat.Summary{AgentKey: "research"})
	if err == nil || err.Status != 409 {
		t.Fatalf("owner mismatch: %v", err)
	}
}

func TestResolveAgentTargetInheritsExistingNonTeamAgent(t *testing.T) {
	registry := fixedTeamRegistry{
		agents: map[string]catalog.AgentDefinition{
			"owner":  {Key: "owner", Mode: "REACT"},
			"writer": {Key: "writer", Mode: "CHANNEL"},
		},
	}
	agentKey, snapshot, statusErr := resolveAgentTarget(registry, "", &chat.Summary{AgentKey: "owner"})
	if statusErr != nil {
		t.Fatalf("resolveAgentTarget error: %v", statusErr)
	}
	if agentKey != "owner" || snapshot != nil {
		t.Fatalf("expected non-Team chat owner to be inherited, got team=%q agent=%q snapshot=%#v", "", agentKey, snapshot)
	}
}

func TestResolveAgentTargetRejectsUnrunnableMemberBeforeStartingRun(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	member := registry.agents["reviewer"]
	member.Mode = "UNSUPPORTED"
	registry.agents["reviewer"] = member
	_, _, statusErr := resolveAgentTarget(registry, "research", nil)
	if statusErr == nil || statusErr.Status != http.StatusServiceUnavailable || !strings.Contains(statusErr.Message, "reviewer") {
		t.Fatalf("expected unrunnable member rejection, got %#v", statusErr)
	}
}

func TestPrepareQueryAdmissionUsesPublicTEAMAgent(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	server := &Server{deps: Dependencies{Registry: registry}}
	req := api.QueryRequest{AgentKey: "research", Message: "compare approaches"}

	admission, err := server.prepareQueryAdmissionRequest(t.Context(), req, true, i18n.DefaultLocale, "http://example.com")
	if err != nil {
		t.Fatalf("prepareQueryAdmissionRequest: %v", err)
	}
	if !admission.OrchestratedTeam || admission.Req.AgentKey != "research" || admission.AgentDef.Mode != agentteam.Mode {
		t.Fatalf("unexpected Team admission %#v", admission)
	}
	if admission.AgentDef.Key != "research" || admission.AgentDef.ModelKey != "mock-model" {
		t.Fatalf("unexpected coordinator definition %#v", admission.AgentDef)
	}
	if len(admission.AgentDef.Tools) != 0 {
		t.Fatalf("unexpected coordinator default tools %#v", admission.AgentDef.Tools)
	}
	if _, visible := registry.AgentDefinition(admission.AgentDef.Key); !visible {
		t.Fatal("synthetic coordinator leaked into Agent registry")
	}
}

func TestConfigureTeamCoordinatorSessionAddsOwnerPromptAndLocalTools(t *testing.T) {
	registry := orchestratedTeamTestRegistry()
	snapshot, _ := registry.ResolveTeam("research")
	session := contracts.QuerySession{AgentKey: "research", Mode: agentteam.Mode}
	definitions, err := toolruntime.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatalf("load embedded tools: %v", err)
	}
	baseTool, ok := teamDelegateBaseDefinition(definitions)
	if !ok {
		t.Fatal("embedded agent_delegate definition is unavailable")
	}
	if err := configureTeamCoordinatorSession(&session, snapshot, baseTool); err != nil {
		t.Fatalf("configureTeamCoordinatorSession: %v", err)
	}

	owner := contracts.ResolveRunOwner(session.RunOwner)
	if owner.AgentKey != "research" {
		t.Fatalf("unexpected owner %#v", owner)
	}
	if session.TeamRuntime == nil || len(session.TeamRuntime.Members) != 2 || session.TeamRuntime.MaxParallel != 2 {
		t.Fatalf("unexpected Team runtime %#v", session.TeamRuntime)
	}
	if len(session.ModeToolDefinitions) != 1 || session.ModeToolDefinitions[0].Name != agentteam.ToolDelegate {
		t.Fatalf("unexpected local tools %#v", session.ModeToolDefinitions)
	}
	parameters := session.ModeToolDefinitions[0].Parameters
	tasks, _ := parameters["properties"].(map[string]any)["tasks"].(map[string]any)
	items, _ := tasks["items"].(map[string]any)
	properties, _ := items["properties"].(map[string]any)
	agentKey, _ := properties["agentKey"].(map[string]any)
	enum, _ := agentKey["enum"].([]string)
	if tasks["maxItems"] != 2 || len(enum) != 2 {
		t.Fatalf("dynamic delegate schema was not frozen to roster: %#v", parameters)
	}
	for _, required := range []string{"agentKey=writer", "agentKey=reviewer"} {
		if !strings.Contains(session.ModeSystemPrompt, required) {
			t.Fatalf("Team prompt missing %q:\n%s", required, session.ModeSystemPrompt)
		}
	}
}

var _ catalog.Registry = fixedTeamRegistry{}
var _ catalog.TeamResolver = fixedTeamRegistry{}

func TestTEAMAdmissionValidatesEveryMember(t *testing.T) {
	for _, kind := range []string{"missing", "team", "acp", "nested-invoke", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			r := orchestratedTeamTestRegistry()
			member := r.agents["reviewer"]
			switch kind {
			case "team":
				member.Mode = "TEAM"
			case "acp":
				member.Engine = "acp"
			case "nested-invoke":
				member.Tools = []string{"agent_invoke"}
			case "unsupported":
				member.Mode = "CHANNEL"
			}
			r.agents["reviewer"] = member
			if kind == "missing" {
				delete(r.agents, "reviewer")
			}
			_, _, err := resolveAgentTarget(r, "research", nil)
			if err == nil || err.Status != 503 {
				t.Fatalf("admission=%v", err)
			}
		})
	}
}
func TestTEAMPlanningDoesNotInjectDelegate(t *testing.T) {
	r := orchestratedTeamTestRegistry()
	snapshot, _ := r.ResolveTeam("research")
	definitions, err := toolruntime.LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	base, _ := teamDelegateBaseDefinition(definitions)
	session := contracts.QuerySession{AgentKey: "research", Mode: "TEAM", PlanningMode: true, ToolNames: []string{"file_read"}}
	if err := configureTeamCoordinatorSession(&session, snapshot, base); err != nil {
		t.Fatal(err)
	}
	if len(session.ModeToolDefinitions) != 0 || len(session.ToolNames) != 1 || session.ToolNames[0] != "file_read" {
		t.Fatalf("planning tools=%#v", session)
	}
}
