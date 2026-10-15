package connector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoAuthAdmissionAndConflictingSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	if err := RequireConfigured(root, "demo", AuthNoAuth); err != nil {
		t.Fatal(err)
	}
	if err := RequireConfigured(root, "demo", AuthDelegated); err == nil {
		t.Fatal("delegated mode bypassed configuration")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("admission created state", err)
	}
	for _, extra := range []string{
		`"authBrowser":"system"`,
		`"oauth":{"clientId":"demo"}`,
	} {
		if err := ValidateManifest("demo", []byte(`{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","authMode":"none",`+extra+`}`)); err == nil {
			t.Fatal("accepted conflict", extra)
		}
	}
	for _, pkg := range []Package{
		{Manifest: Manifest{AuthMode: AuthNoAuth}, CLI: map[string]any{"auth": map[string]any{}}},
		{Manifest: Manifest{AuthMode: AuthNoAuth}, MCP: map[string]map[string]any{"main": {"platform": map[string]any{"authSource": "identity-file"}}}},
	} {
		if err := pkg.validateAuthDeclarations(); err == nil {
			t.Fatal("accepted component authentication")
		}
	}
}
