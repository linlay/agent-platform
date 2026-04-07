package chat

import (
	"strconv"
	"testing"
)

func TestNewRunIDUsesBase36EpochMillis(t *testing.T) {
	runID := NewRunID()
	if runID == "" {
		t.Fatalf("expected non-empty run id")
	}
	parsed, err := strconv.ParseInt(runID, 36, 64)
	if err != nil {
		t.Fatalf("expected base36 run id, got %q: %v", runID, err)
	}
	if parsed <= 0 {
		t.Fatalf("expected positive epoch millis, got %d from %q", parsed, runID)
	}
	if millis, ok := ParseRunIDMillis(runID); !ok || millis != parsed {
		t.Fatalf("expected ParseRunIDMillis to round-trip %q, got millis=%d ok=%v", runID, millis, ok)
	}
}
