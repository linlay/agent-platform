package stream

import "strings"

type SseEventNormalizer struct {
	includeToolPayloadEvents bool
}

func NewNormalizer(includeToolPayloadEvents bool) *SseEventNormalizer {
	return &SseEventNormalizer{includeToolPayloadEvents: includeToolPayloadEvents}
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
		out = append(out, n.normalize(event))
	}
	return out
}

func (n *SseEventNormalizer) shouldDrop(event StreamEvent) bool {
	if event.Type == "tool.snapshot" && !n.includeToolPayloadEvents {
		return true
	}
	toolName, _ := event.Payload["toolName"].(string)
	return strings.HasPrefix(toolName, "_hidden_")
}

func (n *SseEventNormalizer) normalize(event StreamEvent) StreamEvent {
	if event.Type == "plan.create" {
		if planID, _ := event.Payload["planId"].(string); planID != "" {
			return event
		}
	}
	return event
}
