package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/timecontract"
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
	payload = normalizeAwaitingAskPayload(eventType, clonePayload(payload))
	return StreamEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func nonNegativeDurationMs(startedAt int64, endedAt int64) int64 {
	duration := endedAt - startedAt
	if duration < 0 {
		return 0
	}
	return duration
}

func (e StreamEvent) ToData() map[string]any {
	return e.Data().Map()
}

func (e StreamEvent) Data() EventData {
	return EventData{
		Seq:       e.Seq,
		Type:      e.Type,
		Timestamp: e.Timestamp,
		Payload:   normalizeAwaitingAskPayload(e.Type, clonePayload(e.Payload)),
	}
}

// ParseEventDataMap validates an externally supplied stream event. Callers
// at JSON/SSE/WebSocket/persisted-replay boundaries must use this:
// timestamp is a required Unix-epoch-milliseconds JSON integer and is never
// inferred from the local clock.
func ParseEventDataMap(data map[string]any, location string) (EventData, error) {
	payload := clonePayload(data)
	if payload == nil {
		payload = map[string]any{}
	}
	timestampValue, ok := payload["timestamp"]
	if !ok {
		return EventData{}, &timecontract.Violation{
			Field:    "timestamp",
			Location: location,
			Reason:   "is required",
		}
	}
	timestamp, err := timecontract.ParseEpochMillis(timestampValue, "timestamp", location)
	if err != nil {
		return EventData{}, err
	}
	seq, _ := int64Value(payload["seq"])
	eventType, _ := payload["type"].(string)
	delete(payload, "seq")
	delete(payload, "type")
	delete(payload, "timestamp")
	payload = normalizeAwaitingAskPayload(eventType, payload)
	if err := validateEventWireTimeContract(seq, eventType, timestamp, payload, location); err != nil {
		return EventData{}, err
	}
	return EventData{
		Seq:       seq,
		Type:      eventType,
		Timestamp: timestamp,
		Payload:   payload,
	}, nil
}

// ParseEventDataJSON decodes an external event with UseNumber so integer
// syntax is preserved. This prevents a generic json.Unmarshal float64 from
// masking a fractional timestamp before the contract check runs.
func ParseEventDataJSON(data []byte, location string) (EventData, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return EventData{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return EventData{}, errors.New("stream event contains multiple JSON values")
		}
		return EventData{}, err
	}
	return ParseEventDataMap(raw, location)
}

