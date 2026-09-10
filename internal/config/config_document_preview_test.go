package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentPreviewRuntimeConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		fail       bool
	}{
		{"omitted", "query:\n  advanced-user-prompt: false\n", false},
		{"enabled", "document-preview:\n  enabled: true\n  open-mode: external\n  max-file-bytes: 1024\n  request-timeout: 30s\n", false},
		{"auth", "document-preview:\n  auth:\n    mode: bearer-token-file\n    token-file: /private/preview-token\n", false},
		{"invalid mode", "document-preview:\n  open-mode: popup\n", true},
		{"credentials URL", "document-preview:\n  api-base-url: https://user:secret@example.com\n", true},
		{"invalid type", "document-preview:\n  enabled: yes\n", true},
		{"invalid limit", "document-preview:\n  max-file-bytes: 0\n", true},
		{"unknown field", "document-preview:\n  arbitrary: true\n", true},
		{"missing token", "document-preview:\n  auth:\n    mode: bearer-token-file\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig(LoadOptions{})
			path := filepath.Join(t.TempDir(), "runtime.yml")
			_ = os.WriteFile(path, []byte(tt.body), 0600)
			err := cfg.applyRuntimeFile(path)
			if (err != nil) != tt.fail {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret leaked")
			}
			if tt.name == "omitted" && cfg.DocumentPreview.Enabled {
				t.Fatal("must default off")
			}
			if tt.name == "enabled" && (!cfg.DocumentPreview.Enabled || cfg.DocumentPreview.OpenMode != "external" || cfg.DocumentPreview.MaxFileBytes != 1024) {
				t.Fatal("config not applied")
			}
		})
	}
}
