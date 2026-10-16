package chat

import (
	"strings"

	"agent-platform/internal/contracts/queryinput"
)

// requestContentKeys are the caller-written fields a request.query event
// carries from the original query. Everything else in the event is a platform
// decision stored at the top level of the line.
var requestContentKeys = []string{"message", "hidden", "params", "scene"}

// requestDecisionKeys are platform decisions stored beside the original query.
var requestDecisionKeys = []string{
	"requestId", "runId", "chatId", "agentKey", "lane", "origin", "accessGrant", "planRunId",
	"accessLevel", "editingMode", "model", "references",
}

// RequestPayload projects the line into the request.query payload used by
// live events, history replay and execution recovery alike.
func (q *QueryLine) RequestPayload() map[string]any {
	if q == nil {
		return nil
	}
	return requestPayloadFromLine(q.lineMap())
}

func (q *QueryLine) lineMap() map[string]any {
	line := map[string]any{
		"_type": "query", "chatId": q.ChatID, "runId": q.RunID, "updatedAt": q.UpdatedAt,
	}
	set := func(key string, value string) {
		if strings.TrimSpace(value) != "" {
			line[key] = value
		}
	}
	set("requestId", q.RequestID)
	set("agentKey", q.AgentKey)
	set("taskId", q.TaskID)
	set("taskToolId", q.TaskToolID)
	set("taskAgentKey", q.TaskAgentKey)
	set("lane", q.Lane)
	set("planRunId", q.PlanRunID)
	set("accessLevel", q.AccessLevel)
	set("locale", q.Locale)
	if origin := q.Origin.Payload(); origin != nil {
		line["origin"] = origin
	}
	if len(q.AccessGrant) > 0 {
		line["accessGrant"] = q.AccessGrant
	}
	if q.PlanningMode {
		line["planningMode"] = true
	}
	if q.EditingMode != nil {
		line["editingMode"] = *q.EditingMode
	}
	if q.Model != nil {
		line["model"] = q.Model
	}
	if q.References != nil {
		line["references"] = q.References
	}
	if q.Query != nil {
		line["query"] = q.Query
	}
	return line
}

// lineHasCurrentLayout reports a line whose platform decisions sit beside the
// original query. Every current writer records the resolved requestId there.
func lineHasCurrentLayout(line map[string]any) bool {
	return strings.TrimSpace(stringFromAny(line["requestId"])) != ""
}

func requestPayloadFromLine(line map[string]any) map[string]any {
	query := anyMap(line["query"])
	if !lineHasCurrentLayout(line) {
		return legacyRequestPayload(query)
	}
	payload := map[string]any{}
	for _, key := range requestDecisionKeys {
		if value, ok := line[key]; ok && value != nil {
			payload[key] = value
		}
	}
	if boolFromAny(line["planningMode"]) {
		payload["planningMode"] = true
	}
	if strings.TrimSpace(stringFromAny(line["taskId"])) != "" {
		if agentKey := lineTaskAgentKey(line); agentKey != "" {
			payload["agentKey"] = agentKey
		}
		payload["message"] = stringFromAny(query["task"])
		return payload
	}
	if query == nil {
		if strings.TrimSpace(stringFromAny(line["planRunId"])) != "" {
			payload["message"] = queryinput.PlanExecutionMessage(stringFromAny(line["locale"]))
		}
		return payload
	}
	for _, key := range requestContentKeys {
		if value, ok := requestContentValue(query, key); ok && value != nil {
			payload[key] = value
		}
	}
	if _, ok := payload["message"]; !ok {
		payload["message"] = ""
	}
	for _, key := range []string{"includeUsage", "includeFullText"} {
		if value, _ := requestContentValue(query, key); boolFromAny(value) {
			payload[key] = true
		}
	}
	if origin := lineOrigin(line); origin != nil && origin.Type == queryinput.OriginAutomation && payload["hidden"] == nil {
		payload["hidden"] = true
	}
	if IsSideLane(stringFromAny(line["lane"])) {
		payload["hidden"] = true
	}
	return payload
}

