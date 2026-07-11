package server

import (
	"strings"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
)

const hiddenTeamAgentPrefix = "__team__:"

func hiddenTeamAgentKey(teamID string) string {
	return hiddenTeamAgentPrefix + strings.TrimSpace(teamID)
}

func buildTeamCoordinatorDefinition(snapshot catalog.TeamSnapshot) catalog.AgentDefinition {
	budget := mergeTeamConfigMap(agentteam.DefaultBudget(), snapshot.Orchestrator.Budget)
	return catalog.AgentDefinition{
		Key:              hiddenTeamAgentKey(snapshot.TeamID),
		Name:             firstNonEmpty(snapshot.Name, snapshot.TeamID),
		Icon:             snapshot.Icon,
		Description:      snapshot.Description,
		Role:             "hidden Team coordinator",
		ModelKey:         snapshot.Orchestrator.ModelKey,
		ServiceTier:      snapshot.Orchestrator.ServiceTier,
		Mode:             agentteam.Mode,
		VisibilityScopes: []string{"internal"},
		Tools:            agentteam.DefaultToolNames(),
		ContextTags:      agentteam.DefaultContextTags(),
		Budget:           budget,
		StageSettings:    contracts.CloneMap(snapshot.Orchestrator.StageSettings),
	}
}

func configureTeamCoordinatorSession(session *contracts.QuerySession, snapshot catalog.TeamSnapshot) {
	if session == nil {
		return
	}
	members := make([]contracts.TeamMember, 0, len(snapshot.ValidAgentKeys))
	promptMembers := make([]agentteam.MemberSpec, 0, len(snapshot.ValidAgentKeys))
	for _, key := range snapshot.ValidAgentKeys {
		def, ok := snapshot.AgentDefinition(key)
		if !ok {
			continue
		}
		member := contracts.TeamMember{Key: key, Name: def.Name, Role: def.Role, Description: def.Description}
		members = append(members, member)
		promptMembers = append(promptMembers, agentteam.MemberSpec{Key: key, Name: def.Name, Role: def.Role, Description: def.Description})
	}
	maxParallel := agentteam.NormalizeMaxParallel(snapshot.Orchestrator.MaxParallel)
	session.RunOwner = contracts.TeamRunOwner(snapshot.TeamID, session.AgentKey)
	session.TeamRuntime = &contracts.TeamRuntimeContext{
		RuntimeMode:             snapshot.RuntimeMode,
		MaxParallel:             maxParallel,
		Members:                 members,
		RosterFingerprint:       snapshot.RosterFingerprint,
		ToolSchemaFingerprint:   snapshot.ToolSchemaFingerprint,
		OrchestratorFingerprint: snapshot.OrchestratorFingerprint,
	}
	session.ModeToolDefinitions = agentteam.HiddenToolDefinitions(promptMembers, maxParallel)
	session.ModeSystemPrompt = agentteam.BuildSystemPrompt(agentteam.PromptConfig{
		TeamID:       snapshot.TeamID,
		TeamName:     snapshot.Name,
		Description:  snapshot.Description,
		Members:      promptMembers,
		SoulPrompt:   snapshot.SoulPrompt,
		AgentsPrompt: snapshot.AgentsPrompt,
		MaxParallel:  maxParallel,
	})
}

func mergeTeamConfigMap(base map[string]any, overlay map[string]any) map[string]any {
	out := contracts.CloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range overlay {
		if nested, ok := value.(map[string]any); ok {
			baseNested, _ := out[key].(map[string]any)
			out[key] = mergeTeamConfigMap(baseNested, nested)
			continue
		}
		out[key] = value
	}
	return out
}
