package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
)

type SQLiteStore struct {
	root   string
	dbPath string
}

func NewSQLiteStore(root string, dbFileName string) (*SQLiteStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dbFileName) == "" {
		dbFileName = "memory.db"
	}
	store := &SQLiteStore{
		root:   root,
		dbPath: filepath.Join(root, dbFileName),
	}
	if err := store.persistIndex(nil); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error) {
	now := time.Now().UnixMilli()
	summary := extractRememberSummary(chatDetail)
	item := api.RememberItemResponse{
		Summary:    summary,
		SubjectKey: chatDetail.ChatID,
	}
	stored := api.StoredMemoryResponse{
		ID:         "mem_" + strings.ReplaceAll(request.ChatID, "-", "")[:min(12, len(strings.ReplaceAll(request.ChatID, "-", "")))],
		RequestID:  request.RequestID,
		ChatID:     request.ChatID,
		AgentKey:   agentKey,
		SubjectKey: chatDetail.ChatID,
		Summary:    summary,
		SourceType: "remember",
		Category:   "remember",
		Importance: 5,
		Tags:       []string{"remember"},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.Write(stored); err != nil {
		return api.RememberResponse{}, err
	}

	memoryPath := filepath.Join(s.root, request.ChatID+".json")
	payload := map[string]any{
		"requestId": request.RequestID,
		"chatId":    request.ChatID,
		"chatName":  chatDetail.ChatName,
		"items":     []api.RememberItemResponse{item},
		"stored":    []api.StoredMemoryResponse{stored},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return api.RememberResponse{}, err
	}
	if err := os.WriteFile(memoryPath, data, 0o644); err != nil {
		return api.RememberResponse{}, err
	}

	preview := &api.PromptPreviewResponse{
		UserPrompt:        firstRawMessage(chatDetail.RawMessages),
		ChatName:          chatDetail.ChatName,
		RawMessageCount:   len(chatDetail.RawMessages),
		EventCount:        len(chatDetail.Events),
		ReferenceCount:    len(chatDetail.References),
		RawMessageSamples: sampleMessages(chatDetail.RawMessages),
		EventSamples:      sampleEvents(chatDetail.Events),
	}

	return api.RememberResponse{
		Accepted:      true,
		Status:        "stored",
		RequestID:     request.RequestID,
		ChatID:        request.ChatID,
		MemoryPath:    memoryPath,
		MemoryRoot:    s.root,
		MemoryCount:   1,
		Detail:        "remember request captured; memory root=" + s.root,
		PromptPreview: preview,
		Items:         []api.RememberItemResponse{item},
		Stored:        []api.StoredMemoryResponse{stored},
	}, nil
}

func (s *SQLiteStore) Search(query string, limit int) ([]api.StoredMemoryResponse, error) {
	items, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	out := make([]api.StoredMemoryResponse, 0)
	for _, item := range items {
		if needle == "" || strings.Contains(strings.ToLower(item.Summary), needle) || strings.Contains(strings.ToLower(item.ChatID), needle) || strings.Contains(strings.ToLower(item.SubjectKey), needle) {
			out = append(out, item)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *SQLiteStore) Read(id string) (*api.StoredMemoryResponse, error) {
	items, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			copy := item
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *SQLiteStore) Write(item api.StoredMemoryResponse) error {
	if item.UpdatedAt == 0 {
		item.UpdatedAt = time.Now().UnixMilli()
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = item.UpdatedAt
	}
	if item.ID == "" {
		item.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}

	items, err := s.readIndex()
	if err != nil {
		return err
	}
	replaced := false
	for idx := range items {
		if items[idx].ID == item.ID {
			items[idx] = item
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, item)
	}
	if err := s.persistIndex(items); err != nil {
		return err
	}
	if err := AppendJournal(s.root, item); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.root, item.ID+".stored.json"), payload, 0o644)
}

func (s *SQLiteStore) readIndex() ([]api.StoredMemoryResponse, error) {
	data, err := os.ReadFile(s.dbPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var items []api.StoredMemoryResponse
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLiteStore) persistIndex(items []api.StoredMemoryResponse) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.dbPath, data, 0o644)
}
