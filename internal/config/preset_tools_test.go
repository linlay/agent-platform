package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPresetToolsConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
		bad   bool
	}{
		{"general: {}", nil, false}, {"preset-tools: []", []string{}, false},
		{"preset-tools:\n  - datetime\n  - wait\n  - datetime", []string{"datetime", "wait"}, false},
		{"preset-tools: wait", nil, true}, {"preset-tools: [1]", nil, true}, {"preset-tools: ['']", nil, true}, {"preset-tools: null", nil, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent-settings.yml")
			_ = os.WriteFile(path, []byte(tc.value), 0600)
			var cfg Config
			err := cfg.applyAgentSettingsFile(path)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if !tc.bad && !reflect.DeepEqual(cfg.PresetTools, tc.want) {
				t.Fatalf("got %#v", cfg.PresetTools)
			}
		})
	}
}

func TestPresetConnectorsConfig(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
		bad   bool
	}{
		{"preset-tools: []", nil, false}, {"preset-connectors: []", []string{}, false},
		{"preset-connectors:\n  - builtin.web-control\n  - builtin.web-control", []string{"builtin.web-control"}, false},
		{"preset-connectors: nope", nil, true}, {"preset-connectors: null", nil, true},
		{"preset-connectors: ['../bad']", nil, true}, {"preset-connectors: [1]", nil, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent-settings.yml")
			_ = os.WriteFile(path, []byte(tc.value), 0600)
			cfg := Config{PresetConnectors: []string{"stale"}}
			err := cfg.applyAgentSettingsFile(path)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if !tc.bad && !reflect.DeepEqual(cfg.PresetConnectors, tc.want) {
				t.Fatalf("got %#v", cfg.PresetConnectors)
			}
		})
	}
}
