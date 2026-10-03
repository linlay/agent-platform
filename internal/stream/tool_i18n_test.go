package stream

import (
	"agent-platform/internal/i18n"
	"testing"
)

func TestToolTranslationSnapshotIsFrozenAndLocalizedPerViewer(t *testing.T) {
	translations := map[string]any{"en": map[string]any{"label": "Settings"}, "zh-CN": map[string]any{"label": "设置"}}
	dispatcher := NewDispatcher(StreamRequest{RunID: "run", ChatID: "chat"})
	events := dispatcher.Dispatch(ToolArgs{ToolID: "tool", ToolName: "desktop_settings", ToolLabel: "Default", ToolI18n: translations, Delta: "{}"})
	translations["en"].(map[string]any)["label"] = "changed"
	events = append(events, dispatcher.Dispatch(ToolEnd{ToolID: "tool"})...)
	count := 0
	for _, event := range events {
		if event.Type != "tool.start" && event.Type != "tool.snapshot" {
			continue
		}
		count++
		for _, tc := range []struct{ locale, label string }{{"en", "Settings"}, {"zh-CN", "设置"}} {
			payload := i18n.LocalizeEventPayload(tc.locale, event.Type, event.Payload)
			if payload["toolLabel"] != tc.label {
				t.Fatalf("%s %s: %#v", event.Type, tc.locale, payload)
			}
			if payload["toolI18n"] != nil {
				t.Fatal("public translation leak")
			}
		}
		if event.Payload["toolLabel"] != "Default" {
			t.Fatal("source changed")
		}
	}
	if count != 2 {
		t.Fatalf("expected start and snapshot: %d", count)
	}
}
