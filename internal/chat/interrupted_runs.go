package chat

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"agent-platform/internal/timecontract"
)

func (s *FileStore) LoadUnfinishedRuns() ([]RunStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT CHAT_ID_, RUN_ID_, AGENT_KEY_, COALESCE(AGENT_MODE_,''), INITIAL_MESSAGE_, STARTED_AT_ FROM RUNS WHERE COMPLETED_AT_=0 ORDER BY STARTED_AT_, RUN_ID_`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []RunStart
	for rows.Next() {
		var run RunStart
		if err := rows.Scan(&run.ChatID, &run.RunID, &run.AgentKey, &run.AgentMode, &run.InitialMessage, &run.StartedAtMillis); err != nil {
			return nil, err
		}
		if err := timecontract.ValidateEpochMillis(run.StartedAtMillis, "startedAt", "chat.unfinishedRun.startedAt"); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// CloseInterruptedRunTools preserves recorded content and supplies a terminal
// result for calls whose outcome was not persisted. Their execution may have
// started before the process stopped, so no executed:false claim is made.
func (s *FileStore) CloseInterruptedRunTools(chatID, runID string, completedAt int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := readPersistedJSONLines(s.chatJSONLPath(chatID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	type pendingGroup struct {
		step  StepLine
		calls []StoredToolCall
	}
	groups := map[string]*pendingGroup{}
	var order []string
	results := map[string]map[string]bool{}
	assistantText := ""
	cancelWritten := false
	for _, line := range lines {
		if strings.TrimSpace(stringValue(line["runId"])) != runID {
			continue
		}
		if event := mapValue(line["event"]); stringValue(event["type"]) == "run.cancel" && stringValue(event["reason"]) == "runtime_restarted" {
			cancelWritten = true
		}
		kind := stringValue(line["_type"])
		if kind != StepLineTypeReact && kind != StepLineTypeReactTool {
			continue
		}
		raw, err := json.Marshal(line)
		if err != nil {
			return "", err
		}
		var step StepLine
		if err := json.Unmarshal(raw, &step); err != nil {
			return "", err
		}
		key := legacyGroupFromLine(line).key()
		if results[key] == nil {
			results[key] = map[string]bool{}
		}
		for _, message := range step.Messages {
			if message.Role == "tool" {
				results[key][firstNonEmptyString(message.ToolCallID, message.ToolID)] = true
			}
			if message.Role != "assistant" {
				continue
			}
			if step.TaskID == "" {
				var parts []string
				for _, part := range message.Content {
					if part.Type == "text" {
						parts = append(parts, part.Text)
					}
				}
				if text := strings.Join(parts, ""); text != "" {
					assistantText = text
				}
			}
			if len(message.ToolCalls) > 0 {
				if groups[key] == nil {
					groups[key] = &pendingGroup{step: step}
					order = append(order, key)
				}
				groups[key].calls = append(groups[key].calls, message.ToolCalls...)
			}
		}
	}
	for _, key := range order {
		group := groups[key]
		var messages []StoredMessage
		for _, call := range group.calls {
			if call.ID == "" || call.Function.Name == "" || results[key][call.ID] {
				continue
			}
			results[key][call.ID] = true
			result, _ := json.Marshal(map[string]any{"error": "runtime_restarted", "exitCode": -1, "output": "Platform restarted before a result was recorded. This Run was canceled; check the operation state before requesting it again."})
			ts := completedAt
			messages = append(messages, StoredMessage{Role: "tool", Name: call.Function.Name, ToolCallID: call.ID, ToolID: call.ID, Ts: &ts, Content: []ContentPart{{Type: "text", Text: string(result)}}})
		}
		if len(messages) == 0 {
			continue
		}
		step := group.step
		line := StepLine{Type: StepLineTypeReactTool, ChatID: chatID, RunID: runID, UpdatedAt: completedAt, Seq: step.Seq, Stage: step.Stage, TaskID: step.TaskID, TaskStatus: step.TaskStatus, TaskAgentKey: step.TaskAgentKey, Presentation: step.Presentation, Messages: messages}
		if err := s.appendJSONLineLocked(s.chatJSONLPath(chatID), line); err != nil {
			return "", err
		}
	}
	if !cancelWritten {
		line := EventLine{Type: "event", ChatID: chatID, RunID: runID, UpdatedAt: completedAt, Event: map[string]any{"type": "run.cancel", "timestamp": completedAt, "runId": runID, "reason": "runtime_restarted"}}
		if err := s.appendJSONLineLocked(s.chatJSONLPath(chatID), line); err != nil {
			return "", err
		}
	}
	return assistantText, nil
}
