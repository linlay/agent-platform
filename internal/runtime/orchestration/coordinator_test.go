package orchestration

import (
	"io"
	"testing"

	"agent-platform/internal/contracts"
)

type deltaStreamStub struct {
	deltas []contracts.AgentDelta
	index  int
}

func (s *deltaStreamStub) Next() (contracts.AgentDelta, error) {
	if s.index >= len(s.deltas) {
		return nil, io.EOF
	}
	delta := s.deltas[s.index]
	s.index++
	return delta, nil
}

func (*deltaStreamStub) Close() error { return nil }

type deltaHandlerStub struct {
	emitted   int
	subAgents int
	team      int
	teamEnds  bool
}

func (h *deltaHandlerStub) Emit(contracts.AgentDelta) { h.emitted++ }
func (h *deltaHandlerStub) HandleSubAgents(contracts.AgentStream, contracts.DeltaInvokeSubAgents) error {
	h.subAgents++
	return nil
}
func (h *deltaHandlerStub) HandleTeamDispatch(contracts.AgentStream, contracts.DeltaTeamDispatch) (bool, error) {
	h.team++
	return h.teamEnds, nil
}

func TestRunInterpretsOrdinaryChildAndTeamDeltas(t *testing.T) {
	stream := &deltaStreamStub{deltas: []contracts.AgentDelta{
		contracts.DeltaContent{Text: "main"},
		contracts.DeltaInvokeSubAgents{MainToolID: "invoke"},
		contracts.DeltaTeamDispatch{MainToolID: "delegate"},
	}}
	handler := &deltaHandlerStub{}
	result, err := Run(stream, handler)
	if err != nil || result.StreamFailed || result.StreamInterrupted {
		t.Fatalf("run result=%#v err=%v", result, err)
	}
	if handler.emitted != 1 || handler.subAgents != 1 || handler.team != 1 {
		t.Fatalf("unexpected dispatch counts: %#v", handler)
	}
}
