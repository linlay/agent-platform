package memory

import (
	"database/sql"
	"strings"

	"agent-platform/internal/api"
)

// The common column order matches memoryRow.destinations. Each projection adds
// only its own fields; score and link metadata remain explicit outer layers.
const memoryRecordColumns = "ID_, TS_, AGENT_KEY_, SUBJECT_KEY_, KIND_, REF_ID_, SCOPE_TYPE_, SCOPE_KEY_, TITLE_, SOURCE_TYPE_, SUMMARY_, CATEGORY_, IMPORTANCE_, CONFIDENCE_, STATUS_, TAGS_, UPDATED_AT_, ACCESS_COUNT_, LAST_ACCESSED_AT_"

func storedMemoryColumns(alias string) string {
	return qualifyMemoryColumns(memoryRecordColumns+", REQUEST_ID_, CHAT_ID_", alias)
}

func toolMemoryColumns(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return qualifyMemoryColumns(memoryRecordColumns+", EMBEDDING_MODEL_", alias) + ", CASE WHEN " + prefix + "EMBEDDING_ IS NULL THEN 0 ELSE 1 END"
}

func qualifyMemoryColumns(columns, alias string) string {
	if alias == "" {
		return columns
	}
	return alias + "." + strings.ReplaceAll(columns, ", ", ", "+alias+".")
}

type memoryRow struct {
	item api.StoredMemoryResponse

	kind, refID, scopeType, scopeKey sql.NullString
	title, status, tags              sql.NullString
	confidence                       sql.NullFloat64
	accessCount, lastAccessedAt      sql.NullInt64
}

func (r *memoryRow) destinations() []any {
	return []any{
		&r.item.ID, &r.item.CreatedAt, &r.item.AgentKey, &r.item.SubjectKey,
		&r.kind, &r.refID, &r.scopeType, &r.scopeKey, &r.title,
		&r.item.SourceType, &r.item.Summary, &r.item.Category, &r.item.Importance,
		&r.confidence, &r.status, &r.tags, &r.item.UpdatedAt, &r.accessCount, &r.lastAccessedAt,
	}
}

func (r *memoryRow) stored() api.StoredMemoryResponse {
	item := r.item
	item.Kind, item.RefID = r.kind.String, r.refID.String
	item.ScopeType, item.ScopeKey = r.scopeType.String, r.scopeKey.String
	item.Title, item.Status = r.title.String, r.status.String
	item.Confidence = r.confidence.Float64
	item.Tags = []string{}
	if r.tags.String != "" {
		item.Tags = strings.Split(r.tags.String, ",")
	}
	item.AccessCount = int(r.accessCount.Int64)
	if r.lastAccessedAt.Valid {
		value := r.lastAccessedAt.Int64
		item.LastAccessedAt = &value
	}
	return item
}

func scanStoredMemory(scanner sqlScanner, location string, extra ...any) (api.StoredMemoryResponse, error) {
	var row memoryRow
	var requestID, chatID sql.NullString
	dest := append(row.destinations(), &requestID, &chatID)
	if err := scanner.Scan(append(dest, extra...)...); err != nil {
		return api.StoredMemoryResponse{}, err
	}
	item := row.stored()
	item.RequestID, item.ChatID = requestID.String, chatID.String
	if err := validateStoredMemoryTimeContract(item, location); err != nil {
		return item, err
	}
	return normalizeStoredItem(item), nil
}

func scanToolRecord(scanner sqlScanner) (ToolRecord, error) {
	return scanToolMemory(scanner, "memory.sqlite.toolRecord")
}

func scanToolRecordWithScore(scanner sqlScanner) (ToolRecord, float64, error) {
	var score float64
	record, err := scanToolMemory(scanner, "memory.sqlite.scoredToolRecord", &score)
	if err != nil {
		return record, 0, err
	}
	return record, score, nil
}

func scanToolMemory(scanner sqlScanner, location string, extra ...any) (ToolRecord, error) {
	var row memoryRow
	var embeddingModel sql.NullString
	var hasEmbedding int
	dest := append(row.destinations(), &embeddingModel, &hasEmbedding)
	if err := scanner.Scan(append(dest, extra...)...); err != nil {
		return ToolRecord{}, err
	}
	item := row.stored()
	if err := validateStoredMemoryTimeContract(item, location); err != nil {
		return ToolRecord{}, err
	}
	// Tool reads retain raw category/importance/tags; stored projections apply
	// normalizeStoredItem. Both share nullable fields and timestamp validation.
	record := toolRecordFromStored(item)
	record.Tags = item.Tags
	record.HasEmbedding = hasEmbedding != 0
	if embeddingModel.Valid {
		value := embeddingModel.String
		record.EmbeddingModel = &value
	}
	return record, nil
}
