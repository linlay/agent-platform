package server

import (
	"agent-platform/internal/i18n"
	"agent-platform/internal/stream"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

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
	events := []stream.EventData{{Type: "tool.snapshot", Timestamp: 1791038000000, Payload: map[string]any{"toolId": "call", "toolName": "platform_inspect", "toolLabel": "platform_inspect"}}}
	original := events[0].Payload
	fixture.server.enrichToolMetadata(events, "")
	for _, tc := range []struct{ locale, label string }{{"en", "Platform Diagnostics"}, {"zh-CN", "平台诊断"}} {
		got := i18n.LocalizeValue(tc.locale, events)
		data, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), tc.label) || strings.Contains(string(data), "toolI18n") {
			t.Fatalf("replay: %s", data)
		}
	}
	if original["toolI18n"] != nil || original["toolLabel"] != "platform_inspect" {
		t.Fatal("legacy source mutated")
	}
}
