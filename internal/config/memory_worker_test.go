package config

import "testing"

func TestMemoryWorkerGlobalConfig(t *testing.T) {
	c := Config{Memory: MemoryConfig{Enabled: true, ContextMaxChars: 12000, Timezone: "UTC", Worker: MemoryWorkerConfig{PollIntervalSeconds: 300, TimeoutSeconds: 120, MaxBatches: 20}}}
	if err := c.applyMemoryValues(map[string]any{"worker": map[string]any{"enabled": true, "poll-interval-seconds": 60, "model-key": "fast"}}); err != nil {
		t.Fatal(err)
	}
	if c.Memory.Worker.PollIntervalSeconds != 60 || c.Memory.Worker.ModelKey != "fast" || !c.Memory.Worker.Enabled {
		t.Fatal(c.Memory)
	}
	for _, w := range []any{"invalid", map[string]any{"poll-interval-seconds": 0}, map[string]any{"unknown": true}} {
		copy := c
		if err := copy.applyMemoryValues(map[string]any{"worker": w}); err == nil {
			t.Fatalf("accepted %#v", w)
		}
	}
}

func TestMemorySummaryBudgets(t *testing.T) {
	base := Config{Memory: MemoryConfig{ContextMaxChars: 12000, Timezone: "UTC", Summary: DefaultMemorySummaryConfig()}}
	c := base
	if err := c.applyMemoryValues(map[string]any{"summary": map[string]any{"agent": map[string]any{"max-tokens": 1800, "max-lines": 180}}}); err != nil {
		t.Fatal(err)
	}
	if c.Memory.Summary.Agent.MaxTokens != 1800 || c.Memory.Summary.Agent.MaxLines != 180 || c.Memory.Summary.Global != base.Memory.Summary.Global {
		t.Fatal(c.Memory.Summary)
	}
	for _, raw := range []any{"invalid", map[string]any{"unknown": map[string]any{}}, map[string]any{"global": "invalid"}, map[string]any{"agent": map[string]any{"max-chars": 8000}}, map[string]any{"global": map[string]any{"max-tokens": 255}}, map[string]any{"agent": map[string]any{"max-tokens": 8001}}, map[string]any{"agent": map[string]any{"max-lines": 19}}, map[string]any{"global": map[string]any{"max-lines": 1001}}} {
		c := base
		if err := c.applyMemoryValues(map[string]any{"summary": raw}); err == nil {
			t.Fatalf("accepted %#v", raw)
		}
	}
	if err := base.applyMemoryValues(map[string]any{"worker": map[string]any{"summary-max-chars": 8000}}); err == nil {
		t.Fatal("old character budget accepted")
	}
}
