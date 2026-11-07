package session

import (
	"context"
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/resources"
	runtimetypes "agent-platform/internal/runtime/types"
)

// PrimaryChatEligible selects only native BOT Agents.
func PrimaryChatEligible(def catalog.AgentDefinition) bool {
	return strings.EqualFold(def.Mode, "BOT") && catalog.AgentEngineForAPI(def) == catalog.AgentEngineNative && !IsProxyRoutedAgent(def)
}

func (b *Builder) buildPrimaryChat(ctx context.Context, req runtimetypes.QueryCommand, summary chat.Summary, def catalog.AgentDefinition, options Options) (contracts.QuerySession, error) {
	protocol, err := resources.ConnectorFS.ReadFile("connectors/builtin.task-control/skills/task-control/references/primary-chat.md")
	if err != nil {
		return contracts.QuerySession{}, fmt.Errorf("load primary Chat protocol: %w", err)
	}
	capabilities := "Worker capabilities:\nTools: " + strings.Join(WorkerChatTools(EffectiveAgentTools(def)), ", ") + "\nSkills: " + strings.Join(def.EffectiveSkills(), ", ")
	// Construct a separate execution definition. Never mutate the published
	// Agent or mount its connectors/skills into the scheduling Chat.
	primary := catalog.AgentDefinition{
		Key: def.Key, Name: def.Name, Description: def.Description,
		Mode: def.Mode, Engine: def.Engine, RuntimeDir: def.RuntimeDir,
		RuntimeRevision: def.RuntimeRevision, SoulPrompt: def.SoulPrompt,
		ModelKey: def.ModelKey, ModelReasoningEffort: def.ModelReasoningEffort,
		MaxInputTokens: def.MaxInputTokens, AccessConfig: def.AccessConfig,
		Budget:            def.Budget,
		InteractionConfig: def.InteractionConfig, Tools: contracts.PrimaryChatTools(),
	}
	for _, tag := range def.ContextTags {
		if tag == "memory-global" || tag == "memory-agent" {
			primary.ContextTags = append(primary.ContextTags, tag)
		}
	}
	options.DispatchOnly = true
	options.AllowInvokeAgents = false
	options.DisableSkillScriptGrants = true
	options.TeamCoordinatorHistory = false
	// References are forwarded as locators; the scheduling Chat never reads
	// skill instructions or receives execution grants for referenced skills.
	session, err := b.BuildQuerySession(ctx, req, summary, primary, options)
	if err != nil {
		return session, err
	}
	session.PrimaryChat = true
	session.PrimaryChatProtocol = strings.TrimSpace(string(protocol))
	session.ToolNames = contracts.PrimaryChatTools()
	session.ModeToolDefinitions = nil
	session.ModeSystemPrompt = strings.TrimSpace(b.deps.Config.Bot.SystemPrompt)
	session.CapabilityPrompts = []string{capabilities}
	session.AgentsPrompt = ""
	session.WorkspaceAgentsPrompt = ""
	session.SkillCatalogPrompt = ""
	session.PlanningMode = false
	session.ConfirmedPlanRun = false
	session.SkillScripts = nil
	return session, nil
}

func WorkerChatTools(names []string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		switch name {
		case "chat_send", "chat_submit", "chat_get_status", "chat_interrupt":
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
}
