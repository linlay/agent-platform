package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Candidate struct {
	ID              string   `json:"id"`
	AgentKey        string   `json:"agentKey,omitempty"`
	ChatID          string   `json:"chatId,omitempty"`
	RunID           string   `json:"runId,omitempty"`
	SourceKind      string   `json:"sourceKind,omitempty"`
	SourceMemoryID  string   `json:"sourceMemoryId,omitempty"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	Procedure       string   `json:"procedure"`
	Intent          string   `json:"intent,omitempty"`
	Preconditions   []string `json:"preconditions,omitempty"`
	Steps           []string `json:"steps,omitempty"`
	FailurePatterns []string `json:"failurePatterns,omitempty"`
	SuccessCriteria []string `json:"successCriteria,omitempty"`
	Category        string   `json:"category,omitempty"`
	Confidence      float64  `json:"confidence,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	Status          string   `json:"status,omitempty"`
	CreatedAt       int64    `json:"createdAt"`
	UpdatedAt       int64    `json:"updatedAt"`
}

type CandidateInput struct {
	AgentKey        string
	ChatID          string
	RunID           string
	SourceKind      string
	SourceMemoryID  string
	Title           string
	Summary         string
	Procedure       string
	Intent          string
	Preconditions   []string
	Steps           []string
	FailurePatterns []string
	SuccessCriteria []string
	Category        string
	Confidence      float64
	Tags            []string
}

type CandidateStore interface {
	Write(input CandidateInput) (Candidate, error)
	List(agentKey string, limit int) ([]Candidate, error)
}

type FileCandidateStore struct {
	root string
	mu   sync.Mutex
}

func NewFileCandidateStore(root string) (*FileCandidateStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileCandidateStore{root: root}, nil
}

func (s *FileCandidateStore) Write(input CandidateInput) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	candidate := Candidate{
		ID:              "skillc_" + strings.ReplaceAll(strings.ToLower(strings.TrimSpace(input.AgentKey))+"_"+time.Now().Format("20060102150405.000000000"), ".", ""),
		AgentKey:        strings.TrimSpace(input.AgentKey),
		ChatID:          strings.TrimSpace(input.ChatID),
		RunID:           strings.TrimSpace(input.RunID),
		SourceKind:      normalizeText(input.SourceKind, "learn"),
		SourceMemoryID:  strings.TrimSpace(input.SourceMemoryID),
		Title:           normalizeText(input.Title, "Untitled Skill Candidate"),
		Summary:         strings.TrimSpace(input.Summary),
		Procedure:       strings.TrimSpace(input.Procedure),
		Intent:          strings.TrimSpace(input.Intent),
		Preconditions:   normalizeTextList(input.Preconditions),
		Steps:           normalizeTextList(input.Steps),
		FailurePatterns: normalizeTextList(input.FailurePatterns),
		SuccessCriteria: normalizeTextList(input.SuccessCriteria),
		Category:        normalizeText(input.Category, "workflow"),
		Confidence:      normalizeConfidence(input.Confidence),
		Tags:            normalizeTags(input.Tags),
		Status:          "candidate",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if strings.TrimSpace(candidate.Procedure) == "" {
		candidate.Procedure = candidate.Summary
	}
	data, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return Candidate{}, err
	}
	if err := os.WriteFile(filepath.Join(s.root, candidate.ID+".json"), data, 0o644); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func (s *FileCandidateStore) List(agentKey string, limit int) ([]Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	items := make([]Candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var item Candidate
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	if limit <= 0 {
		limit = 20
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func normalizeText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func normalizeConfidence(value float64) float64 {
	if value <= 0 {
		return 0.7
	}
	if value > 1 {
		return 1
	}
	return value
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeTextList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
