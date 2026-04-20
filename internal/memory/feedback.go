package memory

import (
	"strings"

	"agent-platform-runner-go/internal/api"
)

type FeedbackSignal struct {
	ItemID          string
	Referenced      bool
	ConfidenceDelta float64
}

const (
	feedbackBoost = 0.05
	feedbackDecay = -0.02
)

func ComputeFeedback(disclosedIDs []string, assistantText string, items []api.StoredMemoryResponse) []FeedbackSignal {
	if len(disclosedIDs) == 0 || strings.TrimSpace(assistantText) == "" {
		return nil
	}
	idSet := make(map[string]struct{}, len(disclosedIDs))
	for _, id := range disclosedIDs {
		idSet[strings.TrimSpace(id)] = struct{}{}
	}
	itemIndex := make(map[string]api.StoredMemoryResponse, len(items))
	for _, item := range items {
		if _, ok := idSet[item.ID]; ok {
			itemIndex[item.ID] = item
		}
	}

	lowerText := strings.ToLower(assistantText)
	signals := make([]FeedbackSignal, 0, len(disclosedIDs))
	for _, id := range disclosedIDs {
		id = strings.TrimSpace(id)
		item, ok := itemIndex[id]
		if !ok {
			continue
		}
		referenced := isMemoryReferenced(item, lowerText)
		delta := feedbackDecay
		if referenced {
			delta = feedbackBoost
		}
		signals = append(signals, FeedbackSignal{
			ItemID:          id,
			Referenced:      referenced,
			ConfidenceDelta: delta,
		})
	}
	return signals
}

func isMemoryReferenced(item api.StoredMemoryResponse, lowerText string) bool {
	keywords := extractKeywords(item.Title, item.Summary)
	matchCount := 0
	for _, kw := range keywords {
		if strings.Contains(lowerText, kw) {
			matchCount++
		}
	}
	return matchCount >= 1 && len(keywords) > 0
}

func extractKeywords(title, summary string) []string {
	combined := strings.ToLower(strings.TrimSpace(title + " " + summary))
	words := strings.Fields(combined)
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}。，；：！？")
		if len([]rune(w)) < 3 {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	return out
}
