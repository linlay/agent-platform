package stream

import "strings"

// SseEventNormalizer filters and transforms SSE events before they reach the client.
// Tools marked clientVisible=false have their tool.* events suppressed.
type SseEventNormalizer struct {
	hiddenToolNames map[string]bool
	hiddenToolIDs   map[string]bool
	frontendTools   map[string]frontendToolMetadata
}

type frontendToolMetadata struct {
	ToolType    string
	ViewportKey string
	ToolTimeout int64
}

func NewNormalizer() *SseEventNormalizer {
	return &SseEventNormalizer{
		hiddenToolNames: map[string]bool{},
		hiddenToolIDs:   map[string]bool{},
		frontendTools:   map[string]frontendToolMetadata{},
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

func (n *SseEventNormalizer) RegisterFrontendTool(name string, toolType string, viewportKey string, toolTimeout int64) {
	normalizedName := strings.ToLower(strings.TrimSpace(name))
	if normalizedName == "" || strings.TrimSpace(viewportKey) == "" {
		return
	}
	n.frontendTools[normalizedName] = frontendToolMetadata{
		ToolType:    strings.TrimSpace(toolType),
		ViewportKey: strings.TrimSpace(viewportKey),
		ToolTimeout: toolTimeout,
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
		event = n.enrich(event)
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
	if !strings.HasPrefix(eventType, "tool.") {
		return false
	}
	toolID, _ := event.Payload["toolId"].(string)

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

func (n *SseEventNormalizer) enrich(event StreamEvent) StreamEvent {
	if event.Type != "tool.start" && event.Type != "tool.snapshot" {
		return event
	}
	toolName, _ := event.Payload["toolName"].(string)
	metadata, ok := n.frontendTools[strings.ToLower(strings.TrimSpace(toolName))]
	if !ok {
		return event
	}
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	if metadata.ToolType != "" {
		event.Payload["toolType"] = metadata.ToolType
	}
	event.Payload["viewportKey"] = metadata.ViewportKey
	if metadata.ToolTimeout > 0 {
		event.Payload["toolTimeout"] = metadata.ToolTimeout
	}
	return event
}
