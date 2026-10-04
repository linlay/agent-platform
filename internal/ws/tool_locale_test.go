package ws

import (
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionToolLocaleMatchesLiveAndReplay(t *testing.T) {
	conn := NewConn(nil, nil, config.WebSocketConfig{WriteQueueSize: 10, MaxObservesPerConn: 4}, AuthSession{})
	translations := map[string]any{"en": map[string]any{"label": "Desktop Settings"}, "zh-CN": map[string]any{"label": "桌面设置"}}
	event := stream.EventData{Type: "tool.start", Timestamp: 1791038000000, Payload: map[string]any{"toolId": "tool", "toolName": "desktop_settings", "toolLabel": "old", "toolI18n": translations}}
	if _, err := conn.ReserveStream("live", "run"); err != nil {
		t.Fatal(err)
	}
	defer conn.ReleaseStream("live")
	for _, tc := range []struct{ id, locale, label string }{{"zh", "zh-CN", "桌面设置"}, {"en", "en-US", "Desktop Settings"}} {
		conn.SetLocale(tc.locale)
		conn.SendResponse("/api/chat", tc.id, 0, "success", map[string]any{"events": []stream.EventData{event}})
		replay, _ := json.Marshal(mustReadQueuedMessage(t, conn.writeQueue).frame)
		conn.CompleteRequest(tc.id)
		conn.SendStreamEvent("live", event)
		live, _ := json.Marshal(mustReadQueuedMessage(t, conn.writeQueue).frame)
		for _, data := range [][]byte{replay, live} {
			if !strings.Contains(string(data), tc.label) || strings.Contains(string(data), "toolI18n") {
				t.Fatalf("%s: %s", tc.id, data)
			}
		}
	}
	if event.Payload["toolLabel"] != "old" {
		t.Fatal("source event mutated")
	}
}
