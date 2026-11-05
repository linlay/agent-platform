package query

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
	rt "agent-platform/internal/runtime/types"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

type registrationRecorder struct{ chat.Store }

func (registrationRecorder) OnRunStarted(chat.RunStart) error { return nil }

func TestRegistrationFailureDrainsObserverBeforeReleasingChat(t *testing.T) {
	runs := runstate.NewManager()
	root := t.TempDir()
	service := NewService(Dependencies{Runs: runs, Config: config.Config{Paths: config.PathsConfig{StateDir: filepath.Join(root, ".state"), ConnectorsCenterDir: filepath.Join(root, "connectors-center")}}})
	pinDir := filepath.Dir(service.connectorSources().RunPinPath("run"))
	if err := os.MkdirAll(filepath.Dir(pinDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinDir, []byte("blocks pin directory creation"), 0600); err != nil {
		t.Fatal(err)
	}
	attached := make(chan *rt.Subscription, 1)
	ctx := context.WithValue(context.Background(), queryHooksKey{}, rt.QueryHooks{OnRunStarted: func(start chat.RunStart) {
		sub, err := service.AttachRun(context.Background(), rt.RunRef{RunID: start.RunID}, 0)
		if err != nil {
			t.Error(err)
			return
		}
		attached <- sub
	}})
	prepared := preparedQuery{Req: rt.QueryCommand{RunID: "run", ChatID: "chat", AgentKey: "agent"},
		Session:   contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "agent"},
		AgentDef:  catalog.AgentDefinition{Key: "agent", ConnectorMounts: []catalog.ConnectorMount{{ID: "test", Dir: t.TempDir(), Digest: "test"}}},
		Execution: &rt.QueryExecutionOptions{CompletionStore: registrationRecorder{}},
	}
	finished := make(chan *statusError, 1)
	go func() { _, err := service.RegisterPreparedQuery(ctx, prepared); finished <- err }()
	var sub *rt.Subscription
	select {
	case sub = <-attached:
	case err := <-finished:
		t.Fatalf("registration failed before observer attached: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no observer")
	}
	defer sub.Close()
	select {
	case _, open := <-sub.Events:
		if open {
			t.Fatal("unexpected event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration failure left observer open")
	}
	if _, active, _ := runs.ActiveRunForChat("chat"); !active {
		t.Fatal("Chat released before delivery acknowledgement")
	}
	sub.Close()
	select {
	case err := <-finished:
		if err == nil || err.Code != "run_connector_snapshot_unavailable" {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration rollback did not finish")
	}
	if _, active, _ := runs.ActiveRunForChat("chat"); active {
		t.Fatal("Chat still active")
	}
	if _, held := service.connectorSnapshots.Load("run"); held {
		t.Fatal("failed registration retained connector snapshot")
	}
	next, err := runs.RegisterExclusiveForChat(context.Background(), contracts.QuerySession{RunID: "next", ChatID: "chat"})
	if err != nil || !next.Registered {
		t.Fatalf("next registration: %#v %v", next, err)
	}
	runs.Finish("next")
}
