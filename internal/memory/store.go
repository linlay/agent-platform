package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/skills"
	"agent-platform-runner-go/internal/stream"
)

type Store interface {
	Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error)
	Search(query string, limit int) ([]api.StoredMemoryResponse, error)
	SearchDetailed(agentKey string, query string, category string, limit int) ([]ScoredRecord, error)
	Read(id string) (*api.StoredMemoryResponse, error)
	ReadDetail(agentKey string, id string) (*ToolRecord, error)
	List(agentKey string, category string, limit int, sort string) ([]ToolRecord, error)
	Write(item api.StoredMemoryResponse) error
	BuildContextBundle(request ContextRequest) (ContextBundle, error)
	Learn(input LearnInput) (api.LearnResponse, error)
	Consolidate(agentKey string) (ConsolidationResult, error)
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
		Kind:       KindFact,
		RefID:      request.ChatID,
		ScopeType:  ScopeAgent,
		ScopeKey:   normalizeScopeKey(ScopeAgent, "", agentKey, "", request.ChatID, ""),
		Title:      normalizeMemoryTitle("", summary),
		Summary:    summary,
		SourceType: "remember",
		Category:   "remember",
		Importance: rememberImportance,
		Confidence: 0.9,
		Status:     StatusActive,
		Tags:       []string{"remember"},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	stored = normalizeStoredItem(stored)
	if err := s.Write(stored); err != nil {
		return api.RememberResponse{}, err
	}
	logMemoryOperation("remember", map[string]any{
		"agentKey":    agentKey,
		"chatId":      request.ChatID,
		"requestId":   request.RequestID,
		"memoryCount": 1,
		"memoryId":    stored.ID,
	})

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
	logMemoryOperation("search", map[string]any{"query": query, "limit": limit, "count": len(out)})
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
			logMemoryRead("read", "", id, true)
			return &copy, nil
		}
	}
	logMemoryRead("read", "", id, false)
	return nil, nil
}

func (s *FileStore) ReadDetail(agentKey string, id string) (*ToolRecord, error) {
	item, err := s.Read(id)
	if err != nil || item == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
		logMemoryRead("read_detail", agentKey, id, false)
		return nil, nil
	}
	record := toolRecordFromStored(*item)
	logMemoryRead("read_detail", agentKey, id, true)
	return &record, nil
}

func (s *FileStore) List(agentKey string, category string, limit int, sortBy string) ([]ToolRecord, error) {
	items, err := s.readAllStored()
	if err != nil {
		return nil, err
	}
	filtered := make([]ToolRecord, 0, len(items))
	normalizedCategory := normalizeOptionalCategory(category)
	for _, item := range items {
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if normalizedCategory != "" && normalizeOptionalCategory(item.Category) != normalizedCategory {
			continue
		}
		filtered = append(filtered, toolRecordFromStored(item))
	}

	sortToolRecords(filtered, normalizeSort(sortBy))
	limit = normalizeLimit(limit, 10)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	logMemoryOperation("list", map[string]any{"agentKey": agentKey, "category": category, "limit": limit, "sort": sortBy, "count": len(filtered)})
	return filtered, nil
}

func (s *FileStore) SearchDetailed(agentKey string, query string, category string, limit int) ([]ScoredRecord, error) {
	items, err := s.readAllStored()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return []ScoredRecord{}, nil
	}
	normalizedCategory := normalizeOptionalCategory(category)
	results := make([]ScoredRecord, 0)
	for _, item := range items {
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if normalizedCategory != "" && normalizeOptionalCategory(item.Category) != normalizedCategory {
			continue
		}
		if !matchesMemoryNeedle(item, needle) {
			continue
		}
		results = append(results, ScoredRecord{
			Memory:    toolRecordFromStored(item),
			Score:     1,
			MatchType: "like",
		})
	}
	sortScoredRecords(results)
	limit = normalizeLimit(limit, 10)
	if len(results) > limit {
		results = results[:limit]
	}
	logMemoryOperation("search_detailed", map[string]any{"agentKey": agentKey, "query": query, "category": category, "limit": limit, "count": len(results)})
	return results, nil
}

func (s *FileStore) Write(item api.StoredMemoryResponse) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	item = normalizeStoredItem(item)
	if err := validateStoredMemoryItem(item); err != nil {
		logMemoryWriteRejected("write_rejected", item, err)
		return err
	}
	if strings.TrimSpace(item.ScopeKey) == "" {
		item.ScopeKey = normalizeScopeKey(item.ScopeType, "", item.AgentKey, "", item.ChatID, "")
	}
	payload, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.root, item.ID+".stored.json"), payload, 0o644); err != nil {
		return err
	}
	logMemoryWrite("write", item)
	if s.root != "" && strings.TrimSpace(item.AgentKey) != "" {
		items, err := s.readAllStored()
		if err == nil {
			_ = refreshSnapshots(s.root, item.AgentKey, items)
		}
	}
	return nil
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
		items = append(items, normalizeStoredItem(item))
	}
	return items, nil
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

