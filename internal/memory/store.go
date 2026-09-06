package memory

import (
	"sort"
	"strings"

	"agent-platform/internal/api"
)

type Store interface {
	Search(query string, limit int) ([]api.StoredMemoryResponse, error)
	SearchDetailed(agentKey string, query string, category string, limit int) ([]ScoredRecord, error)
	Read(id string) (*api.StoredMemoryResponse, error)
	ReadDetail(agentKey string, id string) (*ToolRecord, error)
	List(agentKey string, category string, limit int, sort string) ([]ToolRecord, error)
	Write(item api.StoredMemoryResponse) error
	BuildContextBundle(request ContextRequest) (ContextBundle, error)
	Consolidate(agentKey string) (ConsolidationResult, error)
}

func isNearDuplicateFactMemory(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse) bool {
	if strings.TrimSpace(existing.ID) == strings.TrimSpace(incoming.ID) {
		return false
	}
	if normalizeMemoryKind(existing.Kind) != KindFact || normalizeMemoryKind(incoming.Kind) != KindFact {
		return false
	}
	if normalizeMemoryStatus(existing.Status, existing.Kind) != StatusActive || normalizeMemoryStatus(incoming.Status, incoming.Kind) != StatusActive {
		return false
	}
	if strings.TrimSpace(existing.AgentKey) != strings.TrimSpace(incoming.AgentKey) {
		return false
	}
	return memoryNearDuplicate(existing, incoming, "stable")
}

func mergeNearDuplicateFactMemory(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse, now int64) api.StoredMemoryResponse {
	merged := existing
	merged.Title = mergeNearDuplicateFactText(existing.Title, incoming.Title)
	merged.Summary = mergeNearDuplicateFactText(existing.Summary, incoming.Summary)
	merged.Importance = max(existing.Importance, incoming.Importance)
	merged.Confidence = maxFloat(existing.Confidence, incoming.Confidence)
	merged.Tags = normalizeTags(append(existing.Tags, incoming.Tags...))
	merged.UpdatedAt = now
	merged.AccessCount++
	merged.LastAccessedAt = &now
	return normalizeStoredItem(merged)
}

func mergeNearDuplicateFactText(existing string, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if existing == "" {
		return incoming
	}
	if incoming == "" {
		return existing
	}
	existingNorm := normalizeMemoryComparableText(existing)
	incomingNorm := normalizeMemoryComparableText(incoming)
	if existingNorm == incomingNorm {
		if len([]rune(incoming)) > len([]rune(existing)) {
			return incoming
		}
		return existing
	}
	if existingNorm != "" && incomingNorm != "" {
		if strings.Contains(incomingNorm, existingNorm) {
			return incoming
		}
		if strings.Contains(existingNorm, incomingNorm) {
			return existing
		}
	}
	return strings.TrimSpace(existing + "\n" + incoming)
}

func matchesMemoryNeedle(item api.StoredMemoryResponse, needle string) bool {
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(item.Title), needle) ||
		strings.Contains(strings.ToLower(item.Summary), needle) ||
		strings.Contains(strings.ToLower(item.SubjectKey), needle) ||
		strings.Contains(strings.ToLower(item.Category), needle) {
		return true
	}
	for _, tag := range item.Tags {
		if strings.Contains(strings.ToLower(tag), needle) {
			return true
		}
	}
	return false
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func chooseTags(input []string, fallback []string) []string {
	if len(input) > 0 {
		return input
	}
	return fallback
}

func sortScoredRecords(items []ScoredRecord) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		if items[i].Memory.Importance != items[j].Memory.Importance {
			return items[i].Memory.Importance > items[j].Memory.Importance
		}
		return items[i].Memory.UpdatedAt > items[j].Memory.UpdatedAt
	})
}
