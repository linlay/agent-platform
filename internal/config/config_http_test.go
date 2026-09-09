package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPProxyConfiguration(t *testing.T) {
	for _, tt := range []struct {
		name, body, mode string
		fail             bool
	}{
		{name: "omitted", body: "query:\n  advanced-user-prompt: false\n", mode: "auto"},
		{name: "auto", body: "http-proxy:\n  mode: auto # auto / direct / fixed\n  system-refresh-interval: 15s\n", mode: "auto"},
		{name: "direct", body: "http-proxy:\n  mode: direct\n", mode: "direct"},
		{name: "fixed", body: "http-proxy:\n  mode: fixed\n  url: http://127.0.0.1:10809\n  bypass: [localhost, \"*.internal\"]\n  system-refresh-interval: 30s\n", mode: "fixed"},
		{name: "missing URL", body: "http-proxy:\n  mode: fixed\n", fail: true},
		{name: "URL needs fixed", body: "http-proxy:\n  url: http://127.0.0.1:10809\n", fail: true},
		{name: "bad URL redaction", body: "http-proxy:\n  mode: fixed\n  url: http://user:secret@%zz\n", fail: true},
		{name: "bad mode", body: "http-proxy:\n  mode: system\n", fail: true},
		{name: "zero interval", body: "http-proxy:\n  system-refresh-interval: 0s\n", fail: true},
		{name: "invalid interval", body: "http-proxy:\n  system-refresh-interval: often\n", fail: true},
		{name: "bad field", body: "http-proxy:\n  enabled: true\n", fail: true},
		{name: "scalar", body: "http-proxy: direct\n", fail: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.yml")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := defaultConfig(LoadOptions{})
			err := cfg.applyRuntimeFile(path)
			if (err != nil) != tt.fail {
				t.Fatalf("got %v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret") {
					t.Fatal("credential leaked")
				}
				return
			}
			if cfg.HTTPProxy.Mode != tt.mode {
				t.Fatalf("got mode %q", cfg.HTTPProxy.Mode)
			}
			if tt.mode == "fixed" {
				if cfg.HTTPProxy.SystemRefreshInterval != 30*time.Second || len(cfg.HTTPProxy.Bypass) != 2 {
					t.Fatalf("%+v", cfg.HTTPProxy)
				}
			} else if cfg.HTTPProxy.SystemRefreshInterval != 15*time.Second {
				t.Fatal("wrong default refresh interval")
			}
		})
	}
}
