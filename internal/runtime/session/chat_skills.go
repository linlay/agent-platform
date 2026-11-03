package session

import (
	"context"
	"log"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	runtimetypes "agent-platform/internal/runtime/types"
)

func (b *Builder) inheritChatSkills(ctx context.Context, session contracts.QuerySession, req runtimetypes.QueryCommand, def catalog.AgentDefinition, options Options) {
	// A recovered Run must not reconstruct credentials from today's bytes.
	// Side lanes and children do not inherit the main Chat's skill selection.
	if !options.IncludeHistory || options.DisableSkillScriptGrants || options.SubTaskID != "" ||
		options.TeamHistoryAgentKey != "" || req.Lane != "" || remoteReferenceBackend(def) {
		return
	}
	reader, ok := b.deps.Chats.(chat.SkillReferenceReader)
	if !ok {
		return
	}
	known := map[string]bool{}
	for _, id := range append(def.EffectiveSkills(), queryinput.ResourceIDs(req.References, "skill")...) {
		known[strings.ToLower(strings.TrimSpace(id))] = true
	}
	// Known skills do not consume the historical candidate budget. Fetch enough
	// distinct IDs to retain up to 32 candidates after those IDs are filtered.
	ids, err := reader.PriorSkillReferenceIDs(req.ChatID, req.RunID, contracts.MaxReferencedSkills+len(known))
	if err != nil {
		log.Printf("[session][chat-skills] chat=%s run=%s: history unavailable: %v", req.ChatID, req.RunID, err)
		return
	}
	remaining := contracts.MaxReferencedSkills
	for _, id := range ids {
		if known[strings.ToLower(strings.TrimSpace(id))] {
			continue
		}
		if remaining == 0 {
			break
		}
		remaining--
		// Reapply this entry's current policy. Historical Agent-origin steers
		// cannot carry their interaction-policy exemption into a user query.
		grant, err := b.PrepareRunReferences(ctx, session, def, queryinput.MakeReferences("skill", []string{id}), false, true, options.SkipInteractionPolicy)
		if err != nil {
			log.Printf("[session][chat-skills] chat=%s run=%s skill=%s: grant skipped: %v", req.ChatID, req.RunID, id, err)
			continue
		}
		session.ReferencesScope.Inherit(grant)
	}
}
