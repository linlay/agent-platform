package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
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

func ScopeLabel(scopeType string) string {
	return formatScopeLabel(scopeType)
}

func DefaultScopeKey(scopeType string, agentKey string, teamID string, chatID string, userKey string) string {
	return normalizeScopeKey(scopeType, "", agentKey, teamID, chatID, userKey)
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

func buildScopeView(agentKey string, scopeType string, scopeKey string, records []api.StoredMemoryResponse) ScopeView {
	sortScopeRecords(records)
	return ScopeView{
		AgentKey:  strings.TrimSpace(agentKey),
		ScopeType: normalizeScopeType(scopeType),
		ScopeKey:  strings.TrimSpace(scopeKey),
		Label:     formatScopeLabel(scopeType),
		FileName:  scopeFileName(scopeType),
		Markdown:  renderScopeMarkdown(scopeType, records),
		Records:   append([]api.StoredMemoryResponse(nil), records...),
		UpdatedAt: latestUpdatedAt(records),
	}
}

func latestUpdatedAt(records []api.StoredMemoryResponse) int64 {
	var updatedAt int64
	for _, item := range records {
		if item.UpdatedAt > updatedAt {
			updatedAt = item.UpdatedAt
		}
	}
	return updatedAt
}

func scopeFileName(scopeType string) string {
	switch normalizeScopeType(scopeType) {
	case ScopeUser:
		return "USER.md"
	case ScopeTeam:
		return "TEAM.md"
	case ScopeGlobal:
		return "GLOBAL.md"
	default:
		return "AGENT.md"
	}
}

func primaryScopeKey(scopeType string, agentKey string, teamID string, userKey string, items []api.StoredMemoryResponse) string {
	defaultKey := normalizeScopeKey(scopeType, "", agentKey, teamID, "", userKey)
	var selected api.StoredMemoryResponse
	found := false
	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if normalizeMemoryKind(item.Kind) != KindFact || normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
			continue
		}
		if normalizeScopeType(item.ScopeType) != normalizeScopeType(scopeType) {
			continue
		}
		if !found || item.UpdatedAt > selected.UpdatedAt {
			selected = item
			found = true
		}
	}
	if found && strings.TrimSpace(selected.ScopeKey) != "" {
		return strings.TrimSpace(selected.ScopeKey)
	}
	return defaultKey
}

func activeFactsForScope(items []api.StoredMemoryResponse, scopeType string, scopeKey string) []api.StoredMemoryResponse {
	out := make([]api.StoredMemoryResponse, 0)
	for _, raw := range items {
		item := normalizeStoredItem(raw)
		if normalizeMemoryKind(item.Kind) != KindFact {
			continue
		}
		if normalizeMemoryStatus(item.Status, item.Kind) != StatusActive {
			continue
		}
		if normalizeScopeType(item.ScopeType) != normalizeScopeType(scopeType) {
			continue
		}
		if strings.TrimSpace(item.ScopeKey) != strings.TrimSpace(scopeKey) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func sortScopeRecords(records []api.StoredMemoryResponse) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Importance != records[j].Importance {
			return records[i].Importance > records[j].Importance
		}
		if records[i].UpdatedAt != records[j].UpdatedAt {
			return records[i].UpdatedAt > records[j].UpdatedAt
		}
		return records[i].ID > records[j].ID
	})
}

func renderScopeMarkdown(scopeType string, records []api.StoredMemoryResponse) string {
	lines := []string{"# " + formatScopeLabel(scopeType)}
	if len(records) == 0 {
		lines = append(lines, "", "No active memory.")
		return strings.Join(lines, "\n")
	}
	for _, item := range records {
		lines = append(lines, "")
		identifier := strings.TrimSpace(item.ID)
		if identifier == "" {
			identifier = "new"
		}
		lines = append(lines, "- ["+identifier+"] "+strings.TrimSpace(item.Title))
		lines = append(lines, "  category: "+normalizeCategory(item.Category))
		lines = append(lines, "  importance: "+strconv.Itoa(normalizeImportance(item.Importance)))
		lines = append(lines, "  confidence: "+trimFloat(normalizeMemoryConfidence(item.Confidence, item.Kind)))
		lines = append(lines, "  tags: "+strings.Join(normalizeTags(item.Tags), ","))
		lines = append(lines, "  content: "+strings.TrimSpace(item.Summary))
	}
	return strings.Join(lines, "\n")
}

