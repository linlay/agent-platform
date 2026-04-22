package memory

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

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

	facts, sessionObs, crossChatObs = dedupeDisclosedMemoryLayers(facts, sessionObs, crossChatObs)

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

func dedupeDisclosedMemoryLayers(
	facts []api.StoredMemoryResponse,
	sessionObs []api.StoredMemoryResponse,
	crossChatObs []api.StoredMemoryResponse,
) ([]api.StoredMemoryResponse, []api.StoredMemoryResponse, []api.StoredMemoryResponse) {
	stableSeen := map[string]struct{}{}
	disclosedSeen := map[string]struct{}{}
	facts = dedupeMemoryItems(facts, stableSeen, "stable")
	facts = collapseNearDuplicateMemoryItems(facts, "stable")
	for _, item := range facts {
		for _, key := range memoryItemDedupeKeys(item, "disclosed") {
			disclosedSeen[key] = struct{}{}
		}
	}
	sessionObs = dedupeMemoryItems(sessionObs, disclosedSeen, "disclosed")
	sessionObs = collapseNearDuplicateMemoryItems(sessionObs, "disclosed")
	for _, item := range sessionObs {
		for _, key := range memoryItemDedupeKeys(item, "disclosed") {
			disclosedSeen[key] = struct{}{}
		}
	}
	crossChatObs = dedupeMemoryItems(crossChatObs, disclosedSeen, "disclosed")
	crossChatObs = collapseNearDuplicateMemoryItems(crossChatObs, "disclosed")
	return facts, sessionObs, crossChatObs
}

func dedupeMemoryItems(items []api.StoredMemoryResponse, seen map[string]struct{}, mode string) []api.StoredMemoryResponse {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		if memoryItemSeen(item, seen, mode) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func memoryItemSeen(item api.StoredMemoryResponse, seen map[string]struct{}, mode string) bool {
	keys := memoryItemDedupeKeys(item, mode)
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	return false
}

func memoryItemDedupeKeys(item api.StoredMemoryResponse, mode string) []string {
	keys := make([]string, 0, 3)
	push := func(prefix string, value string) {
		normalized := normalizeMemoryDedupeText(value)
		if normalized == "" {
			return
		}
		keys = append(keys, prefix+":"+normalized)
	}
	category := strings.TrimSpace(item.Category)
	scopeType := strings.TrimSpace(item.ScopeType)
	scopeKey := strings.TrimSpace(item.ScopeKey)
	if mode == "stable" {
		push("stable-title:"+category+":"+scopeType+":"+scopeKey, item.Title)
		push("stable-summary:"+category+":"+scopeType+":"+scopeKey, item.Summary)
	} else {
		push("disclosed-title:"+category, item.Title)
		push("disclosed-summary:"+category, item.Summary)
		if value := strings.TrimSpace(firstNonBlankText(item.Title, item.Summary)); value != "" {
			push("disclosed-cross:"+category, value)
		}
	}
	return keys
}

func normalizeMemoryDedupeText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 48 {
		value = strings.TrimSpace(string(runes[:48]))
	}
	return value
}

func collapseNearDuplicateMemoryItems(items []api.StoredMemoryResponse, mode string) []api.StoredMemoryResponse {
	if len(items) <= 1 {
		return items
	}
	out := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		replaced := false
		for idx := range out {
			if !memoryNearDuplicate(out[idx], item, mode) {
				continue
			}
			out[idx] = pickPreferredMemoryItem(out[idx], item)
			replaced = true
			break
		}
		if !replaced {
			out = append(out, item)
		}
	}
	return out
}

func memoryNearDuplicate(left api.StoredMemoryResponse, right api.StoredMemoryResponse, mode string) bool {
	if strings.TrimSpace(left.Category) != strings.TrimSpace(right.Category) {
		return false
	}
	if mode == "stable" {
		if strings.TrimSpace(left.ScopeType) != strings.TrimSpace(right.ScopeType) || strings.TrimSpace(left.ScopeKey) != strings.TrimSpace(right.ScopeKey) {
			return false
		}
	}
	leftText := memoryComparableText(left)
	rightText := memoryComparableText(right)
	if leftText == "" || rightText == "" {
		return false
	}
	leftNorm := normalizeMemoryComparableText(leftText)
	rightNorm := normalizeMemoryComparableText(rightText)
	if leftNorm == "" || rightNorm == "" {
		return false
	}
	if leftNorm == rightNorm {
		return true
	}
	shorter, longer := leftNorm, rightNorm
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if shorter != "" && strings.Contains(longer, shorter) && len([]rune(shorter))*4 >= len([]rune(longer))*3 {
		return true
	}
	return memorySimilarity(leftNorm, rightNorm) >= 0.78
}

func memoryComparableText(item api.StoredMemoryResponse) string {
	return firstNonBlankText(item.Summary, item.Title)
}

func normalizeMemoryComparableText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"默认", " ",
		"优先", " ",
		"即", " ",
		"按照", " ",
		"按", " ",
		"要", " ",
		"的", " ",
		"需要", " ",
		"保证", " ",
		"保持", " ",
		"工作时间", "工时",
		"小时", "h",
		"小時", "h",
	)
	text = replacer.Replace(text)
	var b strings.Builder
	lastSpace := false
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func memoryTokenSet(text string) map[string]struct{} {
	text = normalizeMemoryComparableText(text)
	if text == "" {
		return nil
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	stopWords := map[string]struct{}{
		"default": {}, "prefer": {}, "preferred": {}, "should": {}, "need": {}, "needs": {},
	}
	tokens := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, ok := stopWords[field]; ok {
			continue
		}
		if len([]rune(field)) > 1 {
			tokens[field] = struct{}{}
		}
		for _, fragment := range hanFragments(field) {
			tokens[fragment] = struct{}{}
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	return tokens
}

func memorySimilarity(left string, right string) float64 {
	leftTokens := memoryTokenSet(left)
	rightTokens := memoryTokenSet(right)
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}
	overlap := 0
	for token := range leftTokens {
		if _, ok := rightTokens[token]; ok {
			overlap++
		}
	}
	union := len(leftTokens) + len(rightTokens) - overlap
	if union <= 0 {
		return 0
	}
	jaccard := float64(overlap) / float64(union)
	overlapCoeff := float64(overlap) / float64(minInt(len(leftTokens), len(rightTokens)))
	score := math.Max(jaccard, overlapCoeff)
	shorter, longer := left, right
	if len([]rune(shorter)) > len([]rune(longer)) {
		shorter, longer = longer, shorter
	}
	if shorter != "" && strings.Contains(longer, shorter) {
		score += 0.1
	}
	if score > 1 {
		score = 1
	}
	return score
}

func pickPreferredMemoryItem(left api.StoredMemoryResponse, right api.StoredMemoryResponse) api.StoredMemoryResponse {
	if left.Importance != right.Importance {
		if right.Importance > left.Importance {
			return right
		}
		return left
	}
	if left.Confidence != right.Confidence {
		if right.Confidence > left.Confidence {
			return right
		}
		return left
	}
	if left.UpdatedAt != right.UpdatedAt {
		if right.UpdatedAt > left.UpdatedAt {
			return right
		}
		return left
	}
	if len([]rune(strings.TrimSpace(right.Summary))) > len([]rune(strings.TrimSpace(left.Summary))) {
		return right
	}
	return left
}

func hanFragments(text string) []string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) < 2 {
		return nil
	}
	fragments := make([]string, 0, len(runes)-1)
	for i := 0; i < len(runes)-1; i++ {
		if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
			fragments = append(fragments, string(runes[i:i+2]))
		}
	}
	return fragments
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonBlankText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
