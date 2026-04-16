package stream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type StreamEvent struct {
	Seq       int64
	Type      string
	Timestamp int64
	Payload   map[string]any
}

type EventData struct {
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
	return e.Data().Map()
}

func (e StreamEvent) Data() EventData {
	return EventData{
		Seq:       e.Seq,
		Type:      e.Type,
		Timestamp: e.Timestamp,
		Payload:   clonePayload(e.Payload),
	}
}

func EventDataFromMap(data map[string]any) EventData {
	payload := clonePayload(data)
	if payload == nil {
		payload = map[string]any{}
	}
	seq, _ := int64Value(payload["seq"])
	timestamp, _ := int64Value(payload["timestamp"])
	eventType, _ := payload["type"].(string)
	delete(payload, "seq")
	delete(payload, "type")
	delete(payload, "timestamp")
	return EventData{
		Seq:       seq,
		Type:      eventType,
		Timestamp: timestamp,
		Payload:   payload,
	}
}

func (d EventData) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	writeField := func(key string, value any) error {
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return err
		}
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(keyJSON)
		buf.WriteByte(':')
		buf.Write(valueJSON)
		return nil
	}

	if err := writeField("seq", d.Seq); err != nil {
		return nil, err
	}
	if err := writeField("type", d.Type); err != nil {
		return nil, err
	}

	payload := clonePayload(d.Payload)
	for _, key := range orderedPayloadKeys(d.Type, payload) {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if shouldOmitPayloadField(d.Type, key, value) {
			delete(payload, key)
			continue
		}
		if err := writeField(key, value); err != nil {
			return nil, err
		}
		delete(payload, key)
	}

	if len(payload) > 0 {
		keys := make([]string, 0, len(payload))
		for key := range payload {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if shouldOmitPayloadField(d.Type, key, payload[key]) {
				continue
			}
			if err := writeField(key, payload[key]); err != nil {
				return nil, err
			}
		}
	}

	if err := writeField("timestamp", d.Timestamp); err != nil {
		return nil, err
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (d *EventData) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*d = EventDataFromMap(raw)
	return nil
}

func (d EventData) Map() map[string]any {
	data := clonePayload(d.Payload)
	if data == nil {
		data = map[string]any{}
	}
	data["seq"] = d.Seq
	data["type"] = d.Type
	data["timestamp"] = d.Timestamp
	return data
}

func (d EventData) Value(key string) any {
	switch key {
	case "seq":
		return d.Seq
	case "type":
		return d.Type
	case "timestamp":
		return d.Timestamp
	default:
		if d.Payload == nil {
			return nil
		}
		return d.Payload[key]
	}
}

func (d EventData) String(key string) string {
	value, _ := d.Value(key).(string)
	return value
}

