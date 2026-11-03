package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceRuntimeConfig(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		valid      bool
	}{
		{"disabled", "voice:\n  enabled: false\n", true},
		{"enabled", "voice:\n  enabled: true\n  asr-model: asr\n  tts-model: tts\n  default-voice: a\n  voices: [a, b]\n", true},
		{"missing models", "voice:\n  enabled: true\n", false},
		{"unknown", "voice:\n  generation: 1\n", false},
		{"bad boolean", "voice:\n  enabled: yes\n", false},
		{"bad type", "voice:\n  max-sentence-silence-ms: '600'\n", false},
		{"short silence", "voice:\n  max-sentence-silence-ms: 199\n", false},
		{"long silence", "voice:\n  max-sentence-silence-ms: 6001\n", false},
		{"lead zero", "voice:\n  downlink-lead-ms: 0\n", true},
		{"lead limit", "voice:\n  downlink-lead-ms: 1001\n", false},
		{"duplicate voices", "voice:\n  voices: [a, a]\n", false},
		{"voice scalar", "voice:\n  voices: a\n", false},
		{"null voice", "voice:\n  default-voice: null\n", false},
		{"unlisted voice", "voice:\n  enabled: true\n  asr-model: asr\n  tts-model: tts\n  default-voice: a\n  voices: [b]\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultConfig(LoadOptions{})
			before := cfg.Voice
			p := filepath.Join(t.TempDir(), "runtime.yml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			err := cfg.applyRuntimeFile(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err != nil && (cfg.Voice.Enabled != before.Enabled || cfg.Voice.ASRModel != before.ASRModel) {
				t.Fatal("invalid section partially applied")
			}
		})
	}
}
