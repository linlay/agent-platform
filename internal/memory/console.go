package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"agent-platform/internal/api"
)

type ConsoleReader interface {
	ListAll(agentKey string) ([]api.StoredMemoryResponse, error)
	ReadConsoleDetail(agentKey string, id string) (ConsoleRecordDetail, error)
}

type ConsoleRecordDetail struct {
	Record         api.StoredMemoryResponse
	SourceTable    string
	RawFields      map[string]any
	HasEmbedding   bool
	EmbeddingModel *string
}

type ScopeView struct {
	AgentKey  string
	ScopeType string
	ScopeKey  string
	Label     string
	FileName  string
	Markdown  string
	Records   []api.StoredMemoryResponse
	UpdatedAt int64
}

type ScopeRecordInput struct {
	ID         string
	Title      string
	Summary    string
	Category   string
	Importance int
	Confidence float64
	Tags       []string
}

type ScopeSaveInput struct {
	AgentKey       string
	ScopeType      string
	ScopeKey       string
	UserKey        string
	TeamID         string
	Mode           string
	Markdown       string
	Records        []ScopeRecordInput
	ArchiveMissing bool
}

type ScopeSaveSummary struct {
	Created   int
	Updated   int
	Archived  int
	Unchanged int
}

type ScopeSaveResult struct {
	Summary  ScopeSaveSummary
	Records  []api.StoredMemoryResponse
	Markdown string
}

type ScopeValidationIssue struct {
	Line    int
	Field   string
	Message string
}

type ScopeValidationResult struct {
	Valid    bool
	Errors   []ScopeValidationIssue
	Warnings []ScopeValidationIssue
	Records  []ScopeRecordInput
}

type RecordFilter struct {
	AgentKey  string
	Kind      string
	ScopeType string
	Status    string
	Category  string
	ChatID    string
	Keyword   string
	Limit     int
	Cursor    string
}

type RecordListResult struct {
	Count      int
	NextCursor string
	Results    []api.StoredMemoryResponse
}

func BuildScopeSummaries(store Store, agentKey string, userKey string, teamID string) ([]ScopeView, error) {
	reader, ok := store.(ConsoleReader)
	if !ok {
		return nil, fmt.Errorf("memory console reader not configured")
	}
	items, err := reader.ListAll(strings.TrimSpace(agentKey))
	if err != nil {
		return nil, err
	}
	views := make([]ScopeView, 0, 4)
	for _, scopeType := range []string{ScopeUser, ScopeAgent, ScopeTeam, ScopeGlobal} {
		scopeKey := primaryScopeKey(scopeType, agentKey, teamID, userKey, items)
		records := activeFactsForScope(items, scopeType, scopeKey)
		views = append(views, buildScopeView(agentKey, scopeType, scopeKey, records))
	}
	return views, nil
}

func BuildScopeView(store Store, agentKey string, scopeType string, scopeKey string, userKey string, teamID string) (ScopeView, error) {
	reader, ok := store.(ConsoleReader)
	if !ok {
		return ScopeView{}, fmt.Errorf("memory console reader not configured")
	}
	agentKey = strings.TrimSpace(agentKey)
	items, err := reader.ListAll(agentKey)
	if err != nil {
		return ScopeView{}, err
	}
	scopeType = normalizeEditableScopeType(scopeType)
	if scopeType == "" {
		return ScopeView{}, fmt.Errorf("scopeType must be user, agent, team, or global")
	}
	if strings.TrimSpace(scopeKey) == "" {
		scopeKey = primaryScopeKey(scopeType, agentKey, teamID, userKey, items)
	}
	return buildScopeView(agentKey, scopeType, scopeKey, activeFactsForScope(items, scopeType, scopeKey)), nil
}

func ValidateScopeMarkdown(scopeType string, markdown string) ScopeValidationResult {
	scopeType = normalizeEditableScopeType(scopeType)
	if scopeType == "" {
		return ScopeValidationResult{
			Valid:  false,
			Errors: []ScopeValidationIssue{{Field: "scopeType", Message: "scopeType must be user, agent, team, or global"}},
		}
	}
	return parseScopeMarkdown(markdown)
}

