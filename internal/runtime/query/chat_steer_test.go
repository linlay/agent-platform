package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/runtime/runstate"
	runtimetypes "agent-platform/internal/runtime/types"
)

func TestSteerSourceCannotBeDecodedFromClientInput(t *testing.T) {
	var request queryinput.SteerRequest
	if err := json.Unmarshal([]byte(`{"runId":"target","agentKey":"agent","message":"change","origin":{"type":"agent","agentKey":"forged","chatId":"forged","runId":"forged","toolId":"forged"}}`), &request); !errors.Is(err, queryinput.ErrUnknownField) {
		t.Fatalf("client provenance must be rejected: %v", err)
	}
	if request.Origin != nil {
		t.Fatal("client supplied trusted provenance")
	}
	if err := json.Unmarshal([]byte(`{"runId":"target","agentKey":"agent","message":"change"}`), &request); err != nil {
		t.Fatal(err)
	}
	runs := runstate.NewManager()
	_, control, _ := runs.Register(context.Background(), contracts.QuerySession{RunID: "target", ChatID: "chat", AgentKey: "agent", RunOwner: contracts.AgentRunOwner("agent")})
	service := NewService(Dependencies{Runs: runs})
	result, err := service.Steer(context.Background(), runtimetypes.SteerCommand{RunRef: runtimetypes.RunRef{RunID: request.RunID, AgentKey: request.AgentKey}, Message: request.Message})
	if err != nil || !result.Accepted {
		t.Fatalf("%#v %v", result, err)
	}
	if got := control.DrainSteers(); len(got) != 1 || got[0].Origin != nil || !reflect.DeepEqual(got[0].Message, request.Message) {
		t.Fatal(got)
	}
}

type failedSteerProxy struct {
	unavailableProxy
	calls   int
	request queryinput.SteerRequest
	err     *runtimetypes.RequestError
}

func (p *failedSteerProxy) Steer(req queryinput.SteerRequest) (queryinput.SteerResponse, *runtimetypes.RequestError, bool) {
	p.calls++
	p.request = req
	return queryinput.SteerResponse{}, p.err, true
}
