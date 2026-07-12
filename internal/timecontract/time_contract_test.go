package timecontract

import (
	"encoding/json"
	"testing"
)

func TestValidateEpochMillisRejectsSecondsAndUnsafeValues(t *testing.T) {
	for _, value := range []int64{0, 1_700_000_000, MinEpochMillis - 1, MaxEpochMillis + 1} {
		if err := ValidateEpochMillis(value, "timestamp", "test"); err == nil {
			t.Fatalf("expected %d to be rejected", value)
		}
	}
	for _, value := range []int64{MinEpochMillis, 1_700_000_000_000, MaxEpochMillis} {
		if err := ValidateEpochMillis(value, "timestamp", "test"); err != nil {
			t.Fatalf("expected %d to be accepted: %v", value, err)
		}
	}
}

func TestOptionalEpochMillisOmitsZero(t *testing.T) {
	value, err := OptionalEpochMillis(0, "expiresAt", "test")
	if err != nil || value != nil {
		t.Fatalf("expected absent optional time, got %#v %v", value, err)
	}
}

func TestErrorDataIncludesContractFields(t *testing.T) {
	err := ValidateEpochMillis(0, "timestamp", "stream.event")
	data := ErrorData(err)
	if data["code"] != "time_contract_violation" || data["field"] != "timestamp" || data["location"] != "stream.event" || data["expected"] != Expected {
		t.Fatalf("unexpected data %#v", data)
	}
}

func TestParseEpochMillisRejectsCoercibleJSONValues(t *testing.T) {
	for name, value := range map[string]any{
		"string":   "1700000000000",
		"float":    float64(1_700_000_000_000),
		"fraction": json.Number("1700000000000.5"),
		"seconds":  json.Number("1700000000"),
		"zero":     json.Number("0"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseEpochMillis(value, "timestamp", "test")
			if !IsViolation(err) {
				t.Fatalf("expected contract violation, got %v", err)
			}
		})
	}
	value, err := ParseEpochMillis(json.Number("1700000000000"), "timestamp", "test")
	if err != nil || value != 1_700_000_000_000 {
		t.Fatalf("expected valid epoch milliseconds, got %d %v", value, err)
	}
}
