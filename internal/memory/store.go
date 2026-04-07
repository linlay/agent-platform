package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/stream"
)

type Store interface {
	Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error)
	Search(query string, limit int) ([]api.StoredMemoryResponse, error)
	Read(id string) (*api.StoredMemoryResponse, error)
	Write(item api.StoredMemoryResponse) error
}

type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error) {
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

func extractRememberSummary(detail chat.Detail) string {
	for i := len(detail.RawMessages) - 1; i >= 0; i-- {
		message := detail.RawMessages[i]
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if role == "assistant" && strings.TrimSpace(content) != "" {
			return content
		}
	}
	if len(detail.Events) > 0 {
		last := detail.Events[len(detail.Events)-1]
		if text := last.String("text"); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return "No assistant memory extracted yet."
}

func firstRawMessage(raw []map[string]any) string {
	for _, message := range raw {
		if content, _ := message["content"].(string); strings.TrimSpace(content) != "" {
			return content
		}
	}
	return ""
}

func sampleMessages(raw []map[string]any) []string {
	samples := make([]string, 0, min(3, len(raw)))
	for _, message := range raw {
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if strings.TrimSpace(content) == "" {
			continue
		}
		samples = append(samples, role+": "+content)
		if len(samples) == 3 {
			return samples
		}
	}
	return samples
}

func sampleEvents(events []stream.EventData) []string {
	samples := make([]string, 0, min(3, len(events)))
	for _, event := range events {
		samples = append(samples, event.Type)
		if len(samples) == 3 {
			return samples
		}
	}
	return samples
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *FileStore) Search(query string, limit int) ([]api.StoredMemoryResponse, error) {
	items, err := s.readAllStored()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
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

func (s *FileStore) Read(id string) (*api.StoredMemoryResponse, error) {
	items, err := s.readAllStored()
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

func (s *FileStore) Write(item api.StoredMemoryResponse) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.root, item.ID+".stored.json"), payload, 0o644)
}

func (s *FileStore) readAllStored() ([]api.StoredMemoryResponse, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	items := make([]api.StoredMemoryResponse, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".stored.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item api.StoredMemoryResponse
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
