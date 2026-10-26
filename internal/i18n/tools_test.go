package i18n

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestToolResultLocalizationDoesNotTranslateBusinessOutput(t *testing.T) {
	result := map[string]any{"code": "not_found", "message": "agent not found", "name": "record", "toolI18n": map[string]any{"zh-CN": map[string]any{"label": "business field"}}}
	payload := map[string]any{"toolId": "call", "toolName": "file_read", "toolLabel": "file_read", "result": result,
		"toolI18n": map[string]any{"zh-CN": map[string]any{"label": "读取文件"}}}
	live := LocalizeEventPayload("zh-CN", "tool.result", payload)
	replayEvent := cloneMap(payload)
	replayEvent["type"] = "tool.result"
	replay := LocalizeValue("zh-CN", replayEvent).(map[string]any)
	for _, got := range []map[string]any{live, replay} {
		if got["toolLabel"] != "读取文件" || got["toolI18n"] != nil || !reflect.DeepEqual(got["result"], result) {
			t.Fatalf("localization changed output: %#v", got)
		}
	}
	if payload["toolLabel"] != "file_read" || payload["toolI18n"] == nil {
		t.Fatal("source payload mutated")
	}
}

func TestToolPresentationPerViewerAndFallback(t *testing.T) {
	translations, err := ParseToolTranslations(map[string]any{
		"en-US": map[string]any{"label": "Settings"},
		"zh-CN": map[string]any{"label": "设置", "description": "界面说明"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]any{"toolId": "call", "toolName": "settings", "toolLabel": "Default", "toolDescription": "Model instructions", "toolI18n": translations, "arguments": "unchanged"}
	for _, event := range []string{"tool.start", "tool.snapshot", "tool.result"} {
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

func TestToolLabelNeverFallsBackToAnotherLanguage(t *testing.T) {
	got := LocalizeValue("en", map[string]any{"name": "custom", "label": "旧中文名称", "toolI18n": map[string]any{"zh-CN": map[string]any{"label": "中文名称"}}}).(map[string]any)
	if got["label"] != "custom" {
		t.Fatalf("foreign-language fallback: %#v", got)
	}
}
