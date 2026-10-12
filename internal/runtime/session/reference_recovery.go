package session

import (
	"context"
	"encoding/json"

	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

// RestoreSteerReferences reads only structured, consumed request.steer events
// of this Run. Ordinary replay/new Runs never call this method.
func (b *Builder) RestoreSteerReferences(session *contracts.QuerySession, def catalog.AgentDefinition) error {
	if b.deps.Chats == nil {
		return nil
	}
	detail, err := b.deps.Chats.LoadChat(session.ChatID)
	if err != nil {
		return err
	}
	for _, event := range detail.Events {
		if event.Type != "request.steer" || event.String("runId") != session.RunID {
			continue
		}
		raw, _ := json.Marshal(event.Payload["references"])
		var refs []queryinput.Reference
		if json.Unmarshal(raw, &refs) != nil {
			continue
		}
		for _, ref := range refs {
			grant, err := b.PrepareRunReferences(context.Background(), *session, def, []queryinput.Reference{ref}, false, false)
			if err == nil {
				_ = session.ReferencesScope.Commit(grant)
			}
		}
	}
	b.BindReferencePreparer(session, def, false)
	return nil
}

// RunRequestReferences collects only the original query and accepted steer
// events from the specified Run, for an explicitly confirmed planning handoff.
func (b *Builder) RunRequestReferences(chatID, runID string, initial []queryinput.Reference) ([]queryinput.Reference, error) {
	refs := append([]queryinput.Reference(nil), initial...)
	detail, err := b.deps.Chats.LoadChat(chatID)
	if err != nil {
		return nil, err
	}
	for _, event := range detail.Events {
		if event.Type != "request.steer" || event.String("runId") != runID {
			continue
		}
		raw, err := json.Marshal(event.Payload["references"])
		if err != nil {
			return nil, err
		}
		var steered []queryinput.Reference
		if err := json.Unmarshal(raw, &steered); err != nil {
			return nil, err
		}
		refs = append(refs, steered...)
	}
	return queryinput.NormalizeResourceReferences(refs)
}
