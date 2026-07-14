package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
)

func TestWriteJSONDoesNotInferExternalTimeFromFieldNames(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, api.Success(map[string]any{
		"createdAt": "2026-07-14T08:00:00Z",
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response api.ApiResponse[map[string]any]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data["createdAt"] != "2026-07-14T08:00:00Z" {
		t.Fatalf("external payload changed: %#v", response.Data)
	}
}

func TestWriteJSONAllowsPlatformProducerPayload(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, api.Success(map[string]any{
		"createdAt": int64(1_700_000_000_000),
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}
