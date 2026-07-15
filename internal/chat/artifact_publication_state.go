package chat

import (
	"strings"

	"agent-platform/internal/stream"
)

func artifactPublicationItemFromEvent(event stream.EventData) map[string]any {
	item := eventPayloadWithoutSeq(event)
	delete(item, "type")
	delete(item, "chatId")
	if event.Timestamp > 0 {
		item["timestamp"] = event.Timestamp
	}
	if event.Seq > 0 {
		item["liveSeq"] = event.Seq
	}
	return item
}

func appendArtifactPublicationStateItem(state *ArtifactPublicationState, item map[string]any) *ArtifactPublicationState {
	if len(item) == 0 {
		return state
	}
	if state == nil {
		state = &ArtifactPublicationState{}
	}
	state.Items = append(state.Items, cloneMapDeep(item))
	return state
}

func cloneArtifactPublicationState(state *ArtifactPublicationState) *ArtifactPublicationState {
	if state == nil || len(state.Items) == 0 {
		return nil
	}
	out := &ArtifactPublicationState{Items: make([]map[string]any, 0, len(state.Items))}
	for _, item := range state.Items {
		if len(item) > 0 {
			out.Items = append(out.Items, cloneMapDeep(item))
		}
	}
	if len(out.Items) == 0 {
		return nil
	}
	return out
}

func storedMessagesContainToolID(messages []StoredMessage, toolID string) bool {
	toolID = strings.TrimSpace(toolID)
	if toolID == "" {
		return false
	}
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		if toolID == strings.TrimSpace(message.ToolID) || toolID == strings.TrimSpace(message.ToolCallID) {
			return true
		}
	}
	return false
}
