package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	ChatStorageSchemaViolationCode     = "chat_storage_schema_violation"
	chatStorageSchemaExpectedLine      = "one JSON object per physical line"
	chatStorageSchemaExpectedLineTypes = "query|react|react-tool|event|steer|submit|compact.checkpoint|compact.run.checkpoint|compact.tool"
)

var currentJSONLLineTypes = map[string]struct{}{
	"query":                      {},
	StepLineTypeReact:            {},
	StepLineTypeReactTool:        {},
	"event":                      {},
	"steer":                      {},
	"submit":                     {},
	CompactCheckpointLineType:    {},
	RunCompactCheckpointLineType: {},
	ToolCompactLineType:          {},
}

var currentSteerLineFields = map[string]struct{}{
	"_compact":  {},
	"_type":     {},
	"chatId":    {},
	"runId":     {},
	"updatedAt": {},
	"liveSeq":   {},
	"steer":     {},
}

var currentSteerPayloadFields = map[string]struct{}{
	"requestId": {},
	"chatId":    {},
	"runId":     {},
	"steerId":   {},
	"message":   {},
	"role":      {},
}

// JSONLSchemaViolation reports persisted chat data that does not satisfy the
// current storage schema. It deliberately exposes only structural metadata.
type JSONLSchemaViolation struct {
	Field    string
	Location string
	Expected string
	Actual   string
	Reason   string
	ChatID   string
	RunID    string
}

