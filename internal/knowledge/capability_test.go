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
		"enabled": true,
	})
	if err != nil {
		t.Fatalf("parse capability config: %v", err)
	}
	if !cfg.Enabled {
		t.Fatalf("unexpected capability fields: %#v", cfg)
	}

	disabled, err := ParseConfig(map[string]any{"enabled": false})
	if err != nil {
		t.Fatalf("parse disabled config: %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("explicit false was not retained: %#v", disabled)
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
