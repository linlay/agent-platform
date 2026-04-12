package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/memory"
)

var offsetTokenPattern = regexp.MustCompile(`([+-])(\d+)([ywDHMmS])`)

func buildDateTimePayload(args map[string]any, now time.Time) (map[string]any, error) {
	location, zoneID, err := parseDateTimeZone(stringArg(args, "timezone"), now)
	if err != nil {
		return nil, err
	}
	normalizedOffset, err := normalizeDateTimeOffset(stringArg(args, "offset"))
	if err != nil {
		return nil, err
	}
	dateTime, err := applyDateTimeOffset(now.In(location), normalizedOffset)
	if err != nil {
		return nil, err
	}
	_, offsetSeconds := dateTime.Zone()
	dateTime = dateTime.Truncate(time.Second)
	return map[string]any{
		"timezone":       zoneID,
		"timezoneOffset": utcOffsetOf(offsetSeconds),
		"offset":         normalizedOffset,
		"date":           dateTime.Format("2006-01-02"),
		"weekday":        weekdayOf(dateTime.Weekday()),
		"lunarDate":      lunarDateText(dateTime),
		"time":           dateTime.Format("15:04:05"),
		"iso":            dateTime.Format(time.RFC3339),
		"source":         "system-clock",
	}, nil
}

func parseDateTimeZone(raw string, now time.Time) (*time.Location, string, error) {
	timezone := strings.TrimSpace(raw)
	if timezone == "" {
		location := now.Location()
		if location == nil {
			location = time.Local
		}
		return location, location.String(), nil
	}

	normalized := strings.ToUpper(timezone)
	if normalized == "Z" || normalized == "UTC" || normalized == "GMT" {
		return time.FixedZone("Z", 0), "Z", nil
	}
	if strings.HasPrefix(normalized, "UTC") || strings.HasPrefix(normalized, "GMT") || strings.HasPrefix(normalized, "+") || strings.HasPrefix(normalized, "-") {
		offsetValue := normalized
		if strings.HasPrefix(offsetValue, "UTC") || strings.HasPrefix(offsetValue, "GMT") {
			offsetValue = offsetValue[3:]
		}
		offsetValue = normalizeDateTimeTimezoneOffset(offsetValue)
		seconds, err := parseOffsetSeconds(offsetValue)
		if err != nil {
			return nil, "", invalidTimezoneError(timezone)
		}
		return time.FixedZone(offsetValue, seconds), offsetValue, nil
	}

	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, "", invalidTimezoneError(timezone)
	}
	return location, timezone, nil
}

func normalizeDateTimeTimezoneOffset(offset string) string {
	if matched, _ := regexp.MatchString(`^[+-]\d{1,2}$`, offset); matched {
		sign := offset[:1]
		hours, _ := strconv.Atoi(offset[1:])
		return fmt.Sprintf("%s%02d:00", sign, hours)
	}
	if matched, _ := regexp.MatchString(`^[+-]\d{1,2}:\d{2}$`, offset); matched {
		sign := offset[:1]
		parts := strings.Split(offset[1:], ":")
		hours, _ := strconv.Atoi(parts[0])
		return fmt.Sprintf("%s%02d:%s", sign, hours, parts[1])
	}
	return offset
}

func parseOffsetSeconds(offset string) (int, error) {
	if offset == "Z" {
		return 0, nil
	}
	sign := 1
	if strings.HasPrefix(offset, "-") {
		sign = -1
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(offset, "+"), "-")
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid offset")
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}
	return sign * (hours*3600 + minutes*60), nil
}

func normalizeDateTimeOffset(raw string) (string, error) {
	offset := strings.TrimSpace(raw)
	if offset == "" || offset == "0" {
		return "0", nil
	}
	compact := strings.ReplaceAll(offset, " ", "")
	matches := offsetTokenPattern.FindAllStringSubmatchIndex(compact, -1)
	if len(matches) == 0 {
		return "", invalidOffsetError(offset)
	}
	var builder strings.Builder
	index := 0
	for _, match := range matches {
		if match[0] != index {
			return "", invalidOffsetError(offset)
		}
		builder.WriteString(compact[match[2]:match[3]])
		builder.WriteString(compact[match[4]:match[5]])
		builder.WriteString(compact[match[6]:match[7]])
		index = match[1]
	}
	if index != len(compact) {
		return "", invalidOffsetError(offset)
	}
	return builder.String(), nil
}

