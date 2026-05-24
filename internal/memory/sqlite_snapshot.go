package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"agent-platform/internal/api"

	_ "modernc.org/sqlite"
)

func (s *SQLiteStore) BuildContextBundle(request ContextRequest) (ContextBundle, error) {
	runtime := s.runtimeForAgent(request.AgentKey)
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.listProjectionItemsLocked(strings.TrimSpace(request.AgentKey))
	if err != nil {
		return ContextBundle{}, err
	}
	var snapshot *MemorySnapshot
	if request.FreezeStable {
		snapshot, err = s.loadMemorySnapshotLocked(request.AgentKey, request.ChatID)
		if err != nil {
			return ContextBundle{}, err
		}
		if snapshot != nil {
			items = pinStableSnapshotItems(items, snapshot.StableItemIDs)
		}
	}

	hp := hybridParams{
		vectorWeight: s.ftsVectorWeight,
		ftsWeight:    s.ftsFTSWeight,
	}
	query := strings.TrimSpace(request.Query)
	if runtime.Embedder != nil && query != "" {
		if qvec, err := runtime.Embedder.EmbedSingle(context.Background(), query); err == nil {
			hp.queryEmbedding = qvec
			hp.itemEmbeddings = s.loadEmbeddingsLocked(items)
		}
	}

	bundle := buildContextBundleWithHybrid(request, items, hp)
	if request.FreezeStable && strings.TrimSpace(request.ChatID) != "" {
		hadSnapshot := snapshot != nil
		if snapshot == nil {
			snapshot = &MemorySnapshot{
				ID:              bundle.SnapshotID,
				ChatID:          strings.TrimSpace(request.ChatID),
				AgentKey:        strings.TrimSpace(request.AgentKey),
				StableItemIDs:   memoryIDs(bundle.StableFacts),
				ObservedItemIDs: memoryIDs(bundle.RelevantObservations),
			}
			if !request.PreviewOnly {
				if err := s.saveMemorySnapshotLocked(*snapshot); err != nil {
					return ContextBundle{}, err
				}
			}
		}
		if snapshot != nil {
			bundle.SnapshotID = snapshot.ID
			if !request.PreviewOnly || hadSnapshot {
				markStableSnapshotPinned(&bundle, snapshot.StableItemIDs)
			}
		}
	}
	logMemoryOperation("build_context_bundle", map[string]any{
		"agentKey":         request.AgentKey,
		"teamId":           request.TeamID,
		"chatId":           request.ChatID,
		"userKey":          request.UserKey,
		"query":            request.Query,
		"totalCandidates":  len(items),
		"stableFacts":      len(bundle.StableFacts),
		"sessionItems":     len(bundle.SessionSummaries),
		"observations":     len(bundle.RelevantObservations),
		"stableChars":      len(bundle.StablePrompt),
		"sessionChars":     len(bundle.SessionPrompt),
		"observationChars": len(bundle.ObservationPrompt),
		"layers":           bundle.DisclosedLayers,
		"stopReason":       bundle.StopReason,
		"hybrid":           len(hp.queryEmbedding) > 0,
		"maxChars":         request.MaxChars,
	})
	_ = s.recordRecallHistoryLocked(request, bundle, len(items), len(hp.queryEmbedding) > 0)
	return bundle, nil
}

func (s *SQLiteStore) loadMemorySnapshotLocked(agentKey string, chatID string) (*MemorySnapshot, error) {
	agentKey = strings.TrimSpace(agentKey)
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, nil
	}
	var snapshot MemorySnapshot
	var stableJSON string
	var observedJSON string
	err := s.db.QueryRow(
		`SELECT ID_, CHAT_ID_, AGENT_KEY_, STABLE_ITEM_IDS_, OBSERVED_ITEM_IDS_
		FROM MEMORY_SNAPSHOTS
		WHERE CHAT_ID_ = ? AND AGENT_KEY_ = ?`,
		chatID, agentKey,
	).Scan(&snapshot.ID, &snapshot.ChatID, &snapshot.AgentKey, &stableJSON, &observedJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot.StableItemIDs = decodeStringList(stableJSON)
	snapshot.ObservedItemIDs = decodeStringList(observedJSON)
	return &snapshot, nil
}

