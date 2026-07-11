package chat

import (
	"encoding/json"
	"strings"
)

// LoadRawMessages loads conversation history from {chatId}.jsonl step lines.
func (s *FileStore) LoadRawMessages(chatID string, k int) ([]map[string]any, error) {
	return loadRawMessagesFromPath(s.chatJSONLPath(chatID), k)
}

func loadRawMessagesFromPath(path string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = DefaultHistoryRunWindow
	}

	lines, err := readJSONLines(path)
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	messages := rawMessagesFromJSONLLines(lines)
	if len(messages) == 0 {
		return nil, nil
	}
	return limitRawMessagesByRuns(messages, k, hasActiveCompactCheckpoint(lines)), nil
}

func (s *FileStore) LoadTeamMemberRawMessages(chatID string, k int, memberAgentKey string) ([]map[string]any, error) {
	if k <= 0 {
		k = DefaultHistoryRunWindow
	}
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	messages := teamMemberRawMessagesFromJSONLLines(lines, memberAgentKey)
	return limitRawMessagesByRuns(messages, k, hasActiveCompactCheckpoint(lines)), nil
}

func (s *FileStore) LoadTeamCoordinatorRawMessages(chatID string, k int) ([]map[string]any, error) {
	if k <= 0 {
		k = DefaultHistoryRunWindow
	}
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	messages := teamCoordinatorRawMessagesFromJSONLLines(lines)
	return limitRawMessagesByRuns(messages, k, hasActiveCompactCheckpoint(lines)), nil
}

func limitRawMessagesByRuns(messages []map[string]any, k int, compacted bool) []map[string]any {
	if len(messages) == 0 || compacted {
		return messages
	}

	// Group by runId, keep last K runs (sliding window)
	type runBucket struct {
		runID    string
		messages []map[string]any
	}
	var runs []*runBucket
	runIndex := map[string]*runBucket{}
	for _, msg := range messages {
		runID, _ := msg["runId"].(string)
		if runID == "" {
			bucket := &runBucket{messages: []map[string]any{msg}}
			runs = append(runs, bucket)
			continue
		}
		bucket, ok := runIndex[runID]
		if !ok {
			bucket = &runBucket{runID: runID}
			runIndex[runID] = bucket
			runs = append(runs, bucket)
		}
		bucket.messages = append(bucket.messages, msg)
	}
	if len(runs) > k {
		runs = runs[len(runs)-k:]
	}
	var result []map[string]any
	for _, bucket := range runs {
		result = append(result, bucket.messages...)
	}
	return result
}

// loadRawMessagesFromJSONL extracts OpenAI-format messages from step lines.
func (s *FileStore) loadRawMessagesFromJSONL(chatID string) []map[string]any {
	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil || len(lines) == 0 {
		return nil
	}
	return rawMessagesFromJSONLLines(lines)
}

func rawMessagesFromJSONLLines(lines []map[string]any) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}

	var messages []map[string]any
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)

		switch lineType {
		case CompactCheckpointLineType:
			summary, ok := activeCompactCheckpointSummary(line)
			if !ok {
				continue
			}
			messages = append(messages, map[string]any{
				"role":    "user",
				"content": compactCheckpointSummaryMessage(summary),
				"ts":      line["updatedAt"],
			})
		case "query":
			if lineIsSystemInitQuery(line) {
				continue
			}
			// Child-agent query lines are persisted for replay and system-init
			// lookup, but their task prompts are not part of the shared chat
			// conversation. Replaying them as user messages leaks internal
			// orchestration instructions into later root-agent turns.
			if strings.TrimSpace(stringValue(line["taskId"])) != "" ||
				strings.TrimSpace(stringValue(line["subAgentKey"])) != "" {
				continue
			}
			if rawMsgs, _ := line["messages"].([]any); len(rawMsgs) > 0 {
				for _, raw := range rawMsgs {
					m, _ := raw.(map[string]any)
					if m == nil {
						continue
					}
					msg := cloneMessageMap(m)
					msg["runId"] = runID
					if _, ok := msg["ts"]; !ok {
						msg["ts"] = line["updatedAt"]
					}
					messages = append(messages, msg)
				}
			}

		case StepLineTypeStep, StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute:
			if strings.TrimSpace(stringValue(line["taskSubAgentKey"])) != "" {
				continue
			}
			rawMsgs, _ := line["messages"].([]any)
			for _, raw := range rawMsgs {
				m, _ := raw.(map[string]any)
				if m == nil {
					continue
				}
				role, _ := m["role"].(string)
				msg := map[string]any{"runId": runID}
				for k, v := range m {
					msg[k] = v
				}
				// Flatten content parts to plain text for LLM context
				if role == "user" || role == "assistant" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
					if parts, ok := m["reasoning_content"].([]any); ok {
						msg["reasoning_content"] = extractTextFromContent(parts)
					}
				}
				if role == "tool" {
					if parts, ok := m["content"].([]any); ok {
						msg["content"] = extractTextFromContent(parts)
					}
				}
				messages = append(messages, msg)
			}
		}
	}
	return messages
}

