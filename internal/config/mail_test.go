package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMailEnabledConfiguration(t *testing.T) {
	if defaultConfig(LoadOptions{}).Mail.Enabled {
		t.Fatal("mailbox must be disabled by default")
	}
	for _, tc := range []struct {
		value string
		want  bool
		valid bool
	}{
		{"true", true, true},
		{"false", false, true},
		{"null", false, false},
		{"1", false, false},
		{"\"true\"", false, false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.yml")
			if err := os.WriteFile(path, []byte("mail:\n  enabled: "+tc.value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig(LoadOptions{})
			// Explicit false must override an already enabled value.
			cfg.Mail.Enabled = true
			err := cfg.applyRuntimeFile(path)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid enabled accepted")
				}
				return
			}
			if err != nil || cfg.Mail.Enabled != tc.want || cfg.Mail.MaxHops != 4 || cfg.Mail.RetentionDays != 30 {
				t.Fatal(cfg.Mail, err)
			}
		})
	}
}

func TestMailRetentionConfiguration(t *testing.T) {
	if got := defaultConfig(LoadOptions{}).Mail.RetentionDays; got != 30 {
		t.Fatal(got)
	}
	for _, tc := range []struct {
		value string
		want  int
	}{{"7", 7}, {"30", 30}, {"365000", 365000}, {"0", 0}, {"-1", 0}, {"365001", 0}, {"1.5", 0}, {"null", 0}, {"\"30\"", 0}} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.yml")
			if err := os.WriteFile(path, []byte("mail:\n  retention-days: "+tc.value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig(LoadOptions{})
			err := cfg.applyRuntimeFile(path)
			if tc.want == 0 {
				if err == nil {
					t.Fatal("invalid retention accepted")
				}
				return
			}
			if err != nil || cfg.Mail.RetentionDays != tc.want || cfg.Mail.MaxHops != 4 {
				t.Fatal(cfg.Mail, err)
			}
		})
	}
}
