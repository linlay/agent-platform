package timecontract

import (
	"encoding/json"
	"testing"
)

func TestValidateJSONPayloadRejectsNonIntegerTimeValues(t *testing.T) {
	invalid := []any{
		map[string]any{"timestamp": "1700000000000"},
		map[string]any{"timestamp": 1_700_000_000},
		map[string]any{"timestamp": float64(1_700_000_000_000)},
		map[string]any{"timestamp": 1_700_000_000_000.5},
		map[string]any{"timestamp": json.Number("not-an-integer")},
		map[string]any{"nested": map[string]any{"createdAt": float64(1_700_000_000_000)}},
		map[string]any{"createdUnixMs": "1700000000000"},
		map[string]any{"expiresAt": nil},
		map[string]any{"readAt": 0},
	}
	for _, payload := range invalid {
		if err := ValidateJSONPayload(payload, "test"); err == nil {
			t.Fatalf("expected %#v to be rejected", payload)
		}
	}
}

func TestValidateJSONPayloadRejectsIntegralFloatInTaggedDTO(t *testing.T) {
	type payload struct {
		Timestamp any `json:"timestamp"`
	}
	if err := ValidateJSONPayload(payload{Timestamp: float64(1_700_000_000_000)}, "test"); !IsViolation(err) {
		t.Fatalf("expected tagged integral float to be rejected, got %v", err)
	}
}

func TestValidateJSONPayloadRejectsReusedFloatPointerAtTimeKey(t *testing.T) {
	value := float64(1_700_000_000_000)
	err := ValidateJSONPayload(map[string]any{
		"ordinary":  &value,
		"timestamp": &value,
	}, "test")
	if !IsViolation(err) {
		t.Fatalf("expected reused float pointer to be rejected at timestamp, got %v", err)
	}
}

func TestValidateJSONPayloadAcceptsOptionalOmissionAndPairedReadableTime(t *testing.T) {
	if err := ValidateJSONPayload(map[string]any{
		"startedAt":   int64(1_700_000_000_000),
		"startedTime": "2023-11-14T22:13:20Z",
	}, "test"); err != nil {
		t.Fatalf("expected valid payload: %v", err)
	}
	if err := ValidateJSONPayload(map[string]any{"name": "omitted optional time"}, "test"); err != nil {
		t.Fatalf("expected omitted optional time to be accepted: %v", err)
	}
}

func TestValidateJSONPayloadRejectsMismatchedReadableTime(t *testing.T) {
	err := ValidateJSONPayload(map[string]any{
		"completedAt":   int64(1_700_000_000_000),
		"completedTime": "2023-11-14T22:13:21Z",
	}, "test")
	if err == nil || !IsViolation(err) {
		t.Fatalf("expected mismatch violation, got %v", err)
	}
}

func TestValidateJSONPayloadRejectsSubMillisecondReadablePair(t *testing.T) {
	err := ValidateJSONPayload(map[string]any{
		"completedAt":   int64(1_700_000_000_000),
		"completedTime": "2023-11-14T22:13:20.000000001Z",
	}, "test")
	if err == nil || !IsViolation(err) {
		t.Fatalf("expected sub-millisecond mismatch violation, got %v", err)
	}
}

func TestValidateJSONPayloadRejectsUnpairedReadableTime(t *testing.T) {
	err := ValidateJSONPayload(map[string]any{
		"startedTime": "2023-11-14T22:13:20Z",
	}, "test")
	if err == nil || !IsViolation(err) {
		t.Fatalf("expected unpaired readable time violation, got %v", err)
	}
}

func TestValidateJSONPayloadAllowsStandaloneDatetimeISO(t *testing.T) {
	if err := ValidateJSONPayload(map[string]any{
		"date": "2023-11-14",
		"time": "22:13:20",
		"iso":  "2023-11-14T22:13:20Z",
	}, "test"); err != nil {
		t.Fatalf("expected standalone datetime-tool iso: %v", err)
	}
}

func TestValidateJSONPayloadPropagatesMarshalerTimeViolation(t *testing.T) {
	payload := invalidTimeMarshaler{}
	if err := ValidateJSONPayload(payload, "test"); !IsViolation(err) {
		t.Fatalf("expected marshaler time violation, got %v", err)
	}
}

type invalidTimeMarshaler struct{}

func (invalidTimeMarshaler) MarshalJSON() ([]byte, error) {
	return nil, ValidateEpochMillis(0, "timestamp", "test.marshaler")
}