// encoding/json accepts case-insensitive request field names. Preserve the
// caller's spelling on disk while using the same names for replayed content.
func requestContentValue(query map[string]any, key string) (any, bool) {
	if value, ok := query[key]; ok {
		return value, true
	}
	for name, value := range query {
		if strings.EqualFold(name, key) {
			return value, true
		}
	}
	return nil, false
}

// legacyRequestPayload reads lines written before platform decisions moved out
// of query; their query already is the resolved request.query event.
func legacyRequestPayload(query map[string]any) map[string]any {
	if query == nil {
		return nil
	}
	payload := cloneStringAnyMap(query)
	delete(payload, "seq")
	delete(payload, "liveSeq")
	return payload
}

// lineIsSystemInitQuery reports a query line that records only a system
// snapshot change: it carries no input and starts no Run of its own.
func lineIsSystemInitQuery(line map[string]any) bool {
	if strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
		return false
	}
	if !lineHasCurrentLayout(line) {
		query, _ := line["query"].(map[string]any)
		return strings.TrimSpace(stringFromAny(query["kind"])) == "system-init"
	}
	return line["query"] == nil && strings.TrimSpace(stringFromAny(line["planRunId"])) == ""
}

// lineOrigin returns who sent the line's input.
func lineOrigin(line map[string]any) *queryinput.Origin {
	if lineHasCurrentLayout(line) {
		return queryinput.OriginFromPayload(line["origin"])
	}
	query := anyMap(line["query"])
	if stringFromAny(query["source"]) == "automation" {
		return &queryinput.Origin{Type: queryinput.OriginAutomation}
	}
	if runOrigin := anyMap(query["runOrigin"]); runOrigin != nil {
		return &queryinput.Origin{
			Type: queryinput.OriginAgent, AgentKey: stringFromAny(runOrigin["agentKey"]),
			ChatID: stringFromAny(runOrigin["chatId"]), RunID: stringFromAny(runOrigin["runId"]), ToolID: stringFromAny(runOrigin["toolId"]),
		}
	}
	switch strings.TrimSpace(stringFromAny(query["role"])) {
	case queryinput.QueryRoleAutomation:
		return &queryinput.Origin{Type: queryinput.OriginAutomation}
	case "", queryinput.QueryRoleUser:
		return &queryinput.Origin{Type: queryinput.OriginUser}
	default:
		// Historical assistant/system inputs had no sender of their own.
		return nil
	}
}

// lineUserMessage returns the text a user typed for this line. Inputs sent by
// an Agent or an Automation, task inputs and platform lines have none.
func lineUserMessage(line map[string]any) (string, bool) {
	if strings.TrimSpace(stringFromAny(line["taskId"])) != "" || lineTaskAgentKey(line) != "" || lineIsSystemInitQuery(line) {
		return "", false
	}
	origin := lineOrigin(line)
	if origin == nil || origin.Type != queryinput.OriginUser {
		return "", false
	}
	if lineHasCurrentLayout(line) && line["query"] == nil {
		// The start of an approved plan shows a platform message, not user text.
		return "", false
	}
	return stringFromAny(requestPayloadFromLine(line)["message"]), true
}

// lineTaskAgentKey returns the Agent executing a task line, including the names
// used before taskAgentKey.
func lineTaskAgentKey(line map[string]any) string {
	for _, key := range []string{"taskAgentKey", "taskSubAgentKey", "subAgentKey"} {
		if value := strings.TrimSpace(stringFromAny(line[key])); value != "" {
			return value
		}
	}
	return ""
}

// lineTaskName returns the display name of a task line: the name the caller
// wrote, else the executing Agent.
func lineTaskName(line map[string]any) string {
	if value := strings.TrimSpace(stringFromAny(line["taskName"])); value != "" {
		return value
	}
	if value := strings.TrimSpace(stringFromAny(anyMap(line["query"])["taskName"])); value != "" {
		return value
	}
	return lineTaskAgentKey(line)
}

