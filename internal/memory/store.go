package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/skills"
	"agent-platform/internal/timecontract"
)

type Store interface {
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

type RuntimeConfig struct {
	Embedder   *EmbeddingProvider
	Summarizer RememberSummarizer
}

type RuntimeResolver func(agentKey string) RuntimeConfig

type FileStore struct {
	root       string
	summarizer RememberSummarizer
}

func NewFileStore(root string) (*FileStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) SetRememberSummarizer(summarizer RememberSummarizer) {
	if s == nil {
		return
	}
	s.summarizer = summarizer
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
	now := time.Now().UnixMilli()
	// A newly produced memory is owned by this runtime, so it receives one
	// explicit creation clock. Persisted records are validated by readAllStored
	// before they can reach this path and are never repaired here.
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}
	if item.UpdatedAt == 0 {
		item.UpdatedAt = item.CreatedAt
	}
	if err := validateStoredMemoryTimeContract(item, "memory.file.write"); err != nil {
		return err
	}
	if err := validateStoredMemoryItem(item); err != nil {
		logMemoryWriteRejected("write_rejected", item, err)
		return err
	}
	if strings.TrimSpace(item.ScopeKey) == "" {
		item.ScopeKey = normalizeScopeKey(item.ScopeType, "", item.AgentKey, "", item.ChatID, "")
	}
	items, err := s.readAllStored()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, existing := range items {
		if !isExactDuplicateMemory(existing, item) {
			continue
		}
		existing.Importance = max(existing.Importance, item.Importance)
		existing.Confidence = maxFloat(existing.Confidence, item.Confidence)
		existing.Tags = normalizeTags(append(existing.Tags, item.Tags...))
		existing.UpdatedAt = now
		existing.AccessCount++
		existing.LastAccessedAt = &now
		payload, err := json.MarshalIndent(existing, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(s.root, existing.ID+".stored.json"), payload, 0o644); err != nil {
			return err
		}
		logMemoryOperation("write_duplicate", map[string]any{
			"id":          existing.ID,
			"incomingId":  item.ID,
			"agentKey":    existing.AgentKey,
			"kind":        existing.Kind,
			"scopeType":   existing.ScopeType,
			"scopeKey":    existing.ScopeKey,
			"category":    existing.Category,
			"accessCount": existing.AccessCount,
		})
		if s.root != "" && strings.TrimSpace(existing.AgentKey) != "" {
			updatedItems, err := s.readAllStored()
			if err == nil {
				_ = refreshSnapshots(s.root, existing.AgentKey, updatedItems)
			}
		}
		return nil
	}
	if normalizeMemoryKind(item.Kind) == KindFact && normalizeMemoryStatus(item.Status, item.Kind) == StatusActive {
		for _, existing := range items {
			if !isNearDuplicateFactMemory(existing, item) {
				continue
			}
			merged := mergeNearDuplicateFactMemory(existing, item, now)
			payload, err := json.MarshalIndent(merged, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(s.root, existing.ID+".stored.json"), payload, 0o644); err != nil {
				return err
			}
			logMemoryOperation("write_near_duplicate_fact", map[string]any{
				"id":         existing.ID,
				"incomingId": item.ID,
				"agentKey":   existing.AgentKey,
				"scopeType":  existing.ScopeType,
				"scopeKey":   existing.ScopeKey,
				"category":   existing.Category,
			})
			if s.root != "" && strings.TrimSpace(existing.AgentKey) != "" {
				updatedItems, err := s.readAllStored()
				if err == nil {
					_ = refreshSnapshots(s.root, existing.AgentKey, updatedItems)
				}
			}
			return nil
		}
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

func isExactDuplicateMemory(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse) bool {
	return strings.TrimSpace(existing.ID) != strings.TrimSpace(incoming.ID) &&
		strings.TrimSpace(existing.AgentKey) == strings.TrimSpace(incoming.AgentKey) &&
		strings.TrimSpace(existing.Kind) == strings.TrimSpace(incoming.Kind) &&
		strings.TrimSpace(existing.ScopeType) == strings.TrimSpace(incoming.ScopeType) &&
		strings.TrimSpace(existing.ScopeKey) == strings.TrimSpace(incoming.ScopeKey) &&
		strings.TrimSpace(existing.Category) == strings.TrimSpace(incoming.Category) &&
		strings.TrimSpace(existing.Title) == strings.TrimSpace(incoming.Title) &&
		strings.TrimSpace(existing.Summary) == strings.TrimSpace(incoming.Summary) &&
		strings.TrimSpace(existing.Status) == strings.TrimSpace(incoming.Status) &&
		strings.TrimSpace(existing.ChatID) == strings.TrimSpace(incoming.ChatID)
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
		item, err := decodeStoredMemoryResponse(data, "memory.file["+entry.Name()+"]")
		if err != nil {
			return nil, err
		}
		items = append(items, normalizeStoredItem(item))
	}
	return items, nil
}

// decodeStoredMemoryResponse keeps the persisted-file boundary strict before
// typed decoding. In particular, a numeric string, floating point value, or
// null at a structured time field must remain a time_contract_violation rather
// than degrading into a generic JSON type error and an HTTP 500 upstream.
func decodeStoredMemoryResponse(data []byte, location string) (api.StoredMemoryResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return api.StoredMemoryResponse{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return api.StoredMemoryResponse{}, fmt.Errorf("stored memory contains multiple JSON values")
		}
		return api.StoredMemoryResponse{}, err
	}
	if err := validatePersistedStoredMemoryTimePayload(payload, location); err != nil {
		return api.StoredMemoryResponse{}, err
	}
	var item api.StoredMemoryResponse
	if err := json.Unmarshal(data, &item); err != nil {
		return api.StoredMemoryResponse{}, err
	}
	if err := validateStoredMemoryTimeContract(item, location); err != nil {
		return api.StoredMemoryResponse{}, err
	}
	return item, nil
}

