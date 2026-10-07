package ws

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

func TestConnectionReasoningLocaleMatchesLiveAndReplay(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 10, MaxObservesPerConn: 4}, AuthSession{})
	event := stream.EventData{Type: "reasoning.start", Timestamp: 1791038000000, Payload: map[string]any{
		"reasoningId": "hello", "runId": "run", "reasoningLabel": "正在思考",
	}}
	if _, err := conn.ReserveStream("live", "run"); err != nil {
		t.Fatal(err)
	}
	defer conn.ReleaseStream("live")
	for _, tc := range []struct{ id, locale, label string }{{"zh", "zh-cn", "思索"}, {"en", "en", "Cogitating"}} {
		conn.SetLocale(tc.locale)
		conn.SendResponse("/api/chat", tc.id, 0, "success", map[string]any{"events": []stream.EventData{event}})
		replay, _ := json.Marshal(mustReadQueuedMessage(t, conn.writeQueue).frame)
		conn.CompleteRequest(tc.id)
		conn.SendStreamEvent("live", event)
		live, _ := json.Marshal(mustReadQueuedMessage(t, conn.writeQueue).frame)
		for _, data := range [][]byte{replay, live} {
			if !strings.Contains(string(data), `"reasoningLabel":"`+tc.label+`"`) || strings.Contains(string(data), "reasoningLabelKey") {
				t.Fatalf("%s: %s", tc.id, data)
			}
		}
	}
	if event.Payload["reasoningLabel"] != "正在思考" {
		t.Fatal("source event mutated")
	}
}
