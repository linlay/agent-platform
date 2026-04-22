package memory

import (
	"strings"

	"agent-platform-runner-go/internal/api"
)

type ToolRecord struct {
	ID             string
	AgentKey       string
	SubjectKey     string
	Kind           string
	RefID          string
	ScopeType      string
	ScopeKey       string
	Title          string
	Content        string
	SourceType     string
	Category       string
	Importance     int
	Confidence     float64
	Status         string
	Tags           []string
	HasEmbedding   bool
	EmbeddingModel *string
	CreatedAt      int64
	UpdatedAt      int64
	AccessCount    int
	LastAccessedAt *int64
}

type ScoredRecord struct {
	Memory    ToolRecord
	Score     float64
	MatchType string
}

func normalizeCategory(category string) string {
	if strings.TrimSpace(category) == "" {
		return "general"
	}
	return strings.ToLower(strings.TrimSpace(category))
}

func normalizeOptionalCategory(category string) string {
	if strings.TrimSpace(category) == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(category))
}

func normalizeSort(sortBy string) string {
	if strings.EqualFold(strings.TrimSpace(sortBy), "importance") {
		return "importance"
	}
	return "recent"
}

func normalizeLimit(limit int, fallback int) int {
	if fallback <= 0 {
		fallback = 10
	}
	if limit <= 0 {
		return fallback
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeSourceType(sourceType string) string {
	if strings.TrimSpace(sourceType) == "" {
		return "tool-write"
	}
	return strings.ToLower(strings.TrimSpace(sourceType))
}

func normalizeImportance(importance int) int {
	if importance <= 0 {
		importance = 5
	}
	if importance < 1 {
		return 1
	}
	if importance > 10 {
		return 10
	}
	return importance
}

func normalizeSubjectKey(subjectKey string, chatID string, agentKey string) string {
	if strings.TrimSpace(subjectKey) != "" {
		return strings.TrimSpace(subjectKey)
	}
	if strings.TrimSpace(chatID) != "" {
		return "chat:" + strings.TrimSpace(chatID)
	}
	if strings.TrimSpace(agentKey) != "" {
		return "agent:" + strings.TrimSpace(agentKey)
	}
	return "_global"
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
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

func toolRecordFromStored(item api.StoredMemoryResponse) ToolRecord {
	record := ToolRecord{
		ID:             item.ID,
		AgentKey:       item.AgentKey,
		SubjectKey:     item.SubjectKey,
		Kind:           item.Kind,
		RefID:          item.RefID,
		ScopeType:      item.ScopeType,
		ScopeKey:       item.ScopeKey,
		Title:          item.Title,
		Content:        item.Summary,
		SourceType:     item.SourceType,
		Category:       item.Category,
		Importance:     item.Importance,
		Confidence:     item.Confidence,
		Status:         item.Status,
		Tags:           append([]string(nil), item.Tags...),
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		AccessCount:    item.AccessCount,
		LastAccessedAt: item.LastAccessedAt,
	}
	record.Kind = normalizeMemoryKind(record.Kind)
	record.ScopeType = normalizeScopeType(record.ScopeType)
	record.Title = normalizeMemoryTitle(record.Title, record.Content)
	record.Status = normalizeMemoryStatus(record.Status, record.Kind)
	record.Confidence = normalizeMemoryConfidence(record.Confidence, record.Kind)
	if strings.TrimSpace(record.RefID) == "" {
		record.RefID = record.ID
	}
	return record
}
