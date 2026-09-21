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
		{"enabled", "document-preview:\n  enabled: true\n  api-base-url: http://hub:8090\n  public-base-url: https://docs.test\n  open-mode: external\n  max-file-bytes: 1024\n  request-timeout: 30s\n", false},
		{"auth", "document-preview:\n  auth:\n    mode: bearer-token-file\n    token-file: /private/preview-token\n", false},
		{"enabled missing URLs", "document-preview:\n  enabled: true\n", true},
		{"disabled empty URLs", "document-preview:\n  enabled: false\n  api-base-url: \"\"\n  public-base-url: \"\"\n", false},
		{"enabled missing public URL", "document-preview:\n  enabled: true\n  api-base-url: https://docs.test\n", true},
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
