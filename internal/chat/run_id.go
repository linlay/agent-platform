package chat

import (
	"strconv"
	"strings"
	"time"
)

const legacyRunIDLayout = "20060102150405.000000000"

func NewRunID() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 36)
}

func ParseRunIDMillis(runID string) (int64, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0, false
	}
	if millis, err := strconv.ParseInt(runID, 36, 64); err == nil {
		return millis, true
	}
	if !strings.HasPrefix(runID, "run_") {
		return 0, false
	}
	parsed, err := time.Parse(legacyRunIDLayout, strings.TrimPrefix(runID, "run_"))
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

func RunIDAfter(runID string, cursor string) bool {
	runMillis, runOK := ParseRunIDMillis(runID)
	cursorMillis, cursorOK := ParseRunIDMillis(cursor)
	switch {
	case runOK && cursorOK:
		if runMillis != cursorMillis {
			return runMillis > cursorMillis
		}
		return strings.Compare(runID, cursor) > 0
	case runOK != cursorOK:
		return strings.Compare(runID, cursor) > 0
	default:
		return strings.Compare(runID, cursor) > 0
	}
}
