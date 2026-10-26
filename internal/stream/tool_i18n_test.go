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
	events = append(events, dispatcher.Dispatch(ToolResult{ToolID: "tool", ToolName: "desktop_settings", ToolLabel: "new catalog label", ToolI18n: translations, Result: "done"})...)
	count := 0
	for _, event := range events {
		if event.Type != "tool.start" && event.Type != "tool.snapshot" && event.Type != "tool.result" {
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
	if count != 3 {
		t.Fatalf("expected start, snapshot and result: %d", count)
	}
}

func TestToolResultPresentationWithoutStartAndGatewayTarget(t *testing.T) {
	translations := map[string]any{"zh-CN": map[string]any{"label": "调用 MCP 工具"}}
	for _, target := range []bool{false, true} {
		dispatcher := NewDispatcher(StreamRequest{RunID: "run", ChatID: "chat"})
		input := ToolResult{ToolID: "call", ToolName: "mcp_call", ToolLabel: "List tables", ToolI18n: translations, Result: "done"}
		if target {
			input.Target = map[string]any{"toolName": "list_tables"}
			dispatcher.Dispatch(ToolArgs{ToolID: "call", ToolName: "mcp_call", ToolLabel: "mcp_call", ToolI18n: translations, Delta: "{}"})
		}
		events := dispatcher.Dispatch(input)
		event := events[len(events)-1]
		payload := i18n.LocalizeEventPayload("zh-CN", event.Type, event.Payload)
		want := "调用 MCP 工具"
		if target {
			want = "List tables"
		}
		if payload["toolLabel"] != want || payload["toolI18n"] != nil || payload["result"] != "done" {
			t.Fatalf("target=%v: %#v", target, payload)
		}
	}
}
