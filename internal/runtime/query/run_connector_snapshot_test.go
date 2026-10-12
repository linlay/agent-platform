package query

import (
	"path/filepath"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func TestRunConnectorSnapshotLivesInMemoryUntilRunEnds(t *testing.T) {
	root := t.TempDir()
	service := &Service{deps: Dependencies{Config: config.Config{Paths: config.PathsConfig{StateDir: filepath.Join(root, ".state"), ConnectorsCenterDir: filepath.Join(root, "connectors-center")}}}}
	service.connectorSnapshots.Store("run", runConnectorSnapshot{RunID: "run", Agents: []catalog.ConnectorSnapshot{{AgentKey: "agent"}}})

	if err := service.RestoreRunConnectors("run", &catalog.AgentDefinition{Key: "other"}); err == nil {
		t.Fatal("Agent outside the snapshot was restored")
	}
	if err := service.RestoreRunConnectors("unknown", &catalog.AgentDefinition{Key: "agent"}); err != nil {
		t.Fatalf("Run without a snapshot must use its current definition: %v", err)
	}
	service.finishRunConnectorPins("run", "chat")
	if _, held := service.connectorSnapshots.Load("run"); held {
		t.Fatal("finished Run kept its connector snapshot")
	}
	if entries, _ := filepath.Glob(filepath.Join(root, ".state", "*")); len(entries) != 0 {
		t.Fatalf("connector snapshot touched the state directory: %v", entries)
	}
}