func sortToolRecords(items []ToolRecord, sortBy string) {
	sort.SliceStable(items, func(i, j int) bool {
		if sortBy == "importance" {
			if items[i].Importance != items[j].Importance {
				return items[i].Importance > items[j].Importance
			}
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].Importance > items[j].Importance
	})
}

func (s *FileStore) BuildContextBundle(request ContextRequest) (ContextBundle, error) {
	items, err := s.readAllStored()
	if err != nil {
		return ContextBundle{}, err
	}
	bundle := buildContextBundleFromStored(request, items)
	logMemoryOperation("build_context_bundle", map[string]any{
		"agentKey":       request.AgentKey,
		"teamId":         request.TeamID,
		"chatId":         request.ChatID,
		"userKey":        request.UserKey,
		"query":          request.Query,
		"stableFacts":    len(bundle.StableFacts),
		"observations":   len(bundle.RelevantObservations),
		"stableChars":    len(bundle.StablePrompt),
		"observationLen": len(bundle.ObservationPrompt),
	})
	return bundle, nil
}

func (s *FileStore) Learn(input LearnInput) (api.LearnResponse, error) {
	stored := extractLearnedMemories(input)
	for _, item := range stored {
		if err := s.Write(item); err != nil {
			return api.LearnResponse{}, err
		}
	}
	response := buildLearnResponse(input, stored)
	if len(stored) == 0 {
		response.Accepted = false
	}
	autoConsolidation := ConsolidationResult{}
	candidateCount := 0
	if input.SkillCandidates != nil {
		if candidate, ok := skills.CandidateFromRunTrace(input.Trace, input.AgentKey, input.Request.ChatID); ok {
			if _, err := input.SkillCandidates.Write(candidate); err != nil {
				return api.LearnResponse{}, err
			}
			candidateCount = 1
		}
	}
	if strings.TrimSpace(input.AgentKey) != "" && len(stored) > 0 {
		items, err := s.readAllStored()
		if err != nil {
			return api.LearnResponse{}, err
		}
		autoConsolidation, err = s.applyConsolidationPlan(input.AgentKey, buildObservationConsolidationPlanWithMode(input.AgentKey, items, time.Now(), false))
		if err != nil {
			return api.LearnResponse{}, err
		}
	}
	logMemoryOperation("learn", map[string]any{
		"agentKey":            input.AgentKey,
		"chatId":              input.Request.ChatID,
		"requestId":           input.Request.RequestID,
		"observationCount":    len(stored),
		"skillCandidateCount": candidateCount,
		"archivedCount":       autoConsolidation.ArchivedCount,
		"mergedCount":         autoConsolidation.MergedCount,
		"promotedCount":       autoConsolidation.PromotedCount,
		"accepted":            response.Accepted,
	})
	return response, nil
}

func (s *FileStore) Consolidate(agentKey string) (ConsolidationResult, error) {
	items, err := s.readAllStored()
	if err != nil {
		return ConsolidationResult{}, err
	}
	return s.applyConsolidationPlan(agentKey, buildObservationConsolidationPlan(agentKey, items, time.Now()))
}

func (s *FileStore) applyConsolidationPlan(agentKey string, plan consolidationPlan) (ConsolidationResult, error) {
	result := ConsolidationResult{}
	for id := range plan.archiveIDs {
		status := StatusArchived
		record, err := s.Update(agentKey, MutationInput{ID: id, Status: &status})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.ArchivedCount++
			if _, merged := plan.mergeIDs[id]; merged {
				result.MergedCount++
			}
		}
	}
	for _, id := range plan.promoteIDs {
		record, err := s.Promote(agentKey, PromoteInput{
			SourceID:      id,
			ScopeType:     ScopeAgent,
			ArchiveSource: true,
		})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.PromotedCount++
			result.ArchivedCount++
		}
	}
	logMemoryOperation("consolidate", map[string]any{
		"agentKey":      agentKey,
		"archivedCount": result.ArchivedCount,
		"mergedCount":   result.MergedCount,
		"promotedCount": result.PromotedCount,
	})
	return result, nil
}

