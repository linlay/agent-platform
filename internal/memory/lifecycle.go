package memory

import (
	"sort"
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
	archiveIDs   map[string]struct{}
	mergeIDs     map[string]struct{}
	supersedeIDs map[string]string
	promoteIDs   []string
}

func buildObservationConsolidationPlan(agentKey string, items []api.StoredMemoryResponse, now time.Time) consolidationPlan {
	return buildObservationConsolidationPlanWithMode(agentKey, items, now, true)
}

func buildObservationConsolidationPlanWithMode(agentKey string, items []api.StoredMemoryResponse, now time.Time, allowHeuristicPromotion bool) consolidationPlan {
	plan := consolidationPlan{
		archiveIDs:   map[string]struct{}{},
		mergeIDs:     map[string]struct{}{},
		supersedeIDs: map[string]string{},
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

func buildConsolidationPlan(agentKey string, items []api.StoredMemoryResponse, now time.Time) consolidationPlan {
	plan := buildObservationConsolidationPlan(agentKey, items, now)
	mergeFactConsolidationPlan(&plan, agentKey, items)
	return plan
}

func mergeFactConsolidationPlan(plan *consolidationPlan, agentKey string, items []api.StoredMemoryResponse) {
	if plan == nil {
		return
	}
	if plan.archiveIDs == nil {
		plan.archiveIDs = map[string]struct{}{}
	}
	if plan.mergeIDs == nil {
		plan.mergeIDs = map[string]struct{}{}
	}
	if plan.supersedeIDs == nil {
		plan.supersedeIDs = map[string]string{}
	}
	facts := make([]api.StoredMemoryResponse, 0, len(items))
	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if normalizeMemoryKind(item.Kind) != KindFact {
			continue
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
			continue
		}
		facts = append(facts, item)
	}
	sort.SliceStable(facts, func(i, j int) bool {
		return preferMemoryRecord(facts[i], facts[j])
	})
	keepers := make([]api.StoredMemoryResponse, 0, len(facts))
	for _, item := range facts {
		duplicateIdx := -1
		for idx := range keepers {
			if memoryNearDuplicate(keepers[idx], item, "stable") {
				duplicateIdx = idx
				break
			}
		}
		if duplicateIdx < 0 {
			keepers = append(keepers, item)
			continue
		}
		keeper := keepers[duplicateIdx]
		if strings.TrimSpace(keeper.ID) == "" || strings.TrimSpace(item.ID) == "" || keeper.ID == item.ID {
			continue
		}
		plan.supersedeIDs[item.ID] = keeper.ID
		plan.mergeIDs[item.ID] = struct{}{}
	}
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

func preferMemoryRecord(left api.StoredMemoryResponse, right api.StoredMemoryResponse) bool {
	if left.Importance != right.Importance {
		return left.Importance > right.Importance
	}
	if left.Confidence != right.Confidence {
		return left.Confidence > right.Confidence
	}
	if left.UpdatedAt != right.UpdatedAt {
		return left.UpdatedAt > right.UpdatedAt
	}
	return len([]rune(strings.TrimSpace(left.Summary))) >= len([]rune(strings.TrimSpace(right.Summary)))
}
