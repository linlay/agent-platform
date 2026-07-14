package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/api"

	_ "modernc.org/sqlite"
)

func (s *SQLiteStore) ApplyFeedback(signals []FeedbackSignal) error {
	if len(signals) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	for _, sig := range signals {
		before, _ := s.readProjectionByIDLocked(strings.TrimSpace(sig.ItemID))
		_, err := s.db.Exec(
			`UPDATE MEMORIES SET
				CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)),
				ACCESS_COUNT_ = ACCESS_COUNT_ + 1,
				LAST_ACCESSED_AT_ = ?,
				UPDATED_AT_ = ?
			WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, now, sig.ItemID,
		)
		if err != nil {
			return fmt.Errorf("apply feedback for %s: %w", sig.ItemID, err)
		}
		_, _ = s.db.Exec(
			`UPDATE MEMORY_FACTS SET CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)), UPDATED_AT_ = ? WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, sig.ItemID,
		)
		_, _ = s.db.Exec(
			`UPDATE MEMORY_OBSERVATIONS SET CONFIDENCE_ = MAX(0.1, MIN(1.0, CONFIDENCE_ + ?)), UPDATED_AT_ = ? WHERE ID_ = ?`,
			sig.ConfidenceDelta, now, sig.ItemID,
		)
		after, _ := s.readProjectionByIDLocked(strings.TrimSpace(sig.ItemID))
		if after != nil {
			operation := "feedback.decay"
			if sig.Referenced {
				operation = "feedback.boost"
			}
			event := historyEventFromMemory(*after, operation, "feedback")
			if before != nil {
				event.Before = historyAfterFromStored(*before)
			}
			event.After = historyAfterFromStored(*after)
			event.Delta = map[string]any{
				"confidenceDelta": sig.ConfidenceDelta,
				"referenced":      sig.Referenced,
			}
			_ = s.recordHistoryLocked(event)
		}
	}
	logMemoryOperation("apply_feedback", map[string]any{"signalCount": len(signals)})
	return nil
}