func parseScopeMarkdown(markdown string) ScopeValidationResult {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var records []ScopeRecordInput
	var current *markdownEntry
	var errors []ScopeValidationIssue

	flush := func() {
		if current == nil {
			return
		}
		recordErrors := validateMarkdownEntry(*current)
		errors = append(errors, recordErrors...)
		if len(recordErrors) == 0 {
			records = append(records, current.toInput())
		}
		current = nil
	}

	for idx, line := range lines {
		lineNo := idx + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- [") {
			flush()
			entry, err := parseMarkdownEntryHeader(trimmed)
			if err != nil {
				errors = append(errors, ScopeValidationIssue{Line: lineNo, Field: "entry", Message: err.Error()})
				continue
			}
			current = &entry
			continue
		}
		if current == nil {
			errors = append(errors, ScopeValidationIssue{Line: lineNo, Field: "entry", Message: "expected a list item header like '- [mem_xxx] title'"})
			continue
		}
		if err := current.applyField(trimmed); err != nil {
			errors = append(errors, ScopeValidationIssue{Line: lineNo, Field: "field", Message: err.Error()})
		}
	}
	flush()
	return ScopeValidationResult{
		Valid:   len(errors) == 0,
		Errors:  errors,
		Records: records,
	}
}

func validateScopeRecords(records []ScopeRecordInput) ScopeValidationResult {
	var errors []ScopeValidationIssue
	normalized := make([]ScopeRecordInput, 0, len(records))
	for idx, record := range records {
		raw := record
		if strings.TrimSpace(raw.Title) == "" {
			errors = append(errors, ScopeValidationIssue{Line: idx + 1, Field: "title", Message: "title is required"})
		}
		if strings.TrimSpace(raw.Summary) == "" {
			errors = append(errors, ScopeValidationIssue{Line: idx + 1, Field: "summary", Message: "summary is required"})
		}
		if raw.Importance != 0 && (raw.Importance < 1 || raw.Importance > 10) {
			errors = append(errors, ScopeValidationIssue{Line: idx + 1, Field: "importance", Message: "importance must be between 1 and 10"})
		}
		if raw.Confidence != 0 && (raw.Confidence <= 0 || raw.Confidence > 1) {
			errors = append(errors, ScopeValidationIssue{Line: idx + 1, Field: "confidence", Message: "confidence must be between 0 and 1"})
		}
		normalized = append(normalized, normalizeScopeRecord(record))
	}
	return ScopeValidationResult{
		Valid:   len(errors) == 0,
		Errors:  errors,
		Records: normalized,
	}
}

type markdownEntry struct {
	ID         string
	Title      string
	Category   string
	Importance int
	Confidence float64
	Tags       []string
	Summary    string
}

func parseMarkdownEntryHeader(line string) (markdownEntry, error) {
	closeIdx := strings.Index(line, "]")
	if !strings.HasPrefix(line, "- [") || closeIdx < 3 {
		return markdownEntry{}, fmt.Errorf("invalid entry header")
	}
	identifier := strings.TrimSpace(line[3:closeIdx])
	title := strings.TrimSpace(line[closeIdx+1:])
	if identifier == "" {
		return markdownEntry{}, fmt.Errorf("entry id is required")
	}
	if title == "" {
		return markdownEntry{}, fmt.Errorf("title is required")
	}
	return markdownEntry{
		ID:         identifier,
		Title:      title,
		Category:   "general",
		Importance: 5,
		Confidence: 0.9,
		Tags:       []string{},
	}, nil
}

func (e *markdownEntry) applyField(line string) error {
	parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected 'key: value'")
	}
	key := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.TrimSpace(parts[1])
	switch key {
	case "category":
		e.Category = value
	case "importance":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("importance must be an integer")
		}
		e.Importance = parsed
	case "confidence":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("confidence must be a number")
		}
		e.Confidence = parsed
	case "tags":
		if value == "" {
			e.Tags = []string{}
		} else {
			e.Tags = normalizeTags(strings.Split(value, ","))
		}
	case "content":
		e.Summary = value
	default:
		return fmt.Errorf("unsupported field %q", key)
	}
	return nil
}

func validateMarkdownEntry(entry markdownEntry) []ScopeValidationIssue {
	record := entry.toInput()
	validation := validateScopeRecords([]ScopeRecordInput{record})
	return validation.Errors
}

