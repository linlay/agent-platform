package query

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/runtime/runstate"
)

func TestAwaitingResolutionClaimHasSingleOwnerAcrossActivation(t *testing.T) {
	runs := runstate.NewManager()
	session := contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "agent", StartedAtMillis: time.Now().UnixMilli(), RunOwner: contracts.AgentRunOwner("agent", "")}
	_, err := runs.RegisterRecoveredAwaiting(context.Background(), session, "await", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Finish("run")
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			state, claimed := contracts.ClaimAwaitingResolution(runs, "run", "await")
			if state != contracts.AwaitingResolutionOwned {
				t.Errorf("claim failure treated as absent owner: %v", state)
			}
			if claimed != nil {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claim winners=%d", wins.Load())
	}
	if !runs.ActivateRecoveredAwaiting("run", "await") {
		t.Fatal("activate claimed run")
	}
	if !contracts.AwaitingHasLiveExecutor(runs, "run", "await") {
		t.Fatal("activated continuation must retain ownership without a pending submit")
	}
	if state, claim := contracts.ClaimAwaitingResolution(runs, "run", "await"); state != contracts.AwaitingResolutionOwned || claim != nil {
		t.Fatalf("activated executor lost ownership: %v %#v", state, claim)
	}
	runs.Finish("run")
	if state, claim := contracts.ClaimAwaitingResolution(runs, "run", "await"); state != contracts.AwaitingResolutionFinished || claim != nil {
		t.Fatalf("completed run misclassified: %v %#v", state, claim)
	}
	if state, claim := contracts.ClaimAwaitingResolution(runs, "absent", "await"); state != contracts.AwaitingResolutionUnowned || claim != nil {
		t.Fatalf("absent run misclassified: %v %#v", state, claim)
	}
}
