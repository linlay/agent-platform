package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func webAppConfigForTest(t *testing.T) Config {
	t.Helper()
	c := defaultConfig(LoadOptions{RuntimeMode: "standalone", WebAppHost: "compose"})
	c.WebApps.Enabled = true
	c.WebApps.PlatformOrigin = "https://demo.platform.example.com"
	c.WebApps.ContentDomain = "apps.example.net"
	c.WebApps.InstanceSlug = "demo"
	c.Auth.Enabled, c.Auth.RequireExpiry = true, true
	c.Auth.Issuer, c.Auth.Audience = "https://identity.example.org", "instance-demo"
	c.Auth.AllowedSubjects = []string{"owner"}
	c.Paths.WebAppsDir = filepath.Join(t.TempDir(), "webapps")
	c.Paths.WebAppDataDir = filepath.Join(t.TempDir(), "webapp-data")
	c.Paths.StateDir = filepath.Join(t.TempDir(), ".state")
	return c
}

func TestWebAppDeploymentBoundary(t *testing.T) {
	for _, tc := range []struct {
		os            string
		mode          RuntimeMode
		host          WebAppHostMode
		enabled, want bool
	}{
		{"linux", RuntimeModeStandalone, WebAppHostCompose, true, true},
		{"linux", RuntimeModeStandalone, WebAppHostDisabled, true, false},
		{"linux", RuntimeModeDesktop, WebAppHostCompose, true, false},
		{"darwin", RuntimeModeStandalone, WebAppHostCompose, true, false},
		{"windows", RuntimeModeStandalone, WebAppHostCompose, true, false},
		{"darwin", RuntimeModeDesktop, WebAppHostDisabled, false, true},
	} {
		t.Run(tc.os+"/"+string(tc.mode)+"/"+string(tc.host), func(t *testing.T) {
			c := webAppConfigForTest(t)
			c.RuntimeMode, c.WebAppHost, c.WebApps.Enabled = tc.mode, tc.host, tc.enabled
			if err := c.validateWebApps(tc.os); (err == nil) != tc.want {
				t.Fatalf("validate = %v, want success=%t", err, tc.want)
			}
			for _, path := range []string{c.WebAppsCodeDir(), c.WebAppsDataDir(), c.WebAppsStateDir()} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("validation created %s", path)
				}
			}
		})
	}
}

func TestWebAppOwnerAndOrigins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"auth_disabled", func(c *Config) { c.Auth.Enabled = false }},
		{"expiry_optional", func(c *Config) { c.Auth.RequireExpiry = false }},
		{"issuer_missing", func(c *Config) { c.Auth.Issuer = "" }},
		{"audience_missing", func(c *Config) { c.Auth.Audience = "" }},
		{"owner_missing", func(c *Config) { c.Auth.AllowedSubjects = nil }},
		{"multiple_owners", func(c *Config) { c.Auth.AllowedSubjects = []string{"a", "b"} }},
		{"http", func(c *Config) { c.WebApps.PlatformOrigin = "http://demo.example.com" }},
		{"path", func(c *Config) { c.WebApps.PlatformOrigin += "/" }},
		{"query", func(c *Config) { c.WebApps.PlatformOrigin += "?" }},
		{"userinfo", func(c *Config) { c.WebApps.PlatformOrigin = "https://user@demo.example.com" }},
		{"port", func(c *Config) { c.WebApps.PlatformOrigin += ":443" }},
		{"same_site", func(c *Config) { c.WebApps.ContentDomain = "apps.example.com" }},
		{"public_suffix", func(c *Config) { c.WebApps.ContentDomain = "co.uk" }},
		{"registrable_domain", func(c *Config) { c.WebApps.ContentDomain = "example.net" }},
		{"uppercase", func(c *Config) { c.WebApps.ContentDomain = "Apps.example.net" }},
		{"wildcard", func(c *Config) { c.WebApps.ContentDomain = "*.apps.example.net" }},
		{"slug", func(c *Config) { c.WebApps.InstanceSlug = "demo.dot" }},
		{"long_slug", func(c *Config) { c.WebApps.InstanceSlug = strings.Repeat("x", 46) }},
		{"origin_mismatch", func(c *Config) { c.Server.PublicBaseURL = "https://other.example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := webAppConfigForTest(t)
			tc.mutate(&c)
			if err := c.validateWebApps("linux"); err == nil {
				t.Fatal("accepted invalid deployment policy")
			}
		})
	}
}

