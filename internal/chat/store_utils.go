package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readJSONLines reads a JSONL file. Uses json.Decoder so it handles both
// single-line JSON objects (Go's writer) and pretty-printed multi-line JSON
// objects (Java may write either format).
func readJSONLines(path string) ([]map[string]any, error) {
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

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "default"
	}
	return truncateRunes(message, 24)
}

func truncateRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) > max {
		return string(runes[:max])
	}
	return text
}

func ValidChatID(chatID string) bool {
	if strings.TrimSpace(chatID) == "" {
		return false
	}
	if strings.Contains(chatID, "..") || strings.Contains(chatID, "/") || strings.Contains(chatID, `\`) {
		return false
	}
	clean := filepath.Clean(chatID)
	return clean == chatID && clean != "." && clean != string(filepath.Separator)
}
