package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/skills"

	_ "modernc.org/sqlite"
)

func (s *SQLiteStore) Learn(input LearnInput) (api.LearnResponse, error) {
	runtime := s.runtimeForAgent(input.AgentKey)
	s.mu.Lock()
	history, err := s.listProjectionItemsLocked(input.AgentKey)
	s.mu.Unlock()
	if err != nil {
		return api.LearnResponse{}, err
	}
	drafts := summarizeLearnWithFallback(runtime.Summarizer, LearnSynthesisInput{
		Request:  input.Request,
		Trace:    input.Trace,
		AgentKey: input.AgentKey,
		TeamID:   input.TeamID,
		UserKey:  input.UserKey,
		History:  history,
	})
	stored := buildLearnedMemoriesFromDrafts(input, drafts)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range stored {
		if err := s.writeLocked(item, runtime.Embedder); err != nil {
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
		items, err := s.listProjectionItemsLocked(input.AgentKey)
		if err != nil {
			return api.LearnResponse{}, err
		}
		autoConsolidation, err = s.applyConsolidationPlanLocked(input.AgentKey, buildObservationConsolidationPlanWithMode(input.AgentKey, items, time.Now(), false), runtime.Embedder)
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

func (s *SQLiteStore) Consolidate(agentKey string) (ConsolidationResult, error) {
	embedder := s.runtimeForAgent(agentKey).Embedder
	s.mu.Lock()
	items, err := s.listProjectionItemsLocked(agentKey)
	if err != nil {
		s.mu.Unlock()
		return ConsolidationResult{}, err
	}
	result, err := s.applyConsolidationPlanLocked(agentKey, buildConsolidationPlan(agentKey, items, time.Now()), embedder)
	s.mu.Unlock()
	return result, err
}

func (s *SQLiteStore) applyConsolidationPlanLocked(agentKey string, plan consolidationPlan, embedder *EmbeddingProvider) (ConsolidationResult, error) {
	result := ConsolidationResult{}
	for id := range plan.archiveIDs {
		record, err := s.updateLocked(agentKey, MutationInput{ID: id, Status: ptrString(StatusArchived)}, embedder)
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
		record, err := s.updateLocked(agentKey, MutationInput{ID: id, Status: ptrString(StatusSuperseded)}, embedder)
		if err != nil {
			return result, err
		}
		if record != nil {
			result.MergedCount++
			if err := s.insertMemoryLinkLocked(keeperID, id, "supersedes", 1.0); err != nil {
				return result, err
			}
		}
	}
	for _, id := range plan.promoteIDs {
		record, err := s.promoteLocked(agentKey, PromoteInput{
			SourceID:      id,
			ScopeType:     ScopeAgent,
			ArchiveSource: true,
		}, embedder)
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

func (s *SQLiteStore) Update(agentKey string, input MutationInput) (*ToolRecord, error) {
	embedder := s.runtimeForAgent(agentKey).Embedder
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.updateLocked(agentKey, input, embedder)
}

func (s *SQLiteStore) updateLocked(agentKey string, input MutationInput, embedder *EmbeddingProvider) (*ToolRecord, error) {

	current, err := s.readProjectionByIDLocked(strings.TrimSpace(input.ID))
	if err != nil || current == nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(current.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	before := *current
	if input.Title != nil {
		current.Title = strings.TrimSpace(*input.Title)
	}
	if input.Summary != nil {
		current.Summary = strings.TrimSpace(*input.Summary)
	}
	if input.Category != nil {
		current.Category = normalizeCategory(*input.Category)
	}
	if input.ScopeType != nil {
		current.ScopeType = normalizeScopeType(*input.ScopeType)
	}
	if input.ScopeKey != nil {
		current.ScopeKey = strings.TrimSpace(*input.ScopeKey)
	}
	if input.Status != nil {
		current.Status = normalizeMemoryStatus(*input.Status, current.Kind)
	}
	if input.Importance != nil {
		current.Importance = normalizeImportance(*input.Importance)
	}
	if input.Confidence != nil {
		current.Confidence = normalizeMemoryConfidence(*input.Confidence, current.Kind)
	}
	if input.ReplaceTags {
		current.Tags = normalizeTags(input.Tags)
	}
	current.UpdatedAt = time.Now().UnixMilli()
	if err := s.writeLocked(*current, embedder); err != nil {
		return nil, err
	}
	record := toolRecordFromStored(*current)
	event := historyEventFromMemory(*current, "update", "mutation")
	event.Before = historyAfterFromStored(before)
	event.After = historyAfterFromStored(*current)
	_ = s.recordHistoryLocked(event)
	logMemoryOperation("update", map[string]any{"agentKey": agentKey, "id": input.ID, "status": record.Status})
	return &record, nil
}

func (s *SQLiteStore) Forget(agentKey string, id string, status string) (*ToolRecord, error) {
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

func (s *SQLiteStore) Timeline(agentKey string, id string, limit int) ([]TimelineEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = normalizeLimit(limit, 10)
	item, err := s.readProjectionByIDLocked(id)
	if err != nil || item == nil {
		return nil, err
	}
	record := toolRecordFromStored(*item)
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
		return nil, nil
	}
	entries := []TimelineEntry{{
		Memory:       record,
		RelationType: "self",
		Direction:    "self",
	}}
	rows, err := s.db.Query(
		`SELECT l.FROM_ID_, l.TO_ID_, l.RELATION_TYPE_,
			m.ID_, m.AGENT_KEY_, m.SUBJECT_KEY_, m.KIND_, m.REF_ID_, m.SCOPE_TYPE_, m.SCOPE_KEY_, m.TITLE_, m.SUMMARY_, m.SOURCE_TYPE_,
			m.CATEGORY_, m.IMPORTANCE_, m.CONFIDENCE_, m.STATUS_, m.TAGS_, m.EMBEDDING_MODEL_, m.TS_, m.UPDATED_AT_, m.ACCESS_COUNT_, m.LAST_ACCESSED_AT_,
			CASE WHEN m.EMBEDDING_ IS NULL THEN 0 ELSE 1 END
		FROM MEMORY_LINKS l
		JOIN MEMORIES m ON m.ID_ = CASE WHEN l.FROM_ID_ = ? THEN l.TO_ID_ ELSE l.FROM_ID_ END
		WHERE l.FROM_ID_ = ? OR l.TO_ID_ = ?
		ORDER BY l.CREATED_AT_ DESC
		LIMIT ?`,
		id, id, id, limit-1,
	)
	if err != nil {
		return entries, nil
	}
	defer rows.Close()
	for rows.Next() {
		var fromID, toID, relationType string
		record, err := scanToolRecord(linkScanner{scanner: rows, fromID: &fromID, toID: &toID, relationType: &relationType})
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
			continue
		}
		direction := "peer"
		if strings.TrimSpace(fromID) == strings.TrimSpace(id) {
			direction = "outgoing"
		} else if strings.TrimSpace(toID) == strings.TrimSpace(id) {
			direction = "incoming"
		}
		entries = append(entries, TimelineEntry{
			Memory:       record,
			RelationType: relationType,
			Direction:    direction,
		})
	}
	err = rows.Err()
	if err == nil {
		logMemoryOperation("timeline", map[string]any{"agentKey": agentKey, "id": id, "limit": limit, "count": len(entries)})
	}
	return entries, err
}

func (s *SQLiteStore) Promote(agentKey string, input PromoteInput) (*ToolRecord, error) {
	embedder := s.runtimeForAgent(agentKey).Embedder
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.promoteLocked(agentKey, input, embedder)
}

func (s *SQLiteStore) promoteLocked(agentKey string, input PromoteInput, embedder *EmbeddingProvider) (*ToolRecord, error) {

	source, err := s.readProjectionByIDLocked(strings.TrimSpace(input.SourceID))
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
	if err := s.writeLocked(item, embedder); err != nil {
		return nil, err
	}
	if err := s.insertMemoryLinkLocked(item.ID, source.ID, "derived_from", 1.0); err != nil {
		return nil, err
	}
	createEvent := historyEventFromMemory(item, "promote.create_fact", "promote")
	createEvent.After = historyAfterFromStored(item)
	createEvent.Meta = map[string]any{"sourceId": source.ID}
	_ = s.recordHistoryLocked(createEvent)
	if input.ArchiveSource {
		beforeSource := *source
		source.Status = StatusArchived
		source.UpdatedAt = time.Now().UnixMilli()
		if err := s.writeLocked(*source, embedder); err != nil {
			return nil, err
		}
		archiveEvent := historyEventFromMemory(*source, "promote.archive_source", "promote")
		archiveEvent.Before = historyAfterFromStored(beforeSource)
		archiveEvent.After = historyAfterFromStored(*source)
		archiveEvent.Meta = map[string]any{"promotedId": item.ID}
		_ = s.recordHistoryLocked(archiveEvent)
	}
	record := toolRecordFromStored(item)
	logMemoryOperation("promote", map[string]any{"agentKey": agentKey, "sourceId": input.SourceID, "id": item.ID, "archiveSource": input.ArchiveSource})
	return &record, nil
}

func (s *SQLiteStore) writeLocked(item api.StoredMemoryResponse, embedder *EmbeddingProvider) error {
	if item.ID == "" {
		item.ID = generateMemoryID()
	}
	now := time.Now().UnixMilli()
	if item.UpdatedAt == 0 {
		item.UpdatedAt = now
	}
	if item.CreatedAt == 0 {
		item.CreatedAt = item.UpdatedAt
	}
	item = normalizeStoredItem(item)
	if err := validateStoredMemoryTimeContract(item, "memory.sqlite.write"); err != nil {
		logMemoryWriteRejected("write_rejected", item, err)
		return err
	}
	if err := validateStoredMemoryItem(item); err != nil {
		logMemoryWriteRejected("write_rejected", item, err)
		return err
	}
	if strings.TrimSpace(item.ScopeKey) == "" {
		item.ScopeKey = normalizeScopeKey(item.ScopeType, "", item.AgentKey, "", item.ChatID, "")
	}
	before, err := s.readProjectionByIDLocked(strings.TrimSpace(item.ID))
	if err != nil {
		return err
	}
	if existing, err := s.findExactDuplicateLocked(item); err != nil {
		return err
	} else if existing != nil {
		return s.bumpDuplicateMemoryLocked(*existing, item, now)
	}
	if existing, err := s.findNearDuplicateFactLocked(item); err != nil {
		return err
	} else if existing != nil {
		return s.mergeNearDuplicateFactLocked(*existing, item, now)
	}
	if normalizeMemoryKind(item.Kind) == KindFact {
		if err := s.supersedeMatchingFactsLocked(item); err != nil {
			return err
		}
	}
	if err := s.upsertProjectionLocked(item); err != nil {
		return err
	}
	if normalizeMemoryKind(item.Kind) == KindObservation {
		if err := s.upsertObservationSourceLocked(item); err != nil {
			return err
		}
	} else {
		if err := s.upsertFactSourceLocked(item); err != nil {
			return err
		}
	}
	if s.dualWriteMD {
		_ = AppendJournal(s.root, item)
	}
	if embedder != nil {
		text := strings.TrimSpace(item.Title + " " + item.Summary)
		if text != "" {
			if vec, err := embedder.EmbedSingle(context.Background(), text); err == nil {
				if blob, err := json.Marshal(vec); err == nil {
					_, _ = s.db.Exec(
						`UPDATE MEMORIES SET EMBEDDING_ = ?, EMBEDDING_MODEL_ = ? WHERE ID_ = ?`,
						blob, embedder.Model, item.ID,
					)
				}
			}
		}
	}
	_ = s.refreshSnapshotsLocked(item.AgentKey)
	operation := "write.create"
	if before != nil {
		operation = "write.update"
	}
	event := historyEventFromMemory(item, operation, item.SourceType)
	if before != nil {
		event.Before = historyAfterFromStored(*before)
	}
	event.After = historyAfterFromStored(item)
	_ = s.recordHistoryLocked(event)
	return nil
}

func (s *SQLiteStore) findExactDuplicateLocked(item api.StoredMemoryResponse) (*api.StoredMemoryResponse, error) {
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE ID_ != ?
			AND AGENT_KEY_ = ?
			AND KIND_ = ?
			AND SCOPE_TYPE_ = ?
			AND SCOPE_KEY_ = ?
			AND CATEGORY_ = ?
			AND TITLE_ = ?
			AND SUMMARY_ = ?
			AND STATUS_ = ?
			AND ((? = '' AND CHAT_ID_ = '') OR CHAT_ID_ = ?)
		ORDER BY UPDATED_AT_ DESC
		LIMIT 1`,
		item.ID,
		item.AgentKey,
		item.Kind,
		item.ScopeType,
		item.ScopeKey,
		item.Category,
		item.Title,
		item.Summary,
		item.Status,
		item.ChatID, item.ChatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	existing, err := scanMemoryRow(rows)
	if err != nil {
		return nil, err
	}
	return &existing, rows.Err()
}

func (s *SQLiteStore) bumpDuplicateMemoryLocked(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse, now int64) error {
	before := existing
	existing.Importance = max(existing.Importance, incoming.Importance)
	existing.Confidence = maxFloat(existing.Confidence, incoming.Confidence)
	existing.Tags = normalizeTags(append(existing.Tags, incoming.Tags...))
	existing.UpdatedAt = now
	existing.AccessCount++
	existing.LastAccessedAt = &now
	if err := s.upsertProjectionLocked(existing); err != nil {
		return err
	}
	if normalizeMemoryKind(existing.Kind) == KindObservation {
		if err := s.upsertObservationSourceLocked(existing); err != nil {
			return err
		}
	} else {
		if err := s.upsertFactSourceLocked(existing); err != nil {
			return err
		}
	}
	_ = s.refreshSnapshotsLocked(existing.AgentKey)
	event := historyEventFromMemory(existing, "write.duplicate_bump", incoming.SourceType)
	event.Before = historyAfterFromStored(before)
	event.After = historyAfterFromStored(existing)
	event.Delta = map[string]any{"incomingId": incoming.ID}
	_ = s.recordHistoryLocked(event)
	logMemoryOperation("write_duplicate", map[string]any{
		"id":          existing.ID,
		"incomingId":  incoming.ID,
		"agentKey":    existing.AgentKey,
		"kind":        existing.Kind,
		"scopeType":   existing.ScopeType,
		"scopeKey":    existing.ScopeKey,
		"category":    existing.Category,
		"accessCount": existing.AccessCount,
	})
	return nil
}

func (s *SQLiteStore) findNearDuplicateFactLocked(item api.StoredMemoryResponse) (*api.StoredMemoryResponse, error) {
	if normalizeMemoryKind(item.Kind) != KindFact {
		return nil, nil
	}
	if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE ID_ != ?
			AND AGENT_KEY_ = ?
			AND KIND_ = ?
			AND SCOPE_TYPE_ = ?
			AND SCOPE_KEY_ = ?
			AND CATEGORY_ = ?
			AND STATUS_ = ?
		ORDER BY IMPORTANCE_ DESC, CONFIDENCE_ DESC, UPDATED_AT_ DESC`,
		item.ID,
		item.AgentKey,
		KindFact,
		item.ScopeType,
		item.ScopeKey,
		item.Category,
		StatusActive,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		existing, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		if isNearDuplicateFactMemory(existing, item) {
			return &existing, nil
		}
	}
	return nil, rows.Err()
}

func (s *SQLiteStore) mergeNearDuplicateFactLocked(existing api.StoredMemoryResponse, incoming api.StoredMemoryResponse, now int64) error {
	before := existing
	merged := mergeNearDuplicateFactMemory(existing, incoming, now)
	if err := s.upsertProjectionLocked(merged); err != nil {
		return err
	}
	if err := s.upsertFactSourceLocked(merged); err != nil {
		return err
	}
	_ = s.refreshSnapshotsLocked(existing.AgentKey)
	event := historyEventFromMemory(merged, "write.near_duplicate_merge", incoming.SourceType)
	event.Before = historyAfterFromStored(before)
	event.After = historyAfterFromStored(merged)
	event.Delta = map[string]any{"incomingId": incoming.ID}
	_ = s.recordHistoryLocked(event)
	logMemoryOperation("write_near_duplicate_fact", map[string]any{
		"id":         existing.ID,
		"incomingId": incoming.ID,
		"agentKey":   existing.AgentKey,
		"scopeType":  existing.ScopeType,
		"scopeKey":   existing.ScopeKey,
		"category":   existing.Category,
	})
	return nil
}

func maxFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (s *SQLiteStore) supersedeMatchingFactsLocked(item api.StoredMemoryResponse) error {
	if normalizeMemoryKind(item.Kind) != KindFact {
		return nil
	}
	if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
		return nil
	}
	dedupeKey := factDedupeKey(item)
	rows, err := s.db.Query(
		`SELECT ID_ FROM MEMORY_FACTS
		WHERE DEDUPE_KEY_ = ? AND ID_ != ? AND STATUS_ = ?`,
		dedupeKey, item.ID, StatusActive,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var priorIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if strings.TrimSpace(id) != "" {
			priorIDs = append(priorIDs, strings.TrimSpace(id))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, priorID := range priorIDs {
		now := time.Now().UnixMilli()
		before, _ := s.readProjectionByIDLocked(priorID)
		if _, err := s.db.Exec(
			`UPDATE MEMORY_FACTS SET STATUS_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			StatusSuperseded, now, priorID,
		); err != nil {
			return err
		}
		if _, err := s.db.Exec(
			`UPDATE MEMORIES SET STATUS_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			StatusSuperseded, now, priorID,
		); err != nil {
			return err
		}
		if err := s.insertMemoryLinkLocked(item.ID, priorID, "supersedes", 1.0); err != nil {
			return err
		}
		after, _ := s.readProjectionByIDLocked(priorID)
		if after != nil {
			event := historyEventFromMemory(*after, "consolidate.supersede", "write")
			if before != nil {
				event.Before = historyAfterFromStored(*before)
			}
			event.After = historyAfterFromStored(*after)
			event.Meta = map[string]any{"supersededBy": item.ID}
			_ = s.recordHistoryLocked(event)
		}
	}
	return nil
}

func (s *SQLiteStore) upsertProjectionLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORIES (ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			CHAT_ID_ = excluded.CHAT_ID_,
			AGENT_KEY_ = excluded.AGENT_KEY_,
			SUBJECT_KEY_ = excluded.SUBJECT_KEY_,
			KIND_ = excluded.KIND_,
			REF_ID_ = excluded.REF_ID_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			TITLE_ = excluded.TITLE_,
			SOURCE_TYPE_ = excluded.SOURCE_TYPE_,
			SUMMARY_ = excluded.SUMMARY_,
			CATEGORY_ = excluded.CATEGORY_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			TAGS_ = excluded.TAGS_,
			UPDATED_AT_ = excluded.UPDATED_AT_,
			ACCESS_COUNT_ = excluded.ACCESS_COUNT_,
			LAST_ACCESSED_AT_ = excluded.LAST_ACCESSED_AT_`,
		item.ID, item.CreatedAt, item.RequestID, item.ChatID,
		item.AgentKey, item.SubjectKey, item.Kind, item.RefID, item.ScopeType, item.ScopeKey, item.Title,
		item.SourceType, item.Summary, item.Category, item.Importance, item.Confidence, item.Status, tagsStr, item.UpdatedAt, item.AccessCount, item.LastAccessedAt,
	)
	return err
}

func (s *SQLiteStore) upsertFactSourceLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_FACTS (ID_, AGENT_KEY_, SCOPE_TYPE_, SCOPE_KEY_, CATEGORY_, TITLE_, CONTENT_,
			TAGS_, IMPORTANCE_, CONFIDENCE_, STATUS_, SOURCE_KIND_, SOURCE_REF_, DEDUPE_KEY_, CREATED_AT_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			AGENT_KEY_ = excluded.AGENT_KEY_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			CATEGORY_ = excluded.CATEGORY_,
			TITLE_ = excluded.TITLE_,
			CONTENT_ = excluded.CONTENT_,
			TAGS_ = excluded.TAGS_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			SOURCE_KIND_ = excluded.SOURCE_KIND_,
			SOURCE_REF_ = excluded.SOURCE_REF_,
			DEDUPE_KEY_ = excluded.DEDUPE_KEY_,
			UPDATED_AT_ = excluded.UPDATED_AT_`,
		item.ID, item.AgentKey, item.ScopeType, item.ScopeKey, item.Category, item.Title, item.Summary,
		tagsStr, item.Importance, item.Confidence, item.Status, item.SourceType, item.RefID, factDedupeKey(item), item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *SQLiteStore) upsertObservationSourceLocked(item api.StoredMemoryResponse) error {
	tagsStr := strings.Join(item.Tags, ",")
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_OBSERVATIONS (ID_, AGENT_KEY_, CHAT_ID_, RUN_ID_, SCOPE_TYPE_, SCOPE_KEY_, TYPE_, TITLE_, SUMMARY_, DETAIL_,
			FILES_JSON_, TOOLS_JSON_, TAGS_, IMPORTANCE_, CONFIDENCE_, STATUS_, CREATED_AT_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO UPDATE SET
			AGENT_KEY_ = excluded.AGENT_KEY_,
			CHAT_ID_ = excluded.CHAT_ID_,
			RUN_ID_ = excluded.RUN_ID_,
			SCOPE_TYPE_ = excluded.SCOPE_TYPE_,
			SCOPE_KEY_ = excluded.SCOPE_KEY_,
			TYPE_ = excluded.TYPE_,
			TITLE_ = excluded.TITLE_,
			SUMMARY_ = excluded.SUMMARY_,
			DETAIL_ = excluded.DETAIL_,
			TAGS_ = excluded.TAGS_,
			IMPORTANCE_ = excluded.IMPORTANCE_,
			CONFIDENCE_ = excluded.CONFIDENCE_,
			STATUS_ = excluded.STATUS_,
			UPDATED_AT_ = excluded.UPDATED_AT_`,
		item.ID, item.AgentKey, item.ChatID, item.RefID, item.ScopeType, item.ScopeKey, item.Category, item.Title, item.Summary, item.Summary,
		tagsStr, item.Importance, item.Confidence, item.Status, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func factDedupeKey(item api.StoredMemoryResponse) string {
	title := strings.ToLower(strings.TrimSpace(item.Title))
	if title == "" {
		title = strings.ToLower(strings.TrimSpace(item.Summary))
	}
	return fmt.Sprintf("%s|%s|%s", normalizeScopeType(item.ScopeType), strings.TrimSpace(item.ScopeKey), title)
}

func (s *SQLiteStore) insertMemoryLinkLocked(fromID string, toID string, relationType string, weight float64) error {
	if strings.TrimSpace(fromID) == "" || strings.TrimSpace(toID) == "" || strings.TrimSpace(relationType) == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_LINKS (ID_, FROM_ID_, TO_ID_, RELATION_TYPE_, WEIGHT_, CREATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(ID_) DO NOTHING`,
		generateMemoryID(),
		strings.TrimSpace(fromID),
		strings.TrimSpace(toID),
		strings.TrimSpace(relationType),
		weight,
		time.Now().UnixMilli(),
	)
	return err
}
