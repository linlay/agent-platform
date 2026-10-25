package stream

import "strings"

// DetailLevel selects which events a viewer receives. Persistence always
// keeps every event; the level only filters delivery.
//
//   - summary (default): what a user reads and answers — input, Run and
//     subtask lifecycle, content, plans, artifacts, sources, awaiting
//     questions — plus one-sentence progress activities. Reasoning, tool
//     calls, usage snapshots and retries stay behind.
//   - full: every event, progress and retry activities included, so one
//     full stream can serve viewers of every level (a Proxy records its
//     upstream in full and filters locally). A detailed view ignores
//     progress activities; the detail events already show them.
//   - none: the summary events, but progress only as the model announces it:
//     announcements and the clear when output starts, no other activities.
type DetailLevel string

const (
	DetailSummary DetailLevel = "summary"
	DetailFull    DetailLevel = "full"
	DetailNone    DetailLevel = "none"
)

// ParseDetailLevel validates an explicit override. Empty inherits the Agent default.
func ParseDetailLevel(raw string) (DetailLevel, bool) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", true
	case string(DetailSummary):
		return DetailSummary, true
	case string(DetailFull):
		return DetailFull, true
	case string(DetailNone):
		return DetailNone, true
	default:
		return "", false
	}
}

var summaryEventTypes = map[string]bool{
	"request.query": true, "request.steer": true, "request.submit": true,
	"chat.start": true,
	"run.start":  true, "run.complete": true, "run.cancel": true, "run.error": true,
	"task.start": true, "task.complete": true, "task.cancel": true, "task.error": true,
	"content.start": true, "content.delta": true, "content.end": true,
	"content.snapshot": true, "content.view": true,
	"planning.start": true, "planning.delta": true, "planning.end": true,
	"planning.snapshot": true, "planning.superseded": true,
	"plan.create": true, "plan.update": true,
	"artifact.publish": true, "source.publish": true,
	"awaiting.ask": true, "awaiting.answer": true,
}

// Allows reports whether a viewer at this level receives the event. Unlisted
// event types never reach summary or none, so new types default to hidden there.
func (level DetailLevel) Allows(eventType string, payload map[string]any) bool {
	switch level {
	case DetailFull:
		return true
	case DetailNone:
		if IsProgressActivity(eventType, payload) {
			kind := activityKind(payload)
			return kind == ActivityKindAnnounce || kind == "idle"
		}
		return summaryEventTypes[eventType]
	}
	return summaryEventTypes[eventType] || IsProgressActivity(eventType, payload)
}

// FilterEvents keeps the events a viewer at this level receives.
func (level DetailLevel) FilterEvents(events []EventData) []EventData {
	out := make([]EventData, 0, len(events))
	for _, event := range events {
		if level.Allows(event.Type, event.Payload) {
			out = append(out, event)
		}
	}
	return out
}

// WithDisplay annotates run.start at the delivery boundary. Shared/persisted
// events remain untouched; a broker may request full while displaying default.
func WithDisplay(event EventData, defaultDetail, effectiveDetail DetailLevel) EventData {
	if event.Type != "run.start" {
		return event
	}
	payload := make(map[string]any, len(event.Payload)+1)
	for key, value := range event.Payload {
		payload[key] = value
	}
	payload["display"] = map[string]any{"defaultDetail": defaultDetail, "effectiveDetail": effectiveDetail}
	event.Payload = payload
	return event
}