func (s *FileStore) Update(agentKey string, input MutationInput) (*ToolRecord, error) {
	item, err := s.Read(strings.TrimSpace(input.ID))
	if err != nil || item == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	if input.Title != nil {
		item.Title = strings.TrimSpace(*input.Title)
	}
	if input.Summary != nil {
		item.Summary = strings.TrimSpace(*input.Summary)
	}
	if input.Category != nil {
		item.Category = normalizeCategory(*input.Category)
	}
	if input.ScopeType != nil {
		item.ScopeType = normalizeScopeType(*input.ScopeType)
	}
	if input.ScopeKey != nil {
		item.ScopeKey = strings.TrimSpace(*input.ScopeKey)
	}
	if input.Status != nil {
		item.Status = normalizeMemoryStatus(*input.Status, item.Kind)
	}
	if input.Importance != nil {
		item.Importance = normalizeImportance(*input.Importance)
	}
	if input.Confidence != nil {
		item.Confidence = normalizeMemoryConfidence(*input.Confidence, item.Kind)
	}
	if input.ReplaceTags {
		item.Tags = normalizeTags(input.Tags)
	}
	item.UpdatedAt = time.Now().UnixMilli()
	if err := s.Write(*item); err != nil {
		return nil, err
	}
	record := toolRecordFromStored(*item)
	logMemoryOperation("update", map[string]any{"agentKey": agentKey, "id": input.ID, "status": record.Status})
	return &record, nil
}

func (s *FileStore) Forget(agentKey string, id string, status string) (*ToolRecord, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		status = StatusArchived
	}
	record, err := s.Update(agentKey, MutationInput{
		ID:     strings.TrimSpace(id),
		Status: &status,
	})
	if err == nil && record != nil {
		logMemoryOperation("forget", map[string]any{"agentKey": agentKey, "id": id, "status": record.Status})
	}
	return record, err
}

func (s *FileStore) Timeline(agentKey string, id string, limit int) ([]TimelineEntry, error) {
	record, err := s.ReadDetail(agentKey, id)
	if err != nil || record == nil {
		return nil, err
	}
	limit = normalizeLimit(limit, 10)
	out := []TimelineEntry{{
		Memory:       *record,
		RelationType: "self",
		Direction:    "self",
	}}
	items, err := s.readAllStored()
	if err != nil {
		return out, nil
	}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == strings.TrimSpace(id) {
			continue
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(item.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		if strings.TrimSpace(item.RefID) == strings.TrimSpace(id) ||
			strings.TrimSpace(record.RefID) == strings.TrimSpace(item.ID) ||
			strings.TrimSpace(item.RefID) == strings.TrimSpace(record.RefID) ||
			factDedupeKey(item) == factDedupeKey(api.StoredMemoryResponse{
				ID:        record.ID,
				ScopeType: record.ScopeType,
				ScopeKey:  record.ScopeKey,
				Title:     record.Title,
				Summary:   record.Content,
			}) {
			out = append(out, TimelineEntry{
				Memory:       toolRecordFromStored(item),
				RelationType: "related",
				Direction:    "peer",
			})
		}
		if len(out) >= limit {
			break
		}
	}
	logMemoryOperation("timeline", map[string]any{"agentKey": agentKey, "id": id, "limit": limit, "count": len(out)})
	return out, nil
}

func (s *FileStore) Promote(agentKey string, input PromoteInput) (*ToolRecord, error) {
	source, err := s.Read(strings.TrimSpace(input.SourceID))
	if err != nil || source == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(source.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	if normalizeMemoryKind(source.Kind) != KindObservation {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         generateMemoryID(),
		RequestID:  source.RequestID,
		ChatID:     source.ChatID,
		AgentKey:   source.AgentKey,
		SubjectKey: normalizeSubjectKey("", source.ChatID, source.AgentKey),
		Kind:       KindFact,
		RefID:      source.ID,
		ScopeType:  normalizeScopeType(input.ScopeType),
		ScopeKey:   strings.TrimSpace(input.ScopeKey),
		Title:      normalizeMemoryTitle(input.Title, firstNonBlank(input.Summary, source.Title, source.Summary)),
		Summary:    firstNonBlank(input.Summary, source.Summary),
		SourceType: "promote",
		Category:   normalizeCategory(firstNonBlank(input.Category, source.Category)),
		Importance: normalizeImportance(firstPositive(input.Importance, source.Importance)),
		Confidence: normalizeMemoryConfidence(firstPositiveFloat(input.Confidence, source.Confidence), KindFact),
		Status:     StatusActive,
		Tags:       normalizeTags(append([]string{"promoted"}, chooseTags(input.Tags, source.Tags)...)),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if item.ScopeType == "" || item.ScopeType == ScopeChat {
		item.ScopeType = ScopeAgent
	}
	item.ScopeKey = normalizeScopeKey(item.ScopeType, item.ScopeKey, item.AgentKey, "", item.ChatID, "")
	if err := s.Write(item); err != nil {
		return nil, err
	}
	if input.ArchiveSource {
		status := StatusArchived
		_, _ = s.Update(agentKey, MutationInput{ID: source.ID, Status: &status})
	}
	record := toolRecordFromStored(item)
	logMemoryOperation("promote", map[string]any{"agentKey": agentKey, "sourceId": input.SourceID, "id": item.ID, "archiveSource": input.ArchiveSource})
	return &record, nil
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
