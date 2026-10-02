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
		{"bash: {}", nil, false}, {"preset-tools: []", []string{}, false},
		{"preset-tools:\n  - datetime\n  - sleep\n  - datetime", []string{"datetime", "sleep"}, false},
		{"preset-tools: sleep", nil, true}, {"preset-tools: [1]", nil, true}, {"preset-tools: ['']", nil, true}, {"preset-tools: null", nil, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tools.yml")
			_ = os.WriteFile(path, []byte(tc.value), 0600)
			var cfg Config
			err := cfg.applyToolsFile(path, false)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if !tc.bad && !reflect.DeepEqual(cfg.PresetTools, tc.want) {
				t.Fatalf("got %#v", cfg.PresetTools)
			}
		})
	}
}