func (e *JSONLSchemaViolation) Error() string {
	if e == nil {
		return "chat storage schema violation"
	}
	parts := []string{"chat storage schema violation"}
	if e.Location != "" {
		parts = append(parts, "location="+e.Location)
	}
	if e.Field != "" {
		parts = append(parts, "field="+e.Field)
	}
	if e.ChatID != "" {
		parts = append(parts, "chatId="+e.ChatID)
	}
	if e.RunID != "" {
		parts = append(parts, "runId="+e.RunID)
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	return strings.Join(parts, " ")
}

func IsJSONLSchemaViolation(err error) bool {
	var violation *JSONLSchemaViolation
	return errors.As(err, &violation)
}

func JSONLSchemaErrorData(err error) map[string]any {
	data := map[string]any{
		"code":      ChatStorageSchemaViolationCode,
		"status":    422,
		"retryable": false,
	}
	var violation *JSONLSchemaViolation
	if !errors.As(err, &violation) || violation == nil {
		return data
	}
	if violation.Field != "" {
		data["field"] = violation.Field
	}
	if violation.Location != "" {
		data["location"] = violation.Location
	}
	if violation.Expected != "" {
		data["expected"] = violation.Expected
	}
	if violation.Actual != "" {
		data["actual"] = violation.Actual
	}
	return data
}

func newJSONLSchemaViolation(line map[string]any, field, expected, actual, reason string) *JSONLSchemaViolation {
	return &JSONLSchemaViolation{
		Field:    field,
		Expected: expected,
		Actual:   actual,
		Reason:   reason,
		ChatID:   strings.TrimSpace(stringFromAny(line["chatId"])),
		RunID:    strings.TrimSpace(stringFromAny(line["runId"])),
	}
}

func withJSONLSchemaLocation(err error, location string) error {
	var violation *JSONLSchemaViolation
	if errors.As(err, &violation) && violation != nil {
		copy := *violation
		copy.Location = location
		return &copy
	}
	return &JSONLSchemaViolation{
		Field:    "$",
		Location: location,
		Expected: "current chat storage schema",
		Reason:   err.Error(),
	}
}

func validateCurrentJSONLLine(line map[string]any) error {
	rawType, found := line["_type"]
	if !found {
		return newJSONLSchemaViolation(line, "_type", chatStorageSchemaExpectedLineTypes, "missing", "_type is required")
	}
	lineType, ok := rawType.(string)
	if !ok || lineType == "" || lineType != strings.TrimSpace(lineType) {
		return newJSONLSchemaViolation(line, "_type", chatStorageSchemaExpectedLineTypes, jsonValueType(rawType), "_type must be an exact non-empty string")
	}
	if _, ok := currentJSONLLineTypes[lineType]; !ok {
		return newJSONLSchemaViolation(line, "_type", chatStorageSchemaExpectedLineTypes, lineType, "unsupported line type")
	}
	if err := validateCurrentCompactMarkerSchema(line); err != nil {
		return err
	}
	if lineType == "steer" {
		if err := validateCurrentSteerSchema(line); err != nil {
			return err
		}
	}

	event, hasEvent := line["event"].(map[string]any)
	if lineType == "event" {
		if !hasEvent || len(event) == 0 {
			return newJSONLSchemaViolation(line, "event", "non-empty JSON object", jsonValueType(line["event"]), "event payload is required")
		}
	}
	if hasEvent {
		switch strings.TrimSpace(stringFromAny(event["type"])) {
		case "awaiting.ask", "planning.snapshot":
			return newJSONLSchemaViolation(line, "event.type", "persistable event type", stringFromAny(event["type"]), "event type is not persisted as an event line")
		case "request.steer":
			return newJSONLSchemaViolation(line, "event.type", "non-steer event type", stringFromAny(event["type"]), "request.steer is persisted only as a steer line")
		}
	}

	if err := validateCurrentAwaitingSchema(line); err != nil {
		return err
	}
	if err := validatePersistedSystemInitSchema([]map[string]any{line}); err != nil {
		return newJSONLSchemaViolation(line, "system", "single query.system and exact step.systemRef", "invalid", err.Error())
	}
	return nil
}

func validateCurrentCompactMarkerSchema(line map[string]any) error {
	rawCompactID, found := line["_compact"]
	if !found {
		return nil
	}
	compactID, ok := rawCompactID.(string)
	if !ok || compactID == "" || compactID != strings.TrimSpace(compactID) {
		return newJSONLSchemaViolation(line, "_compact", "exact non-empty string", jsonValueType(rawCompactID), "_compact must be an exact non-empty string")
	}
	return nil
}

func validateCurrentSteerSchema(line map[string]any) error {
	for field, value := range line {
		if _, ok := currentSteerLineFields[field]; !ok {
			return newJSONLSchemaViolation(line, field, "steer line known fields", jsonValueType(value), "unknown steer line field")
		}
	}
	for _, field := range []string{"chatId", "runId"} {
		value, ok := line[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return newJSONLSchemaViolation(line, field, "non-empty string", jsonValueType(line[field]), "steer line identity field is required")
		}
	}

	rawSteer, found := line["steer"]
	steer, ok := rawSteer.(map[string]any)
	if !found || !ok || len(steer) == 0 {
		return newJSONLSchemaViolation(line, "steer", "non-empty JSON object", jsonValueType(rawSteer), "steer payload is required")
	}
	for field, value := range steer {
		if _, ok := currentSteerPayloadFields[field]; !ok {
			return newJSONLSchemaViolation(line, "steer."+field, "steer payload known fields", jsonValueType(value), "unknown steer payload field")
		}
	}
	for _, field := range []string{"chatId", "runId", "steerId", "message", "role"} {
		value, ok := steer[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return newJSONLSchemaViolation(line, "steer."+field, "non-empty string", jsonValueType(steer[field]), "steer payload field is required")
		}
	}
	if requestID, found := steer["requestId"]; found {
		value, ok := requestID.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return newJSONLSchemaViolation(line, "steer.requestId", "non-empty string when present", jsonValueType(requestID), "empty requestId must be omitted")
		}
	}
	if stringFromAny(steer["role"]) != "user" {
		return newJSONLSchemaViolation(line, "steer.role", "user", stringFromAny(steer["role"]), "steer role must be user")
	}
	for _, field := range []string{"chatId", "runId"} {
		if stringFromAny(steer[field]) != stringFromAny(line[field]) {
			return newJSONLSchemaViolation(line, "steer."+field, "value matching top-level "+field, "mismatch", "steer payload identity must match its line")
		}
	}
	return nil
}

