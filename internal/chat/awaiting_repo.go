package chat

import (
	"os"
	"strings"
	"time"

	"agent-platform/internal/stream"
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
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *FileStore) LoadAwaitingAsk(chatID string, awaitingID string) (*PersistedAwaitingAsk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines, err := readJSONLines(s.chatJSONLPath(chatID))
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		lines = nil
	}
	return loadPersistedAwaitingAskFromLines(lines, awaitingID), nil
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
		case "react", "plan-execute", "step":
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
		case "event", "steer":
			event, _ := line["event"].(map[string]any)
			candidate := persistedAwaitingAskFromMap(event, runID)
			if candidate != nil && candidate.AwaitingID == awaitingID {
				latest = candidate
			}
		default:
			candidate := persistedAwaitingAskFromMap(line, runID)
			if candidate != nil && candidate.AwaitingID == awaitingID {
				latest = candidate
			}
		}
	}
	return latest
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

func (s *FileStore) PersistAwaitingAsk(chatID string, pending PendingAwaiting, event stream.EventData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	awaitingID := strings.TrimSpace(pending.AwaitingID)
	if awaitingID == "" {
		awaitingID = strings.TrimSpace(event.String("awaitingId"))
	}
	if awaitingID == "" {
		return nil
	}
	runID := strings.TrimSpace(pending.RunID)
	if runID == "" {
		runID = strings.TrimSpace(event.String("runId"))
	}
	mode := strings.TrimSpace(pending.Mode)
	if mode == "" {
		mode = strings.TrimSpace(event.String("mode"))
	}
	createdAt := pending.CreatedAt
	if createdAt <= 0 {
		createdAt = event.Timestamp
	}
	if createdAt <= 0 {
		createdAt = time.Now().UnixMilli()
	}

	eventPayload := event.Map()
	delete(eventPayload, "seq")
	if strings.TrimSpace(stringValue(eventPayload["type"])) == "" {
		eventPayload["type"] = "awaiting.ask"
	}
	if strings.TrimSpace(stringValue(eventPayload["awaitingId"])) == "" {
		eventPayload["awaitingId"] = awaitingID
	}
	if strings.TrimSpace(stringValue(eventPayload["mode"])) == "" && mode != "" {
		eventPayload["mode"] = mode
	}
	if strings.TrimSpace(stringValue(eventPayload["timestamp"])) == "" {
		eventPayload["timestamp"] = createdAt
	}

	if err := s.appendJSONLineLocked(s.chatJSONLPath(chatID), EventLine{
		ChatID:    chatID,
		RunID:     runID,
		UpdatedAt: createdAt,
		Event:     eventPayload,
		Type:      "event",
	}); err != nil {
		return err
	}

	_, err := s.db.Exec(`UPDATE CHATS
		SET AWAITING_ID_=?, AWAITING_RUN_ID_=?, AWAITING_MODE_=?, AWAITING_CREATED_AT_=?
		WHERE CHAT_ID_=?`,
		awaitingID, runID, mode, createdAt, chatID)
	return err
}

func (s *FileStore) SetPendingAwaiting(chatID string, pending PendingAwaiting) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS
		SET AWAITING_ID_=?, AWAITING_RUN_ID_=?, AWAITING_MODE_=?, AWAITING_CREATED_AT_=?
		WHERE CHAT_ID_=?`,
		pending.AwaitingID, pending.RunID, pending.Mode, pending.CreatedAt, chatID)
	return err
}

func (s *FileStore) ClearPendingAwaiting(chatID string, awaitingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`UPDATE CHATS
		SET AWAITING_ID_='', AWAITING_RUN_ID_='', AWAITING_MODE_='', AWAITING_CREATED_AT_=0
		WHERE CHAT_ID_=? AND AWAITING_ID_=?`,
		chatID, awaitingID)
	return err
}
