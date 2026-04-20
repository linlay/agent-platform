package memory

import (
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

type MutationInput struct {
	ID          string
	Title       *string
	Summary     *string
	Category    *string
	ScopeType   *string
	ScopeKey    *string
	Status      *string
	Importance  *int
	Confidence  *float64
	Tags        []string
	ReplaceTags bool
}

type PromoteInput struct {
	SourceID      string
	Title         string
	Summary       string
	Category      string
	ScopeType     string
	ScopeKey      string
	Importance    int
	Confidence    float64
	Tags          []string
	ArchiveSource bool
}

type TimelineEntry struct {
	Memory       ToolRecord
	RelationType string
	Direction    string
}

type Mutator interface {
	Update(agentKey string, input MutationInput) (*ToolRecord, error)
	Forget(agentKey string, id string, status string) (*ToolRecord, error)
}

type TimelineProvider interface {
	Timeline(agentKey string, id string, limit int) ([]TimelineEntry, error)
}

type Promoter interface {
	Promote(agentKey string, input PromoteInput) (*ToolRecord, error)
}

type ConsolidationResult struct {
	ArchivedCount int
	MergedCount   int
	PromotedCount int
}

type Consolidator interface {
	Consolidate(agentKey string) (ConsolidationResult, error)
}

const observationTTL = 30 * 24 * time.Hour

type consolidationPlan struct {
	archiveIDs map[string]struct{}
	mergeIDs   map[string]struct{}
	promoteIDs []string
}

func buildObservationConsolidationPlan(agentKey string, items []api.StoredMemoryResponse, now time.Time) consolidationPlan {
	return buildObservationConsolidationPlanWithMode(agentKey, items, now, true)
}

func buildObservationConsolidationPlanWithMode(agentKey string, items []api.StoredMemoryResponse, now time.Time, allowHeuristicPromotion bool) consolidationPlan {
	plan := consolidationPlan{
		archiveIDs: map[string]struct{}{},
		mergeIDs:   map[string]struct{}{},
	}
	duplicateCount := map[string]int{}
	keepers := map[string]api.StoredMemoryResponse{}
	cutoff := now.Add(-observationTTL).UnixMilli()

	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if normalizeMemoryKind(item.Kind) != KindObservation {
			continue
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if item.Status != StatusOpen && item.Status != StatusActive {
			continue
		}
		if item.UpdatedAt > 0 && item.UpdatedAt < cutoff {
			plan.archiveIDs[item.ID] = struct{}{}
			continue
		}
		fingerprint := observationFingerprint(item)
		duplicateCount[fingerprint]++
		if existing, ok := keepers[fingerprint]; ok {
			if item.UpdatedAt > existing.UpdatedAt || (item.UpdatedAt == existing.UpdatedAt && item.Importance >= existing.Importance) {
				plan.archiveIDs[existing.ID] = struct{}{}
				plan.mergeIDs[existing.ID] = struct{}{}
				keepers[fingerprint] = item
			} else {
				plan.archiveIDs[item.ID] = struct{}{}
				plan.mergeIDs[item.ID] = struct{}{}
			}
			continue
		}
		keepers[fingerprint] = item
	}

	for fingerprint, item := range keepers {
		if _, archived := plan.archiveIDs[item.ID]; archived {
			continue
		}
		if shouldPromoteObservation(item, duplicateCount[fingerprint], allowHeuristicPromotion) {
			plan.promoteIDs = append(plan.promoteIDs, item.ID)
		}
	}
	return plan
}

func observationFingerprint(item api.StoredMemoryResponse) string {
	return strings.ToLower(strings.TrimSpace(item.Category)) + "|" + normalizeLifecycleText(item.Summary)
}

func normalizeLifecycleText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(text))), " ")
}

func shouldPromoteObservation(item api.StoredMemoryResponse, duplicateCount int, allowHeuristicPromotion bool) bool {
	if normalizeMemoryKind(item.Kind) != KindObservation {
		return false
	}
	if item.Status != StatusOpen && item.Status != StatusActive {
		return false
	}
	if duplicateCount > 1 {
		return true
	}
	if !allowHeuristicPromotion {
		return false
	}
	if item.Importance >= 9 && item.Confidence >= 0.75 {
		return true
	}
	switch normalizeCategory(item.Category) {
	case "bugfix", "workaround", "preference", "decision":
		return item.Importance >= 8 && item.Confidence >= 0.75
	default:
		return false
	}
}
