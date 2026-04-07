package stream

import "time"

type StreamEvent struct {
	Seq       int64
	Type      string
	Timestamp int64
	Payload   map[string]any
}

func NewEvent(eventType string, payload map[string]any) StreamEvent {
	return StreamEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   clonePayload(payload),
	}
}

func (e StreamEvent) ToData() map[string]any {
	data := clonePayload(e.Payload)
	if data == nil {
		data = map[string]any{}
	}
	data["seq"] = e.Seq
	data["type"] = e.Type
	data["timestamp"] = e.Timestamp
	return data
}

func IsPersistedEventType(eventType string) bool {
	switch eventType {
	case "request.query", "request.submit", "request.steer",
		"chat.start",
		"run.start", "run.complete", "run.cancel", "run.error",
		"reasoning.snapshot", "content.snapshot",
		"tool.snapshot", "tool.result",
		"action.snapshot", "action.result",
		"plan.create", "plan.update",
		"task.start", "task.complete", "task.cancel", "task.fail",
		"stage.marker", "artifact.publish":
		return true
	default:
		return false
	}
}

func clonePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value
	}
	return out
}