func IsPersistedEventType(eventType string) bool {
	switch eventType {
	case "request.query", "request.submit", "request.steer",
		"awaiting.ask", "awaiting.payload", "awaiting.answer",
		"chat.start",
		"run.start", "run.complete", "run.cancel", "run.error",
		"reasoning.snapshot", "content.snapshot",
		"tool.snapshot", "tool.result",
		"action.snapshot", "action.result",
		"plan.create", "plan.update",
		"task.start", "task.complete", "task.cancel", "task.fail",
		"artifact.publish":
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

func orderedPayloadKeys(eventType string, payload map[string]any) []string {
	keys := append([]string(nil), eventPayloadKeyOrder(eventType)...)
	switch eventType {
	case "request.query":
		if _, ok := payload["runId"]; ok && !containsKey(keys, "runId") {
			insertAfter(&keys, "requestId", "runId")
		}
	case "request.steer":
		if _, ok := payload["role"]; ok && !containsKey(keys, "role") {
			keys = append(keys, "role")
		}
	}
	return keys
}

func shouldOmitPayloadField(eventType string, key string, value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	if strings.TrimSpace(text) != "" {
		return false
	}
	switch eventType {
	case "request.query":
		return key == "agentKey" || key == "teamId"
	case "request.steer":
		return key == "requestId"
	case "chat.start":
		return key == "chatName"
	case "reasoning.start", "reasoning.snapshot",
		"content.start", "content.snapshot",
		"tool.start", "tool.snapshot",
		"action.start", "action.snapshot":
		return key == "taskId" || key == "reasoningLabel" || key == "toolName" || key == "toolLabel" ||
			key == "toolDescription" || key == "viewportKey" || key == "actionName" ||
			key == "description" || key == "arguments"
	default:
		return false
	}
}

func eventPayloadKeyOrder(eventType string) []string {
	switch eventType {
	case "request.query":
		return []string{"requestId", "chatId", "role", "message", "agentKey", "teamId", "references", "params", "scene", "stream", "hidden"}
	case "awaiting.ask":
		return []string{"awaitingId", "viewportType", "viewportKey", "mode", "toolTimeout", "runId", "questions"}
	case "awaiting.payload":
		return []string{"awaitingId", "questions"}
	case "awaiting.answer":
		return []string{"awaitingId", "mode", "cancelled", "reason", "questions"}
	case "request.submit":
		return []string{"requestId", "chatId", "runId", "awaitingId", "params"}
	case "request.steer":
		return []string{"requestId", "chatId", "runId", "steerId", "message", "role"}
	case "chat.start":
		return []string{"chatId", "chatName"}
	case "run.start":
		return []string{"runId", "chatId", "agentKey"}
	case "debug.preCall", "debug.postCall":
		return []string{"runId", "chatId", "data"}
	case "run.complete":
		return []string{"runId", "finishReason", "usage"}
	case "run.cancel":
		return []string{"runId", "usage"}
	case "run.error":
		return []string{"runId", "error", "usage"}
	case "reasoning.start":
		return []string{"reasoningId", "runId", "taskId", "reasoningLabel"}
	case "reasoning.delta":
		return []string{"reasoningId", "delta"}
	case "reasoning.end":
		return []string{"reasoningId"}
	case "reasoning.snapshot":
		return []string{"reasoningId", "runId", "text", "taskId", "reasoningLabel"}
	case "content.start":
		return []string{"contentId", "runId", "taskId"}
	case "content.delta":
		return []string{"contentId", "delta"}
	case "content.end":
		return []string{"contentId"}
	case "content.snapshot":
		return []string{"contentId", "runId", "text", "taskId"}
	case "tool.start":
		return []string{"toolId", "runId", "taskId", "toolName", "toolLabel", "toolDescription"}
	case "tool.args":
		return []string{"toolId", "delta", "chunkIndex"}
	case "tool.end":
		return []string{"toolId"}
	case "tool.snapshot":
		return []string{"toolId", "runId", "toolName", "taskId", "toolLabel", "toolDescription", "arguments"}
	case "tool.result":
		return []string{"toolId", "result"}
	case "action.start":
		return []string{"actionId", "runId", "taskId", "actionName", "description"}
	case "action.args":
		return []string{"actionId", "delta"}
	case "action.end":
		return []string{"actionId"}
	case "action.snapshot":
		return []string{"actionId", "runId", "actionName", "taskId", "description", "arguments"}
	case "action.result":
		return []string{"actionId", "result"}
	case "plan.create", "plan.update":
		return []string{"planId", "chatId", "plan"}
	case "task.start":
		return []string{"taskId", "runId", "taskName", "description"}
	case "task.complete", "task.cancel":
		return []string{"taskId"}
	case "task.fail":
		return []string{"taskId", "error"}
	case "stage.marker":
		return []string{"runId", "chatId", "stage"}
	case "artifact.publish":
		return []string{"artifactId", "chatId", "runId", "artifact"}
	default:
		return nil
	}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func insertAfter(keys *[]string, after string, value string) {
	if containsKey(*keys, value) {
		return
	}
	for idx, key := range *keys {
		if key != after {
			continue
		}
		next := append([]string(nil), (*keys)[:idx+1]...)
		next = append(next, value)
		*keys = append(next, (*keys)[idx+1:]...)
		return
	}
	*keys = append(*keys, value)
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func MarshalEventDataList(events []EventData) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for idx, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("marshal event %d: %w", idx, err)
		}
		if idx > 0 {
			buf.WriteByte(',')
		}
		buf.Write(data)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}
