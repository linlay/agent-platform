package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func TestHandleAttachDefaultsMissingLastSeqToZero(t *testing.T) {
	runs := contracts.NewInMemoryRunManager()
	session := contracts.QuerySession{
		RunID:    "run_1",
		ChatID:   "chat_1",
		AgentKey: "agent_1",
	}
	_, _, _ = runs.Register(context.Background(), session)
	eventBus, ok := runs.EventBus(session.RunID)
	if !ok {
		t.Fatalf("expected event bus for run %s", session.RunID)
	}
	eventBus.Publish(stream.EventData{
		Seq:       1,
		Type:      "request.query",
		Timestamp: testEpochMillis,
		Payload: map[string]any{
			"runId":  session.RunID,
			"chatId": session.ChatID,
		},
	})
	runs.Finish(session.RunID)
	eventBus.Freeze()

	server := &Server{
		deps: Dependencies{
			Config: config.Config{
				SSE: config.SSEConfig{},
				Logging: config.LoggingConfig{
					SSE: config.ToggleConfig{Enabled: false},
				},
			},
			Runs: runs,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/attach?runId="+session.RunID+"&agentKey="+session.AgentKey, nil)
	rec := httptest.NewRecorder()

	server.handleAttach(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"request.query"`) {
		t.Fatalf("expected replayed request.query event, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected done sentinel, got %s", body)
	}
}

func TestHandleAttachTerminatesInvalidObserverEventWithLocalTimeContractError(t *testing.T) {
	runs := contracts.NewInMemoryRunManager()
	session := contracts.QuerySession{
		RunID:    "run_attach_time_contract",
		ChatID:   "chat_attach_time_contract",
		AgentKey: "agent_1",
	}
	_, _, _ = runs.Register(context.Background(), session)
	eventBus, ok := runs.EventBus(session.RunID)
	if !ok {
		t.Fatalf("expected event bus for run %s", session.RunID)
	}
	eventBus.Publish(stream.EventData{
		Seq:       7,
		Type:      "content.delta",
		Timestamp: 1_700_000_000_000,
		Payload: map[string]any{
			"runId":  session.RunID,
			"chatId": session.ChatID,
			"delta":  "valid first event",
		},
	})
	// This simulates a producer bypassing the earlier EventData validation.
	// SSE is the final observer boundary and must replace it with a platform
	// error rather than forwarding or repairing the bad nested field.
	eventBus.Publish(stream.EventData{
		Seq:       8,
		Type:      "content.delta",
		Timestamp: 1_700_000_000_001,
		Payload: map[string]any{
			"runId":     session.RunID,
			"chatId":    session.ChatID,
			"delta":     "must not reach client",
			"createdAt": "1700000000000",
		},
	})

	server := &Server{
		deps: Dependencies{
			Config: config.Config{
				SSE: config.SSEConfig{},
				Logging: config.LoggingConfig{
					SSE: config.ToggleConfig{Enabled: false},
				},
			},
			Runs: runs,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/attach?runId="+session.RunID+"&agentKey="+session.AgentKey+"&lastSeq=6", nil)
	rec := httptest.NewRecorder()
	server.handleAttach(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected started SSE response, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected SSE done sentinel after time-contract violation, got %s", body)
	}
	if strings.Contains(body, `"createdAt":"1700000000000"`) {
		t.Fatalf("invalid observer event reached SSE: %s", body)
	}

	var localError map[string]any
	for _, event := range decodeSSEMessages(t, body) {
		if event["type"] == "run.error" {
			localError = event
		}
	}
	if localError == nil {
		t.Fatalf("expected local run.error, got %s", body)
	}
	assertLocalTimeContractRunError(t, localError)
	if got, ok := localError["seq"].(float64); !ok || got != 8 {
		t.Fatalf("local error seq = %#v, want 8", localError["seq"])
	}

	status, ok := runs.RunStatus(session.RunID)
	if !ok {
		t.Fatalf("expected run status after terminated observer")
	}
	if status.State != contracts.RunLoopStateCancelled || status.CompletedAt == 0 {
		t.Fatalf("expected cancelled and finished run after SSE violation, got %#v", status)
	}
}

func TestHandleAttachInvalidCompletedReplayDoesNotCancelHistoricalRun(t *testing.T) {
	runs := contracts.NewInMemoryRunManager()
	session := contracts.QuerySession{
		RunID:    "run_attach_completed_time_contract",
		ChatID:   "chat_attach_completed_time_contract",
		AgentKey: "agent_1",
	}
	_, _, _ = runs.Register(context.Background(), session)
	eventBus, ok := runs.EventBus(session.RunID)
	if !ok {
		t.Fatalf("expected event bus for run %s", session.RunID)
	}
	eventBus.Publish(stream.EventData{
		Seq:       9,
		Type:      "content.delta",
		Timestamp: 1_700_000_000_000,
		Payload: map[string]any{
			"runId":     session.RunID,
			"chatId":    session.ChatID,
			"createdAt": "1700000000000",
		},
	})
	runs.Finish(session.RunID)
	eventBus.Freeze()
	before, ok := runs.RunStatus(session.RunID)
	if !ok || before.State != contracts.RunLoopStateCompleted || before.CompletedAt == 0 {
		t.Fatalf("expected completed replay fixture, got %#v", before)
	}

	server := &Server{
		deps: Dependencies{
			Config: config.Config{
				SSE: config.SSEConfig{},
				Logging: config.LoggingConfig{
					SSE: config.ToggleConfig{Enabled: false},
				},
			},
			Runs: runs,
		},
	}
	rec := httptest.NewRecorder()
	server.handleAttach(rec, httptest.NewRequest(http.MethodGet, "/api/attach?runId="+session.RunID+"&agentKey="+session.AgentKey+"&lastSeq=8", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Fatalf("expected local SSE error and done for completed replay, got %d %s", rec.Code, rec.Body.String())
	}

	after, ok := runs.RunStatus(session.RunID)
	if !ok {
		t.Fatal("expected completed run status after replay")
	}
	if after.State != contracts.RunLoopStateCompleted || after.CompletedAt != before.CompletedAt {
		t.Fatalf("completed replay must not be interrupted or re-finished: before=%#v after=%#v", before, after)
	}
}
