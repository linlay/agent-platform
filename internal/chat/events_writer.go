package chat

import (
	"encoding/json"
	"os"
	"path/filepath"

	"agent-platform/internal/stream"
)

func (s *FileStore) AppendEvent(chatID string, event stream.EventData) error {
	return s.AppendEventLine(chatID, EventLine{
		Type:      "event",
		ChatID:    chatID,
		RunID:     event.String("runId"),
		UpdatedAt: event.Timestamp,
		LiveSeq:   event.Seq,
		Event:     eventPayloadWithoutSeq(event),
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

// chatJSONLPath returns the flat-file path for the chat's JSONL stream.
func (s *FileStore) chatJSONLPath(chatID string) string {
	return filepath.Join(s.root, chatID+".jsonl")
}

func (s *FileStore) appendJSONLine(path string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.appendJSONLineLocked(path, payload)
}

func (s *FileStore) appendJSONLineLocked(path string, payload any) error {
	// Validate the exact JSON representation that will reach disk so writers and
	// readers enforce the same current storage contract.
	raw, err := validateJSONLLinePayload(payload, "chat.jsonl.write")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(raw); err != nil {
		return err
	}
	_, err = file.Write([]byte("\n"))
	return err
}

func validateJSONLLinePayload(payload any, location string) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	records, err := decodeJSONLRecords(raw, location, true)
	if err != nil {
		return nil, err
	}
	if err := validatePersistedTimeContract(recordValues(records), location); err != nil {
		return nil, err
	}
	return raw, nil
}