func (d EventData) MarshalJSON() ([]byte, error) {
	payload := normalizedEventWirePayload(d.Type, d.Payload)
	if err := validateEventWireTimeContract(d.Seq, d.Type, d.Timestamp, payload, "stream.event"); err != nil {
		return nil, err
	}
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

// ValidateEventData validates platform-owned event envelope fields before a
// server-side consumer persists or publishes them. Payload maps deliberately
// remain opaque here: a tool.result can contain an external business document
// whose createdAt or timestamp property has no platform time semantics.
func ValidateEventData(data EventData, location string) error {
	payload := normalizedEventWirePayload(data.Type, data.Payload)
	return validateEventWireTimeContract(data.Seq, data.Type, data.Timestamp, payload, location)
}

func normalizedEventWirePayload(eventType string, input map[string]any) map[string]any {
	payload := normalizeAwaitingAskPayload(eventType, clonePayload(input))
	for key, value := range payload {
		if shouldOmitPayloadField(eventType, key, value) {
			delete(payload, key)
		}
	}
	return payload
}

func validateEventWireTimeContract(seq int64, eventType string, timestamp int64, payload map[string]any, location string) error {
	if err := timecontract.ValidateEpochMillis(timestamp, "timestamp", location+".timestamp"); err != nil {
		return err
	}
	// seq, type and the payload are not time declarations. Keep their values
	// untouched so external tool/MCP data cannot be reinterpreted by name.
	_ = seq
	_ = eventType
	_ = payload
	return nil
}

func (d *EventData) UnmarshalJSON(data []byte) error {
	parsed, err := ParseEventDataJSON(data, "stream.event")
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func (d EventData) Map() map[string]any {
	data := normalizeAwaitingAskPayload(d.Type, clonePayload(d.Payload))
	if data == nil {
		data = map[string]any{}
	}
	data["seq"] = d.Seq
	data["type"] = d.Type
	data["timestamp"] = d.Timestamp
	return data
}

func normalizeAwaitingAskPayload(eventType string, payload map[string]any) map[string]any {
	if eventType != "awaiting.ask" || payload == nil {
		return payload
	}
	mode, _ := payload["mode"].(string)
	if strings.EqualFold(strings.TrimSpace(mode), "planning") {
		delete(payload, "timeout")
	}
	return payload
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
	if eventType == "request.query" && key == "messages" {
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
	case "run.start":
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
	case "source.publish":
		return key == "taskId" || key == "toolId" || key == "query"
	case "planning.start", "planning.delta", "planning.end", "planning.snapshot":
		return key == "planningId"
	case "task.start":
		return key == "description" || key == "subAgentKey" || key == "invokingToolId"
	case "task.cancel":
		return key == "reason"
	default:
		return false
	}
}

func eventPayloadKeyOrder(eventType string) []string {
	switch eventType {
	case "request.query":
		return []string{"requestId", "runId", "chatId", "role", "message", "agentKey", "teamId", "kind", "stage", "btwId", "parentChatId", "hidden", "references", "params", "scene", "stream", "includeUsage", "includeFullText", "messages", "system"}
	case "awaiting.ask":
		return []string{"awaitingId", "mode", "viewportType", "viewportKey", "timeout", "runId", "taskId", "agentKey", "questions", "approvals", "forms", "planning"}
	case "awaiting.answer":
		return []string{"awaitingId", "taskId", "mode", "status", "submitId", "durationMs", "answers", "approvals", "forms", "planning", "error"}
	case "request.submit":
		return []string{"requestId", "chatId", "runId", "taskId", "awaitingId", "submitId", "params"}
	case "request.steer":
		return []string{"requestId", "chatId", "runId", "steerId", "message", "role"}
	case "chat.start":
		return []string{"chatId", "chatName"}
	case "run.start":
		return []string{"runId", "chatId", "agentKey"}
	case "debug.llmChat":
		return []string{"runId", "chatId", "data"}
	case "llm.request":
		return []string{"runId", "chatId", "taskId", "model", "system", "systemRef", "toolChoice", "requestOptions", "inputMessages"}
	case "usage.snapshot":
		return []string{"runId", "chatId", "taskId", "model", "contextWindow", "usage"}
	case "run.activity":
		return []string{"runId", "chatId", "taskId", "phase", "status", "backend", "key", "message", "retry", "recovery", "degradation"}
	case "memory.write", "memory.read", "memory.search", "memory.update",
		"memory.forget", "memory.timeline", "memory.promote", "memory.consolidate":
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
		return []string{"toolId", "fileChange"}
	case "tool.snapshot":
		return []string{"toolId", "runId", "toolName", "taskId", "toolLabel", "toolDescription", "arguments", "fileChange"}
	case "tool.result":
		return []string{"toolId", "toolName", "result", "durationMs", "fileChange", "hitl", "approval"}
	case "source.publish":
		return []string{"publishId", "runId", "taskId", "toolId", "kind", "query", "sourceCount", "chunkCount", "sources"}
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
	case "planning.start":
		return []string{"planningId"}
	case "planning.delta":
		return []string{"planningId", "delta"}
	case "planning.end":
		return []string{"planningId"}
	case "planning.snapshot":
		return []string{"planningId", "planningFile", "chatId", "runId", "text"}
	case "task.start":
		return []string{"taskId", "runId", "taskName", "description", "subAgentKey", "invokingToolId"}
	case "task.complete":
		return []string{"taskId"}
	case "task.cancel":
		return []string{"taskId", "reason"}
	case "task.error":
		return []string{"taskId", "error"}
	case "artifact.publish":
		return []string{"chatId", "runId", "artifactCount", "artifacts"}
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
