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

func TestQueryRequestUsesMustUseSkills(t *testing.T) {
	var request QueryRequest
	if err := json.Unmarshal([]byte(`{"message":"hi","mustUseSkills":["pdf","ppt-master"]}`), &request); err != nil {
		t.Fatalf("unmarshal query: %v", err)
	}
	if strings.Join(request.MustUseSkills, ",") != "pdf,ppt-master" {
		t.Fatalf("mustUseSkills = %#v", request.MustUseSkills)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}
	if strings.Contains(string(encoded), "requiredSkillKeys") || !strings.Contains(string(encoded), "mustUseSkills") {
		t.Fatalf("encoded query = %s", encoded)
	}
}

func TestQueryRequestRejectsRemovedRequiredSkillKeys(t *testing.T) {
	var request QueryRequest
	err := json.Unmarshal([]byte(`{"message":"hi","requiredSkillKeys":[]}`), &request)
	if !errors.Is(err, ErrRequiredSkillKeysRemoved) {
		t.Fatalf("error = %v", err)
	}
}
