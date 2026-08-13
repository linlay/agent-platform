package stream

import "strings"

// SseEventNormalizer filters and transforms SSE events before they reach the client.
// Tools marked clientVisible=false have only their tool.* events suppressed; capability
// events produced by those tools, such as awaiting.*, artifact.publish, and memory.*
// remain visible.
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
// will be suppressed.
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

// IsVisible applies the stateful tool visibility policy to a single event.
// Callers that assign public stream sequence numbers must call this before
// reserving a sequence so hidden tool lifecycle events do not create gaps.
func (n *SseEventNormalizer) IsVisible(event StreamEvent) bool {
	return !n.shouldDrop(event)
}

func (n *SseEventNormalizer) shouldDrop(event StreamEvent) bool {
	toolName, _ := event.Payload["toolName"].(string)
	if strings.HasPrefix(toolName, "_hidden_") {
		return true
	}

	// Suppress tool events for clientVisible=false tools.
	eventType := event.Type
	toolID, _ := event.Payload["toolId"].(string)
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
