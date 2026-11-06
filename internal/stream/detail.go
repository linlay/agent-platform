package stream

import "strings"

// DetailLevel selects which events viewers receive. A Run's level comes from
// its Agent configuration when it is admitted, never from the request, and
// every observer gets the same events; only a Chat with no active Run is read
// at another level. Persistence always keeps every event; the level only
// filters delivery.
//
//   - simple (default): what a user reads and answers — input, Run and
//     subtask lifecycle, content, plans, artifacts, sources, awaiting
//     questions — plus state notifications, usage, waits, announcements and retries.
//     One startup placeholder is simple-only. Automatic progress, reasoning
//     bodies and ordinary tool call details stay behind; calls of tools whose
//     definitions declare simpleVisible are delivered whole (see DetailFilter).
//   - detailed: real process events and retries, plus the initial model wait
//     and clears. Activity selection happens here before internal metadata
//     is removed; viewers display the resulting messages without classification.
type DetailLevel string

const (
	DetailSimple   DetailLevel = "simple"
	DetailDetailed DetailLevel = "detailed"
)

// ParseDetailLevel validates a configured or history-read level. Empty means unset.
func ParseDetailLevel(raw string) (DetailLevel, bool) {
	switch strings.TrimSpace(raw) {
	case "":
		return "", true
	case string(DetailSimple):
		return DetailSimple, true
	case string(DetailDetailed):
		return DetailDetailed, true
	default:
		return "", false
	}
}

// ResolveDetailLevel is the level a Run is delivered at: the Agent default,
// else simple.
func ResolveDetailLevel(agentDefault string) DetailLevel {
	if level, ok := ParseDetailLevel(agentDefault); ok && level != "" {
		return level
	}
	return DetailSimple
}

var simpleEventTypes = map[string]bool{
	"request.query": true, "request.steer": true, "request.submit": true,
	"chat.start": true,
	"run.start":  true, "run.complete": true, "run.cancel": true, "run.error": true,
	"run.access_level.changed": true, "run.activity": true,
	"context.compact.start": true, "context.compact.complete": true, "context.compact.failed": true,
	"tool.wait": true, "tool.wait.update": true, "usage.snapshot": true,
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
// event types never reach simple, so new types default to hidden there.
func (level DetailLevel) Allows(eventType string, payload map[string]any) bool {
	if IsStartupActivity(eventType, payload) {
		return level != DetailDetailed
	}
	switch level {
	case DetailDetailed:
		if IsProgressActivity(eventType, payload) {
			kind := activityKind(payload)
			return kind == "model_waiting" || kind == "idle" || kind == "forwarded"
		}
		return true
	default:
		if IsProgressActivity(eventType, payload) {
			kind := activityKind(payload)
			return kind == ActivityKindAnnounce || kind == "idle" || kind == "forwarded"
		}
		// Wait completion closes the interactive wait card even when ordinary
		// tool results are hidden by simple delivery.
		if eventType == "tool.result" && payload["toolName"] == "wait" {
			return true
		}
		return simpleEventTypes[eventType]
	}
}

// DetailFilter applies a level to one ordered event sequence (a live stream
// or a history read). simpleTool reports whether a tool definition declares
// simpleVisible; every event of such a call reaches simple viewers. Calls are
// tracked by toolId because argument chunks, tool.end and replayed results do
// not repeat the tool name. A filter is not safe for concurrent use.
type DetailFilter struct {
	level       DetailLevel
	simpleTool  func(toolName string) bool
	simpleCalls map[string]bool
}

func NewDetailFilter(level DetailLevel, simpleTool func(toolName string) bool) *DetailFilter {
	return &DetailFilter{level: level, simpleTool: simpleTool, simpleCalls: map[string]bool{}}
}

func (f *DetailFilter) Level() DetailLevel {
	return f.level
}

func (f *DetailFilter) Allows(event EventData) bool {
	if f.level != DetailDetailed && f.simpleTool != nil && strings.HasPrefix(event.Type, "tool.") {
		toolID, _ := event.Payload["toolId"].(string)
		if toolID != "" {
			if name, _ := event.Payload["toolName"].(string); name != "" && f.simpleTool(name) {
				f.simpleCalls[toolID] = true
			}
			if f.simpleCalls[toolID] {
				return true
			}
		}
	}
	return f.level.Allows(event.Type, event.Payload)
}

// FilterEvents keeps the events a viewer at this level receives.
func (f *DetailFilter) FilterEvents(events []EventData) []EventData {
	out := make([]EventData, 0, len(events))
	for _, event := range events {
		if f.Allows(event) {
			out = append(out, event)
		}
	}
	return out
}

// WithDisplay annotates run.start at the delivery boundary. Shared/persisted
// events remain untouched; a broker may request detailed while displaying default.
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
