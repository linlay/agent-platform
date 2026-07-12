package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

func nestedInvalidTimeToolResultAgent() contracts.AgentEngine {
	return &orchestratorAgentEngine{
		streams: []contracts.AgentStream{
			&stubOrchestratableStream{deltas: []contracts.AgentDelta{
				contracts.DeltaToolResult{
					ToolID:   "bad-time-tool",
					ToolName: "bad_time_tool",
					Result: contracts.ToolExecutionResult{Structured: map[string]any{
						// A nested tool result is exactly the case that used to make
						// it to SSE JSON marshaling after StepWriter persistence.
						"createdAt": "1700000000000",
					}},
				},
			}},
		},
	}
}

func TestQuerySSETerminatesWithLocalTimeContractErrorBeforePersistingInvalidToolResult(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.agent = nestedInvalidTimeToolResultAgent()
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"chat-sse-time-contract",
		"runId":"run-sse-time-contract",
		"agentKey":"mock-agent",
		"message":"trigger malformed tool result"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected SSE done after local time-contract error, got %s", body)
	}

	var localError map[string]any
	for _, event := range decodeSSEMessages(t, body) {
		if event["type"] == "tool.result" {
			t.Fatalf("invalid tool event reached SSE: %#v", event)
		}
		if event["type"] == "run.error" {
			localError = event
		}
	}
	if localError == nil {
		t.Fatalf("expected local run.error, got %s", body)
	}
	assertLocalTimeContractRunError(t, localError)

	persisted, err := fixture.chats.LoadJSONLContent("chat-sse-time-contract")
	if err != nil {
		t.Fatalf("load persisted chat: %v", err)
	}
	if strings.Contains(persisted, `"createdAt":"1700000000000"`) {
		t.Fatalf("invalid tool timestamp was persisted: %s", persisted)
	}
}

func TestQueryNonStreamReturns422ForNestedInvalidToolResultTime(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.agent = nestedInvalidTimeToolResultAgent()
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"chat-non-stream-time-contract",
		"runId":"run-non-stream-time-contract",
		"agentKey":"mock-agent",
		"message":"trigger malformed tool result",
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected HTTP 422, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	details, _ := envelope.Data["error"].(map[string]any)
	if envelope.Code != http.StatusUnprocessableEntity || details["code"] != "time_contract_violation" || details["field"] != "createdAt" || details["expected"] != timecontract.Expected {
		t.Fatalf("unexpected time-contract response: %#v", envelope)
	}

	persisted, err := fixture.chats.LoadJSONLContent("chat-non-stream-time-contract")
	if err != nil {
		t.Fatalf("load persisted chat: %v", err)
	}
	if strings.Contains(persisted, `"createdAt":"1700000000000"`) {
		t.Fatalf("invalid tool timestamp was persisted: %s", persisted)
	}
}

func assertLocalTimeContractRunError(t *testing.T, event map[string]any) {
	t.Helper()
	if event["code"] != "time_contract_violation" || event["field"] != "createdAt" || event["expected"] != timecontract.Expected {
		t.Fatalf("unexpected local time-contract error: %#v", event)
	}
	value, ok := event["timestamp"].(float64)
	if !ok {
		t.Fatalf("local run.error must have a numeric timestamp: %#v", event)
	}
	if err := timecontract.ValidateEpochMillis(int64(value), "timestamp", "test"); err != nil {
		t.Fatalf("local run.error timestamp must be real epoch milliseconds: %v (%#v)", err, event)
	}
	errorData, ok := event["error"].(map[string]any)
	if !ok || errorData["code"] != "time_contract_violation" || errorData["field"] != "createdAt" || errorData["expected"] != timecontract.Expected {
		t.Fatalf("expected nested time-contract payload, got %#v", event)
	}
}
