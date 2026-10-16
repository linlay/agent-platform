package llm

import (
	"context"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func TestAgentSteerSourceDoesNotChangeModelInput(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "target")
	defer control.Finish()
	source := &queryinput.Origin{Type: "agent", AgentKey: "caller", ChatID: "parent-chat", RunID: "parent-run", ToolID: "call"}
	want := source.Payload()
	if !control.EnqueueSteer(api.SteerRequest{RunID: "target", ChatID: "chat", SteerID: "steer-id", Message: "new goal", Origin: source}) {
		t.Fatal("not enqueued")
	}
	source.AgentKey = "mutated"
	s := &llmRunStream{runControl: control}
	s.appendPendingSteers()
	if len(s.messages) != 1 || s.messages[0].Role != "user" || s.messages[0].Content != "new goal" {
		t.Fatalf("model input %#v", s.messages)
	}
	if len(s.pending) != 1 {
		t.Fatalf("pending %#v", s.pending)
	}
	mapper := NewDeltaMapper("target", "chat", contracts.Budget{}, nil, nil)
	inputs := mapper.Map(s.pending[0])
	if len(inputs) != 1 {
		t.Fatal(inputs)
	}
	input := inputs[0].(stream.RequestSteer)
	if !reflect.DeepEqual(input.Origin.Payload(), want) || input.Message != "new goal" {
		t.Fatal(input)
	}
}
