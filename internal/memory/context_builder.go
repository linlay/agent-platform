package memory

import (
	"math"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

type hybridParams struct {
	queryEmbedding []float64
	itemEmbeddings map[string][]float64
	vectorWeight   float64
	ftsWeight      float64
}

func buildContextBundleFromStored(request ContextRequest, items []api.StoredMemoryResponse) ContextBundle {
	return buildContextBundleWithHybrid(request, items, hybridParams{})
}

func buildContextBundleWithHybrid(request ContextRequest, items []api.StoredMemoryResponse, hp hybridParams) ContextBundle {
	topFacts := normalizeLimit(request.TopFacts, 5)
	topObs := normalizeLimit(request.TopObs, 5)
	maxChars := request.MaxChars
	if request.AvailableTokens > 0 {
		maxChars = request.AvailableTokens * 4
	}
	if maxChars <= 0 {
		maxChars = 4000
	}

	facts := make([]api.StoredMemoryResponse, 0)
	observations := make([]api.StoredMemoryResponse, 0)
	queryNeedle := strings.ToLower(strings.TrimSpace(request.Query))
	useHybrid := len(hp.queryEmbedding) > 0 && len(hp.itemEmbeddings) > 0

	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if strings.TrimSpace(request.AgentKey) != "" && strings.TrimSpace(item.AgentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(request.AgentKey) {
			continue
		}
		if !scopeMatches(item, request) {
			continue
		}
		if item.Kind == KindObservation {
			if item.Status != StatusOpen && item.Status != StatusActive {
				continue
			}
			if !useHybrid {
				if queryNeedle != "" && !matchesMemoryNeedle(item, queryNeedle) {
					continue
				}
			}
			observations = append(observations, item)
			continue
		}
		if item.Status != StatusActive {
			continue
		}
		facts = append(facts, item)
	}

	nowMs := time.Now().UnixMilli()

	sort.SliceStable(facts, func(i, j int) bool {
		ei := computeEffectiveImportance(facts[i], nowMs)
		ej := computeEffectiveImportance(facts[j], nowMs)
		if ei != ej {
			return ei > ej
		}
		return facts[i].UpdatedAt > facts[j].UpdatedAt
	})

	if useHybrid {
		scoreMap := computeHybridScores(observations, hp)
		sort.SliceStable(observations, func(i, j int) bool {
			si := scoreMap[observations[i].ID]
			sj := scoreMap[observations[j].ID]
			if si != sj {
				return si > sj
			}
			return observations[i].UpdatedAt > observations[j].UpdatedAt
		})
	} else {
		sort.SliceStable(observations, func(i, j int) bool {
			ei := computeEffectiveImportance(observations[i], nowMs)
			ej := computeEffectiveImportance(observations[j], nowMs)
			if ei != ej {
				return ei > ej
			}
			return observations[i].UpdatedAt > observations[j].UpdatedAt
		})
	}

	// Split observations: session (current chat) vs cross-chat
	chatID := strings.TrimSpace(request.ChatID)
	sessionObs := make([]api.StoredMemoryResponse, 0)
	crossChatObs := make([]api.StoredMemoryResponse, 0)
	for _, obs := range observations {
		if chatID != "" && strings.TrimSpace(obs.ChatID) == chatID {
			sessionObs = append(sessionObs, obs)
		} else {
			crossChatObs = append(crossChatObs, obs)
		}
	}

	topSession := normalizeLimit(request.TopObs, 5)
	if len(sessionObs) > topSession {
		sessionObs = sessionObs[:topSession]
	}

	candidateCounts := map[string]int{
		string(LayerStable):      len(facts),
		string(LayerSession):     len(sessionObs),
		string(LayerObservation): len(crossChatObs),
		string(LayerRawTrace):    0,
	}

	if len(facts) > topFacts {
		facts = facts[:topFacts]
	}
	if len(crossChatObs) > topObs {
		crossChatObs = crossChatObs[:topObs]
	}

	// Dynamic budget allocation: render untruncated, then allocate proportionally
	stableRaw := renderPromptSection("Stable Memory", facts)
	sessionRaw := renderPromptSection("Current Session", sessionObs)
	obsRaw := renderPromptSection("Relevant Observations", crossChatObs)

	stableBudget, sessionBudget, obsBudget := allocateBudget(maxChars, len(stableRaw), len(sessionRaw), len(obsRaw))

	stablePrompt := truncatePrompt(stableRaw, stableBudget)
	sessionPrompt := truncatePrompt(sessionRaw, sessionBudget)
	observationPrompt := ""
	disclosedLayers := []string{}
	stopReason := "no_memory"
	decisions := make([]DisclosureDecision, 0, 3)

	if len(facts) > 0 {
		disclosedLayers = append(disclosedLayers, string(LayerStable))
		stopReason = "stable_only"
		decisions = append(decisions, DisclosureDecision{
			Layer:   LayerStable,
			ItemIDs: memoryIDs(facts),
			Reason:  string(SelectionReasonHighRank),
		})
	}
	if strings.TrimSpace(sessionPrompt) != "" {
		disclosedLayers = append(disclosedLayers, string(LayerSession))
		stopReason = "session_added"
		decisions = append(decisions, DisclosureDecision{
			Layer:   LayerSession,
			ItemIDs: memoryIDs(sessionObs),
			Reason:  string(SelectionReasonScopeMatch),
		})
	}
	if len(crossChatObs) > 0 {
		observationPrompt = truncatePrompt(obsRaw, obsBudget)
		if strings.TrimSpace(observationPrompt) != "" {
			disclosedLayers = append(disclosedLayers, string(LayerObservation))
			stopReason = "observation_added"
			reason := string(SelectionReasonQueryMatch)
			if useHybrid {
				reason = string(SelectionReasonHybridScore)
			}
			decisions = append(decisions, DisclosureDecision{
				Layer:   LayerObservation,
				ItemIDs: memoryIDs(crossChatObs),
				Reason:  reason,
			})
		}
	}
	selectedCounts := map[string]int{
		string(LayerStable):      len(facts),
		string(LayerSession):     len(sessionObs),
		string(LayerObservation): len(crossChatObs),
		string(LayerRawTrace):    0,
	}

	return ContextBundle{
		StableFacts:          facts,
		SessionSummaries:     sessionObs,
		RelevantObservations: crossChatObs,
		DisclosedLayers:      disclosedLayers,
		StopReason:           stopReason,
		SnapshotID:           buildSnapshotID(request.AgentKey, request.ChatID, facts, crossChatObs),
		CandidateCounts:      candidateCounts,
		SelectedCounts:       selectedCounts,
		Decisions:            decisions,
		StablePrompt:         stablePrompt,
		SessionPrompt:        sessionPrompt,
		ObservationPrompt:    observationPrompt,
	}
}

func computeHybridScores(observations []api.StoredMemoryResponse, hp hybridParams) map[string]float64 {
	scores := make(map[string]float64, len(observations))
	maxImportance := 1.0
	for _, obs := range observations {
		if float64(obs.Importance) > maxImportance {
			maxImportance = float64(obs.Importance)
		}
	}
	for _, obs := range observations {
		importanceNorm := float64(obs.Importance) / maxImportance
		var vectorScore float64
		if itemVec, ok := hp.itemEmbeddings[obs.ID]; ok && len(itemVec) > 0 {
			vectorScore = CosineSimilarity(hp.queryEmbedding, itemVec)
			if vectorScore < 0 {
				vectorScore = 0
			}
		}
		scores[obs.ID] = hp.vectorWeight*vectorScore + hp.ftsWeight*importanceNorm
	}
	return scores
}

func allocateBudget(total, stableLen, sessionLen, obsLen int) (stable, session, obs int) {
	sum := stableLen + sessionLen + obsLen
	if sum <= total {
		return stableLen, sessionLen, obsLen
	}
	// Minimum guarantees: 30% stable, 20% session, 20% observation
	minStable := total * 3 / 10
	minSession := total * 2 / 10

	stable = clampBudget(stableLen, minStable, total)
	session = clampBudget(sessionLen, minSession, total-stable)
	obs = total - stable - session
	if obs < 0 {
		obs = 0
	}
	return stable, session, obs
}

func clampBudget(needed, minimum, maximum int) int {
	if needed <= minimum {
		return needed
	}
	if needed > maximum {
		return maximum
	}
	if minimum > maximum {
		return maximum
	}
	return needed
}

func computeEffectiveImportance(item api.StoredMemoryResponse, nowMs int64) float64 {
	base := float64(item.Importance)

	var daysSinceAccess float64
	if item.LastAccessedAt != nil && *item.LastAccessedAt > 0 {
		daysSinceAccess = float64(nowMs-*item.LastAccessedAt) / (24 * 3600 * 1000)
	} else {
		daysSinceAccess = float64(nowMs-item.UpdatedAt) / (24 * 3600 * 1000)
	}
	if daysSinceAccess < 0 {
		daysSinceAccess = 0
	}
	decay := math.Min(2.0, daysSinceAccess/30.0*0.5)
	boost := math.Min(2.0, float64(item.AccessCount)*0.1)

	eff := base - decay + boost
	if eff < 1 {
		return 1
	}
	return eff
}

func memoryIDs(items []api.StoredMemoryResponse) []string {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		ids = append(ids, strings.TrimSpace(item.ID))
	}
	return ids
}

func renderPromptSection(title string, items []api.StoredMemoryResponse) string {
	if len(items) == 0 {
		return ""
	}
	lines := []string{"Runtime Context: " + title}
	for _, item := range items {
		lines = append(lines, "- "+sanitizeMemoryText(memoryLine(item)))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func truncatePrompt(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit])
}
