package config

import "testing"

func TestMemoryWorkerGlobalConfig(t *testing.T) {
	c := Config{Memory: MemoryConfig{Enabled: true, ContextMaxChars: 12000, Timezone: "UTC", Worker: MemoryWorkerConfig{PollIntervalSeconds: 300, TimeoutSeconds: 120, MaxBatches: 20, SummaryMaxChars: 8000}}}
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
