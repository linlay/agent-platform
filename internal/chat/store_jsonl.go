package chat

import (
	"errors"
	"os"
	"strings"
)

// readJSONLines reads the current chat JSONL format: exactly one JSON object
// per physical line.
func readJSONLines(path string) ([]map[string]any, error) {
	return readJSONLinesWithNumber(path, false)
}

func readJSONLinesWithNumber(path string, useNumber bool) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	records, err := decodeJSONLRecords(data, "chat.jsonl", useNumber)
	if err != nil {
		return nil, err
	}
	return recordValues(records), nil
}

// readPersistedJSONLines is the strict read boundary for active chat data.
func readPersistedJSONLines(path string) ([]map[string]any, error) {
	lines, err := readJSONLinesWithNumber(path, true)
	if err != nil {
		return nil, err
	}
	if err := validatePersistedTimeContract(lines, "chat.jsonl"); err != nil {
		return nil, err
	}
	return lines, nil
}

func (s *FileStore) LoadJSONLContent(chatID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return "", os.ErrPermission
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return "", err
	}
	if summary == nil {
		return "", ErrChatNotFound
	}
	content, err := readFileStringIfExists(s.chatJSONLPath(chatID))
	if err != nil {
		return "", err
	}
	if _, err := readPersistedJSONLines(s.chatJSONLPath(chatID)); err != nil {
		return "", err
	}
	return content, nil
}
