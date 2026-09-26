package skillmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresentationLocaleFallbackAndIsolation(t *testing.T) {
	p := Parse(map[string]any{"displayName": "默认", "revision": "r18", "i18n": map[string]any{
		"en":     map[string]any{"displayName": "Workflow", "description": "English description"},
		"en-US":  map[string]any{"displayName": "US workflow"},
		"zh-CN":  map[string]any{"displayName": "简体"},
		"broken": 42,
	}}, "1.0.0")
	for _, tt := range []struct{ locale, name, description string }{
		{"en-US", "US workflow", "English description"}, {"en-GB", "Workflow", "English description"},
		{"zh-CN", "简体", "原始描述"}, {"zh-TW", "默认", "原始描述"},
	} {
		got, description := p.Resolve(tt.locale, "stable-name", "stable-key", "原始描述")
		if got.DisplayName != tt.name || description != tt.description || got.Version != "1.0.0" || got.Revision != "r18" {
			t.Fatalf("%s: %#v %s", tt.locale, got, description)
		}
		data, _ := json.Marshal(got)
		if strings.Contains(string(data), "i18n") || strings.Contains(string(data), "Translations") {
			t.Fatalf("translations exposed: %s", data)
		}
	}
	if p.DisplayName != "默认" || len(p.Translations) != 2+1 {
		t.Fatalf("mutated source: %#v", p)
	}
	got, _ := (Presentation{}).Resolve("en", "old name", "key", "description")
	if got.DisplayName != "old name" {
		t.Fatal(got)
	}
	got, _ = (Presentation{}).Resolve("en", "", "key", "")
	if got.DisplayName != "key" || got.Version != "" {
		t.Fatal(got)
	}
}
