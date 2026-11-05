package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	"agent-platform/internal/stream"
)

func TestGetRunStatusReturnsFailedError(t *testing.T) {
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{
		RunID: "failed-run", ChatID: "failed-chat", AgentKey: "mock-agent", RunOwner: contracts.AgentRunOwner("mock-agent"),
	})
	eventBus, ok := runs.EventBus("failed-run")
	if !ok {
		t.Fatal("event bus is unavailable")
	}
	eventBus.Publish(stream.EventData{
		Seq:       1,
		Type:      "run.error",
		Timestamp: time.Now().UnixMilli(),
		Payload: map[string]any{
			"error": map[string]any{"code": "provider_error", "message": "upstream failed"},
		},
	})
	control.TransitionState(contracts.RunLoopStateFailed)
	runs.Finish("failed-run")

	server := &Server{deps: Dependencies{Runs: runs}}
	status, err := server.GetRunStatus("failed-run")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Status != "failed" || status.LastSeq != 1 || status.Error["code"] != "provider_error" || status.Error["message"] != "upstream failed" {
		t.Fatalf("unexpected failed status %#v", status)
	}
}

func waitGetRunStatus(t *testing.T, server *Server, runID string, want string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	encoded, _ := json.Marshal(status)
	t.Fatalf("run %s did not reach %s: %s", runID, want, encoded)
	return contracts.RunSnapshot{}
}

func waitRunTerminal(t *testing.T, server *Server, runID string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		switch status.Status {
		case "completed", "failed", "interrupted":
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	t.Fatalf("run did not terminate: %#v", status)
	return contracts.RunSnapshot{}
}

func waitRunFinished(t *testing.T, server *Server, runID string, want string) contracts.RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := server.GetRunStatus(runID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if status.Status == want && status.CompletedAt != 0 {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := server.GetRunStatus(runID)
	t.Fatalf("run did not finish as %s: %#v", want, status)
	return contracts.RunSnapshot{}
}
