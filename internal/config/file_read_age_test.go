package config

import (
	"testing"
	"time"
)

func TestFileReadAgeConfig(t *testing.T) {
	cfg := defaultConfig(LoadOptions{})
	if cfg.FileTools.ReadBeforeWriteScope != "chat" || cfg.FileTools.ReadBeforeWriteMaxAge != time.Hour {
		t.Fatal("unexpected defaults")
	}
	for _, value := range []string{"60m", "2h", "0", "-1m", "invalid", "1ns", ""} {
		err := cfg.applyFileToolsValues("tools.yml", map[string]any{"read-before-write-max-age": value})
		valid := value == "60m" || value == "2h"
		if (err == nil) != valid {
			t.Fatalf("%q: %v", value, err)
		}
		if valid {
			want, _ := time.ParseDuration(value)
			if cfg.FileTools.ReadBeforeWriteMaxAge != want {
				t.Fatal("duration not applied")
			}
		}
	}
}
