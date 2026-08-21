package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var offsetTokenPattern = regexp.MustCompile(`([+-])(\d+)([ywDHMmS])`)

var zoneLessBaseLayouts = []string{
	"2006-01-02",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

type dateTimeAnchor struct {
	instant        time.Time
	displayLoc     *time.Location
	zoneID         string
	source         string
	normalizedBase string
}

func buildDateTimePayload(args map[string]any, now time.Time) (map[string]any, error) {
	anchor, err := resolveDateTimeAnchor(args, now)
	if err != nil {
		return nil, err
	}
	normalizedOffset, err := normalizeDateTimeOffset(stringArg(args, "offset"))
	if err != nil {
		return nil, err
	}
	dateTime, err := applyDateTimeOffset(anchor.instant.In(anchor.displayLoc), normalizedOffset)
	if err != nil {
		return nil, err
	}
	_, offsetSeconds := dateTime.Zone()
	dateTime = dateTime.Truncate(time.Second)
	payload := map[string]any{
		"timezone":       anchor.zoneID,
		"timezoneOffset": utcOffsetOf(offsetSeconds),
		"offset":         normalizedOffset,
		"date":           dateTime.Format("2006-01-02"),
		"weekday":        weekdayOf(dateTime.Weekday()),
		"lunarDate":      lunarDateText(dateTime),
		"time":           dateTime.Format("15:04:05"),
		"iso":            dateTime.Format(time.RFC3339),
		"source":         anchor.source,
	}
	if anchor.normalizedBase != "" {
		payload["base"] = anchor.normalizedBase
	}
	return payload, nil
}

func resolveDateTimeAnchor(args map[string]any, now time.Time) (*dateTimeAnchor, error) {
	rawBase, hasBase, err := dateTimeBaseArg(args)
	if err != nil {
		return nil, err
	}
	if hasBase {
		return parseDateTimeBase(rawBase, stringArg(args, "timezone"), now)
	}
	location, zoneID, err := parseDateTimeZone(stringArg(args, "timezone"), now)
	if err != nil {
		return nil, err
	}
	return &dateTimeAnchor{
		instant:    now,
		displayLoc: location,
		zoneID:     zoneID,
		source:     "system-clock",
	}, nil
}

func dateTimeBaseArg(args map[string]any) (string, bool, error) {
	value, exists := args["base"]
	if !exists {
		return "", false, nil
	}
	raw, ok := value.(string)
	if !ok {
		return "", false, invalidBaseError(value)
	}
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	return raw, true, nil
}

func parseDateTimeBase(raw string, timezoneParam string, now time.Time) (*dateTimeAnchor, error) {
	location, zoneID, err := parseDateTimeZone(timezoneParam, now)
	if err != nil {
		return nil, err
	}
	for _, layout := range zoneLessBaseLayouts {
		parsed, err := time.ParseInLocation(layout, raw, location)
		if err != nil || parsed.Format(layout) != raw {
			continue
		}
		return &dateTimeAnchor{
			instant:        parsed,
			displayLoc:     location,
			zoneID:         zoneID,
			source:         "input-base",
			normalizedBase: parsed.Format(time.RFC3339),
		}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || !rfc3339RoundTripOK(parsed, raw) {
		return nil, invalidBaseError(raw)
	}
	_, inputOffset := parsed.Zone()
	displayLoc, displayZoneID := parsed.Location(), fixedZoneIDOf(inputOffset)
	if strings.TrimSpace(timezoneParam) != "" {
		displayLoc, displayZoneID = location, zoneID
	}
	return &dateTimeAnchor{
		instant:        parsed,
		displayLoc:     displayLoc,
		zoneID:         displayZoneID,
		source:         "input-base",
		normalizedBase: parsed.Format(time.RFC3339),
	}, nil
}

func rfc3339RoundTripOK(parsed time.Time, raw string) bool {
	if parsed.Format(time.RFC3339) == raw {
		return true
	}
	// Go renders a zero offset as Z; accept the equivalent explicit ±00:00 forms.
	_, offset := parsed.Zone()
	if offset != 0 {
		return false
	}
	for _, suffix := range []string{"+00:00", "-00:00"} {
		if prefix, ok := strings.CutSuffix(raw, suffix); ok {
			return prefix == parsed.UTC().Format("2006-01-02T15:04:05")
		}
	}
	return false
}

func fixedZoneIDOf(totalSeconds int) string {
	if totalSeconds == 0 {
		return "Z"
	}
	sign := "+"
	if totalSeconds < 0 {
		sign = "-"
		totalSeconds = -totalSeconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, totalSeconds/3600, (totalSeconds%3600)/60)
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
	if offset == "" || offset == "0" || offset == "+0" || offset == "-0" {
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

func invalidBaseError(raw any) error {
	return fmt.Errorf("Invalid base: %v. Use YYYY-MM-DD, YYYY-MM-DDTHH:mm:ss, YYYY-MM-DD HH:mm:ss, or RFC3339 YYYY-MM-DDTHH:mm:ssZ / YYYY-MM-DDTHH:mm:ss±HH:MM.", raw)
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