// lineRequestID returns the request id of a query line in either layout.
func lineRequestID(line map[string]any) string {
	if value := strings.TrimSpace(stringFromAny(line["requestId"])); value != "" {
		return value
	}
	return strings.TrimSpace(stringFromAny(anyMap(line["query"])["requestId"]))
}

// queryLineFromMap decodes a persisted query line without a JSON round trip, so
// numbers keep the decoder's json.Number form.
func queryLineFromMap(line map[string]any, fallbackChatID string) *QueryLine {
	chatID := strings.TrimSpace(stringFromAny(line["chatId"]))
	if chatID == "" {
		chatID = strings.TrimSpace(fallbackChatID)
	}
	out := &QueryLine{
		Type:         "query",
		ChatID:       chatID,
		RunID:        strings.TrimSpace(stringFromAny(line["runId"])),
		UpdatedAt:    int64FromAny(line["updatedAt"]),
		LiveSeq:      int64FromAny(line["liveSeq"]),
		RequestID:    strings.TrimSpace(stringFromAny(line["requestId"])),
		AgentKey:     strings.TrimSpace(stringFromAny(line["agentKey"])),
		TaskID:       strings.TrimSpace(stringFromAny(line["taskId"])),
		TaskToolID:   strings.TrimSpace(stringFromAny(line["taskToolId"])),
		TaskAgentKey: lineTaskAgentKey(line),
		Lane:         strings.TrimSpace(stringFromAny(line["lane"])),
		Origin:       queryinput.OriginFromPayload(line["origin"]),
		AccessGrant:  cloneStringAnyMap(anyMap(line["accessGrant"])),
		PlanRunID:    strings.TrimSpace(stringFromAny(line["planRunId"])),
		AccessLevel:  strings.TrimSpace(stringFromAny(line["accessLevel"])),
		PlanningMode: boolFromAny(line["planningMode"]),
		Model:        line["model"],
		References:   line["references"],
		Locale:       strings.TrimSpace(stringFromAny(line["locale"])),
		Query:        cloneStringAnyMap(anyMap(line["query"])),
		Messages:     messagesFromEventValue(line["messages"]),
	}
	if system, ok := systemFromEventValue(line["system"]); ok {
		out.System = &system
	}
	if editing, ok := line["editingMode"].(bool); ok {
		out.EditingMode = &editing
	}
	return out
}

// queryLineDecisionsFromPayload copies the platform decisions of a
// request.query event onto a query line.
func queryLineDecisionsFromPayload(payload map[string]any) QueryLine {
	line := QueryLine{
		RequestID:   strings.TrimSpace(stringFromAny(payload["requestId"])),
		AgentKey:    strings.TrimSpace(stringFromAny(payload["agentKey"])),
		Lane:        strings.TrimSpace(stringFromAny(payload["lane"])),
		Origin:      queryinput.OriginFromPayload(payload["origin"]),
		AccessGrant: cloneStringAnyMap(anyMap(payload["accessGrant"])),
		PlanRunID:   strings.TrimSpace(stringFromAny(payload["planRunId"])),
		AccessLevel: strings.TrimSpace(stringFromAny(payload["accessLevel"])),
		Model:       payload["model"],
		References:  payload["references"],
	}
	if planning, ok := payload["planningMode"].(bool); ok && planning {
		line.PlanningMode = true
	}
	if editing, ok := payload["editingMode"].(bool); ok {
		line.EditingMode = &editing
	}
	return line
}

// requestContentFromPayload is the input of an internal caller that did not
// record its own request body; it keeps only caller-written fields.
func requestContentFromPayload(payload map[string]any) map[string]any {
	content := map[string]any{"message": stringFromAny(payload["message"])}
	for _, key := range requestContentKeys {
		if value, ok := payload[key]; ok && value != nil {
			content[key] = value
		}
	}
	return content
}
