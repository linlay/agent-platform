package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/timecontract"
)

func opaqueTimeNamedToolResultAgent() contracts.AgentEngine {
	return &orchestratorAgentEngine{
		streams: []contracts.AgentStream{
			&stubOrchestratableStream{deltas: []contracts.AgentDelta{
				contracts.DeltaToolResult{
					ToolID:   "bad-time-tool",
					ToolName: "bad_time_tool",
					Result: contracts.ToolExecutionResult{Structured: map[string]any{
						// Tool output is external business data. Its property name
						// must not create platform time semantics by itself.
						"createdAt": "2026-07-14T08:00:00Z",
					}},
				},
			}},
		},
	}
}

func TestQuerySSEPersistsOpaqueToolResultWithTimeNamedBusinessField(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.agent = opaqueTimeNamedToolResultAgent()
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"chat-sse-time-contract",
		"runId":"run-sse-time-contract",
		"agentKey":"mock-agent",
		"message":"trigger opaque tool result"
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected SSE completion, got %s", body)
	}

	foundToolResult := false
	for _, event := range decodeSSEMessages(t, body) {
		if event["type"] == "tool.result" {
			result, _ := event["result"].(map[string]any)
			if result["createdAt"] == "2026-07-14T08:00:00Z" {
				foundToolResult = true
			}
		}
	}
	if !foundToolResult {
		t.Fatalf("expected opaque tool result, got %s", body)
	}

	persisted, err := fixture.chats.LoadJSONLContent("chat-sse-time-contract")
	if err != nil {
		t.Fatalf("load persisted chat: %v", err)
	}
	if !strings.Contains(persisted, `\"createdAt\":\"2026-07-14T08:00:00Z\"`) {
		t.Fatalf("opaque tool result was not persisted: %s", persisted)
	}
}

func TestQueryNonStreamAllowsOpaqueToolResultWithTimeNamedBusinessField(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.agent = opaqueTimeNamedToolResultAgent()
	server := newServerFromFixture(t, fixture)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{
		"chatId":"chat-non-stream-time-contract",
		"runId":"run-non-stream-time-contract",
		"agentKey":"mock-agent",
		"message":"trigger opaque tool result",
		"stream":false
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	persisted, err := fixture.chats.LoadJSONLContent("chat-non-stream-time-contract")
	if err != nil {
		t.Fatalf("load persisted chat: %v", err)
	}
	if !strings.Contains(persisted, `\"createdAt\":\"2026-07-14T08:00:00Z\"`) {
		t.Fatalf("opaque tool result was not persisted: %s", persisted)
	}
}

func assertLocalTimeContractRunError(t *testing.T, event map[string]any, fields ...string) {
	t.Helper()
	field := "createdAt"
	if len(fields) > 0 {
		field = fields[0]
	}
	if event["code"] != "time_contract_violation" || event["field"] != field || event["expected"] != timecontract.Expected {
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
	if !ok || errorData["code"] != "time_contract_violation" || errorData["field"] != field || errorData["expected"] != timecontract.Expected {
		t.Fatalf("expected nested time-contract payload, got %#v", event)
	}
}
