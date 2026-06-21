package chat

import (
	"encoding/json"
	"os"
	"path/filepath"

	"agent-platform/internal/stream"
)

func (s *FileStore) AppendEvent(chatID string, event stream.EventData) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), EventLine{
		Type:    "event",
		ChatID:  chatID,
		RunID:   event.String("runId"),
		LiveSeq: event.Seq,
		Event:   eventPayloadWithoutSeq(event),
	})
}

func (s *FileStore) AppendQueryLine(chatID string, line QueryLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendStepLine(chatID string, line StepLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendEventLine(chatID string, line EventLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

func (s *FileStore) AppendSubmitLine(chatID string, line SubmitLine) error {
	return s.appendJSONLine(s.chatJSONLPath(chatID), line)
}

// chatJSONLPath returns the path to {chatId}.jsonl (flat file, matching Java).
func (s *FileStore) chatJSONLPath(chatID string) string {
	return filepath.Join(s.root, chatID+".jsonl")
}

func (s *FileStore) appendJSONLine(path string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.appendJSONLineLocked(path, payload)
}

func (s *FileStore) appendJSONLineLocked(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	return encoder.Encode(payload)
}
