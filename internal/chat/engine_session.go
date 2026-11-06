package chat

import (
	dto "agent-platform/internal/contracts/agentengine"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const engineSessionFile = ".agent-engine-session.json"

// Engine bindings live with Chat data, not plugin installation state or indexes.
func (s *FileStore) LoadEngineSession(chatID string) (*dto.SessionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidChatID(chatID) {
		return nil, os.ErrPermission
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrChatNotFound
	}
	file, err := os.Open(filepath.Join(s.ChatDir(chatID), engineSessionFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 || dto.Validate("SessionBinding", data) != nil {
		return nil, errors.New("invalid engine session binding")
	}
	var binding dto.SessionBinding
	if err = json.Unmarshal(data, &binding); err != nil {
		return nil, err
	}
	if binding.ChatID != chatID {
		return nil, errors.New("engine session chat mismatch")
	}
	return &binding, nil
}
func (s *FileStore) SaveEngineSession(binding dto.SessionBinding) error {
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if !ValidChatID(binding.ChatID) || len(data) > 1<<20 || dto.Validate("SessionBinding", data) != nil {
		return errors.New("invalid engine session binding")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	summary, err := s.loadSummary(binding.ChatID)
	if err != nil {
		return err
	}
	if summary == nil {
		return ErrChatNotFound
	}
	dir := s.ChatDir(binding.ChatID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".engine-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return atomicReplaceFile(tmp.Name(), filepath.Join(dir, engineSessionFile))
}
