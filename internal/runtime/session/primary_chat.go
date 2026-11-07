package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/resources"
	runtimetypes "agent-platform/internal/runtime/types"
)

func SelfWorkerChat(store chat.Store, req runtimetypes.QueryCommand, def catalog.AgentDefinition) (bool, error) {
	isWorker := func(origin *queryinput.Origin, opened bool) bool {
		return opened && origin != nil && origin.Type == queryinput.OriginAgent && origin.AgentKey == def.Key
	}
	if isWorker(req.Origin, req.Mail != nil && req.Mail.NewChat) {
		return true, nil
	}
	if store == nil {
		return false, nil
	}
	content, err := store.LoadJSONLContent(req.ChatID)
	if err != nil {
		return false, err
	}
	scan := bufio.NewScanner(strings.NewReader(content))
	scan.Buffer(make([]byte, 65536), 64<<20)
	for scan.Scan() {
		var row map[string]any
		if json.Unmarshal(scan.Bytes(), &row) != nil {
			continue
		}
		lane := strings.TrimSpace(contracts.AnyStringNode(row["lane"]))
		if row["_type"] != "query" || strings.TrimSpace(contracts.AnyStringNode(row["taskId"])) != "" || lane != "" && lane != "main" {
			continue
		}
		opened, recorded := row["mailNewChat"].(bool)
		// Legacy self-created Chats predate the explicit newChat provenance.
		// Their first platform origin is the existing compatibility boundary.
		return isWorker(queryinput.OriginFromPayload(row["origin"]), opened || !recorded), nil
	}
	return false, scan.Err()
}

// PrimaryChatEligible is deliberately narrower than the set of native modes.
// GENERAL remains the persisted mode for both Chat roles.
func PrimaryChatEligible(def catalog.AgentDefinition) bool {
	return strings.EqualFold(def.Mode, "GENERAL") && catalog.AgentEngineForAPI(def) == catalog.AgentEngineNative && !IsProxyRoutedAgent(def)
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
	session.ModeSystemPrompt = strings.TrimSpace(b.deps.Config.PrimaryChat.SystemPrompt)
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
