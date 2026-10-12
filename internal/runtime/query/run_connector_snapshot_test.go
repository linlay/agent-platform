package query

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
)

func TestRunConnectorSnapshotsAreKeptOnlyForSuspendedRuns(t *testing.T) {
	root := t.TempDir()
	runs := runstate.NewManager()
	runs.Register(context.Background(), contracts.QuerySession{RunID: "run-live", ChatID: "chat", AgentKey: "agent", RunOwner: contracts.AgentRunOwner("agent")})
	defer runs.Finish("run-live")
	service := &Service{deps: Dependencies{Runs: runs, Config: config.Config{Paths: config.PathsConfig{StateDir: filepath.Join(root, ".state"), ConnectorsCenterDir: filepath.Join(root, "connectors-center")}}}}
	write := func(runID string) string {
		path := service.connectorSnapshotPath(runID)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(runConnectorSnapshot{RunID: runID})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	suspended, finished, stale, live := write("run-suspended"), write("run-finished"), write("run-stale"), write("run-live")
	service.sweepRunConnectorSnapshots([]chat.PendingAwaitingWithChat{{ChatID: "chat", RunID: "run-suspended"}})
	if !exists(suspended) || exists(finished) || exists(stale) {
		t.Fatalf("after sweep: suspended=%t finished=%t stale=%t", exists(suspended), exists(finished), exists(stale))
	}
	if !exists(live) {
		t.Fatal("sweep removed a live Run's connector snapshot")
	}
	service.finishRunConnectorPins("run-suspended", "chat")
	if exists(suspended) {
		t.Fatal("finished Run kept its connector snapshot")
	}
}
