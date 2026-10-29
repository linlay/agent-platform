package query

import (
	"context"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
)

func TestAwaitingResolutionOwnerFollowsRunLifecycle(t *testing.T) {
	runs := runstate.NewManager()
	runs.Register(context.Background(), contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "agent", StartedAtMillis: time.Now().UnixMilli(), RunOwner: contracts.AgentRunOwner("agent")})
	if !contracts.AwaitingHasLiveExecutor(runs, "run") {
		t.Fatal("active run must own its awaiting")
	}
	if state := contracts.AwaitingResolutionOwner(runs, "run"); state != contracts.AwaitingResolutionOwned {
		t.Fatalf("active run misclassified: %v", state)
	}
	runs.Finish("run")
	if contracts.AwaitingHasLiveExecutor(runs, "run") {
		t.Fatal("finished run must not own its awaiting")
	}
	if state := contracts.AwaitingResolutionOwner(runs, "run"); state != contracts.AwaitingResolutionFinished {
		t.Fatalf("completed run misclassified: %v", state)
	}
	if state := contracts.AwaitingResolutionOwner(runs, "absent"); state != contracts.AwaitingResolutionUnowned {
		t.Fatalf("absent run misclassified: %v", state)
	}
}
