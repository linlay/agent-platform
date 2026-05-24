package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
)

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
	validation := validateScopeRecords(records)
	if len(validation.Errors) > 0 {
		errors = append(errors, validation.Errors...)
	}
	return ScopeValidationResult{
		Valid:   len(errors) == 0,
		Errors:  errors,
		Records: validation.Records,
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
	var errors []ScopeValidationIssue
	if strings.TrimSpace(entry.Title) == "" {
		errors = append(errors, ScopeValidationIssue{Field: "title", Message: "title is required"})
	}
	if strings.TrimSpace(entry.Summary) == "" {
		errors = append(errors, ScopeValidationIssue{Field: "summary", Message: "summary is required"})
	}
	if entry.Importance < 1 || entry.Importance > 10 {
		errors = append(errors, ScopeValidationIssue{Field: "importance", Message: "importance must be between 1 and 10"})
	}
	if entry.Confidence <= 0 || entry.Confidence > 1 {
		errors = append(errors, ScopeValidationIssue{Field: "confidence", Message: "confidence must be between 0 and 1"})
	}
	return errors
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
