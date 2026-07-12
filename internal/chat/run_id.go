package chat

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var lastAllocatedRunIDMillis atomic.Int64

func NewRunID() string {
	// Run IDs retain their base36 epoch-millisecond form, but concurrent or
	// immediately adjacent lifecycle operations must not reuse the same ID.
	// In particular, a derived chat can allocate a copied run in the same
	// millisecond as the next query.  Advance monotonically instead of relying
	// on a timestamp collision being unlikely.
	now := time.Now().UnixMilli()
	for {
		last := lastAllocatedRunIDMillis.Load()
		candidate := now
		if candidate <= last {
			candidate = last + 1
		}
		if lastAllocatedRunIDMillis.CompareAndSwap(last, candidate) {
			return strconv.FormatInt(candidate, 36)
		}
	}
}

func ParseRunIDMillis(runID string) (int64, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0, false
	}
	if millis, err := strconv.ParseInt(runID, 36, 64); err == nil {
		return millis, true
	}
	return 0, false
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
