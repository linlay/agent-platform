package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
