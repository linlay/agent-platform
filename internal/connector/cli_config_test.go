package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAndMCPDeclarationsFollowWorkBuddyFieldSet(t *testing.T) {
	commands := map[string]any{"darwin": "demo x", "linux": "demo x", "win32": "demo x"}
	managed := func(extra map[string]any) map[string]any {
		cli := map[string]any{"auth": commands, "status": commands, "unAuth": commands}
		for key, value := range extra {
			cli[key] = value
		}
		return cli
	}
	flow := map[string]any{"uriPattern": `(https://\S+)`, "codePattern": `code: (\S+)`, "defaultExpiresInSeconds": 300.0, "codeEmbeddedInUri": true}
	for _, tc := range []struct {
		name, mode string
		cli, mcp   map[string]any
		reject     string
	}{
		{name: "WorkBuddy CLI fields", mode: "self", cli: managed(map[string]any{
			"init": commands, "npmRegistry": "https://registry.npmmirror.com", "versionCheck": map[string]any{"command": commands, "minVersion": "1.0.0"},
			"statusMatch": "ok", "statusMatchJson": map[string]any{"ok": true}, "authUrlDomain": "example.test",
			"authWaitForExit": true, "authSuppressBrowser": true, "authDeviceFlow": flow,
		})},
		{name: "per-step overrides", mode: "self", cli: managed(map[string]any{"auth": []any{map[string]any{"command": commands, "skipIf": commands, "authWaitForExit": false, "authDeviceFlow": flow}}})},
		{name: "no versionCheck without init", mode: "self", cli: managed(nil)},
		{name: "token status check", mode: "token", cli: map[string]any{"status": commands, "statusMatch": "ok"}},
		{name: "preAuth", mode: "self", cli: managed(nil), mcp: map[string]any{"preAuth": "cli"}},
		{name: "removed platform", mode: "self", cli: managed(map[string]any{"platform": map[string]any{"command": "demo"}}), reject: "unsupported field platform"},
		{name: "removed authQrModal", mode: "self", cli: managed(map[string]any{"authQrModal": true}), reject: "unsupported field authQrModal"},
		{name: "init without versionCheck", mode: "self", cli: managed(map[string]any{"init": commands}), reject: "init requires versionCheck"},
		{name: "both registry forms", mode: "self", cli: managed(map[string]any{"init": commands, "versionCheck": map[string]any{}, "npmRegistry": "https://a.test", "npmRegistries": []any{"https://b.test"}}), reject: "both npmRegistry"},
		{name: "plain HTTP registry", mode: "self", cli: managed(map[string]any{"init": commands, "versionCheck": map[string]any{}, "npmRegistry": "http://a.test"}), reject: "https URL"},
		{name: "invalid device pattern", mode: "self", cli: managed(map[string]any{"authDeviceFlow": map[string]any{"uriPattern": "("}}), reject: "uriPattern is invalid"},
		{name: "auth outside self", mode: "token", cli: managed(nil), reject: "requires authMode self"},
		{name: "auth without status", mode: "self", cli: map[string]any{"auth": commands, "unAuth": commands}, reject: "requires status"},
		{name: "self status without auth", mode: "self", cli: map[string]any{"status": commands}, reject: "requires authMode token"},
		{name: "preAuth without CLI login", mode: "self", cli: map[string]any{}, mcp: map[string]any{"preAuth": "cli"}, reject: "preAuth cli requires"},
		{name: "unknown preAuth", mode: "self", cli: managed(nil), mcp: map[string]any{"preAuth": "oauth"}, reject: "preAuth must be cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "demo")
			files := map[string]any{
				"connector.json": map[string]any{"id": "demo", "name": "Demo", "version": "1.0.0", "type": "cli", "authMode": tc.mode},
				"cli.json":       tc.cli,
			}
			if tc.mode == "token" {
				files["token-schema.json"] = map[string]any{"fields": []any{map[string]any{"key": "API_KEY", "label": "Key", "type": "password", "required": true}}}
			}
			if tc.mcp != nil {
				tc.mcp["mcpServers"] = map[string]any{"main": map[string]any{"type": "stdio", "command": "demo"}}
				files["mcp.json"] = tc.mcp
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, value := range files {
				data, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Load(root, "demo")
			if tc.reject == "" && err != nil {
				t.Fatal(err)
			}
			if tc.reject != "" && (err == nil || !strings.Contains(err.Error(), tc.reject)) {
				t.Fatalf("want %q, got %v", tc.reject, err)
			}
		})
	}
}
