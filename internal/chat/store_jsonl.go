package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readJSONLines reads a JSONL file. Uses json.Decoder so it handles both
// single-line JSON objects (Go's writer) and pretty-printed multi-line JSON
// objects (Java may write either format).
func readJSONLines(path string) ([]map[string]any, error) {
	return readJSONLinesWithNumber(path, false)
}

func readJSONLinesWithNumber(path string, useNumber bool) ([]map[string]any, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var items []map[string]any
	decoder := json.NewDecoder(file)
	if useNumber {
		decoder.UseNumber()
	}
	for {
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse JSONL: %w", err)
		}
		if payload != nil {
			items = append(items, payload)
		}
	}
	return items, nil
}

// readPersistedJSONLines is the single strict read boundary for active chat
// history.  readJSONLines intentionally remains a low-level decoder for
// writers/tests and must not be used by a public replay/history consumer:
// doing so would let legacy strings, seconds, zeroes, or missing required
// timestamps escape validation before the consumer derives state from them.
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
