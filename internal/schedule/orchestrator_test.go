package schedule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
)

func TestRegistryLoadsScheduleDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"name: Demo\n"+
			"cron: @every 1s\n"+
			"agentKey: demo-agent\n"+
			"message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}
	defs, err := NewRegistry(root).Load()
	if err != nil {
		t.Fatalf("load schedules: %v", err)
	}
	if len(defs) != 1 || defs[0].Request.AgentKey != "demo-agent" || defs[0].Request.Message != "hello" {
		t.Fatalf("unexpected schedule definitions %#v", defs)
	}
}

func TestOrchestratorDispatchesScheduledQuery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.yml"), []byte(
		"name: Demo\n"+
			"cron: @every 1s\n"+
			"agentKey: demo-agent\n"+
			"message: hello\n",
	), 0o644); err != nil {
		t.Fatalf("write schedule file: %v", err)
	}

	dispatched := make(chan string, 1)
	orchestrator := NewOrchestrator(NewRegistry(root), NewDispatcher(func(_ context.Context, req api.QueryRequest) error {
		dispatched <- req.Message
		return nil
	}))
	if err := orchestrator.Start(context.Background()); err != nil {
		t.Fatalf("start orchestrator: %v", err)
	}
	defer orchestrator.Stop()

	select {
	case message := <-dispatched:
		if message != "hello" {
			t.Fatalf("unexpected dispatched message %q", message)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("timed out waiting for scheduled dispatch")
	}
}
