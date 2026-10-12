package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFailureCanCarryStructuredError(t *testing.T) {
	payload := map[string]any{"code": "provider_quota_exhausted"}
	response := Failure(429, "quota", payload)
	if response.Code != 429 || response.Msg != "quota" {
		t.Fatalf("unexpected response envelope %#v", response)
	}
	errPayload, _ := response.Data["error"].(map[string]any)
	if errPayload["code"] != "provider_quota_exhausted" {
		t.Fatalf("expected structured error payload, got %#v", response.Data)
	}
	payload["code"] = "changed"
	if errPayload["code"] != "provider_quota_exhausted" {
		t.Fatalf("expected payload to be cloned, got %#v", errPayload)
	}
}

func TestQueryRequestUsesResourceReferences(t *testing.T) {
	var request QueryRequest
	if err := json.Unmarshal([]byte(`{"message":"hi","references":[{"type":"skill","id":"pdf"},{"type":"kbase","id":"docs"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.References) != 2 {
		t.Fatal(request)
	}
	encoded, _ := json.Marshal(request)
	if !strings.Contains(string(encoded), "references") {
		t.Fatal(string(encoded))
	}
	for _, key := range []string{"unexpectedResource", "unexpectedOption"} {
		for _, value := range []string{"null", "[]", "[\"id\"]"} {
			if err := json.Unmarshal([]byte(`{"message":"hi","`+key+`":`+value+`}`), &request); err == nil {
				t.Fatalf("accepted %s=%s", key, value)
			}
		}
	}
}

func TestQueryRequestRejectsRemovedRequiredSkillIDs(t *testing.T) {
	var request QueryRequest
	err := json.Unmarshal([]byte(`{"message":"hi","requiredSkillKeys":[]}`), &request)
	if !errors.Is(err, ErrRequiredSkillKeysRemoved) {
		t.Fatalf("error = %v", err)
	}
}