func SaveScope(store Store, input ScopeSaveInput) (ScopeSaveResult, error) {
	scopeType := normalizeEditableScopeType(input.ScopeType)
	if scopeType == "" {
		return ScopeSaveResult{}, fmt.Errorf("scopeType must be user, agent, team, or global")
	}
	agentKey := strings.TrimSpace(input.AgentKey)
	if agentKey == "" {
		return ScopeSaveResult{}, fmt.Errorf("agentKey is required")
	}
	reader, ok := store.(ConsoleReader)
	if !ok {
		return ScopeSaveResult{}, fmt.Errorf("memory console reader not configured")
	}
	mutator, ok := store.(Mutator)
	if !ok {
		return ScopeSaveResult{}, fmt.Errorf("memory mutator not configured")
	}
	allItems, err := reader.ListAll(agentKey)
	if err != nil {
		return ScopeSaveResult{}, err
	}
	scopeKey := strings.TrimSpace(input.ScopeKey)
	if scopeKey == "" {
		scopeKey = primaryScopeKey(scopeType, agentKey, input.TeamID, input.UserKey, allItems)
	}

	var desired []ScopeRecordInput
	switch strings.ToLower(strings.TrimSpace(input.Mode)) {
	case "markdown":
		validation := parseScopeMarkdown(input.Markdown)
		if !validation.Valid {
			if len(validation.Errors) > 0 {
				return ScopeSaveResult{}, errors.New(validation.Errors[0].Message)
			}
			return ScopeSaveResult{}, fmt.Errorf("invalid markdown payload")
		}
		desired = validation.Records
	case "records":
		validation := validateScopeRecords(input.Records)
		if !validation.Valid {
			if len(validation.Errors) > 0 {
				return ScopeSaveResult{}, errors.New(validation.Errors[0].Message)
			}
			return ScopeSaveResult{}, fmt.Errorf("invalid records payload")
		}
		desired = validation.Records
	default:
		return ScopeSaveResult{}, fmt.Errorf("mode must be markdown or records")
	}

	currentScopeRecords := activeFactsForScope(allItems, scopeType, scopeKey)

	summary := ScopeSaveSummary{}
	saved := make([]api.StoredMemoryResponse, 0, len(desired))
	desiredCurrentIDs := map[string]struct{}{}
	for _, record := range desired {
		normalized, result, err := saveScopeRecord(store, reader, mutator, agentKey, scopeType, scopeKey, record)
		if err != nil {
			return ScopeSaveResult{}, err
		}
		switch result {
		case "created":
			summary.Created++
		case "updated":
			summary.Updated++
		default:
			summary.Unchanged++
		}
		if strings.TrimSpace(normalized.ID) != "" {
			desiredCurrentIDs[strings.TrimSpace(normalized.ID)] = struct{}{}
		}
		saved = append(saved, normalized)
	}

	if input.ArchiveMissing {
		status := StatusArchived
		for _, existing := range currentScopeRecords {
			if _, ok := desiredCurrentIDs[strings.TrimSpace(existing.ID)]; ok {
				continue
			}
			record, err := mutator.Update(agentKey, MutationInput{ID: existing.ID, Status: &status})
			if err != nil {
				return ScopeSaveResult{}, err
			}
			if record != nil {
				summary.Archived++
			}
		}
	}

	view, err := BuildScopeView(store, agentKey, scopeType, scopeKey, input.UserKey, input.TeamID)
	if err != nil {
		return ScopeSaveResult{}, err
	}
	return ScopeSaveResult{
		Summary:  summary,
		Records:  view.Records,
		Markdown: view.Markdown,
	}, nil
}

