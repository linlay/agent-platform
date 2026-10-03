package i18n

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolPresentationPerViewerAndFallback(t *testing.T) {
	translations, err := ParseToolTranslations(map[string]any{
		"en-US": map[string]any{"label": "Settings"},
		"zh-CN": map[string]any{"label": "设置", "description": "界面说明"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]any{"toolId": "call", "toolName": "settings", "toolLabel": "Default", "toolDescription": "Model instructions", "toolI18n": translations, "arguments": "unchanged"}
	for _, event := range []string{"tool.start", "tool.snapshot"} {
		for _, tc := range []struct{ locale, label, description string }{{"zh-CN", "设置", "界面说明"}, {"en-US", "Settings", "Model instructions"}, {"fr", "Settings", "Model instructions"}} {
			got := LocalizeEventPayload(tc.locale, event, original)
			if got["toolLabel"] != tc.label || got["toolDescription"] != tc.description || got["arguments"] != "unchanged" {
				t.Fatalf("%s: %#v", tc.locale, got)
			}
			if _, leaked := got["toolI18n"]; leaked {
				t.Fatal("translations leaked")
			}
		}
	}
	if original["toolLabel"] != "Default" || original["toolI18n"] == nil {
		t.Fatal("mutated source")
	}
	for _, meta := range []bool{false, true} {
		tool := map[string]any{"name": "settings", "label": "Default", "description": "Model instructions"}
		if meta {
			tool["meta"] = map[string]any{"toolI18n": translations}
		} else {
			tool["toolI18n"] = translations
		}
		got := LocalizeValue("en", tool).(map[string]any)
		if got["label"] != "Settings" || got["description"] != "Model instructions" {
			t.Fatalf("catalog: %#v", got)
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "toolI18n") {
			t.Fatal("catalog leaked translations")
		}
	}
}

func TestToolTranslationsValidation(t *testing.T) {
	for _, raw := range []any{"invalid", map[string]any{"en": "bad"}, map[string]any{"fr": map[string]any{}}, map[string]any{"en": map[string]any{"label": 1}}, map[string]any{"en": map[string]any{"name": "renamed"}}, map[string]any{"en": map[string]any{}, "en-US": map[string]any{}}} {
		if _, err := ParseToolTranslations(raw); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	translations, _ := ParseToolTranslations(map[string]any{"zh-CN": map[string]any{"label": " "}})
	got := LocalizeValue("zh-CN", map[string]any{"name": "stable_name", "toolI18n": translations}).(map[string]any)
	if got["label"] != "stable_name" {
		t.Fatalf("fallback: %#v", got)
	}
}
