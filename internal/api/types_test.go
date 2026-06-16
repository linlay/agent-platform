package api

import "testing"

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
