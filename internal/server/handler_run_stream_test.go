package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/runctl"
	"agent-platform-runner-go/internal/stream"
)

func TestHandleRunStreamDefaultsMissingLastSeqToZero(t *testing.T) {
	runs := runctl.NewInMemoryRunManager()
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
		Seq:  1,
		Type: "request.query",
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
				H2A: config.H2AConfig{Render: config.H2ARenderConfig{}},
				Logging: config.LoggingConfig{
					SSE: config.ToggleConfig{Enabled: false},
				},
			},
			Runs: runs,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/run/stream?runId="+session.RunID, nil)
	rec := httptest.NewRecorder()

	server.handleRunStream(rec, req)

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