func teamMemberRawMessagesFromJSONLLines(lines []map[string]any, memberAgentKey string) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}
	memberAgentKey = strings.TrimSpace(memberAgentKey)
	var messages []map[string]any
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)
		switch lineType {
		case CompactCheckpointLineType:
			summary, ok := activeCompactCheckpointSummary(line)
			if ok {
				messages = append(messages, map[string]any{"role": "user", "content": compactCheckpointSummaryMessage(summary), "ts": line["updatedAt"]})
			}
		case "query":
			if lineIsSystemInitQuery(line) || strings.TrimSpace(stringValue(line["taskId"])) != "" || strings.TrimSpace(stringValue(line["subAgentKey"])) != "" {
				continue
			}
			for _, raw := range anyMessageSlice(line["messages"]) {
				msg := cloneMessageMap(raw)
				msg["runId"] = runID
				if _, ok := msg["ts"]; !ok {
					msg["ts"] = line["updatedAt"]
				}
				messages = append(messages, msg)
			}
		case StepLineTypeStep, StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute:
			taskAgentKey := strings.TrimSpace(stringValue(line["taskSubAgentKey"]))
			if taskAgentKey != "" && strings.EqualFold(taskAgentKey, memberAgentKey) {
				messages = append(messages, normalizedStepMessages(line, runID)...)
				continue
			}
			if final := finalAssistantMessageFromStep(line, runID, taskAgentKey); final != nil {
				messages = append(messages, final)
			}
		}
	}
	return messages
}

func teamCoordinatorRawMessagesFromJSONLLines(lines []map[string]any) []map[string]any {
	if !isNewFormat(lines) {
		return nil
	}
	var messages []map[string]any
	for _, line := range lines {
		if lineIsCompacted(line) {
			continue
		}
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)
		switch lineType {
		case CompactCheckpointLineType:
			summary, ok := activeCompactCheckpointSummary(line)
			if ok {
				messages = append(messages, map[string]any{"role": "user", "content": compactCheckpointSummaryMessage(summary), "ts": line["updatedAt"]})
			}
		case "query":
			if lineIsSystemInitQuery(line) || strings.TrimSpace(stringValue(line["taskId"])) != "" || strings.TrimSpace(stringValue(line["subAgentKey"])) != "" {
				continue
			}
			for _, raw := range anyMessageSlice(line["messages"]) {
				msg := cloneMessageMap(raw)
				msg["runId"] = runID
				if _, ok := msg["ts"]; !ok {
					msg["ts"] = line["updatedAt"]
				}
				messages = append(messages, msg)
			}
		case StepLineTypeStep, StepLineTypeReact, StepLineTypeReactTool, StepLineTypePlanExecute:
			taskAgentKey := strings.TrimSpace(stringValue(line["taskSubAgentKey"]))
			if taskAgentKey != "" {
				if final := finalAssistantMessageFromStep(line, runID, taskAgentKey); final != nil {
					messages = append(messages, final)
				}
				continue
			}
			if routing := safeTeamRoutingMessageFromStep(line, runID); routing != nil {
				messages = append(messages, routing)
			}
			if final := finalAssistantMessageFromStep(line, runID, ""); final != nil {
				messages = append(messages, final)
			}
		}
	}
	return messages
}

