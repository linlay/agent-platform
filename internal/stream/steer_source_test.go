package stream

import (
	"encoding/json"
	"reflect"
	"testing"

	"agent-platform/internal/contracts/queryinput"
)

func TestSteerSourceIsOptionalEventMetadata(t *testing.T) {
	d := NewDispatcher(StreamRequest{RunID: "target", ChatID: "chat"})
	source := &queryinput.SteerSource{Type: "agent", AgentKey: "caller", ChatID: "parent-chat", RunID: "parent-run", ToolID: "call"}
	for _, s := range []*queryinput.SteerSource{nil, source} {
		events := d.Dispatch(RequestSteer{RunID: "target", ChatID: "chat", SteerID: "steer-id", Message: "change", Source: s})
		if len(events) != 1 || events[0].Type != "request.steer" || events[0].Payload["role"] != "user" || events[0].Payload["message"] != "change" {
			t.Fatal(events)
		}
		data, err := json.Marshal(events[0].Data())
		if err != nil {
			t.Fatal(err)
		}
		var flat map[string]any
		if err := json.Unmarshal(data, &flat); err != nil {
			t.Fatal(err)
		}
		if s == nil {
			if _, ok := flat["source"]; ok {
				t.Fatal(string(data))
			}
		} else if !reflect.DeepEqual(flat["source"], source.Payload()) {
			t.Fatal(string(data))
		}
	}
}
