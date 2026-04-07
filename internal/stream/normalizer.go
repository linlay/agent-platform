package stream

import "strings"

type SseEventNormalizer struct{}

func NewNormalizer() *SseEventNormalizer { return &SseEventNormalizer{} }

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