func validatePersistedStoredMemoryTimePayload(payload map[string]any, location string) error {
	for _, field := range []string{"createdAt", "updatedAt"} {
		value, exists := payload[field]
		if !exists {
			return &timecontract.Violation{Field: field, Location: location + "." + field, Reason: "is required"}
		}
		if _, err := timecontract.ParseEpochMillis(value, field, location+"."+field); err != nil {
			return err
		}
	}
	if value, exists := payload["lastAccessedAt"]; exists {
		if _, err := timecontract.ParseEpochMillis(value, "lastAccessedAt", location+".lastAccessedAt"); err != nil {
			return err
		}
	}
	return nil
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
	history, err := s.readAllStored()
	if err != nil {
		return api.LearnResponse{}, err
	}
	drafts := summarizeLearnWithFallback(s.summarizer, LearnSynthesisInput{
		Request:  input.Request,
		Trace:    input.Trace,
		AgentKey: input.AgentKey,
		TeamID:   input.TeamID,
		UserKey:  input.UserKey,
		History:  filterHistoryByAgent(history, input.AgentKey),
	})
	stored := buildLearnedMemoriesFromDrafts(input, drafts)
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
	return s.applyConsolidationPlan(agentKey, buildConsolidationPlan(agentKey, items, time.Now()))
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
	for id, keeperID := range plan.supersedeIDs {
		status := StatusSuperseded
		record, err := s.Update(agentKey, MutationInput{ID: id, Status: &status})
		if err != nil {
			return result, err
		}
		if record != nil {
			result.MergedCount++
			logMemoryOperation("consolidate.supersede", map[string]any{
				"agentKey": agentKey,
				"id":       id,
				"keeperId": keeperID,
			})
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

func filterHistoryByAgent(items []api.StoredMemoryResponse, agentKey string) []api.StoredMemoryResponse {
	if strings.TrimSpace(agentKey) == "" {
		return append([]api.StoredMemoryResponse(nil), items...)
	}
	filtered := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.AgentKey) == strings.TrimSpace(agentKey) || strings.TrimSpace(item.AgentKey) == "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
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