func (s *SQLiteStore) saveMemorySnapshotLocked(snapshot MemorySnapshot) error {
	if strings.TrimSpace(snapshot.ChatID) == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	stableJSON, err := json.Marshal(snapshot.StableItemIDs)
	if err != nil {
		return err
	}
	observedJSON, err := json.Marshal(snapshot.ObservedItemIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO MEMORY_SNAPSHOTS (ID_, CHAT_ID_, AGENT_KEY_, STABLE_ITEM_IDS_, OBSERVED_ITEM_IDS_, CREATED_AT_, UPDATED_AT_)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(CHAT_ID_, AGENT_KEY_) DO NOTHING`,
		strings.TrimSpace(snapshot.ID),
		strings.TrimSpace(snapshot.ChatID),
		strings.TrimSpace(snapshot.AgentKey),
		string(stableJSON),
		string(observedJSON),
		now,
		now,
	)
	return err
}

func pinStableSnapshotItems(items []api.StoredMemoryResponse, stableIDs []string) []api.StoredMemoryResponse {
	pinned := make(map[string]struct{}, len(stableIDs))
	for _, id := range stableIDs {
		if strings.TrimSpace(id) != "" {
			pinned[strings.TrimSpace(id)] = struct{}{}
		}
	}
	out := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		if item.Kind == KindFact {
			if _, ok := pinned[item.ID]; !ok {
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func markStableSnapshotPinned(bundle *ContextBundle, stableIDs []string) {
	if bundle == nil || len(stableIDs) == 0 {
		return
	}
	pinned := make(map[string]struct{}, len(stableIDs))
	for _, id := range stableIDs {
		pinned[strings.TrimSpace(id)] = struct{}{}
	}
	for i, decision := range bundle.Decisions {
		if decision.Layer != LayerStable {
			continue
		}
		for _, id := range decision.ItemIDs {
			if _, ok := pinned[strings.TrimSpace(id)]; ok {
				bundle.Decisions[i].Reason = string(SelectionReasonSnapshotPin)
				for traceIdx := range bundle.Decisions[i].Traces {
					bundle.Decisions[i].Traces[traceIdx].Reason = string(SelectionReasonSnapshotPin)
				}
				return
			}
		}
	}
}

func decodeStringList(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &values); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func (s *SQLiteStore) loadEmbeddingsLocked(items []api.StoredMemoryResponse) map[string][]float64 {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if normalizeMemoryKind(item.Kind) == KindObservation {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.Query(
		`SELECT ID_, EMBEDDING_ FROM MEMORIES WHERE ID_ IN (`+strings.Join(placeholders, ",")+`) AND EMBEDDING_ IS NOT NULL`,
		args...,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string][]float64)
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			continue
		}
		var vec []float64
		if err := json.Unmarshal(blob, &vec); err != nil {
			continue
		}
		result[id] = vec
	}
	return result
}

func (s *SQLiteStore) listProjectionItemsLocked(agentKey string) ([]api.StoredMemoryResponse, error) {
	rows, err := s.db.Query(
		`SELECT ID_, TS_, REQUEST_ID_, CHAT_ID_, AGENT_KEY_, SUBJECT_KEY_,
			KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_,
			SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_,
			UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_
		FROM MEMORIES
		WHERE (? = '' OR AGENT_KEY_ = ? OR AGENT_KEY_ = '')
		ORDER BY UPDATED_AT_ DESC, IMPORTANCE_ DESC`,
		agentKey, agentKey,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]api.StoredMemoryResponse, 0)
	for rows.Next() {
		item, err := scanMemoryRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) refreshSnapshotsLocked(agentKey string) error {
	items, err := s.listProjectionItemsLocked(strings.TrimSpace(agentKey))
	if err != nil {
		return err
	}
	return refreshSnapshots(s.root, agentKey, items)
}
