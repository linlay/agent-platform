package server

import (
	"encoding/json"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestFeedbackResponseOmitsClearedSetAtAndRejectsInvalidValue(t *testing.T) {
	cleared, err := feedbackResponse("chat-1", "run-1", "clear", 0)
	if err != nil {
		t.Fatalf("clear feedback response: %v", err)
	}
	encoded, err := json.Marshal(cleared)
	if err != nil {
		t.Fatalf("marshal clear response: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("decode clear response: %v", err)
	}
	if _, exists := raw["setAt"]; exists {
		t.Fatalf("clear feedback must omit setAt, got %s", encoded)
	}

	setAt := int64(1_700_000_000_000)
	set, err := feedbackResponse("chat-1", "run-1", "thumbs_down", setAt)
	if err != nil || set.SetAt == nil || *set.SetAt != setAt {
		t.Fatalf("expected valid epoch-ms setAt, response=%#v err=%v", set, err)
	}
	if _, err := feedbackResponse("chat-1", "run-1", "thumbs_down", 1_700_000_000); !timecontract.IsViolation(err) {
		t.Fatalf("expected seconds to be rejected, got %v", err)
	}
}
