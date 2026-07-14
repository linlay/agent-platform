package stream

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/timecontract"
)

func TestParseEventDataJSONRequiresEpochMillisecondsInteger(t *testing.T) {
	valid := []byte(`{"type":"content.delta","timestamp":1700000000000,"delta":"ok"}`)
	event, err := ParseEventDataJSON(valid, "test.stream")
	if err != nil {
		t.Fatalf("parse valid event: %v", err)
	}
	if event.Timestamp != 1_700_000_000_000 {
		t.Fatalf("timestamp = %d", event.Timestamp)
	}

	for name, payload := range map[string][]byte{
		"string":   []byte(`{"type":"content.delta","timestamp":"1700000000000"}`),
		"float":    []byte(`{"type":"content.delta","timestamp":1700000000000.5}`),
		"exponent": []byte(`{"type":"content.delta","timestamp":1e12}`),
		"seconds":  []byte(`{"type":"content.delta","timestamp":1700000000}`),
		"zero":     []byte(`{"type":"content.delta","timestamp":0}`),
		"missing":  []byte(`{"type":"content.delta"}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseEventDataJSON(payload, "test.stream")
			if !timecontract.IsViolation(err) {
				t.Fatalf("expected time contract violation, got %v", err)
			}
			data := timecontract.ErrorData(err)
			if data["field"] != "timestamp" || data["location"] != "test.stream" || data["expected"] != timecontract.Expected {
				t.Fatalf("unexpected violation data %#v", data)
			}
		})
	}
}

func TestEventDataUnmarshalJSONUsesStrictTimestampContract(t *testing.T) {
	var event EventData
	err := json.Unmarshal([]byte(`{"type":"content.delta","timestamp":"1700000000000"}`), &event)
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected time contract violation, got %v", err)
	}
}

func TestEventDataMarshalJSONRequiresEpochMillisecondsTimestamp(t *testing.T) {
	_, err := json.Marshal(EventData{Type: "content.delta", Timestamp: 0})
	if !timecontract.IsViolation(err) {
		t.Fatalf("expected time contract violation, got %v", err)
	}

	data, err := json.Marshal(EventData{Type: "content.delta", Timestamp: 1_700_000_000_000})
	if err != nil {
		t.Fatalf("marshal valid event: %v", err)
	}
	if strings.Contains(string(data), `"timestamp":"`) || !strings.Contains(string(data), `"timestamp":1700000000000`) {
		t.Fatalf("expected numeric epoch milliseconds, got %s", data)
	}
}

func TestEventDataMarshalJSONLeavesToolResultBusinessPayloadOpaque(t *testing.T) {
	data, err := json.Marshal(EventData{
		Type:      "tool.result",
		Timestamp: 1_700_000_000_000,
		Payload: map[string]any{
			"result": map[string]any{"mtimeMs": int64(1_700_000_000), "createdAt": "2026-07-14T08:00:00Z"},
		},
	})
	if err != nil {
		t.Fatalf("external tool payload must remain opaque: %v", err)
	}
	if !strings.Contains(string(data), `"createdAt":"2026-07-14T08:00:00Z"`) {
		t.Fatalf("unexpected tool event payload: %s", data)
	}
}
