package api

import (
	"encoding/json"
	"testing"
)

func TestAutomationRemainingRunsUpdatePresenceAndRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		present    bool
		count      *int
	}{
		{name: "omitted", body: `{"id":"task"}`},
		{name: "clear", body: `{"id":"task","remainingRuns":null}`, present: true},
		{name: "set", body: `{"id":"task","remainingRuns":3}`, present: true, count: func() *int { n := 3; return &n }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req UpdateAutomationRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatal(err)
			}
			if req.RemainingRunsSet != tc.present || (req.RemainingRuns == nil) != (tc.count == nil) || tc.count != nil && *req.RemainingRuns != *tc.count {
				t.Fatalf("wrong update semantics: %+v", req)
			}
			encoded, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			_, present := fields["remainingRuns"]
			if present != tc.present || tc.present && tc.count == nil && string(fields["remainingRuns"]) != "null" {
				t.Fatalf("lost explicit update on round trip: %s", encoded)
			}
		})
	}
	for _, body := range []string{
		`{"remainingRuns":"3"}`, `{"remainingRuns":1.5}`,
		`{"clearRemainingRuns":true}`, `{"query":{"message":"task","unknown":true}}`,
	} {
		var req UpdateAutomationRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Fatalf("invalid update accepted: %s", body)
		}
	}
}
