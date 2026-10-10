package query

import (
	"errors"
	"testing"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

type continuationLeaseRegistry struct {
	catalog.Registry
	catalog.RuntimeLeaser
	released int
	frozen   bool
}

func (r *continuationLeaseRegistry) AcquireAgentRuntime(key string) (catalog.AgentDefinition, func(), bool) {
	return catalog.AgentDefinition{Key: key}, func() { r.released++ }, true
}

func (r *continuationLeaseRegistry) AcquireAgentSnapshot(def catalog.AgentDefinition) (catalog.AgentDefinition, func(), bool) {
	r.frozen = true
	return def, func() { r.released++ }, true
}

type continuationBrokenStore struct{ chat.Store }

func (continuationBrokenStore) LoadRunQuery(string, string) (*chat.QueryLine, error) {
	return nil, errors.New("query unavailable")
}

type continuationUnusedEngine struct{ runtimetypes.Engine }

func TestFailedContinuationReleasesFrozenOrCurrentLease(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		r := &continuationLeaseRegistry{}
		s := NewService(Dependencies{Registry: r, Runs: runstate.NewManager(), Chats: continuationBrokenStore{}, Agent: continuationUnusedEngine{}})
		continued, err := s.startAwaitingContinuationWithAdmission(
			DeferredAwaiting{Mode: "question", RunID: "run", ChatID: "chat"},
			queryinput.SubmitRequest{RunID: "run", ChatID: "chat"}, nil,
			&awaitingContinuationAdmission{AgentKey: "agent", AgentDef: catalog.AgentDefinition{Key: "agent"}, Frozen: frozen}, nil,
		)
		if continued || err == nil || err.Error() != "query unavailable" {
			t.Fatalf("continuation=%v err=%v", continued, err)
		}
		if r.released != 1 || r.frozen != frozen {
			t.Fatalf("frozen=%v: releases=%d usedFrozen=%v", frozen, r.released, r.frozen)
		}
	}
}
