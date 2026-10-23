package server

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/conversationexport"
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyToolResultInheritsSnapshotTranslationsAtReadBoundary(t *testing.T) {
	translations := map[string]any{"en": map[string]any{"label": "Read File"}, "zh-CN": map[string]any{"label": "读取文件"}}
	snapshot := stream.EventData{Type: "tool.snapshot", Timestamp: 1791038000000, Payload: map[string]any{
		"toolId": "call", "runId": "run", "toolName": "file_read", "toolLabel": "file_read", "toolI18n": translations,
	}}
	for _, tc := range []struct {
		name       string
		label      string
		target     bool
		resultRun  string
		ambiguous  bool
		wantRepair bool
	}{
		{name: "raw label", label: "file_read", wantRepair: true},
		{name: "missing label", wantRepair: true},
		{name: "explicit label", label: "Custom read"},
		{name: "gateway target", label: "file_read", target: true},
		{name: "different run", label: "file_read", resultRun: "other"},
		{name: "ambiguous invocation", label: "file_read", ambiguous: true},
		{name: "explicit run", label: "file_read", resultRun: "run", ambiguous: true, wantRepair: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := stream.EventData{Type: "tool.result", Timestamp: snapshot.Timestamp + 1, Payload: map[string]any{
				"toolId": "call", "toolName": "file_read", "toolLabel": tc.label, "result": "contents", "runId": tc.resultRun,
			}}
			if tc.target {
				result.Payload["target"] = map[string]any{"toolName": "file_read"}
			}
			events := []stream.EventData{snapshot, result}
			if tc.ambiguous {
				other := snapshot
				other.Payload = map[string]any{"toolId": "call", "runId": "other", "toolName": "file_read", "toolI18n": translations}
				events = append(events, other)
			}
			(&Server{}).enrichToolMetadata(events, "")
			if got := events[1].Value("toolI18n") != nil; got != tc.wantRepair {
				t.Fatalf("repair=%v want=%v", got, tc.wantRepair)
			}
			if result.Value("toolI18n") != nil || result.String("toolLabel") != tc.label {
				t.Fatal("mutated stored payload")
			}
			if !tc.wantRepair {
				return
			}
			for locale, label := range map[string]string{"en": "Read File", "zh-CN": "读取文件"} {
				localized := make([]stream.EventData, 2)
				for i := range localized {
					localized[i] = localizeStreamEventData(locale, events[i])
				}
				if localized[1].String("toolLabel") != label || localized[1].Value("toolI18n") != nil || localized[1].String("result") != "contents" {
					t.Fatalf("%s: %#v", locale, localized[1])
				}
				// Export consumes the same localized replay events.
				exportEvents := append([]stream.EventData{{Type: "request.query", Timestamp: snapshot.Timestamp, Payload: map[string]any{"runId": "run", "message": "Read the file"}}}, localized...)
				doc, err := conversationexport.BuildSnapshotDocument(&chat.Summary{ChatID: "chat", CreatedAt: snapshot.Timestamp}, exportEvents, nil, snapshot.Timestamp+10, locale, nil)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(doc.JSON), label) || strings.Contains(string(doc.JSON), "toolI18n") {
					t.Fatalf("export: %s", doc.JSON)
				}
				for _, turn := range doc.Snapshot.Turns {
					for _, node := range turn.Nodes {
						if node.Kind == "tool" && node.ToolLabel != label {
							t.Fatalf("export label reverted: %#v", node)
						}
					}
				}
			}
		})
	}
}

func TestToolCatalogAndLegacyReplayLocalizePerViewer(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, tc := range []struct{ locale, label string }{{"en", "Date and Time"}, {"zh-CN", "日期时间"}} {
		req := httptest.NewRequest("GET", "/api/admin/tools", nil)
		req.Header.Set("X-Locale", tc.locale)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.label) || strings.Contains(rec.Body.String(), "toolI18n") {
			t.Fatalf("catalog %s: %d %s", tc.locale, rec.Code, rec.Body.String())
		}
	}
	events := []stream.EventData{
		{Type: "tool.snapshot", Timestamp: 1791038000000, Payload: map[string]any{"toolId": "call", "toolName": "platform_inspect", "toolLabel": "platform_inspect"}},
		{Type: "tool.result", Timestamp: 1791038000001, Payload: map[string]any{"toolId": "call", "toolName": "platform_inspect", "toolLabel": "platform_inspect", "result": "done"}},
	}
	original := events[0].Payload
	fixture.server.enrichToolMetadata(events, "")
	for _, tc := range []struct{ locale, label string }{{"en", "Platform Diagnostics"}, {"zh-CN", "平台诊断"}} {
		got := i18n.LocalizeValue(tc.locale, events)
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), tc.label) != 2 || strings.Contains(string(data), "toolI18n") {
			t.Fatalf("replay: %s", data)
		}
	}
	if original["toolI18n"] != nil || original["toolLabel"] != "platform_inspect" {
		t.Fatal("legacy source mutated")
	}
}
