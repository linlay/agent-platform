package stream

import "strings"

// SseEventNormalizer filters and transforms SSE events before they reach the client.
// Tools marked clientVisible=false have their tool.* events suppressed.
type SseEventNormalizer struct {
	hiddenToolNames map[string]bool
	hiddenToolIDs   map[string]bool
}

func NewNormalizer() *SseEventNormalizer {
	return &SseEventNormalizer{
		hiddenToolNames: map[string]bool{},
		hiddenToolIDs:   map[string]bool{},
	}
}

// RegisterHiddenTools marks tool names as non-client-visible.
// Their tool.start/tool.args/tool.end/tool.snapshot/tool.result SSE events
// will be suppressed, matching Java SseEventNormalizer.shouldHideToolEvent.
func (n *SseEventNormalizer) RegisterHiddenTools(names ...string) {
	for _, name := range names {
		if strings.TrimSpace(name) != "" {
			n.hiddenToolNames[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
}

func (n *SseEventNormalizer) Normalize(events []StreamEvent) []StreamEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]StreamEvent, 0, len(events))
	for _, event := range events {
		if n.shouldDrop(event) {
			continue
		}
		out = append(out, event)
	}
	return out
}

func (n *SseEventNormalizer) shouldDrop(event StreamEvent) bool {
	toolName, _ := event.Payload["toolName"].(string)
	if strings.HasPrefix(toolName, "_hidden_") {
		return true
	}

	// Suppress tool events for clientVisible=false tools.
	eventType := event.Type
	toolID, _ := event.Payload["toolId"].(string)
	awaitID, _ := event.Payload["awaitId"].(string)

	if eventType == "await.ask" {
		return awaitID != "" && n.hiddenToolIDs[awaitID]
	}
	if eventType == "await.payload" {
		return awaitID != "" && n.hiddenToolIDs[awaitID]
	}
	if eventType == "request.submit" {
		return toolID != "" && n.hiddenToolIDs[toolID]
	}
	if !strings.HasPrefix(eventType, "tool.") {
		return false
	}

	if eventType == "tool.start" || eventType == "tool.snapshot" {
		if n.isHiddenToolName(toolName) {
			if toolID != "" {
				n.hiddenToolIDs[toolID] = true
			}
			return true
		}
		return false
	}

	// tool.args, tool.end, tool.result — check by toolId
	if toolID != "" && n.hiddenToolIDs[toolID] {
		if eventType == "tool.result" {
			delete(n.hiddenToolIDs, toolID)
		}
		return true
	}
	return false
}

func (n *SseEventNormalizer) isHiddenToolName(name string) bool {
	return n.hiddenToolNames[strings.ToLower(strings.TrimSpace(name))]
}