func (s *SQLiteStore) History(filter HistoryFilter) (HistoryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := normalizeHistoryLimit(filter.Limit)
	clauses := []string{"1=1"}
	args := []any{}
	if value := strings.TrimSpace(filter.AgentKey); value != "" {
		clauses = append(clauses, "AGENT_KEY_ = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.ChatID); value != "" {
		clauses = append(clauses, "CHAT_ID_ = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.RunID); value != "" {
		clauses = append(clauses, "RUN_ID_ = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.MemoryID); value != "" {
		clauses = append(clauses, "MEMORY_ID_ = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Operation); value != "" {
		clauses = append(clauses, "OPERATION_ = ?")
		args = append(args, value)
	}
	if value := strings.TrimSpace(filter.Cursor); value != "" {
		clauses = append(clauses, "TS_ < ?")
		args = append(args, value)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(
		`SELECT ID_, TS_, AGENT_KEY_, CHAT_ID_, RUN_ID_, REQUEST_ID_, USER_KEY_,
			MEMORY_ID_, MEMORY_KIND_, SCOPE_TYPE_, SCOPE_KEY_, OPERATION_, SOURCE_, STATUS_,
			BEFORE_JSON_, AFTER_JSON_, DELTA_JSON_, META_JSON_
		FROM MEMORY_HISTORY
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY TS_ DESC, ID_ DESC
		LIMIT ?`,
		args...,
	)
	if err != nil {
		return HistoryResult{}, err
	}
	defer rows.Close()
	events, err := scanHistoryRows(rows)
	if err != nil {
		return HistoryResult{}, err
	}
	result := HistoryResult{Events: events}
	if len(result.Events) > limit {
		result.NextCursor = fmt.Sprintf("%d", result.Events[limit-1].Timestamp)
		result.Events = result.Events[:limit]
	}
	return result, nil
}

func (s *SQLiteStore) recordRecallHistoryLocked(request ContextRequest, bundle ContextBundle, totalCandidates int, hybrid bool) error {
	source := "query"
	if request.PreviewOnly {
		source = "preview"
	}
	contextEvent := HistoryEvent{
		AgentKey:  strings.TrimSpace(request.AgentKey),
		ChatID:    strings.TrimSpace(request.ChatID),
		UserKey:   strings.TrimSpace(request.UserKey),
		Operation: "recall.context_built",
		Source:    source,
		Status:    HistoryStatusOK,
		Meta: map[string]any{
			"query":            strings.TrimSpace(request.Query),
			"teamId":           strings.TrimSpace(request.TeamID),
			"totalCandidates":  totalCandidates,
			"candidateCounts":  cloneHistoryIntMap(bundle.CandidateCounts),
			"selectedCounts":   cloneHistoryIntMap(bundle.SelectedCounts),
			"disclosedLayers":  append([]string(nil), bundle.DisclosedLayers...),
			"stopReason":       strings.TrimSpace(bundle.StopReason),
			"snapshotId":       strings.TrimSpace(bundle.SnapshotID),
			"stableChars":      len(strings.TrimSpace(bundle.StablePrompt)),
			"sessionChars":     len(strings.TrimSpace(bundle.SessionPrompt)),
			"observationChars": len(strings.TrimSpace(bundle.ObservationPrompt)),
			"hybrid":           hybrid,
			"maxChars":         request.MaxChars,
			"topFacts":         request.TopFacts,
			"topObs":           request.TopObs,
		},
	}
	if err := s.recordHistoryLocked(contextEvent); err != nil {
		return err
	}
	reasons := recallReasonsByItem(bundle.Decisions)
	recordLayer := func(layer string, items []api.StoredMemoryResponse) error {
		for idx, item := range items {
			event := historyEventFromMemory(item, "recall.selected", source)
			event.ChatID = strings.TrimSpace(request.ChatID)
			event.UserKey = strings.TrimSpace(request.UserKey)
			event.Meta = map[string]any{
				"layer":  layer,
				"rank":   idx + 1,
				"reason": strings.TrimSpace(reasons[item.ID]),
				"query":  strings.TrimSpace(request.Query),
			}
			if err := s.recordHistoryLocked(event); err != nil {
				return err
			}
		}
		return nil
	}
	if err := recordLayer(string(LayerStable), bundle.StableFacts); err != nil {
		return err
	}
	if err := recordLayer(string(LayerSession), bundle.SessionSummaries); err != nil {
		return err
	}
	return recordLayer(string(LayerObservation), bundle.RelevantObservations)
}

func recallReasonsByItem(decisions []DisclosureDecision) map[string]string {
	out := map[string]string{}
	for _, decision := range decisions {
		for _, id := range decision.ItemIDs {
			if strings.TrimSpace(id) == "" {
				continue
			}
			out[strings.TrimSpace(id)] = strings.TrimSpace(decision.Reason)
		}
	}
	return out
}

func cloneHistoryIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (s *SQLiteStore) recordHistoryLocked(event HistoryEvent) error {
	if strings.TrimSpace(event.Operation) == "" {
		return nil
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = generateHistoryID()
	}
	if event.Timestamp == 0 {
		// This is a newly-created local audit record, not an upstream/persisted
		// event being replayed. Capture the audit occurrence once here; nonzero
		// invalid values are never repaired below.
		event.Timestamp = time.Now().UnixMilli()
	}
	if strings.TrimSpace(event.Status) == "" {
		event.Status = HistoryStatusOK
	}
	if err := validateHistoryTimeContract(event, "memory.history.write"); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO MEMORY_HISTORY (ID_, TS_, AGENT_KEY_, CHAT_ID_, RUN_ID_, REQUEST_ID_, USER_KEY_,
			MEMORY_ID_, MEMORY_KIND_, SCOPE_TYPE_, SCOPE_KEY_, OPERATION_, SOURCE_, STATUS_,
			BEFORE_JSON_, AFTER_JSON_, DELTA_JSON_, META_JSON_)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID,
		event.Timestamp,
		strings.TrimSpace(event.AgentKey),
		strings.TrimSpace(event.ChatID),
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(event.RequestID),
		strings.TrimSpace(event.UserKey),
		strings.TrimSpace(event.MemoryID),
		strings.TrimSpace(event.MemoryKind),
		strings.TrimSpace(event.ScopeType),
		strings.TrimSpace(event.ScopeKey),
		strings.TrimSpace(event.Operation),
		strings.TrimSpace(event.Source),
		strings.TrimSpace(event.Status),
		historyJSON(event.Before),
		historyJSON(event.After),
		historyJSON(event.Delta),
		historyJSON(event.Meta),
	)
	return err
}

func scanHistoryRows(rows *sql.Rows) ([]HistoryEvent, error) {
	events := []HistoryEvent{}
	for rows.Next() {
		var event HistoryEvent
		var beforeJSON, afterJSON, deltaJSON, metaJSON string
		if err := rows.Scan(
			&event.ID,
			&event.Timestamp,
			&event.AgentKey,
			&event.ChatID,
			&event.RunID,
			&event.RequestID,
			&event.UserKey,
			&event.MemoryID,
			&event.MemoryKind,
			&event.ScopeType,
			&event.ScopeKey,
			&event.Operation,
			&event.Source,
			&event.Status,
			&beforeJSON,
			&afterJSON,
			&deltaJSON,
			&metaJSON,
		); err != nil {
			return nil, err
		}
		event.Before = decodeHistoryJSON(beforeJSON)
		event.After = decodeHistoryJSON(afterJSON)
		event.Delta = decodeHistoryJSON(deltaJSON)
		event.Meta = decodeHistoryJSON(metaJSON)
		if err := validateHistoryTimeContract(event, "memory.sqlite.history"); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func historyEventFromMemory(item api.StoredMemoryResponse, operation string, source string) HistoryEvent {
	return HistoryEvent{
		AgentKey:   strings.TrimSpace(item.AgentKey),
		ChatID:     strings.TrimSpace(item.ChatID),
		RunID:      strings.TrimSpace(item.RefID),
		RequestID:  strings.TrimSpace(item.RequestID),
		MemoryID:   strings.TrimSpace(item.ID),
		MemoryKind: strings.TrimSpace(item.Kind),
		ScopeType:  strings.TrimSpace(item.ScopeType),
		ScopeKey:   strings.TrimSpace(item.ScopeKey),
		Operation:  strings.TrimSpace(operation),
		Source:     strings.TrimSpace(source),
		Status:     HistoryStatusOK,
	}
}