func ListConsoleRecords(store Store, filter RecordFilter) (RecordListResult, error) {
	reader, ok := store.(ConsoleReader)
	if !ok {
		return RecordListResult{}, fmt.Errorf("memory console reader not configured")
	}
	items, err := reader.ListAll(strings.TrimSpace(filter.AgentKey))
	if err != nil {
		return RecordListResult{}, err
	}
	filtered := make([]api.StoredMemoryResponse, 0, len(items))
	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if !recordMatchesFilter(item, filter) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt != filtered[j].UpdatedAt {
			return filtered[i].UpdatedAt > filtered[j].UpdatedAt
		}
		if filtered[i].CreatedAt != filtered[j].CreatedAt {
			return filtered[i].CreatedAt > filtered[j].CreatedAt
		}
		return filtered[i].ID > filtered[j].ID
	})
	start := 0
	if cursor := strings.TrimSpace(filter.Cursor); cursor != "" {
		for idx, item := range filtered {
			if strings.TrimSpace(item.ID) == cursor {
				start = idx + 1
				break
			}
		}
	}
	limit := normalizeLimit(filter.Limit, 20)
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := append([]api.StoredMemoryResponse(nil), filtered[start:end]...)
	nextCursor := ""
	if end < len(filtered) && len(page) > 0 {
		nextCursor = page[len(page)-1].ID
	}
	return RecordListResult{
		Count:      len(filtered),
		NextCursor: nextCursor,
		Results:    page,
	}, nil
}

func ReadConsoleRecord(store Store, agentKey string, id string) (ConsoleRecordDetail, error) {
	reader, ok := store.(ConsoleReader)
	if !ok {
		return ConsoleRecordDetail{}, fmt.Errorf("memory console reader not configured")
	}
	return reader.ReadConsoleDetail(strings.TrimSpace(agentKey), strings.TrimSpace(id))
}

func (s *SQLiteStore) ListAll(agentKey string) ([]api.StoredMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listProjectionItemsLocked(strings.TrimSpace(agentKey))
}

func (s *FileStore) ListAll(agentKey string) ([]api.StoredMemoryResponse, error) {
	items, err := s.readAllStored()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentKey) == "" {
		return items, nil
	}
	filtered := make([]api.StoredMemoryResponse, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.AgentKey) == strings.TrimSpace(agentKey) {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (s *SQLiteStore) ReadConsoleDetail(agentKey string, id string) (ConsoleRecordDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.readProjectionByIDLocked(strings.TrimSpace(id))
	if err != nil {
		return ConsoleRecordDetail{}, err
	}
	if record == nil {
		return ConsoleRecordDetail{}, nil
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
		return ConsoleRecordDetail{}, nil
	}
	detail := ConsoleRecordDetail{
		Record:      *record,
		SourceTable: sourceTableForKind(record.Kind),
	}

	row := s.db.QueryRow(`SELECT EMBEDDING_MODEL_, CASE WHEN EMBEDDING_ IS NULL THEN 0 ELSE 1 END FROM MEMORIES WHERE ID_ = ?`, record.ID)
	var model sql.NullString
	var hasEmbedding int
	if err := row.Scan(&model, &hasEmbedding); err == nil {
		if model.Valid {
			value := model.String
			detail.EmbeddingModel = &value
		}
		detail.HasEmbedding = hasEmbedding != 0
	}

	rawFields, err := s.readSourceFieldsLocked(*record)
	if err != nil {
		return ConsoleRecordDetail{}, err
	}
	detail.RawFields = rawFields
	return detail, nil
}

func (s *FileStore) ReadConsoleDetail(agentKey string, id string) (ConsoleRecordDetail, error) {
	record, err := s.Read(strings.TrimSpace(id))
	if err != nil || record == nil {
		return ConsoleRecordDetail{}, err
	}
	if strings.TrimSpace(agentKey) != "" && strings.TrimSpace(record.AgentKey) != strings.TrimSpace(agentKey) {
		return ConsoleRecordDetail{}, nil
	}
	return ConsoleRecordDetail{
		Record:      normalizeStoredItem(*record),
		SourceTable: sourceTableForKind(record.Kind),
		RawFields:   map[string]any{},
	}, nil
}