func validateCurrentAwaitingSchema(line map[string]any) error {
	raw, found := line["awaiting"]
	if !found {
		return nil
	}
	lineType := strings.TrimSpace(stringFromAny(line["_type"]))
	if lineType != StepLineTypeReact && lineType != StepLineTypeReactTool {
		return newJSONLSchemaViolation(line, "awaiting", "step awaiting[]", lineType, "awaiting is only persisted on step lines")
	}
	items, ok := raw.([]any)
	if !ok {
		return newJSONLSchemaViolation(line, "awaiting", "array of awaiting.ask objects", jsonValueType(raw), "awaiting must be an array")
	}
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		field := fmt.Sprintf("awaiting[%d]", index)
		if !ok || len(item) == 0 {
			return newJSONLSchemaViolation(line, field, "awaiting.ask object", jsonValueType(rawItem), "awaiting item must be an object")
		}
		if strings.TrimSpace(stringFromAny(item["type"])) != "awaiting.ask" {
			return newJSONLSchemaViolation(line, field+".type", "awaiting.ask", stringFromAny(item["type"]), "unsupported awaiting item type")
		}
		mode := strings.TrimSpace(stringFromAny(item["mode"]))
		switch mode {
		case "question", "approval", "form":
		case "planning":
			planning, ok := item["planning"].(map[string]any)
			if !ok || len(planning) == 0 {
				return newJSONLSchemaViolation(line, field+".planning", "planning object", jsonValueType(item["planning"]), "planning payload is required")
			}
			if strings.TrimSpace(stringFromAny(planning["planningId"])) == "" {
				return newJSONLSchemaViolation(line, field+".planning.planningId", "non-empty string", "missing", "planningId is required")
			}
			if strings.TrimSpace(stringFromAny(planning["planningFile"])) == "" {
				return newJSONLSchemaViolation(line, field+".planning.planningFile", "non-empty string", "missing", "planningFile is required")
			}
		default:
			return newJSONLSchemaViolation(line, field+".mode", "question|approval|form|planning", mode, "unsupported awaiting mode")
		}
	}
	return nil
}

func jsonValueType(value any) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprintf("%T", value)
}

func decodeJSONLRecords(data []byte, baseLocation string, useNumber bool) ([]jsonLineRecord, error) {
	if len(data) == 0 {
		return []jsonLineRecord{}, nil
	}
	records := make([]jsonLineRecord, 0, bytes.Count(data, []byte{'\n'})+1)
	for offset, lineNumber := 0, 1; offset < len(data); lineNumber++ {
		remainder := data[offset:]
		newline := bytes.IndexByte(remainder, '\n')
		var physicalLine []byte
		if newline < 0 {
			physicalLine = remainder
			offset = len(data)
		} else {
			physicalLine = remainder[:newline]
			offset += newline + 1
		}
		if len(physicalLine) > 0 && physicalLine[len(physicalLine)-1] == '\r' {
			physicalLine = physicalLine[:len(physicalLine)-1]
		}
		location := fmt.Sprintf("%s[%d]", strings.TrimSpace(baseLocation), lineNumber)
		if len(bytes.TrimSpace(physicalLine)) == 0 {
			return nil, &JSONLSchemaViolation{Field: "$", Location: location, Expected: chatStorageSchemaExpectedLine, Actual: "blank", Reason: "blank lines are not allowed"}
		}

		decoder := json.NewDecoder(bytes.NewReader(physicalLine))
		if useNumber {
			decoder.UseNumber()
		}
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, &JSONLSchemaViolation{Field: "$", Location: location, Expected: chatStorageSchemaExpectedLine, Actual: "invalid_json", Reason: "invalid JSON object"}
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, &JSONLSchemaViolation{Field: "$", Location: location, Expected: chatStorageSchemaExpectedLine, Actual: "multiple_values", Reason: "each physical line must contain exactly one JSON object"}
		}
		payload, ok := decoded.(map[string]any)
		if !ok || payload == nil {
			return nil, &JSONLSchemaViolation{Field: "$", Location: location, Expected: chatStorageSchemaExpectedLine, Actual: jsonValueType(decoded), Reason: "line value must be a JSON object"}
		}
		if err := validateCurrentJSONLLine(payload); err != nil {
			return nil, withJSONLSchemaLocation(err, location)
		}
		records = append(records, jsonLineRecord{Raw: bytes.TrimSpace(physicalLine), Value: payload})
	}
	return records, nil
}

// ValidateJSONLContent validates raw chat JSONL against the current storage
// schema and time contract without rewriting it.
func ValidateJSONLContent(content string, baseLocation string) error {
	records, err := decodeJSONLRecords([]byte(content), baseLocation, true)
	if err != nil {
		return err
	}
	lines := recordValues(records)
	return validatePersistedTimeContract(lines, baseLocation)
}
