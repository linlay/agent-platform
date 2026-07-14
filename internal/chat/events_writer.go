package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// Validate the exact JSON representation that will reach disk.  This keeps
	// decoder semantics (json.Number rather than float64) identical to the
	// historical read boundary and prevents a new internal writer from creating
	// a record that a later replay must reject.
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
	// Inspect the original Go value before json.Marshal. An integral float64
	// would otherwise serialize as an unquoted number and evade the strict
	// JSON decoder below, despite the contract forbidding all float time
	// values.
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var line map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&line); err != nil {
		return nil, fmt.Errorf("decode JSONL line for validation: %w", err)
	}
	if err := validatePersistedTimeContract([]map[string]any{line}, location); err != nil {
		return nil, err
	}
	if err := validatePersistedSystemInitSchema([]map[string]any{line}); err != nil {
		return nil, err
	}
	return raw, nil
}
