package query

import (
	"fmt"
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func TeamDelegateBaseDefinition(definitions []queryinput.ToolDefinition) (queryinput.ToolDefinition, bool) {
	for _, definition := range definitions {
		if strings.EqualFold(strings.TrimSpace(definition.Name), agentbuiltin.TeamToolDelegate) ||
			strings.EqualFold(strings.TrimSpace(definition.Key), agentbuiltin.TeamToolDelegate) {
			return definition, true
		}
	}
	return queryinput.ToolDefinition{}, false
}

func ConfigureTeamCoordinatorSession(session *contracts.QuerySession, snapshot catalog.TeamSnapshot, baseTool queryinput.ToolDefinition) error {
	if session == nil {
		return nil
	}
	members := make([]contracts.TeamMember, 0, len(snapshot.ValidAgentKeys))
	promptMembers := make([]agentbuiltin.TeamMemberSpec, 0, len(snapshot.ValidAgentKeys))
	for _, key := range snapshot.ValidAgentKeys {
		def, ok := snapshot.AgentDefinition(key)
		if !ok {
			continue
		}
		member := contracts.TeamMember{Key: key, Name: def.Name, Role: def.Role, Description: def.Description}
		members = append(members, member)
		promptMembers = append(promptMembers, agentbuiltin.TeamMemberSpec{Key: key, Name: def.Name, Role: def.Role, Description: def.Description})
	}
	maxParallel := agentbuiltin.TeamNormalizeMaxParallel(snapshot.Orchestrator.MaxParallel)
	session.RunOwner = contracts.AgentRunOwner(session.AgentKey)
	session.TeamRuntime = &contracts.TeamRuntimeContext{
		RuntimeMode:             snapshot.RuntimeMode,
		MaxParallel:             maxParallel,
		Members:                 members,
		RosterFingerprint:       snapshot.RosterFingerprint,
		ToolSchemaFingerprint:   snapshot.ToolSchemaFingerprint,
		OrchestratorFingerprint: snapshot.OrchestratorFingerprint,
	}
	toolDefinition, err := agentbuiltin.TeamBuildToolDefinition(baseTool, promptMembers)
	if err != nil {
		return fmt.Errorf("configure Team coordinator tool: %w", err)
	}
	if !session.PlanningMode {
		session.ModeToolDefinitions = []queryinput.ToolDefinition{toolDefinition}
		session.ToolNames = append(session.ToolNames, agentbuiltin.TeamToolDelegate)
	}
	session.ModeSystemPrompt = agentbuiltin.TeamBuildSystemPrompt(agentbuiltin.TeamPromptConfig{
		AgentKey:    snapshot.AgentKey,
		TeamName:    snapshot.Name,
		Description: snapshot.Description,
		Members:     promptMembers,
		MaxParallel: maxParallel,
	})
	return nil
}
