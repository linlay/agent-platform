package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestWriteJSONConvertsInvalidTimePayloadToTimeContractViolation(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, api.Success(map[string]any{
		"timestamp": "1700000000000",
	}))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response api.ApiResponse[map[string]any]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errorData, _ := response.Data["error"].(map[string]any)
	if errorData["code"] != "time_contract_violation" || errorData["field"] != "timestamp" || errorData["expected"] != "epoch_ms_int64" {
		t.Fatalf("unexpected error payload %#v", response.Data)
	}
}

func TestWriteJSONRejectsIntegralFloatTimestamp(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, api.Success(map[string]any{
		"timestamp": float64(1_700_000_000_000),
	}))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "time_contract_violation") {
		t.Fatalf("integral float timestamp must be rejected, status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteJSONAllowsEpochMillisecondsAndOmittedOptionalTime(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, http.StatusOK, api.Success(map[string]any{
		"createdAt": int64(1_700_000_000_000),
	}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}
