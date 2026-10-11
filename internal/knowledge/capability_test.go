package knowledge

import "testing"

func TestParseConfigCapabilityFields(t *testing.T) {
	implicit, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("parse implicit config: %v", err)
	}
	if implicit.Enabled {
		t.Fatalf("implicit config must remain disabled: %#v", implicit)
	}

	cfg, err := ParseConfig(map[string]any{
		"libraryId": "research",
	})
	if err != nil {
		t.Fatalf("parse capability config: %v", err)
	}
	if !cfg.Enabled {
		t.Fatalf("unexpected capability fields: %#v", cfg)
	}

	for _, raw := range []map[string]any{
		{"enabled": "true"},
		{"source": "./knowledge"},
		{"source": map[string]any{"root": 42}},
		{"source": map[string]any{"root": "/knowledge"}},
	} {
		if _, err := ParseConfig(raw); err == nil {
			t.Fatalf("invalid capability config accepted: %#v", raw)
		}
	}
}