func TestWebAppsStrictYAML(t *testing.T) {
	invalid := []string{
		"null", "true", "[]", "{Enabled: true}", "{paths: {root: /tmp}}",
		"{enabled: 'true'}", "{enabled: null}", "{backend: null}",
		"{backend: {Port-start: 20000}}", "{backend: {port-start: 20000.5}}",
		"{backend: {port-start: 20000.0}}", "{enabled: \"true\"}",
		"{backend: {port-start: 80}}", "{backend: {port-start: 21000, port-end: 20000}}",
		"{backend: {port-start: 20000, port-end: 20001}}", "{max-running: 0}",
		"{max-running: 129}", "{logs: {max-files: 0}}", "{max-files: 1e30}",
		"{backend: {rss-stop-mb: 1}}", "{max-extracted-mib: 1}",
		"{backend: {restart: {window-seconds: 0}}}",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			c := defaultConfig(LoadOptions{})
			before := c.WebApps
			err := c.applyRuntimeFile(configFixture(t, "runtime.yml", "webapps: "+raw+"\n"))
			if err == nil {
				t.Fatal("accepted invalid webapps section")
			}
			if !reflect.DeepEqual(before, c.WebApps) {
				t.Fatal("failed configuration partially changed policy")
			}
		})
	}
	c := defaultConfig(LoadOptions{})
	yaml := "webapps:\n  enabled: false\n  max-running: 2\n  backend:\n    port-start: 30000\n    port-end: 30001\n  logs:\n    max-files: 2\n"
	if err := c.applyRuntimeFile(configFixture(t, "runtime.yml", yaml)); err != nil {
		t.Fatal(err)
	}
	if c.WebApps.MaxRunning != 2 || c.WebApps.Backend.PortStart != 30000 || c.WebApps.Backend.MaxOldSpaceSizeMB != 256 || c.WebApps.Logs.MaxFiles != 2 {
		t.Fatalf("unexpected settings: %+v", c.WebApps)
	}
}

func TestWebAppHostOnlyStartupOption(t *testing.T) {
	for _, raw := range []string{"auto", "desktop", "Compose", "linux"} {
		if _, err := ParseWebAppHostMode(raw); err == nil {
			t.Fatalf("accepted host %q", raw)
		}
	}
	for _, raw := range []string{"", "disabled"} {
		mode, err := ParseWebAppHostMode(raw)
		if err != nil || mode != WebAppHostDisabled {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
	}
	c := defaultConfig(LoadOptions{})
	if c.WebApps.Enabled || c.WebAppHost != WebAppHostDisabled {
		t.Fatal("hosting enabled by default")
	}
	t.Setenv("AP_WEBAPP_HOST", "compose")
	c.applyEnv(LoadOptions{})
	if c.WebAppHost != WebAppHostDisabled {
		t.Fatal("environment overrode startup host selection")
	}
}

func TestWebAppDerivedPathsDoNotFollowLaterEnvironmentChanges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AP_RUNTIME_DIR", root)
	c := defaultConfig(LoadOptions{})
	state := filepath.Join(t.TempDir(), "state")
	c.Paths.StateDir = state
	t.Setenv("AP_RUNTIME_DIR", t.TempDir())
	if c.WebAppsCodeDir() != filepath.Join(root, "webapps") || c.WebAppsDataDir() != filepath.Join(root, "webapp-data") || c.WebAppsStateDir() != filepath.Join(state, "webapps") {
		t.Fatal("WebApp paths were not frozen to runtime/state configuration")
	}
}

func TestWebAppsQuotedScalarsDoNotChangeLegacySections(t *testing.T) {
	raw := []byte("legacy:\n  enabled: 'true'\nwebapps:\n  enabled: 'true'\n  max-running: \"4\"\n")
	tree, err := LoadYAMLTreeBytesWithOptions(raw, YAMLTreeOptions{PreserveQuotedScalarPrefixes: []string{"webapps"}})
	if err != nil {
		t.Fatal(err)
	}
	top := tree.(map[string]any)
	if top["legacy"].(map[string]any)["enabled"] != true {
		t.Fatal("legacy scalar behavior changed")
	}
	web := top["webapps"].(map[string]any)
	if web["enabled"] != "true" || web["max-running"] != "4" {
		t.Fatal("strict quoted types not preserved")
	}
}