func (e markdownEntry) toInput() ScopeRecordInput {
	return normalizeScopeRecord(ScopeRecordInput{
		ID:         e.ID,
		Title:      e.Title,
		Summary:    e.Summary,
		Category:   e.Category,
		Importance: e.Importance,
		Confidence: e.Confidence,
		Tags:       e.Tags,
	})
}

func normalizeScopeRecord(record ScopeRecordInput) ScopeRecordInput {
	record.ID = strings.TrimSpace(record.ID)
	record.Title = strings.TrimSpace(record.Title)
	record.Summary = strings.TrimSpace(record.Summary)
	record.Category = normalizeCategory(record.Category)
	record.Importance = normalizeImportance(record.Importance)
	record.Confidence = normalizeMemoryConfidence(record.Confidence, KindFact)
	record.Tags = normalizeTags(record.Tags)
	return record
}

func saveScopeRecord(store Store, reader ConsoleReader, mutator Mutator, agentKey string, scopeType string, scopeKey string, record ScopeRecordInput) (api.StoredMemoryResponse, string, error) {
	record = normalizeScopeRecord(record)
	if strings.TrimSpace(record.ID) != "" && !strings.EqualFold(strings.TrimSpace(record.ID), "new") {
		existing, err := reader.ReadConsoleDetail(agentKey, record.ID)
		if err != nil {
			return api.StoredMemoryResponse{}, "", err
		}
		if strings.TrimSpace(existing.Record.ID) == "" {
			return api.StoredMemoryResponse{}, "", fmt.Errorf("memory not found: %s", record.ID)
		}
		if isScopeRecordUnchanged(existing.Record, record, scopeType, scopeKey) {
			return existing.Record, "unchanged", nil
		}
		status := StatusActive
		title := record.Title
		summary := record.Summary
		category := record.Category
		scopeTypeValue := scopeType
		scopeKeyValue := scopeKey
		importance := record.Importance
		confidence := record.Confidence
		updated, err := mutator.Update(agentKey, MutationInput{
			ID:          existing.Record.ID,
			Title:       &title,
			Summary:     &summary,
			Category:    &category,
			ScopeType:   &scopeTypeValue,
			ScopeKey:    &scopeKeyValue,
			Status:      &status,
			Importance:  &importance,
			Confidence:  &confidence,
			Tags:        record.Tags,
			ReplaceTags: true,
		})
		if err != nil {
			return api.StoredMemoryResponse{}, "", err
		}
		detail, err := reader.ReadConsoleDetail(agentKey, existing.Record.ID)
		if err != nil {
			return api.StoredMemoryResponse{}, "", err
		}
		if updated == nil && strings.TrimSpace(detail.Record.ID) == "" {
			return api.StoredMemoryResponse{}, "", fmt.Errorf("memory not found after update: %s", record.ID)
		}
		return detail.Record, "updated", nil
	}

	now := time.Now().UnixMilli()
	item := api.StoredMemoryResponse{
		ID:         generateMemoryID(),
		AgentKey:   agentKey,
		Kind:       KindFact,
		ScopeType:  scopeType,
		ScopeKey:   scopeKey,
		Title:      record.Title,
		Summary:    record.Summary,
		SourceType: "console-edit",
		Category:   record.Category,
		Importance: record.Importance,
		Confidence: record.Confidence,
		Status:     StatusActive,
		Tags:       record.Tags,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Write(item); err != nil {
		return api.StoredMemoryResponse{}, "", err
	}
	detail, err := reader.ReadConsoleDetail(agentKey, item.ID)
	if err != nil {
		return api.StoredMemoryResponse{}, "", err
	}
	return detail.Record, "created", nil
}

func isScopeRecordUnchanged(existing api.StoredMemoryResponse, desired ScopeRecordInput, scopeType string, scopeKey string) bool {
	existing = normalizeStoredItem(existing)
	return strings.TrimSpace(existing.Title) == strings.TrimSpace(desired.Title) &&
		strings.TrimSpace(existing.Summary) == strings.TrimSpace(desired.Summary) &&
		normalizeCategory(existing.Category) == normalizeCategory(desired.Category) &&
		normalizeImportance(existing.Importance) == normalizeImportance(desired.Importance) &&
		normalizeMemoryConfidence(existing.Confidence, KindFact) == normalizeMemoryConfidence(desired.Confidence, KindFact) &&
		normalizeScopeType(existing.ScopeType) == normalizeScopeType(scopeType) &&
		strings.TrimSpace(existing.ScopeKey) == strings.TrimSpace(scopeKey) &&
		normalizeMemoryStatus(existing.Status, existing.Kind) == StatusActive &&
		stringSlicesEqual(normalizeTags(existing.Tags), normalizeTags(desired.Tags))
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func normalizeEditableScopeType(scopeType string) string {
	switch normalizeScopeType(scopeType) {
	case ScopeUser, ScopeAgent, ScopeTeam, ScopeGlobal:
		return normalizeScopeType(scopeType)
	default:
		return ""
	}
}

func recordMatchesFilter(item api.StoredMemoryResponse, filter RecordFilter) bool {
	if value := strings.TrimSpace(filter.Kind); value != "" && normalizeMemoryKind(item.Kind) != normalizeMemoryKind(value) {
		return false
	}
	if value := normalizeEditableOrChatScopeType(filter.ScopeType); value != "" && normalizeScopeType(item.ScopeType) != value {
		return false
	}
	if value := strings.TrimSpace(filter.Status); value != "" && normalizeMemoryStatus(item.Status, item.Kind) != normalizeMemoryStatus(value, item.Kind) {
		return false
	}
	if value := normalizeOptionalCategory(filter.Category); value != "" && normalizeCategory(item.Category) != value {
		return false
	}
	if value := strings.TrimSpace(filter.ChatID); value != "" && strings.TrimSpace(item.ChatID) != value {
		return false
	}
	if value := strings.TrimSpace(filter.Keyword); value != "" {
		needle := strings.ToLower(value)
		haystack := strings.ToLower(strings.Join([]string{
			item.ID, item.Title, item.Summary, item.Category, item.ScopeType, item.ScopeKey, item.ChatID, item.AgentKey,
		}, " "))
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func normalizeEditableOrChatScopeType(scopeType string) string {
	switch normalizeScopeType(scopeType) {
	case ScopeUser, ScopeAgent, ScopeTeam, ScopeChat, ScopeGlobal:
		return normalizeScopeType(scopeType)
	default:
		return ""
	}
}

func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func sourceTableForKind(kind string) string {
	if normalizeMemoryKind(kind) == KindObservation {
		return "MEMORY_OBSERVATIONS"
	}
	return "MEMORY_FACTS"
}

func (s *SQLiteStore) readSourceFieldsLocked(item api.StoredMemoryResponse) (map[string]any, error) {
	if normalizeMemoryKind(item.Kind) == KindObservation {
		row := s.db.QueryRow(`SELECT RUN_ID_, TYPE_, DETAIL_, FILES_JSON_, TOOLS_JSON_ FROM MEMORY_OBSERVATIONS WHERE ID_ = ?`, item.ID)
		var runID, obsType, detailText, filesJSON, toolsJSON string
		if err := row.Scan(&runID, &obsType, &detailText, &filesJSON, &toolsJSON); err != nil {
			return nil, err
		}
		return map[string]any{
			"runId":     runID,
			"type":      obsType,
			"detail":    detailText,
			"filesJson": decodeJSONList(filesJSON),
			"toolsJson": decodeJSONList(toolsJSON),
		}, nil
	}
	row := s.db.QueryRow(`SELECT SOURCE_KIND_, SOURCE_REF_, DEDUPE_KEY_, LAST_CONFIRMED_AT_, EXPIRES_AT_ FROM MEMORY_FACTS WHERE ID_ = ?`, item.ID)
	var sourceKind, sourceRef, dedupeKey string
	var lastConfirmedAt, expiresAt sql.NullInt64
	if err := row.Scan(&sourceKind, &sourceRef, &dedupeKey, &lastConfirmedAt, &expiresAt); err != nil {
		return nil, err
	}
	var lastConfirmedValue *int64
	var expiresValue *int64
	if lastConfirmedAt.Valid {
		value := lastConfirmedAt.Int64
		lastConfirmedValue = &value
	}
	if expiresAt.Valid {
		value := expiresAt.Int64
		expiresValue = &value
	}
	return map[string]any{
		"sourceKind":      sourceKind,
		"sourceRef":       sourceRef,
		"dedupeKey":       dedupeKey,
		"lastConfirmedAt": lastConfirmedValue,
		"expiresAt":       expiresValue,
	}, nil
}

func decodeJSONList(raw string) []any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []any{raw}
	}
	return out
}