func applyDateTimeOffset(dateTime time.Time, normalizedOffset string) (time.Time, error) {
	if normalizedOffset == "0" {
		return dateTime, nil
	}
	matches := offsetTokenPattern.FindAllStringSubmatch(normalizedOffset, -1)
	result := dateTime
	for _, match := range matches {
		amount, err := strconv.Atoi(match[2])
		if err != nil {
			return time.Time{}, invalidOffsetError(normalizedOffset)
		}
		if match[1] == "-" {
			amount = -amount
		}
		switch match[3] {
		case "y":
			result = result.AddDate(amount, 0, 0)
		case "w":
			result = result.AddDate(0, 0, amount*7)
		case "D":
			result = result.AddDate(0, 0, amount)
		case "H":
			result = result.Add(time.Duration(amount) * time.Hour)
		case "M":
			result = result.AddDate(0, amount, 0)
		case "m":
			result = result.Add(time.Duration(amount) * time.Minute)
		case "S":
			result = result.Add(time.Duration(amount) * time.Second)
		default:
			return time.Time{}, invalidOffsetError(normalizedOffset)
		}
	}
	return result, nil
}

func invalidTimezoneError(raw string) error {
	return fmt.Errorf("Invalid timezone: %s. Use an IANA zone like Asia/Shanghai or an offset like UTC+8/+08:00/Z.", raw)
}

func invalidOffsetError(raw string) error {
	return fmt.Errorf("Invalid offset: %s. Use tokens like +1D, -2y, +3w or chained forms like +1D-3H+20m or +10M+25D.", raw)
}

func utcOffsetOf(totalSeconds int) string {
	if totalSeconds == 0 {
		return "UTC+0"
	}
	sign := "+"
	if totalSeconds < 0 {
		sign = "-"
		totalSeconds = -totalSeconds
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	if minutes == 0 {
		return fmt.Sprintf("UTC%s%d", sign, hours)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, hours, minutes)
}

func weekdayOf(day time.Weekday) string {
	switch day {
	case time.Monday:
		return "星期一"
	case time.Tuesday:
		return "星期二"
	case time.Wednesday:
		return "星期三"
	case time.Thursday:
		return "星期四"
	case time.Friday:
		return "星期五"
	case time.Saturday:
		return "星期六"
	default:
		return "星期日"
	}
}

func requireMemoryToolContext(execCtx *ExecutionContext, toolName string) (string, string, string, *ToolExecutionResult) {
	if execCtx == nil || strings.TrimSpace(execCtx.Session.AgentKey) == "" {
		return "", "", "", &ToolExecutionResult{
			Output:   toolName + " requires an active agent execution context",
			Error:    "memory_context_required",
			ExitCode: -1,
		}
	}
	requestID := strings.TrimSpace(execCtx.Request.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(execCtx.Session.RequestID)
	}
	chatID := strings.TrimSpace(execCtx.Request.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(execCtx.Session.ChatID)
	}
	return strings.TrimSpace(execCtx.Session.AgentKey), requestID, chatID, nil
}

func memoryToolLimit(limit int, fallback int) int {
	return clampMemoryLimit(limit, fallback)
}

func clampMemoryLimit(limit int, fallback int) int {
	if fallback <= 0 {
		fallback = 10
	}
	if limit <= 0 {
		limit = fallback
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeMemoryCategory(category string) string {
	if strings.TrimSpace(category) == "" {
		return "general"
	}
	return strings.ToLower(strings.TrimSpace(category))
}

func normalizeMemorySourceType(sourceType string) string {
	if strings.TrimSpace(sourceType) == "" {
		return "tool-write"
	}
	return strings.ToLower(strings.TrimSpace(sourceType))
}

func normalizeMemoryImportance(importance int) int {
	if importance <= 0 {
		importance = 5
	}
	if importance < 1 {
		return 1
	}
	if importance > 10 {
		return 10
	}
	return importance
}

func normalizeMemorySubjectKey(subjectKey string, chatID string, agentKey string) string {
	if strings.TrimSpace(subjectKey) != "" {
		return strings.TrimSpace(subjectKey)
	}
	if strings.TrimSpace(chatID) != "" {
		return "chat:" + strings.TrimSpace(chatID)
	}
	if strings.TrimSpace(agentKey) != "" {
		return "agent:" + strings.TrimSpace(agentKey)
	}
	return "_global"
}

func normalizeMemoryTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func stringListArg(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return []string{}
	}
	switch value := raw.(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func memoryToolRecordValue(record memory.ToolRecord) map[string]any {
	value := map[string]any{
		"id":             record.ID,
		"agentKey":       record.AgentKey,
		"subjectKey":     record.SubjectKey,
		"content":        record.Content,
		"sourceType":     record.SourceType,
		"category":       record.Category,
		"importance":     record.Importance,
		"tags":           append([]string(nil), record.Tags...),
		"hasEmbedding":   record.HasEmbedding,
		"embeddingModel": record.EmbeddingModel,
		"createdAt":      record.CreatedAt,
		"updatedAt":      record.UpdatedAt,
		"accessCount":    record.AccessCount,
		"lastAccessedAt": record.LastAccessedAt,
	}
	return value
}
