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