func safeTeamRoutingMessageFromStep(line map[string]any, runID string) map[string]any {
	var records []string
	for _, message := range anyMessageSlice(line["messages"]) {
		if !strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "assistant") {
			continue
		}
		for _, call := range anyMessageSlice(message["tool_calls"]) {
			function := mapValue(call["function"])
			name := strings.TrimSpace(stringValue(function["name"]))
			if name != "team_delegate" && name != "team_invoke" {
				continue
			}
			args := map[string]any{}
			switch raw := function["arguments"].(type) {
			case string:
				_ = json.Unmarshal([]byte(raw), &args)
			case map[string]any:
				args = raw
			}
			if name == "team_delegate" {
				record := "team_delegate mode=" + strings.TrimSpace(stringValue(args["mode"]))
				if memberKey := strings.TrimSpace(stringValue(args["memberKey"])); memberKey != "" {
					record += " memberKey=" + memberKey
				}
				records = append(records, strings.TrimSpace(record))
				continue
			}
			var members []string
			for _, task := range anyMessageSlice(args["tasks"]) {
				if memberKey := strings.TrimSpace(stringValue(task["memberKey"])); memberKey != "" {
					members = append(members, memberKey)
				}
			}
			records = append(records, "team_invoke memberKeys="+strings.Join(members, ","))
		}
	}
	if len(records) == 0 {
		return nil
	}
	return map[string]any{
		"role":    "assistant",
		"content": "[Team routing record]\n" + strings.Join(records, "\n"),
		"runId":   runID,
		"ts":      line["updatedAt"],
	}
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func anyMessageSlice(value any) []map[string]any {
	items, _ := value.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && item != nil {
			out = append(out, item)
		}
	}
	return out
}

func normalizedStepMessages(line map[string]any, runID string) []map[string]any {
	var out []map[string]any
	for _, raw := range anyMessageSlice(line["messages"]) {
		msg := cloneMessageMap(raw)
		msg["runId"] = runID
		role, _ := msg["role"].(string)
		if role == "user" || role == "assistant" || role == "tool" {
			if parts, ok := msg["content"].([]any); ok {
				msg["content"] = extractTextFromContent(parts)
			}
		}
		if role == "user" || role == "assistant" {
			if parts, ok := msg["reasoning_content"].([]any); ok {
				msg["reasoning_content"] = extractTextFromContent(parts)
			}
		}
		out = append(out, msg)
	}
	return out
}

func finalAssistantMessageFromStep(line map[string]any, runID string, actorAgentKey string) map[string]any {
	var selected map[string]any
	for _, raw := range anyMessageSlice(line["messages"]) {
		role, _ := raw["role"].(string)
		if role != "assistant" || len(anyMessageSlice(raw["tool_calls"])) > 0 {
			continue
		}
		msg := cloneMessageMap(raw)
		if parts, ok := msg["content"].([]any); ok {
			msg["content"] = extractTextFromContent(parts)
		}
		content, _ := msg["content"].(string)
		if strings.TrimSpace(content) == "" {
			continue
		}
		selected = msg
	}
	if selected == nil {
		return nil
	}
	delete(selected, "reasoning_content")
	delete(selected, "tool_calls")
	selected["runId"] = runID
	if actorAgentKey == "" && strings.EqualFold(strings.TrimSpace(stringValue(selected["actorType"])), "agent") {
		actorAgentKey = strings.TrimSpace(stringValue(selected["agentKey"]))
	}
	if actorAgentKey != "" {
		selected["content"] = "[Team member " + actorAgentKey + "]\n" + strings.TrimSpace(stringValue(selected["content"]))
	}
	return selected
}

func cloneMessageMaps(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		out = append(out, cloneMessageMap(msg))
	}
	return out
}

func cloneMessageMap(message map[string]any) map[string]any {
	if message == nil {
		return nil
	}
	data, err := json.Marshal(message)
	if err != nil {
		return cloneStringAnyMap(message)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return cloneStringAnyMap(message)
	}
	return cloned
}

func extractStoredMessageText(message StoredMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
