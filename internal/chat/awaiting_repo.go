package chat

import (
	"fmt"
	"os"
	"strings"

	"agent-platform/internal/timecontract"
)

func (s *FileStore) LoadAllPendingAwaitings() ([]PendingAwaitingWithChat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT CHAT_ID_, AWAITING_ID_, AWAITING_RUN_ID_, AWAITING_MODE_, AWAITING_CREATED_AT_
		FROM CHATS
		WHERE AWAITING_ID_ != ''
		ORDER BY CHAT_ID_`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []PendingAwaitingWithChat
	for rows.Next() {
		var item PendingAwaitingWithChat
		if err := rows.Scan(&item.ChatID, &item.AwaitingID, &item.RunID, &item.Mode, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := timecontract.ValidateEpochMillis(item.CreatedAt, "createdAt", fmt.Sprintf("chat.pendingAwaiting[%s].createdAt", item.ChatID)); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *FileStore) LoadAwaitingAsk(chatID string, awaitingID string) (*PersistedAwaitingAsk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		lines = nil
	}
	return loadPersistedAwaitingAskFromLines(lines, awaitingID), nil
}

func (s *FileStore) LoadAwaitingSubmit(chatID string, awaitingID string, submitID string) (*PersistedAwaitingSubmit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		lines = nil
	}
	return loadPersistedAwaitingSubmitFromLines(lines, chatID, awaitingID, submitID), nil
}

func (s *FileStore) LoadLatestAwaitingSubmit(chatID string, awaitingID string) (*PersistedAwaitingSubmit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		lines = nil
	}
	return loadPersistedAwaitingSubmitFromLines(lines, chatID, awaitingID, ""), nil
}

func (s *FileStore) LoadAwaitingStep(chatID string, awaitingID string) (*PersistedAwaitingStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return loadPersistedAwaitingStepFromLines(lines, awaitingID), nil
}

func loadPersistedAwaitingStepFromLines(lines []map[string]any, awaitingID string) *PersistedAwaitingStep {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return nil
	}

	var latest *PersistedAwaitingStep
	for _, line := range lines {
		lineType := strings.TrimSpace(stringValue(line["_type"]))
		if lineType != StepLineTypeReact && lineType != StepLineTypeReactTool {
			continue
		}
		var matchedAsk *PersistedAwaitingAsk
		for _, raw := range anyValueSlice(line["awaiting"]) {
			candidate := persistedAwaitingAskFromMap(mapValue(raw), stringValue(line["runId"]))
			if candidate != nil && candidate.AwaitingID == awaitingID {
				matchedAsk = candidate
				break
			}
		}
		if matchedAsk == nil {
			continue
		}
		latest = &PersistedAwaitingStep{
			RunID:           strings.TrimSpace(stringValue(line["runId"])),
			TaskID:          strings.TrimSpace(stringValue(line["taskId"])),
			TaskStatus:      strings.TrimSpace(stringValue(line["taskStatus"])),
			TaskSubAgentKey: strings.TrimSpace(stringValue(line["taskSubAgentKey"])),
			TeamID:          strings.TrimSpace(stringValue(line["teamId"])),
			Presentation:    strings.TrimSpace(stringValue(line["presentation"])),
			Stage:           strings.TrimSpace(stringValue(line["stage"])),
			Seq:             int(int64FromAny(line["seq"])),
			Ask:             matchedAsk,
			ToolCalls:       persistedAwaitingToolCalls(line["messages"]),
			ResultToolIDs:   map[string]bool{},
		}
	}
	if latest == nil {
		return nil
	}

	for _, line := range lines {
		lineType := strings.TrimSpace(stringValue(line["_type"]))
		if lineType != StepLineTypeReact && lineType != StepLineTypeReactTool {
			continue
		}
		if strings.TrimSpace(stringValue(line["runId"])) != latest.RunID ||
			strings.TrimSpace(stringValue(line["taskId"])) != latest.TaskID ||
			int(int64FromAny(line["seq"])) != latest.Seq {
			continue
		}
		for _, rawMessage := range anyValueSlice(line["messages"]) {
			message := mapValue(rawMessage)
			if !strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "tool") {
				continue
			}
			toolID := strings.TrimSpace(stringValue(message["tool_call_id"]))
			if toolID == "" {
				toolID = strings.TrimSpace(stringValue(message["_toolId"]))
			}
			if toolID != "" {
				latest.ResultToolIDs[toolID] = true
			}
		}
	}
	return latest
}

func persistedAwaitingToolCalls(rawMessages any) []PersistedAwaitingToolCall {
	var result []PersistedAwaitingToolCall
	seen := map[string]bool{}
	for _, rawMessage := range anyValueSlice(rawMessages) {
		message := mapValue(rawMessage)
		if !strings.EqualFold(strings.TrimSpace(stringValue(message["role"])), "assistant") {
			continue
		}
		for _, rawCall := range anyValueSlice(message["tool_calls"]) {
			call := mapValue(rawCall)
			toolID := strings.TrimSpace(stringValue(call["id"]))
			if toolID == "" || seen[toolID] {
				continue
			}
			seen[toolID] = true
			result = append(result, PersistedAwaitingToolCall{
				ID:   toolID,
				Name: strings.TrimSpace(stringValue(mapValue(call["function"])["name"])),
			})
		}
	}
	return result
}

func anyValueSlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func (s *FileStore) LoadRunQuery(chatID string, runID string) (*QueryLine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		lines = nil
	}
	return loadRunQueryFromLines(lines, chatID, runID), nil
}

func loadPersistedAwaitingAskFromLines(lines []map[string]any, awaitingID string) *PersistedAwaitingAsk {
	awaitingID = strings.TrimSpace(awaitingID)
	if awaitingID == "" {
		return nil
	}

	var latest *PersistedAwaitingAsk
	for _, line := range lines {
		lineType, _ := line["_type"].(string)
		runID, _ := line["runId"].(string)
		switch lineType {
		case StepLineTypeReact, StepLineTypeReactTool:
			awaitingItems, _ := line["awaiting"].([]any)
			for _, raw := range awaitingItems {
				item, _ := raw.(map[string]any)
				if item == nil {
					continue
				}
				candidate := persistedAwaitingAskFromMap(item, runID)
				if candidate != nil && candidate.AwaitingID == awaitingID {
					latest = candidate
				}
			}
		case "submit":
			answer, _ := line["answer"].(map[string]any)
			if isPersistedAwaitingAnswer(answer, awaitingID) {
				latest = nil
			}
		}
	}
	return latest
}

func loadPersistedAwaitingSubmitFromLines(lines []map[string]any, chatID string, awaitingID string, submitID string) *PersistedAwaitingSubmit {
	awaitingID = strings.TrimSpace(awaitingID)
	submitID = strings.TrimSpace(submitID)
	if awaitingID == "" {
		return nil
	}
	var latest *PersistedAwaitingSubmit
	for _, line := range lines {
		if strings.TrimSpace(stringValue(line["_type"])) != "submit" {
			continue
		}
		submit, _ := line["submit"].(map[string]any)
		answer, _ := line["answer"].(map[string]any)
		if !submitLineMatchesAwaiting(submit, answer, awaitingID) {
			continue
		}
		lineSubmitID := firstNonBlankSubmitLineValue(submit, answer, "submitId")
		if submitID != "" && lineSubmitID != submitID {
			continue
		}
		lineChatID := firstNonBlankSubmitLineValue(submit, answer, "chatId")
		if lineChatID == "" {
			lineChatID = strings.TrimSpace(chatID)
		}
		latest = &PersistedAwaitingSubmit{
			ChatID:     lineChatID,
			RunID:      firstNonBlankSubmitLineValue(submit, answer, "runId"),
			AwaitingID: awaitingID,
			SubmitID:   lineSubmitID,
			UpdatedAt:  int64FromAny(line["updatedAt"]),
			Submit:     cloneStringAnyMap(submit),
			Answer:     cloneStringAnyMap(answer),
		}
	}
	return latest
}

func submitLineMatchesAwaiting(submit map[string]any, answer map[string]any, awaitingID string) bool {
	if strings.TrimSpace(stringValue(submit["awaitingId"])) == awaitingID {
		return true
	}
	if strings.TrimSpace(stringValue(answer["awaitingId"])) == awaitingID {
		return true
	}
	return false
}

func firstNonBlankSubmitLineValue(submit map[string]any, answer map[string]any, key string) string {
	if value := strings.TrimSpace(stringValue(submit[key])); value != "" {
		return value
	}
	return strings.TrimSpace(stringValue(answer[key]))
}

func loadRunQueryFromLines(lines []map[string]any, chatID string, runID string) *QueryLine {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil
	}
	for _, line := range lines {
		if strings.TrimSpace(stringValue(line["_type"])) != "query" {
			continue
		}
		if lineIsSystemInitQuery(line) {
			continue
		}
		if strings.TrimSpace(stringValue(line["runId"])) != runID {
			continue
		}
		query, _ := line["query"].(map[string]any)
		if query == nil {
			continue
		}
		lineChatID := firstNonBlankSubmitLineValue(line, query, "chatId")
		if lineChatID == "" {
			lineChatID = strings.TrimSpace(chatID)
		}
		return &QueryLine{
			Type:      "query",
			ChatID:    lineChatID,
			RunID:     runID,
			UpdatedAt: int64FromAny(line["updatedAt"]),
			Query:     cloneStringAnyMap(query),
		}
	}
	return nil
}

func persistedAwaitingAskFromMap(item map[string]any, fallbackRunID string) *PersistedAwaitingAsk {
	if item == nil || strings.TrimSpace(stringValue(item["type"])) != "awaiting.ask" {
		return nil
	}
	payload := cloneStringAnyMap(item)
	if _, ok := payload["runId"]; !ok && strings.TrimSpace(fallbackRunID) != "" {
		payload["runId"] = fallbackRunID
	}
	awaitingID := strings.TrimSpace(stringValue(payload["awaitingId"]))
	if awaitingID == "" {
		return nil
	}
	return &PersistedAwaitingAsk{
		AwaitingID: awaitingID,
		RunID:      strings.TrimSpace(stringValue(payload["runId"])),
		Mode:       strings.TrimSpace(stringValue(payload["mode"])),
		Payload:    payload,
	}
}

func (s *FileStore) SetPendingAwaiting(chatID string, pending PendingAwaiting) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if summary, err := s.loadSummary(chatID); err != nil {
		return err
	} else if summary == nil {
		return ErrChatNotFound
	}
	if err := timecontract.ValidateEpochMillis(pending.CreatedAt, "createdAt", "chat.pendingAwaiting.createdAt"); err != nil {
		return err
	}

	_, err := s.db.Exec(`UPDATE CHATS
		SET AWAITING_ID_=?, AWAITING_RUN_ID_=?, AWAITING_MODE_=?, AWAITING_CREATED_AT_=?
		WHERE CHAT_ID_=?`,
		pending.AwaitingID, pending.RunID, pending.Mode, pending.CreatedAt, chatID)
	return err
}

func isPersistedAwaitingAnswer(item map[string]any, awaitingID string) bool {
	if item == nil || strings.TrimSpace(stringValue(item["type"])) != "awaiting.answer" {
		return false
	}
	return strings.TrimSpace(stringValue(item["awaitingId"])) == strings.TrimSpace(awaitingID)
}

func (s *FileStore) ClearPendingAwaiting(chatID string, awaitingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if summary, err := s.loadSummary(chatID); err != nil {
		return err
	} else if summary == nil {
		return ErrChatNotFound
	}

	_, err := s.db.Exec(`UPDATE CHATS
		SET AWAITING_ID_='', AWAITING_RUN_ID_='', AWAITING_MODE_='', AWAITING_CREATED_AT_=0
		WHERE CHAT_ID_=? AND AWAITING_ID_=?`,
		chatID, awaitingID)
	return err
}
