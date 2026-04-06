package chat

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrChatNotFound = errors.New("chat not found")

type Store interface {
	EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error)
	Summary(chatID string) (*Summary, error)
	AppendEvent(chatID string, event map[string]any) error
	AppendRawMessage(chatID string, message map[string]any) error
	OnRunCompleted(completion RunCompletion) error
	ListChats(lastRunID string, agentKey string) ([]Summary, error)
	LoadChat(chatID string) (Detail, error)
	MarkRead(chatID string) (Summary, error)
	ResolveResource(file string) (string, error)
	ChatDir(chatID string) string
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) EnsureChat(chatID string, agentKey string, teamID string, firstMessage string) (Summary, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Summary{}, false, err
	}

	if existing, ok := summaries[chatID]; ok {
		return existing, false, nil
	}

	now := time.Now().UnixMilli()
	summary := Summary{
		ChatID:     chatID,
		ChatName:   defaultChatName(firstMessage),
		AgentKey:   agentKey,
		TeamID:     teamID,
		CreatedAt:  now,
		UpdatedAt:  now,
		ReadStatus: 1,
	}
	summaries[chatID] = summary
	if err := s.writeIndexLocked(summaries); err != nil {
		return Summary{}, false, err
	}
	if err := os.MkdirAll(s.ChatDir(chatID), 0o755); err != nil {
		return Summary{}, false, err
	}
	return summary, true, nil
}

func (s *FileStore) AppendEvent(chatID string, event map[string]any) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "events.jsonl"), event)
}

func (s *FileStore) Summary(chatID string) (*Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return nil, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return nil, nil
	}
	copy := summary
	return &copy, nil
}

func (s *FileStore) AppendRawMessage(chatID string, message map[string]any) error {
	return s.appendJSONLine(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"), message)
}

func (s *FileStore) OnRunCompleted(completion RunCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return err
	}

	summary, ok := summaries[completion.ChatID]
	if !ok {
		return ErrChatNotFound
	}

	summary.LastRunID = completion.RunID
	summary.LastRunContent = completion.AssistantText
	summary.UpdatedAt = completion.UpdatedAtMillis
	summary.ReadStatus = 0
	summary.ReadAt = nil
	summaries[completion.ChatID] = summary
	return s.writeIndexLocked(summaries)
}

func (s *FileStore) ListChats(lastRunID string, agentKey string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return nil, err
	}

	items := make([]Summary, 0, len(summaries))
	for _, summary := range summaries {
		if agentKey != "" && summary.AgentKey != agentKey {
			continue
		}
		if lastRunID != "" && !(summary.LastRunID > lastRunID) {
			continue
		}
		items = append(items, summary)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].ChatID > items[j].ChatID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})

	return items, nil
}

func (s *FileStore) LoadChat(chatID string) (Detail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Detail{}, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return Detail{}, ErrChatNotFound
	}

	events, err := readJSONLines(filepath.Join(s.ChatDir(chatID), "events.jsonl"))
	if err != nil {
		return Detail{}, err
	}
	rawMessages, err := readJSONLines(filepath.Join(s.ChatDir(chatID), "raw_messages.jsonl"))
	if err != nil {
		return Detail{}, err
	}

	return Detail{
		ChatID:      summary.ChatID,
		ChatName:    summary.ChatName,
		Events:      events,
		RawMessages: rawMessages,
		References:  nil,
	}, nil
}

func (s *FileStore) MarkRead(chatID string) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	summaries, err := s.readIndexLocked()
	if err != nil {
		return Summary{}, err
	}
	summary, ok := summaries[chatID]
	if !ok {
		return Summary{}, ErrChatNotFound
	}
	now := time.Now().UnixMilli()
	summary.ReadStatus = 1
	summary.ReadAt = &now
	summaries[chatID] = summary
	if err := s.writeIndexLocked(summaries); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func (s *FileStore) ResolveResource(file string) (string, error) {
	clean := filepath.Clean(file)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", os.ErrPermission
	}
	path := filepath.Join(s.root, clean)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FileStore) ChatDir(chatID string) string {
	return filepath.Join(s.root, chatID)
}

func (s *FileStore) appendJSONLine(path string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

func (s *FileStore) readIndexLocked() (map[string]Summary, error) {
	path := filepath.Join(s.root, "index.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var summaries map[string]Summary
	if err := json.NewDecoder(file).Decode(&summaries); err != nil {
		return nil, err
	}
	if summaries == nil {
		return map[string]Summary{}, nil
	}
	return summaries, nil
}

func (s *FileStore) writeIndexLocked(summaries map[string]Summary) error {
	path := filepath.Join(s.root, "index.json")
	tmp := path + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(summaries); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

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
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			return nil, err
		}
		items = append(items, payload)
	}
	return items, scanner.Err()
}

func defaultChatName(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "default"
	}
	runes := []rune(message)
	if len(runes) > 24 {
		return string(runes[:24])
	}
	return message
}
