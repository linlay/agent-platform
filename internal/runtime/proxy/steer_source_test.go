package proxy

import (
	"reflect"
	"testing"

	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/stream"
)

func TestProxySteerSourceIsLocalAndCorrelatedWithRealEvent(t *testing.T) {
	route := NewRoute("target", "chat", "agent")
	source := &queryinput.SteerSource{Type: "agent", AgentKey: "caller", ChatID: "parent-chat", RunID: "parent-run", ToolID: "call"}
	want := source.Payload()
	route.RememberSteerSource("steer-id", source)
	source.AgentKey = "mutated"
	for _, id := range []string{"steer-id", "human-id"} {
		event := stream.EventData{Type: "request.steer", Timestamp: 1700000000000, Payload: map[string]any{"steerId": id, "message": "goal", "source": map[string]any{"type": "agent", "agentKey": "untrusted-upstream"}}}
		got := route.DecorateSteerEvent(event)
		if id == "steer-id" {
			if !reflect.DeepEqual(got.Value("source"), want) {
				t.Fatal(got)
			}
		} else if got.Value("source") != nil {
			t.Fatal("upstream forged local provenance")
		}
		if event.Value("source").(map[string]any)["agentKey"] != "untrusted-upstream" {
			t.Fatal("mutated upstream event")
		}
	}
}
