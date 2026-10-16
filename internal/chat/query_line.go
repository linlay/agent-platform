package chat

import (
	"strings"

	"agent-platform/internal/contracts/queryinput"
)

// requestContentKeys are the caller-written fields replayed from the original
// query. Everything the platform decided is read from the top level.
var requestContentKeys = []string{"message", "params", "scene"}

// requestDecisionKeys are platform decisions stored beside the original query.
// A line written before they moved out of query lacks them; readers treat a
// missing decision as unset, since such a Run is never resumed.
var requestDecisionKeys = []string{
	"requestId", "runId", "chatId", "agentKey", "lane", "origin", "accessGrant", "planRunId",
	"accessLevel", "editingMode", "model", "references",
}

// requestDisplayFallbackKeys are what clients show or restore when reading a
// Chat. Older lines kept them only inside query, so they are read from there
// when the top level lacks them.
var requestDisplayFallbackKeys = []string{"requestId", "agentKey", "references", "hidden", "accessLevel", "model"}

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
	set("taskName", q.TaskName)
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
	if q.Hidden {
		line["hidden"] = true
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

func requestPayloadFromLine(line map[string]any) map[string]any {
	query := anyMap(line["query"])
	payload := map[string]any{}
	for _, key := range requestDecisionKeys {
		if value, ok := line[key]; ok && value != nil {
			payload[key] = value
		}
	}
	if boolFromAny(line["planningMode"]) {
		payload["planningMode"] = true
	}
	if boolFromAny(line["hidden"]) {
		payload["hidden"] = true
	}
	for _, key := range requestDisplayFallbackKeys {
		if _, ok := payload[key]; !ok && query[key] != nil {
			payload[key] = query[key]
		}
	}
	if strings.TrimSpace(stringFromAny(line["taskId"])) != "" {
		if agentKey := lineTaskAgentKey(line); agentKey != "" {
			payload["agentKey"] = agentKey
		}
		message := stringFromAny(query["task"])
		if _, ok := query["task"]; !ok {
			message = stringFromAny(query["message"])
		}
		payload["message"] = message
		return payload
	}
	if origin := lineOrigin(line); origin != nil {
		payload["origin"] = origin.Payload()
	}
	if query == nil {
		if strings.TrimSpace(stringFromAny(line["planRunId"])) != "" {
			payload["message"] = queryinput.PlanExecutionMessage(stringFromAny(line["locale"]))
		}
		return payload
	}
	for _, key := range requestContentKeys {
		if value, ok := query[key]; ok && value != nil {
			payload[key] = value
		}
	}
	if _, ok := payload["message"]; !ok {
		payload["message"] = ""
	}
	for _, key := range []string{"includeUsage", "includeFullText"} {
		if boolFromAny(query[key]) {
			payload[key] = true
		}
	}
	return payload
}

// lineIsSystemInitQuery reports a query line that records only a system
// snapshot change: no input, no model messages and no Run start of its own.
// Older lines marked it with query.kind instead.
func lineIsSystemInitQuery(line map[string]any) bool {
	if strings.TrimSpace(stringFromAny(line["_type"])) != "query" {
		return false
	}
	query, ok := line["query"].(map[string]any)
	if !ok {
		return line["system"] != nil && line["messages"] == nil && strings.TrimSpace(stringFromAny(line["planRunId"])) == ""
	}
	return strings.TrimSpace(stringFromAny(query["kind"])) == "system-init"
}

// lineOrigin returns who sent the line's input. Older lines recorded it only
// through query.role and query.runOrigin.
func lineOrigin(line map[string]any) *queryinput.Origin {
	if origin := queryinput.OriginFromPayload(line["origin"]); origin != nil {
		return origin
	}
	query := anyMap(line["query"])
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
	if line["query"] == nil {
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
		TaskName:     strings.TrimSpace(stringFromAny(line["taskName"])),
		TaskToolID:   strings.TrimSpace(stringFromAny(line["taskToolId"])),
		TaskAgentKey: lineTaskAgentKey(line),
		Lane:         strings.TrimSpace(stringFromAny(line["lane"])),
		Origin:       queryinput.OriginFromPayload(line["origin"]),
		AccessGrant:  cloneStringAnyMap(anyMap(line["accessGrant"])),
		PlanRunID:    strings.TrimSpace(stringFromAny(line["planRunId"])),
		AccessLevel:  strings.TrimSpace(stringFromAny(line["accessLevel"])),
		Hidden:       boolFromAny(line["hidden"]),
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
	if hidden, ok := payload["hidden"].(bool); ok && hidden {
		// The effective visibility, including platform defaults such as an
		// Automation's hidden input or a side lane.
		line.Hidden = true
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
